// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// recordingTracer captures every Exchange the runner emits. Single-Drain
// access is serial inside Runner.Drain; the mutex is defensive in case a
// future test shares one tracer across runners.
type recordingTracer struct {
	mu        sync.Mutex
	exchanges []Exchange
}

func (r *recordingTracer) OnExchange(ex Exchange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exchanges = append(r.exchanges, ex)
}

func (r *recordingTracer) get() []Exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Exchange, len(r.exchanges))
	copy(out, r.exchanges)
	return out
}

// TestTracer_BearerSimple_RedactsAuth covers the single-request case: one
// Exchange is emitted, Authorization is replaced with "<redacted>", URL
// has no query string, and the body metadata is shown (not the bytes).
func TestTracer_BearerSimple_RedactsAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []map[string]any{
				{"id": "e1", "timestamp": "2026-05-12T08:00:00Z"},
			},
		})
	}))
	defer server.Close()

	tr := &recordingTracer{}
	doc := bearerSimpleDoc(server.URL, "super-sensitive-bearer")
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tr}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	exs := tr.get()
	if len(exs) != 1 {
		t.Fatalf("emitted %d exchanges, want 1", len(exs))
	}
	ex := exs[0]
	if ex.Iteration != 1 {
		t.Errorf("Iteration = %d, want 1", ex.Iteration)
	}
	if ex.Method != "GET" {
		t.Errorf("Method = %q, want GET", ex.Method)
	}
	if ex.Status != 200 {
		t.Errorf("Status = %d, want 200", ex.Status)
	}
	// URL must be safe: no query, no userinfo, just scheme+host+path.
	if !strings.HasSuffix(ex.URL, "/api/v1/events") {
		t.Errorf("URL = %q, want suffix /api/v1/events", ex.URL)
	}
	if strings.Contains(ex.URL, "?") {
		t.Errorf("URL contained query string: %q", ex.URL)
	}

	// Authorization must be redacted by name even though its value is not
	// declared via headers (bearer pushes it through applyAuth).
	authVal, present := ex.RequestHeaders["Authorization"]
	if !present {
		t.Fatalf("Authorization header missing from trace: %+v", ex.RequestHeaders)
	}
	if authVal != redactedValue {
		t.Errorf("Authorization = %q, want %q", authVal, redactedValue)
	}
	if strings.Contains(authVal, "super-sensitive-bearer") {
		t.Errorf("trace leaked bearer token: %q", authVal)
	}

	// Non-sensitive Accept header should survive verbatim so the operator
	// can confirm the wire content negotiation.
	if got := ex.RequestHeaders["Accept"]; !strings.Contains(got, "application/json") {
		t.Errorf("Accept = %q, want substring application/json", got)
	}

	// Body metadata only, never raw bytes.
	if got := ex.ResponseBody; !strings.Contains(got, "object-like") {
		t.Errorf("ResponseBody = %q, want object-like classifier", got)
	}
	if strings.Contains(ex.ResponseBody, "events") {
		t.Errorf("ResponseBody leaked content: %q", ex.ResponseBody)
	}

	if ex.Elapsed < 0 {
		t.Errorf("Elapsed = %v, want >= 0", ex.Elapsed)
	}
}

// TestTracer_CursorToken_PerPage proves a multi-page drain emits one
// Exchange per page, in order, with monotonically increasing Iteration
// counters. The cursor query parameter shows the IR shape — not the
// resolved runtime value — but is at least present so the operator can
// confirm pagination plumbing.
func TestTracer_CursorToken_PerPage(t *testing.T) {
	pages := []map[string]any{
		{"findings": []map[string]any{{"id": "f1", "created_at": "2026-05-12T08:00:00Z"}}, "next_cursor": "tok-2"},
		{"findings": []map[string]any{{"id": "f2", "created_at": "2026-05-12T08:05:00Z"}}, "next_cursor": ""},
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		idx := int(hits.Add(1)) - 1
		writeJSON(w, http.StatusOK, pages[idx])
	}))
	defer server.Close()

	tr := &recordingTracer{}
	r := &Runner{
		Doc:    cursorTokenDoc(server.URL, "test-token"),
		Sink:   &captureSink{},
		Now:    fixedNow(),
		Client: server.Client(),
		Tracer: tr,
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	exs := tr.get()
	if len(exs) != 2 {
		t.Fatalf("traces = %d, want 2", len(exs))
	}
	if exs[0].Iteration != 1 || exs[1].Iteration != 2 {
		t.Errorf("Iteration sequence = %d,%d, want 1,2", exs[0].Iteration, exs[1].Iteration)
	}
	// The IR declares query.cursor = {from_pagination: token}. Trace must
	// show the structural shape, not the runtime resolved token.
	got := exs[0].Query["cursor"]
	if got != "<from_pagination:token>" {
		t.Errorf("page 0 cursor query shape = %q, want <from_pagination:token>", got)
	}
}

// TestTracer_AsyncPoll_PhaseLabel covers the async_job submit/poll path:
// each Exchange carries the phase ("submit", "poll", "fetch") so a
// triaging operator can see which step in the phase machine ran.
func TestTracer_AsyncPoll_PhaseLabel(t *testing.T) {
	var pollPending atomic.Bool
	pollPending.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/exports" && r.Method == http.MethodPost:
			writeJSON(w, http.StatusAccepted, map[string]any{"export_id": "exp-1"})
		case strings.HasSuffix(r.URL.Path, "/status"):
			if pollPending.Load() {
				pollPending.Store(false)
				writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":     "complete",
				"result_url": fmt.Sprintf("http://%s/api/v1/exports/exp-1/data", r.Host),
			})
		case strings.HasSuffix(r.URL.Path, "/data"):
			writeJSON(w, http.StatusOK, map[string]any{
				"items": []map[string]any{{"id": "evt-1", "ts": "2026-05-12T09:00:00Z"}},
			})
		}
	}))
	defer server.Close()

	doc := asyncPollDoc(server.URL, "test-token")
	store := &MemoryStore{}
	tr := &recordingTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client(), Tracer: tr}

	// Drain 1: submit (phase=submit) + poll (phase=poll, pending → no fetch).
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	d1 := tr.get()
	if len(d1) != 2 {
		t.Fatalf("drain 1 exchanges = %d, want 2", len(d1))
	}
	if d1[0].Phase != "submit" || d1[0].StepID != "submit" {
		t.Errorf("drain 1 [0] phase/id = %q/%q, want submit/submit", d1[0].Phase, d1[0].StepID)
	}
	if d1[1].Phase != "poll" || d1[1].StepID != "poll" {
		t.Errorf("drain 1 [1] phase/id = %q/%q, want poll/poll", d1[1].Phase, d1[1].StepID)
	}

	// Drain 2: poll (complete) + fetch.
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}
	all := tr.get()
	d2 := all[len(d1):]
	if len(d2) != 2 {
		t.Fatalf("drain 2 exchanges = %d, want 2", len(d2))
	}
	if d2[0].Phase != "poll" || d2[1].Phase != "fetch" {
		t.Errorf("drain 2 phases = %q,%q, want poll,fetch", d2[0].Phase, d2[1].Phase)
	}
}

// TestTracer_TransportError_RedactsURL shows the trace records transport
// errors safely: when client.Do fails (here: a closed server / nonexistent
// port), the error string passes through redactURLError so any
// query-string credential cannot leak. We also check that Status stays 0.
func TestTracer_TransportError_RedactsURL(t *testing.T) {
	// Bind a port, then close immediately so a subsequent dial fails.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	addr := server.URL
	server.Close()

	doc := bearerSimpleDoc(addr, "test-token")
	doc.Error = &schema.ErrorBlock{Mode: "warn"} // surface the error via trace, keep drain non-fatal

	tr := &recordingTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Tracer: tr}
	// drain swallows transport errors under warn mode (logs them), so we
	// don't fail on Drain error.
	_ = r.Drain(context.Background())

	exs := tr.get()
	if len(exs) != 1 {
		t.Fatalf("traces = %d, want 1", len(exs))
	}
	ex := exs[0]
	if ex.Status != 0 {
		t.Errorf("Status = %d on transport failure, want 0", ex.Status)
	}
	if ex.Error == "" {
		t.Fatalf("Error empty; expected a transport error description")
	}
	// The error must not include a query string. The bearer template has
	// since=<RFC3339>, which Go's *url.Error would otherwise embed.
	if strings.Contains(ex.Error, "since=") {
		t.Errorf("Error leaked query string: %q", ex.Error)
	}
}

// TestTracer_APIKeyInQuery_Redacted covers the in-URL credential case:
// auth.api_key with in_query=true puts the credential into RawQuery. The
// trace must redact the api-key parameter name's value (and the URL must
// not carry it either via safeURL).
func TestTracer_APIKeyInQuery_Redacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: server.URL},
			"key": {Type: "secret", Default: "super-sensitive-api-key"},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth: schema.Auth{APIKey: &schema.APIKeyAuth{
			Header:  "api_key",
			Value:   vRef("state.key"),
			InQuery: true,
		}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}

	tr := &recordingTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tr}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	exs := tr.get()
	if len(exs) != 1 {
		t.Fatalf("traces = %d, want 1", len(exs))
	}
	ex := exs[0]
	if strings.Contains(ex.URL, "super-sensitive-api-key") {
		t.Errorf("URL leaked api key: %q", ex.URL)
	}
	got := ex.Query["api_key"]
	if got != redactedValue {
		t.Errorf("Query[api_key] = %q, want %q", got, redactedValue)
	}
}

// TestTracer_SecretHeader_Redacted covers req.Headers entries whose IR
// Value transitively reaches a secret-typed state field — those values
// must be redacted in the trace by schema.IsSecret, not by header name.
func TestTracer_SecretHeader_Redacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":   {Type: "url", Default: server.URL},
			"creds": {Type: "secret", Default: "very-secret"},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
			Headers: map[string]schema.Value{
				"X-Custom-Auth": vRef("state.creds"),
				"X-Tenant-Id":   vStr("tenant-42"),
			},
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}

	tr := &recordingTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tr}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	exs := tr.get()
	if len(exs) != 1 {
		t.Fatalf("traces = %d, want 1", len(exs))
	}
	got := exs[0].RequestHeaders["X-Custom-Auth"]
	if got != redactedValue {
		t.Errorf("X-Custom-Auth = %q, want %q", got, redactedValue)
	}
	if exs[0].RequestHeaders["X-Tenant-Id"] != "tenant-42" {
		t.Errorf("X-Tenant-Id = %q, want tenant-42 (non-secret passes through)", exs[0].RequestHeaders["X-Tenant-Id"])
	}
}

// TestJSONLTracer_WritesOneLinePerExchange covers the bundled JSONL
// tracer: each exchange must be one valid JSON object on its own line.
func TestJSONLTracer_WritesOneLinePerExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []map[string]any{{"id": "e1", "timestamp": "2026-05-12T08:00:00Z"}}})
	}))
	defer server.Close()

	var buf bytes.Buffer
	tracer := NewJSONLTracer(&buf)
	doc := bearerSimpleDoc(server.URL, "test-token")
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tracer}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d output lines, want 1", len(lines))
	}
	var got Exchange
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("JSON decode: %v\nline: %s", err, lines[0])
	}
	if got.Method != "GET" {
		t.Errorf("decoded Method = %q, want GET", got.Method)
	}
	if got.RequestHeaders["Authorization"] != redactedValue {
		t.Errorf("decoded Authorization = %q, want %q", got.RequestHeaders["Authorization"], redactedValue)
	}
}

// TestTracer_RequestBodyMetadata covers the body-metadata surface: when
// a request carries a body (e.g. async_job submit's JSON), the trace
// describes its length and classification without surfacing bytes.
func TestTracer_RequestBodyMetadata(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ = io.ReadAll(r.Body)
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: server.URL},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{{
			Method: "POST",
			Path:   ptrValue(vStr("/echo")),
			Body:   &schema.Body{JSON: map[string]schema.Value{"hello": vStr("world")}},
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}

	tr := &recordingTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tr}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	exs := tr.get()
	if len(exs) != 1 {
		t.Fatalf("traces = %d, want 1", len(exs))
	}
	if exs[0].RequestBody == "" {
		t.Fatalf("RequestBody empty; expected metadata")
	}
	if !strings.Contains(exs[0].RequestBody, "object-like") {
		t.Errorf("RequestBody = %q, want object-like classifier", exs[0].RequestBody)
	}
	if !strings.Contains(exs[0].RequestBody, strconv.Itoa(len(requestBody))) {
		t.Errorf("RequestBody = %q, want byte count %d", exs[0].RequestBody, len(requestBody))
	}
	// Body content must NOT appear in the trace.
	if strings.Contains(exs[0].RequestBody, "world") || strings.Contains(exs[0].RequestBody, "hello") {
		t.Errorf("RequestBody leaked content: %q", exs[0].RequestBody)
	}
	// The server must still have received the actual body (the
	// io-replay rewind is sound).
	if !bytes.Contains(requestBody, []byte("world")) {
		t.Errorf("server received body %q, missing expected hello=world payload", requestBody)
	}
}
