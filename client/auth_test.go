// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestAuth_APIKeyInQuery pins the api_key in_query=true variant: the key
// is added to the URL query string instead of a header.
func TestAuth_APIKeyInQuery(t *testing.T) {
	var seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query().Get("apikey")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["k"] = schema.FieldDecl{Type: "secret", Default: new(vStr("query-token"))}
	doc.Auth = schema.Auth{APIKey: &schema.APIKeyAuth{
		Header:  "apikey",
		Value:   vRef("state.k"),
		InQuery: true,
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seenQuery != "query-token" {
		t.Errorf("?apikey = %q, want query-token", seenQuery)
	}
}

// TestAuth_MultiModeDispatch pins the multi_mode dispatch arms that no
// integration golden covers: the bearer-branch arm and the default arm
// when no branch matches. The api_key arm is exercised end-to-end by
// the multi_mode_auth.txt fixture.
func TestAuth_MultiModeDispatch(t *testing.T) {
	type sample struct{ Bearer, APIKey string }
	var got sample
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = sample{
			Bearer: r.Header.Get("Authorization"),
			APIKey: r.Header.Get("X-API-Key"),
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	build := func(mode string) *schema.Doc {
		doc := minimalDoc(server.URL)
		doc.State["mode"] = schema.FieldDecl{Type: "string", Default: new(vStr(mode))}
		doc.State["token"] = schema.FieldDecl{Type: "secret", Default: new(vStr("bearer-tok"))}
		doc.State["key"] = schema.FieldDecl{Type: "secret", Default: new(vStr("api-tok"))}
		doc.Auth = schema.Auth{MultiMode: &schema.MultiModeAuth{
			Branches: []schema.AuthBranch{
				{
					When: schema.Predicate{Eq: &schema.PredicateEq{
						Path: mustPath("state.mode"), Value: vStr("bearer"),
					}},
					Auth: schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.token")}},
				},
				{
					When: schema.Predicate{Eq: &schema.PredicateEq{
						Path: mustPath("state.mode"), Value: vStr("api_key"),
					}},
					Auth: schema.Auth{APIKey: &schema.APIKeyAuth{Header: "X-API-Key", Value: vRef("state.key")}},
				},
			},
			Default: schema.Auth{None: &struct{}{}},
		}}
		return doc
	}

	t.Run("bearer_branch_wins", func(t *testing.T) {
		got = sample{}
		r := &Runner{Doc: build("bearer"), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
		if err := r.Drain(context.Background()); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if got.Bearer != "Bearer bearer-tok" {
			t.Errorf("bearer mode Authorization = %q", got.Bearer)
		}
	})

	t.Run("default_when_no_branch_matches", func(t *testing.T) {
		got = sample{}
		r := &Runner{Doc: build("other"), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
		if err := r.Drain(context.Background()); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if got.Bearer != "" || got.APIKey != "" {
			t.Errorf("default branch should attach nothing; got %+v", got)
		}
	})
}

// TestAuth_OAuth2_CacheNotPersisted pins the cache-is-process-memory
// invariant: a fresh Runner does NOT inherit the previous run's cache
// slot, so each restart forces a token re-fetch.
func TestAuth_OAuth2_CacheNotPersisted(t *testing.T) {
	var tokenHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		tokenHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "tok-from-fresh-runner",
			"expires_in":   float64(3600),
		})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	build := func() *Runner {
		doc := minimalDoc(server.URL)
		doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: new(mustInterp(server.URL + "/oauth/token"))}
		doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: new(vStr("c"))}
		doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: new(vStr("s"))}
		doc.Requests[0].URL = mustInterp("${state.url}/data")
		doc.Auth = schema.Auth{OAuth2: &schema.OAuth2Auth{
			ClientCredentials: &schema.ClientCredentialsGrant{
				TokenURL:     vRef("state.token_url"),
				ClientID:     vRef("state.client_id"),
				ClientSecret: vRef("state.client_secret"),
				Cache: &schema.Cache{
					To:        mustPath("cache.access_token"),
					ExpiresAt: vRefDefault("response.body.expires_in", vStr("1h")),
					Buffer:    "60s",
				},
			},
		}}
		return &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Store: &MemoryStore{}}
	}

	for i := range 3 {
		if err := build().Drain(context.Background()); err != nil {
			t.Fatalf("Drain %d: %v", i, err)
		}
	}
	if tokenHits.Load() != 3 {
		t.Errorf("tokenHits = %d, want 3 (each fresh Runner re-fetches)", tokenHits.Load())
	}
}

// TestAuth_OAuth2_InvalidateCacheRefetches pins on_status:
// invalidate_cache: a 401 from the data endpoint clears cache.<name>
// and the next iteration re-fetches the token.
func TestAuth_OAuth2_InvalidateCacheRefetches(t *testing.T) {
	var tokenHits atomic.Int32
	var dataHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		tokenHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "tok",
			"expires_in":   float64(3600),
		})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		n := dataHits.Add(1)
		if n == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: new(mustInterp(server.URL + "/oauth/token"))}
	doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: new(vStr("c"))}
	doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: new(vStr("s"))}
	doc.Requests[0].URL = mustInterp("${state.url}/data")
	doc.Requests[0].OnStatus = map[int]string{http.StatusUnauthorized: "invalidate_cache"}
	doc.Auth = schema.Auth{OAuth2: &schema.OAuth2Auth{
		ClientCredentials: &schema.ClientCredentialsGrant{
			TokenURL:     vRef("state.token_url"),
			ClientID:     vRef("state.client_id"),
			ClientSecret: vRef("state.client_secret"),
			Cache: &schema.Cache{
				To:        mustPath("cache.access_token"),
				ExpiresAt: vRefDefault("response.body.expires_in", vStr("1h")),
				Buffer:    "60s",
			},
		},
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if tokenHits.Load() != 2 {
		t.Errorf("tokenHits = %d, want 2 (initial + after invalidate)", tokenHits.Load())
	}
	if dataHits.Load() != 2 {
		t.Errorf("dataHits = %d, want 2 (401 + 200)", dataHits.Load())
	}
}

// TestRequestCache_HitSkipsWireCall pins requests[].cache within one
// drain: the first iteration runs the login step, writes
// cache.<name>; subsequent iterations re-use the cache slot without an
// HTTP round trip.
func TestRequestCache_HitSkipsWireCall(t *testing.T) {
	var loginHits atomic.Int32
	var eventsHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		loginHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      "session-xyz",
			"expires_in": float64(3600),
		})
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		n := eventsHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer session-xyz" {
			t.Errorf("Authorization = %q, want Bearer session-xyz", got)
		}
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
			Token: vRefDefault("cache.session.token", vStr("pending")),
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

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if loginHits.Load() != 1 {
		t.Errorf("loginHits = %d, want 1 (cache HIT on subsequent iterations)", loginHits.Load())
	}
	if eventsHits.Load() < 2 {
		t.Errorf("eventsHits = %d, want >= 2", eventsHits.Load())
	}
}

// TestRequestCache_ExpiryRefetches pins the cache expiry semantics: a
// short expires_in plus a buffer that exceeds it means every iteration
// sees the slot as stale and re-fetches.
func TestRequestCache_ExpiryRefetches(t *testing.T) {
	var loginHits atomic.Int32
	var eventsHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		loginHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      "s",
			"expires_in": float64(1),
		})
	})
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
					ExpiresAt: vRefDefault("response.body.expires_in", vStr("1s")),
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

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if loginHits.Load() != eventsHits.Load() {
		t.Errorf("loginHits=%d eventsHits=%d; want equal (every iteration's cache is stale)", loginHits.Load(), eventsHits.Load())
	}
}

// TestOAuth2_TokenEndpointBodyNotLeaked pins the redaction-safe error
// path: a non-2xx token endpoint response does not include the raw body
// in the error.
func TestOAuth2_TokenEndpointBodyNotLeaked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_client","leaky_secret":"hunter2"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: new(mustInterp(server.URL + "/token"))}
	doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: new(vStr("c"))}
	doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: new(vStr("s"))}
	doc.Error = &schema.ErrorBlock{Mode: "fail"}
	doc.Auth = schema.Auth{OAuth2: &schema.OAuth2Auth{
		ClientCredentials: &schema.ClientCredentialsGrant{
			TokenURL:     vRef("state.token_url"),
			ClientID:     vRef("state.client_id"),
			ClientSecret: vRef("state.client_secret"),
		},
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: expected token endpoint error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("token endpoint error leaks body: %v", err)
	}
}
