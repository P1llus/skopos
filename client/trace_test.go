// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestTracer_BasicExchange pins the Tracer interface: every HTTP
// exchange surfaces as an Exchange record with method, URL, status, and
// timing fields populated.
func TestTracer_BasicExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	tracer := &captureTracer{}
	r := &Runner{Doc: minimalDoc(server.URL), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tracer}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(tracer.exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(tracer.exchanges))
	}
	ex := tracer.exchanges[0]
	if ex.Method != "GET" {
		t.Errorf("Method = %q, want GET", ex.Method)
	}
	if ex.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", ex.Status)
	}
	if ex.Iteration != 1 {
		t.Errorf("Iteration = %d, want 1", ex.Iteration)
	}
	if !strings.Contains(ex.URL, "/events") {
		t.Errorf("URL = %q", ex.URL)
	}
	// safeURL strips userinfo + query; URL should not include any query.
	if strings.Contains(ex.URL, "?") {
		t.Errorf("URL leaks query string: %q", ex.URL)
	}
}

// TestTracer_AuthorizationRedacted pins the always-sensitive header
// allowlist: Authorization is replaced with <redacted>.
func TestTracer_AuthorizationRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	tracer := &captureTracer{}
	doc := bearerDoc(server.URL, "leaky-token")
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tracer}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(tracer.exchanges) != 1 {
		t.Fatalf("exchanges = %d", len(tracer.exchanges))
	}
	ex := tracer.exchanges[0]
	if got := ex.RequestHeaders["Authorization"]; got != "<redacted>" {
		t.Errorf("Authorization = %q, want <redacted>", got)
	}
}

// TestTracer_SecretHeaderRedacted pins the IsSecret-driven header
// redaction: a custom header whose Value is a secret-typed ref is
// redacted by name.
func TestTracer_SecretHeaderRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["api_key"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("xyz"))}
	doc.Requests[0].Headers = map[string]schema.Value{
		"X-Token":  vRef("state.api_key"),
		"X-Public": vStr("ok"),
	}

	tracer := &captureTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tracer}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	ex := tracer.exchanges[0]
	if got := ex.RequestHeaders["X-Token"]; got != "<redacted>" {
		t.Errorf("X-Token = %q, want <redacted>", got)
	}
	if got := ex.RequestHeaders["X-Public"]; got != "ok" {
		t.Errorf("X-Public = %q, want ok", got)
	}
}

// TestTracer_APIKeyInQueryRedacted pins the in_query=true api_key
// branch: the named query key appears with value <redacted>.
func TestTracer_APIKeyInQueryRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["k"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("super-secret"))}
	doc.Auth = schema.Auth{APIKey: &schema.APIKeyAuth{
		Header:  "apikey",
		Value:   vRef("state.k"),
		InQuery: true,
	}}

	tracer := &captureTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tracer}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	ex := tracer.exchanges[0]
	if got := ex.Query["apikey"]; got != "<redacted>" {
		t.Errorf("Query[apikey] = %q, want <redacted>", got)
	}
}

// TestTracer_BodyMetadataOnly pins the redaction-safe body surface: the
// trace carries only length + content-class, never raw bytes.
func TestTracer_BodyMetadataOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []any{},
			"secret": "would-leak-without-meta",
		})
	}))
	defer server.Close()

	tracer := &captureTracer{}
	r := &Runner{Doc: minimalDoc(server.URL), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tracer}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	ex := tracer.exchanges[0]
	if !strings.Contains(ex.ResponseBody, "bytes") || !strings.Contains(ex.ResponseBody, "object-like") {
		t.Errorf("ResponseBody = %q, want metadata format", ex.ResponseBody)
	}
	if strings.Contains(ex.ResponseBody, "would-leak-without-meta") {
		t.Errorf("ResponseBody leaks payload: %q", ex.ResponseBody)
	}
}

// TestTracer_CacheHitTombstone pins the cache-HIT tombstone shape: a
// step served from cache.* surfaces as a minimal Exchange (Iteration +
// StepID + CacheHit=true), wire fields stay zero.
func TestTracer_CacheHitTombstone(t *testing.T) {
	var loginHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		loginHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      "sess",
			"expires_in": float64(3600),
		})
	})
	var eventsHits atomic.Int32
	mux.HandleFunc("/events", func(w http.ResponseWriter, _ *http.Request) {
		n := eventsHits.Add(1)
		body := map[string]any{"events": []any{map[string]any{"id": "e"}}}
		if n < 3 {
			body["next_cursor"] = "p"
		}
		writeJSON(w, http.StatusOK, body)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url":        {Type: "url", Default: ptrValue(vStr(server.URL))},
			"next_token": {Type: "string"},
		},
		Auth: schema.Auth{Bearer: &schema.BearerAuth{
			Token: vRefDefault("cache.session.token", vStr("p")),
		}},
		Requests: []schema.Request{
			{
				ID:     "login",
				Method: "POST",
				URL:    mustInterp("${state.url}/login"),
				Cache: &schema.Cache{
					To:        mustPath("cache.session"),
					ExpiresAt: vRefDefault("response.body.expires_in", vStr("1h")),
					Buffer:    "60s",
				},
			},
			{
				ID:             "events",
				Method:         "GET",
				URL:            mustInterp("${state.url}/events"),
				ProducesEvents: true,
			},
		},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
		Pagination: schema.Pagination{CursorToken: &schema.CursorTokenPagination{
			From: mustPath("response.body.next_cursor"),
			To:   mustPath("state.next_token"),
		}},
	}

	tracer := &captureTracer{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Tracer: tracer}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// Look for at least one cache-HIT tombstone among the login step's
	// exchanges (iteration 2+).
	var hitFound bool
	for _, ex := range tracer.exchanges {
		if ex.StepID == "login" && ex.CacheHit {
			hitFound = true
			if ex.Method != "" || ex.URL != "" || ex.Status != 0 {
				t.Errorf("cache-HIT tombstone leaks wire fields: %+v", ex)
			}
		}
	}
	if !hitFound {
		t.Errorf("expected at least one cache-HIT tombstone for login step; exchanges: %+v", tracer.exchanges)
	}
}

// TestJSONLTracer_RoundTripJSONL pins the JSONLTracer: one Exchange per
// line, valid JSON.
func TestJSONLTracer_RoundTripJSONL(t *testing.T) {
	var buf bytes.Buffer
	tracer := NewJSONLTracer(&buf)
	tracer.OnExchange(Exchange{Iteration: 1, StepID: "a", Method: "GET", URL: "http://x/y", Status: 200})
	tracer.OnExchange(Exchange{Iteration: 2, StepID: "b", CacheHit: true})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	for i, line := range lines {
		var got Exchange
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
		}
	}
}
