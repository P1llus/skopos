// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
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
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
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
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
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
