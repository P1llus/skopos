// SPDX-License-Identifier: Apache-2.0

package client

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/p1llus/skopos/schema"
)

// stepResult is everything one HTTP exchange yields. Persisted into
// scope.steps under the request's id so subsequent steps can reference
// {ref: steps.<id>.body.<path>}.
type stepResult struct {
	headers http.Header
	body    any // map[string]any | []any | nil (row-oriented terminals → []any of rows)
}

// executeRequest builds, sends, and decodes one Request. Returns the
// decoded body, the HTTP status code, and the response headers — or an
// error if the request couldn't be built/sent.
//
// expect_status is enforced here: a status not in the set returns a
// non-nil *unexpectedStatusError that the caller can interpret per
// error.mode (standard / fail / warn) or per-request on_status.
//
// req.Cache is consulted BEFORE firing: when the cache slot is fresh, the
// cached body is returned as the stepResult without an HTTP round trip
// (status 200, no headers). The cache MISS path fires the request, decodes
// the body, evaluates cache.expires_at against the just-decoded body, and
// writes the slot. The cache namespace lives in scope.cache (process
// memory only) and is never persisted.
//
// trace is an optional trace-collection scratchpad. When non-nil
// executeRequest populates it as it goes (final URL, post-auth headers,
// timing, body metadata) — the caller composes a public Exchange from the
// scratchpad plus iteration context only it knows. Cache hits do NOT emit
// a trace record (no wire exchange happened); the caller can disambiguate
// hit-vs-miss by inspecting the returned stepResult against the prior
// scope.cache state if needed.
func (s *scope) executeRequest(ctx context.Context, client *http.Client, req schema.Request, trace *httpTrace) (*stepResult, error) {
	if req.Cache != nil {
		if v, ok := s.cacheGet(req.Cache); ok {
			return &stepResult{body: v}, nil
		}
	}

	u, err := s.buildURL(req)
	if err != nil {
		return nil, fmt.Errorf("url: %w", redactURLError(err))
	}

	// Headers + query are populated before body so body's content-type can
	// override a user-supplied one if needed.
	q := u.Query()
	for k, v := range req.Query {
		got, err := s.evalValue(v)
		if err != nil {
			return nil, fmt.Errorf("query.%s: %w", k, err)
		}
		if got == nil {
			continue
		}
		q.Set(k, toString(got))
	}

	var body io.Reader
	contentType := ""
	if req.Body != nil {
		body, contentType, err = s.buildBody(req.Body)
		if err != nil {
			return nil, fmt.Errorf("body: %w", err)
		}
	}

	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}

	// Capture request-body metadata for the trace if one was attached.
	// Read the buffered reader once into bytes so we can replay it and
	// expose length+class without leaking content.
	if trace != nil && body != nil {
		raw, _ := io.ReadAll(body)
		trace.reqBodyMeta = bodyMeta(len(raw), raw)
		body = bytes.NewReader(raw)
	}

	u.RawQuery = q.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("http.NewRequest: %w", redactURLError(err))
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	httpReq.Header.Set("Accept", "application/json, application/x-ndjson;q=0.9, */*;q=0.1")

	for k, v := range req.Headers {
		got, err := s.evalValue(v)
		if err != nil {
			return nil, fmt.Errorf("headers.%s: %w", k, err)
		}
		httpReq.Header.Set(k, toString(got))
	}

	if err := s.applyAuth(ctx, client, httpReq, s.doc.Auth); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	if trace != nil {
		trace.method = method
		// Clone the post-auth header set — applyAuth wrote Authorization /
		// api-key headers into httpReq.Header and we need the wire view.
		trace.headers = httpReq.Header.Clone()
		trace.finalURL = httpReq.URL
		// Round-trip timing measures real elapsed wall time, so it uses
		// time.Now directly rather than the spec clock s.now(): the latter
		// can be pinned (SOURCE_DATE_EPOCH) to make {now: true} deterministic,
		// which would collapse every duration to zero.
		trace.startedAt = time.Now()
	}

	resp, err := client.Do(httpReq)
	if trace != nil {
		trace.elapsed = time.Since(trace.startedAt)
	}
	if err != nil {
		// Go's *url.Error embeds the full request URL — including any
		// query-string credentials (auth.api_key.in_query, or a
		// {query.<k>: <secret-typed ref>}). Strip the query before the
		// error chain crosses into operational logging or Drain's error
		// return.
		return nil, fmt.Errorf("http.Do: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Check expect_status BEFORE decoding. A non-success response may
	// carry HTML / plaintext that the document's decode setting cannot
	// parse; failing decode-first would hide the real signal (the bad
	// status) behind a parse error. We still attempt decode on success.
	res := &stepResult{
		headers: resp.Header,
	}
	if trace != nil {
		trace.statusCode = resp.StatusCode
	}
	if !s.expectStatusOK(req.ExpectStatus, resp.StatusCode) {
		// Drain the body so the connection can be reused. When a trace
		// is attached, capture body metadata along the way so the
		// operator can tell "non-success + empty body" from "non-success
		// + html error page" without us re-issuing the request.
		if trace != nil {
			raw, _ := io.ReadAll(resp.Body)
			trace.respBodyMeta = bodyMeta(len(raw), raw)
		} else {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		return res, &unexpectedStatusError{
			status: resp.StatusCode,
			expect: req.ExpectStatus,
		}
	}

	var decoded any
	if trace != nil {
		raw, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return res, fmt.Errorf("read body: %w", rerr)
		}
		trace.respBodyMeta = bodyMeta(len(raw), raw)
		decoded, err = decodeResponseBytes(raw, s.doc.Response.Decode)
		if err != nil {
			return res, fmt.Errorf("decode: %w", err)
		}
	} else {
		decoded, err = decodeResponse(resp, s.doc.Response.Decode)
		if err != nil {
			return res, fmt.Errorf("decode: %w", err)
		}
	}
	res.body = decoded

	// Write the cache slot from the just-decoded body. The cache.expires_at
	// Value resolves against the cached step's own body via {ref:
	// response.body.<path>}, so bind scope.body for the evaluation and
	// restore it on the way out — a downstream extract / predicate should
	// see whatever the runner binds, not whatever the cache layer
	// transiently bound.
	if req.Cache != nil {
		prev := s.body
		s.body = res.body
		storeErr := s.cacheStore(req.Cache, res.body)
		s.body = prev
		if storeErr != nil {
			return res, fmt.Errorf("cache: %w", storeErr)
		}
	}

	return res, nil
}

// buildURL evaluates req.URL — an absolute URL Value, often a string
// interpolation like "${state.base_url}/events" — and parses the result.
// Every request carries its own absolute URL; there is no document-level
// URL prefix.
func (s *scope) buildURL(req schema.Request) (*url.URL, error) {
	got, err := s.evalValue(req.URL)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	u, perr := url.Parse(toString(got))
	if perr != nil {
		return nil, redactURLError(perr)
	}
	return u, nil
}

// buildBody serialises the discriminated Body union.
//
//   - body.json:  marshal map[string]any to JSON, Content-Type: application/json.
//   - body.form:  url-encode the map, Content-Type: application/x-www-form-urlencoded.
//   - body.raw:   evaluate the inner Value; expect a string. No content-type set
//     (author should declare one via headers).
func (s *scope) buildBody(b *schema.Body) (io.Reader, string, error) {
	switch {
	case b.JSON != nil:
		out := make(map[string]any, len(b.JSON))
		for k, v := range b.JSON {
			got, err := s.evalValue(v)
			if err != nil {
				return nil, "", fmt.Errorf("body.json.%s: %w", k, err)
			}
			out[k] = jsonifyValue(got)
		}
		data, err := json.Marshal(out)
		if err != nil {
			return nil, "", fmt.Errorf("body.json marshal: %w", err)
		}
		return bytes.NewReader(data), "application/json", nil

	case b.Form != nil:
		vals := url.Values{}
		for k, v := range b.Form {
			got, err := s.evalValue(v)
			if err != nil {
				return nil, "", fmt.Errorf("body.form.%s: %w", k, err)
			}
			vals.Set(k, toString(got))
		}
		return strings.NewReader(vals.Encode()), "application/x-www-form-urlencoded", nil

	case b.Raw != nil:
		got, err := s.evalValue(*b.Raw)
		if err != nil {
			return nil, "", fmt.Errorf("body.raw: %w", err)
		}
		return strings.NewReader(toString(got)), "", nil
	}
	return nil, "", fmt.Errorf("body: no variant set")
}

// jsonifyValue normalises a runtime value so encoding/json renders it in
// the shape the API expects. time.Time renders as RFC 3339; everything else
// passes through.
func jsonifyValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonifyValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonifyValue(e)
		}
		return out
	}
	return v
}

// decodeResponse reads resp.Body and runs the decode chain against it. See
// decodeChain for the chain contract.
func decodeResponse(resp *http.Response, chain schema.DecodeChain) (any, error) {
	return decodeChain(resp.Body, chain)
}

// decodeResponseBytes runs the decode chain against pre-read body bytes. Split
// out so the trace path can read the wire body once (for metadata) and decode
// from the buffer, keeping the decode contract identical to the stream-read
// path.
func decodeResponseBytes(raw []byte, chain schema.DecodeChain) (any, error) {
	return decodeChain(bytes.NewReader(raw), chain)
}

// decodeChain runs the response decode pipeline against r and returns the
// decoded body. Byte-transform stages wrap or expand the upstream reader
// before the terminal decoder runs:
//
//   - gzip:   wraps r in a gzip reader; streams.
//   - zip:    buffers the archive (random access is required), then decodes
//             each member with the remaining chain and concatenates the rows.
//
// The terminal decoder yields the body:
//
//   - json:   one JSON value (map | []any | scalar | nil).
//   - ndjson: []any, one decoded value per non-empty line.
//   - csv:    []any of rows — map per row (header present) or list per row
//             (header absent).
func decodeChain(r io.Reader, chain schema.DecodeChain) (any, error) {
	if len(chain) == 0 {
		return nil, fmt.Errorf("response.decode: empty chain")
	}
	name, payload := chain[0].Variant()
	switch name {
	case "gzip":
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = zr.Close() }()
		return decodeChain(zr, chain[1:])
	case "zip":
		return decodeZip(r, payload.(*schema.ZipDecode), chain[1:])
	case "json":
		return decodeJSONStream(r)
	case "ndjson":
		return decodeNDJSONStream(r)
	case "csv":
		return decodeCSVStream(r, payload.(*schema.CSVDecode))
	default:
		return nil, fmt.Errorf("response.decode: unsupported stage %q", name)
	}
}

// decodeJSONStream decodes the whole stream as one JSON value. An empty (or
// whitespace-only) stream decodes to nil.
func decodeJSONStream(r io.Reader) (any, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("json.Unmarshal: %w (%s)", err, bodyMetadata(raw))
	}
	return out, nil
}

// decodeNDJSONStream decodes one JSON value per non-empty line.
func decodeNDJSONStream(r io.Reader) (any, error) {
	var lines []any
	sc := bufio.NewScanner(r)
	// Default Scanner buffer is 64KB; some NDJSON lines exceed that.
	buf := make([]byte, 1<<20)
	sc.Buffer(buf, 16*1<<20)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("ndjson decode at line %d: %w (%s)", lineNum, err, bodyMetadata(line))
		}
		lines = append(lines, v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ndjson scan: %w", err)
	}
	return lines, nil
}

// decodeCSVStream decodes delimited rows into a []any. With header "present"
// the first record names the fields and each later row decodes to a
// map[string]any; with "absent" each row decodes to a []any of strings.
func decodeCSVStream(r io.Reader, cfg *schema.CSVDecode) (any, error) {
	cr := csv.NewReader(r)
	headerMode := cfg.Header == "present"
	var header []string
	var rows []any
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv: %w", err)
		}
		if headerMode && header == nil {
			header = append(header, rec...)
			continue
		}
		if headerMode {
			m := make(map[string]any, len(header))
			for i, field := range header {
				if i < len(rec) {
					m[field] = rec[i]
				} else {
					m[field] = nil
				}
			}
			rows = append(rows, m)
			continue
		}
		row := make([]any, len(rec))
		for i, field := range rec {
			row[i] = field
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// decodeZip buffers the ZIP archive (archive/zip needs random access for the
// central directory at the file's end), then decodes each non-directory
// member with rest in name-sorted order, concatenating the results. An empty
// glob selects every member; a non-empty glob matches against each member's
// base name.
func decodeZip(r io.Reader, cfg *schema.ZipDecode, rest schema.DecodeChain) (any, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("zip: read archive: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	files := make([]*zip.File, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if cfg.Glob != "" {
			ok, err := path.Match(cfg.Glob, path.Base(f.Name))
			if err != nil {
				return nil, fmt.Errorf("zip: glob %q: %w", cfg.Glob, err)
			}
			if !ok {
				continue
			}
		}
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	var out []any
	for _, f := range files {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("zip: open %s: %w", f.Name, err)
		}
		val, err := decodeChain(rc, rest)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("zip member %s: %w", f.Name, err)
		}
		out = appendDecoded(out, val)
	}
	return out, nil
}

// appendDecoded concatenates a member's decoded value into out: a []any is
// spread element-wise, nil is dropped, and any other value is appended as a
// single element.
func appendDecoded(out []any, v any) []any {
	switch x := v.(type) {
	case []any:
		return append(out, x...)
	case nil:
		return out
	default:
		return append(out, x)
	}
}

// expectStatusOK returns true when status satisfies expect (or, when
// expect is empty, status is exactly 200). Mirrors the spec's default.
func (s *scope) expectStatusOK(expect []int, status int) bool {
	if len(expect) == 0 {
		return status == 200
	}
	return slices.Contains(expect, status)
}

// unexpectedStatusError carries enough context for the runner's error.mode
// dispatcher and per-step on_status overrides to decide what to do.
type unexpectedStatusError struct {
	status int
	expect []int
}

func (e *unexpectedStatusError) Error() string {
	if len(e.expect) == 0 {
		return fmt.Sprintf("unexpected status %d (expected 200)", e.status)
	}
	return fmt.Sprintf("unexpected status %d (expected one of %v)", e.status, e.expect)
}

// bodyMetadata returns a redaction-safe description of a response body for
// inclusion in error messages. The raw bytes are intentionally NOT included:
// a token-exchange endpoint (OAuth2, custom login) can return access tokens
// in its body, and those bodies hit the JSON-decode error path when the
// server returns a non-JSON page (HTML stack trace, plain text). Surfacing
// `body: %q` in an error would put the token into stderr.
//
// The metadata returned is sufficient for triage (byte length, leading
// content-class) without leaking field values. Authors who genuinely need
// to see the raw bytes during template development can re-run with a
// custom *http.Client whose Transport logs request/response bodies — that
// is an explicit opt-in, not a default.
func bodyMetadata(b []byte) string {
	n := len(b)
	if n == 0 {
		return "body empty"
	}
	trimmed := bytes.TrimSpace(b)
	class := "non-json"
	switch {
	case len(trimmed) == 0:
		class = "whitespace-only"
	case trimmed[0] == '{':
		class = "object-like"
	case trimmed[0] == '[':
		class = "array-like"
	case trimmed[0] == '<':
		class = "html-like"
	case trimmed[0] == '"':
		class = "string-like"
	}
	return fmt.Sprintf("body %d bytes, %s", n, class)
}
