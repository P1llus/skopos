// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/p1llus/skopos/schema"
)

// Exchange is a structured trace record for one HTTP request/response pair
// executed during a Drain. It is the per-exchange counterpart to
// Runner.Logger (which captures operational dispatcher decisions —
// on_status verbs, error.mode dispatches, skipped requests).
//
// # Redaction policy
//
// The trace is intentionally redaction-safe by default:
//
//   - URL is rendered via safeURL (scheme + host + path; query and userinfo
//     stripped). Query parameters appear under Query as a separate map so
//     debugging "did we ask the server the right question?" stays possible
//     for non-secret values.
//   - Query values whose IR Value carries a secret (per schema.IsSecret) are
//     replaced with "<redacted>". The same rule covers auth.api_key
//     in_query=true: the configured header name's value is redacted.
//   - Headers are emitted with values replaced by "<redacted>" for:
//     (a) the always-sensitive name allowlist (Authorization, Cookie,
//     Proxy-Authorization), (b) any header whose IR Value is schema.IsSecret,
//     (c) any header named by auth.custom.header or
//     auth.api_key.header (the runtime injects credentials into those).
//   - Request and response bodies are metadata-only by default (byte length
//   - leading-byte classification). Surfacing raw bytes would put
//     access_token / refresh_token responses from OAuth2-style token
//     endpoints into the trace. Authors who genuinely need the raw bytes
//     during template development should plug a *http.Client whose
//     Transport logs request/response — that is an explicit opt-in.
//
// # Stability
//
// The JSON encoding of Exchange is the public wire format for the
// JSONLTracer. Fields added later land as additive (zero-value omitempty);
// renames/removes are breaking.
type Exchange struct {
	// Iteration is the 1-based drain iteration this exchange ran in. One
	// iteration may emit multiple exchanges (one per request in
	// doc.requests). Async-job drains where a phase is skipped still
	// increment iteration but contribute no exchanges for the skipped step.
	Iteration int `json:"iteration"`
	// Phase is the async_job phase ("submit" | "poll" | "fetch") this
	// exchange ran under, or "" for non-async_job drains.
	Phase string `json:"phase,omitempty"`
	// StepID is the request's IR id when set, otherwise empty. Match against
	// doc.requests[].id when triaging which step produced this exchange.
	StepID string `json:"step_id,omitempty"`

	// Method is the HTTP verb actually sent (uppercased; "GET" when the IR
	// did not set one).
	Method string `json:"method"`
	// URL is the wire URL with query and userinfo stripped. The full URL is
	// never emitted because auth.api_key.in_query and any
	// {query.<k>: <secret>-typed-ref} put credentials into RawQuery.
	URL string `json:"url"`
	// Query is the IR-declared query map, with secret-typed values
	// redacted. Auth-injected query keys (api_key in_query=true) are
	// also redacted by name.
	Query map[string]string `json:"query,omitempty"`
	// RequestHeaders is the post-auth header set actually written onto
	// the wire, with sensitive values redacted (see top-of-file policy).
	RequestHeaders map[string]string `json:"request_headers,omitempty"`
	// RequestBody is a metadata-only description of the request body
	// (length + content-class). The raw bytes are never included.
	RequestBody string `json:"request_body,omitempty"`

	// Status is the HTTP status code, or 0 when the request never reached
	// the server (transport-level error before a response).
	Status int `json:"status,omitempty"`
	// ResponseBody is a metadata-only description of the response body.
	ResponseBody string `json:"response_body,omitempty"`

	// StartedAt is when the runner began the round trip (post-build, just
	// before client.Do). UTC. Useful for sorting traces.
	StartedAt time.Time `json:"started_at"`
	// Elapsed is the wall-clock duration of the round trip — connect +
	// TLS + request + response — as measured around client.Do.
	Elapsed time.Duration `json:"elapsed"`

	// Error is the redaction-safe stringification of any error the runner
	// encountered for this exchange (transport, decode, unexpected status).
	// Empty when the exchange completed cleanly.
	Error string `json:"error,omitempty"`
}

// Tracer captures per-exchange records during a Drain. Implementations
// receive OnExchange calls in the order requests run within a Drain.
//
// # Concurrency
//
// A single Runner calls OnExchange serially from inside Drain. A Tracer
// shared between multiple Runners (concurrent Drains) MUST be safe for
// concurrent OnExchange calls; the bundled JSONLTracer is.
type Tracer interface {
	// OnExchange is called once per HTTP exchange. The runner does NOT
	// require the call to be non-blocking, but a slow Tracer blocks the
	// drain proportionally — implementations that fan out to remote
	// observers should buffer.
	OnExchange(Exchange)
}

// TracerFunc adapts a plain function to the Tracer interface.
type TracerFunc func(Exchange)

// OnExchange satisfies Tracer.
func (f TracerFunc) OnExchange(ex Exchange) { f(ex) }

// JSONLTracer writes one Exchange per line as JSON to w. Concurrent
// OnExchange calls are serialised by the embedded mutex so output stays
// line-aligned across Runners that share a tracer.
type JSONLTracer struct {
	mu  sync.Mutex
	w   io.Writer
	enc *json.Encoder
}

// NewJSONLTracer returns a Tracer that writes JSONL to w. Errors writing
// to w are silently swallowed — the tracer is a diagnostic surface, not a
// load-bearing data path; the drain must not fail because the trace
// destination went away. Wrap w with a buffered writer if you need
// throughput on a slow sink.
//
// HTML escaping is disabled on the encoder: the trace is not embedded in
// HTML, and the redaction markers (<redacted>) and valueShape output
// (<format:string>, <ref cursor.last_timestamp>, ...) read as
// nonsense when '<'/'>'/'&' get rewritten to \u003c/\u003e/\u0026.
func NewJSONLTracer(w io.Writer) *JSONLTracer {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &JSONLTracer{w: w, enc: enc}
}

// OnExchange serialises ex as a single JSON line.
func (t *JSONLTracer) OnExchange(ex Exchange) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.enc.Encode(ex)
}

// Flush drains any buffered records to the underlying sink and fsyncs
// regular files, mirroring JSONLSink.Flush. Callers should invoke this
// before shutdown so the last few exchanges survive a crash. Errors are
// returned so the caller can log them, but the tracer itself never fails
// the drain (OnExchange swallows errors by design).
func (t *JSONLTracer) Flush() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if fl, ok := t.w.(interface{ Flush() error }); ok {
		if err := fl.Flush(); err != nil {
			return err
		}
	}
	f, ok := t.w.(*os.File)
	if !ok {
		return nil
	}
	err := f.Sync()
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}

// redactedValue is the string substituted into Exchange fields whose
// runtime value carries a secret. Kept identical to redact.go's marker
// so consumers can grep for one token.
const redactedValue = "<redacted>"

// sensitiveHeaderNames is the always-redact allowlist of header names
// (case-insensitive). Auth-block-specific names (auth.custom.header,
// auth.api_key.header) are added per-request on top of this set.
var sensitiveHeaderNames = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
}

// httpTrace is the internal scratchpad executeRequest populates when a
// Tracer is attached to the Runner. The runIteration step composes it
// into a public Exchange (adding iteration / phase / step id) and
// dispatches to the Tracer.
type httpTrace struct {
	method       string
	finalURL     *url.URL
	headers      http.Header
	reqBodyMeta  string
	statusCode   int
	respBodyMeta string
	startedAt    time.Time
	elapsed      time.Duration
}

// buildExchange composes the public Exchange from the IR request, the
// internal trace scratchpad, an optional error, and the iteration/phase
// context known only to runIteration. doc is required for the IsSecret
// checks; iter is 1-based.
func buildExchange(doc *schema.Doc, req schema.Request, t *httpTrace, runErr error, iter int, phase string) Exchange {
	ex := Exchange{
		Iteration:      iter,
		Phase:          phase,
		StepID:         req.ID,
		Method:         t.method,
		URL:            safeURL(t.finalURL),
		Query:          redactedQuery(doc, req),
		RequestHeaders: redactedHeaders(doc, req, doc.Auth, t.headers),
		RequestBody:    t.reqBodyMeta,
		Status:         t.statusCode,
		ResponseBody:   t.respBodyMeta,
		StartedAt:      t.startedAt.UTC(),
		Elapsed:        t.elapsed,
	}
	if runErr != nil {
		ex.Error = redactURLError(runErr).Error()
	}
	return ex
}

// redactedQuery renders req.Query as a string map, replacing values whose
// IR Value would resolve through a secret-typed state field with
// "<redacted>". Auth.api_key with in_query=true contributes its named key
// as <redacted> too — the runtime injects the credential into the URL,
// but the trace never carries its value.
//
// The returned map is keyed by the IR-declared param name, not the
// resolved URL.Query() encoding. Non-secret values are pretty-printed via
// the value-shape helper so the trace shows structural intent
// ({ref state.foo}, {literal "x"}, {now}) rather than the resolved
// runtime string. That keeps an accidental future leak through a
// non-IsSecret path (e.g. someone introduces a new Value variant that
// IsSecret hasn't been taught yet) from putting raw values in the trace.
func redactedQuery(doc *schema.Doc, req schema.Request) map[string]string {
	out := make(map[string]string, len(req.Query))
	for k, v := range req.Query {
		if schema.IsSecret(doc, v) {
			out[k] = redactedValue
			continue
		}
		out[k] = valueShape(v)
	}
	if doc.Auth.APIKey != nil && doc.Auth.APIKey.InQuery {
		out[doc.Auth.APIKey.Header] = redactedValue
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// redactedHeaders walks the post-auth wire-header set h, redacting values
// for header names in the always-sensitive allowlist, header names that
// originate from the auth block (auth.custom.header, auth.api_key.header
// when not in_query), and IR-declared headers whose Value would resolve
// through a secret-typed state field.
//
// The set of keys is preserved so the operator can see WHICH headers were
// sent — only the values change.
func redactedHeaders(doc *schema.Doc, req schema.Request, auth schema.Auth, h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	// Build a per-name redact decision before walking h, so two requests
	// in the same iteration don't share decisions.
	redactByName := map[string]bool{}
	for name := range sensitiveHeaderNames {
		redactByName[name] = true
	}
	if auth.Custom != nil {
		redactByName[strings.ToLower(auth.Custom.Header)] = true
	}
	if auth.APIKey != nil && !auth.APIKey.InQuery {
		redactByName[strings.ToLower(auth.APIKey.Header)] = true
	}
	for hdrName, hdrVal := range req.Headers {
		if schema.IsSecret(doc, hdrVal) {
			redactByName[strings.ToLower(hdrName)] = true
		}
	}

	out := make(map[string]string, len(h))
	for name, vals := range h {
		key := strings.ToLower(name)
		if redactByName[key] {
			out[name] = redactedValue
			continue
		}
		// Multiple values join with ", " — same convention as net/http.
		out[name] = strings.Join(vals, ", ")
	}
	return out
}

// bodyClass returns the bodyMetadata classifier (object-like, array-like,
// html-like, string-like, non-json) for a body that has already been
// fully read. Mirrors the bodyMetadata string-shape but lets the trace
// emit the metadata separately from any error path. n is the byte count
// the trace should report (the caller has it; we don't recompute).
func bodyMeta(n int, raw []byte) string {
	if n == 0 {
		return "body empty"
	}
	return fmt.Sprintf("body %d bytes, %s", n, bodyClassName(raw))
}

// bodyClassName classifies raw by its leading non-whitespace byte. Mirror
// of the inner switch in bodyMetadata, factored out so trace metadata
// stays consistent with error metadata.
func bodyClassName(b []byte) string {
	trimmed := trimLeadingWhitespace(b)
	if len(trimmed) == 0 {
		return "whitespace-only"
	}
	switch trimmed[0] {
	case '{':
		return "object-like"
	case '[':
		return "array-like"
	case '<':
		return "html-like"
	case '"':
		return "string-like"
	}
	return "non-json"
}

// trimLeadingWhitespace returns b with any ASCII whitespace stripped from
// the front. Used by bodyClassName to classify by first content byte.
func trimLeadingWhitespace(b []byte) []byte {
	i := 0
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return b[i:]
		}
	}
	return nil
}
