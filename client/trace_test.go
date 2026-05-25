// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestTracer_APIKeyInQueryRedacted pins the in_query=true api_key
// branch: the named query key appears with value <redacted>.
func TestTracer_APIKeyInQueryRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["k"] = schema.FieldDecl{Type: "secret", Default: new(vStr("super-secret"))}
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
			"url":        {Type: "url", Default: new(vStr(server.URL))},
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
		Response: schema.Response{Decode: decodeJSON(), EventsAt: mustPath("response.body.events")},
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
