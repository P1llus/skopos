// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/p1llus/skopos/schema"
)

// stepResult is everything one HTTP exchange yields. Persisted into
// scope.steps under the request's id so subsequent steps can reference
// {ref: steps.<id>.body.<path>}.
type stepResult struct {
	statusCode int
	headers    http.Header
	body       any // map[string]any | []any | nil (ndjson → []any of lines)
}

// executeRequest builds, sends, and decodes one Request. Returns the
// decoded body, the HTTP status code, and the response headers — or an
// error if the request couldn't be built/sent.
//
// expect_status is enforced here: a status not in the set returns a
// non-nil *unexpectedStatusError that the caller can interpret per
// error.mode (standard / fail / warn) or per-request on_status.
//
// trace is an optional trace-collection scratchpad. When non-nil
// executeRequest populates it as it goes (final URL, post-auth headers,
// timing, body metadata) — the caller composes a public Exchange from
// the scratchpad plus iteration/phase context only it knows.
//
// Pagination cursors reach the wire only through the template's own
// `query:` / `headers:` / `body:` declarations. Slice 5 deleted the
// implicit `send_as` auto-injector that used to fabricate a producer-step
// slot from the active pagination strategy.
func (s *scope) executeRequest(ctx context.Context, client *http.Client, req schema.Request, trace *httpTrace) (*stepResult, error) {
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
		trace.startedAt = s.now()
	}

	resp, err := client.Do(httpReq)
	if trace != nil {
		trace.elapsed = s.now().Sub(trace.startedAt)
	}
	if err != nil {
		// Go's *url.Error embeds the full request URL — including any
		// query-string credentials (auth.api_key.in_query, or a
		// {query.<k>: <secret-typed ref>}). Strip the query before the
		// error chain crosses into operational logging or Drain's
		// error return.
		return nil, fmt.Errorf("http.Do: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Check expect_status BEFORE decoding. A non-success response may
	// carry HTML / plaintext that the document's decode setting cannot
	// parse; failing decode-first would hide the real signal (the bad
	// status) behind a parse error. We still attempt decode on success.
	res := &stepResult{
		statusCode: resp.StatusCode,
		headers:    resp.Header,
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
	if trace != nil {
		raw, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return res, fmt.Errorf("read body: %w", rerr)
		}
		trace.respBodyMeta = bodyMeta(len(raw), raw)
		decoded, err := decodeResponseBytes(raw, s.doc.Response.Decode)
		if err != nil {
			return res, fmt.Errorf("decode: %w", err)
		}
		res.body = decoded
		return res, nil
	}
	decoded, err := decodeResponse(resp, s.doc.Response.Decode)
	if err != nil {
		return res, fmt.Errorf("decode: %w", err)
	}
	res.body = decoded
	return res, nil
}

// buildURL combines defaults.base_url with req.Path, or evaluates req.URL
// verbatim. The IR validator already enforces that exactly one of Path /
// URL is set.
func (s *scope) buildURL(req schema.Request) (*url.URL, error) {
	if req.URL != nil {
		got, err := s.evalValue(*req.URL)
		if err != nil {
			return nil, fmt.Errorf("url: %w", err)
		}
		u, perr := url.Parse(toString(got))
		if perr != nil {
			return nil, redactURLError(perr)
		}
		return u, nil
	}
	var prefix string
	if s.doc.Defaults != nil {
		got, err := s.evalValue(s.doc.Defaults.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("base_url: %w", err)
		}
		prefix = toString(got)
	}
	pathPart := ""
	if req.Path != nil {
		got, err := s.evalValue(*req.Path)
		if err != nil {
			return nil, fmt.Errorf("path: %w", err)
		}
		pathPart = toString(got)
	}
	u, perr := url.Parse(prefix + pathPart)
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

// decodeResponse reads resp.Body and decodes it according to decode.
//
//   - "json":   one JSON value, returned as map[string]any | []any | scalar | nil.
//   - "ndjson": one decoded object per line, returned as []any.
//
// The spec keeps decode a closed enum; anything else is a hard error.
func decodeResponse(resp *http.Response, decode string) (any, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return decodeResponseBytes(raw, decode)
}

// decodeResponseBytes decodes pre-read body bytes. Split out so the trace
// path can read the body once (for metadata) and decode from the buffer,
// keeping the wire-read and the decode contract identical to the
// stream-read path.
func decodeResponseBytes(raw []byte, decode string) (any, error) {
	switch decode {
	case "json":
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, nil
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("json.Unmarshal: %w (%s)", err, bodyMetadata(raw))
		}
		return out, nil

	case "ndjson":
		var lines []any
		sc := bufio.NewScanner(bytes.NewReader(raw))
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

	default:
		return nil, fmt.Errorf("response.decode %q is not json|ndjson", decode)
	}
}

// expectStatusOK returns true when status satisfies expect (or, when
// expect is empty, status is exactly 200). Mirrors the spec's default.
func (s *scope) expectStatusOK(expect []int, status int) bool {
	if len(expect) == 0 {
		return status == 200
	}
	for _, e := range expect {
		if e == status {
			return true
		}
	}
	return false
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
