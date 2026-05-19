// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestAuth_NoneAttachesNothing pins the none variant: no Authorization
// header is added.
func TestAuth_NoneAttachesNothing(t *testing.T) {
	var seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	r := &Runner{Doc: minimalDoc(server.URL), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seenAuth != "" {
		t.Errorf("Authorization = %q, want empty", seenAuth)
	}
}

// TestAuth_BearerAttachesToken pins the bearer variant: the token Value
// is evaluated and prefixed with "Bearer ".
func TestAuth_BearerAttachesToken(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := bearerDoc(server.URL, "tok-abc")
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seen != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want Bearer tok-abc", seen)
	}
}

// TestAuth_BasicAttachesBase64 pins the basic variant: Authorization
// header = "Basic " + base64(user:pass).
func TestAuth_BasicAttachesBase64(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["user"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("admin"))}
	doc.State["pass"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("hunter2"))}
	doc.Auth = schema.Auth{Basic: &schema.BasicAuth{
		Username: vRef("state.user"),
		Password: vRef("state.pass"),
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:hunter2"))
	if seen != want {
		t.Errorf("Authorization = %q, want %q", seen, want)
	}
}

// TestAuth_APIKeyHeader pins the api_key (header) variant.
func TestAuth_APIKeyHeader(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-API-Key")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["k"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("api-token"))}
	doc.Auth = schema.Auth{APIKey: &schema.APIKeyAuth{
		Header: "X-API-Key",
		Value:  vRef("state.k"),
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seen != "api-token" {
		t.Errorf("X-API-Key = %q, want api-token", seen)
	}
}

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
	doc.State["k"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("query-token"))}
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

// TestAuth_CustomHeader pins the custom variant: a single custom-named
// header carries the value.
func TestAuth_CustomHeader(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Custom-Auth")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["v"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("custom-value"))}
	doc.Auth = schema.Auth{Custom: &schema.CustomAuth{
		Header: "X-Custom-Auth",
		Value:  vRef("state.v"),
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seen != "custom-value" {
		t.Errorf("X-Custom-Auth = %q, want custom-value", seen)
	}
}

// TestAuth_MultiModeDispatch pins the multi_mode variant: the first
// branch whose predicate is true wins; otherwise the default Auth.
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
		doc.State["mode"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr(mode))}
		doc.State["token"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("bearer-tok"))}
		doc.State["key"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("api-tok"))}
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

	t.Run("api_key_branch_wins", func(t *testing.T) {
		got = sample{}
		r := &Runner{Doc: build("api_key"), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
		if err := r.Drain(context.Background()); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if got.APIKey != "api-tok" {
			t.Errorf("api_key mode X-API-Key = %q", got.APIKey)
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

// TestAuth_OAuth2ClientCredentials_CacheReusedAcrossPages pins the
// unified Cache block path within one drain: the first iteration
// fetches a fresh token, writes cache.<name>, and the subsequent
// pagination iterations re-use it without re-hitting the token
// endpoint.
func TestAuth_OAuth2ClientCredentials_CacheReusedAcrossPages(t *testing.T) {
	var tokenHits atomic.Int32
	var dataHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		tokenHits.Add(1)
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Basic ") {
			t.Errorf("token endpoint Authorization = %q, want Basic ...", got)
		}
		bs, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(bs))
		if form.Get("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", form.Get("grant_type"))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "fresh-tok",
			"expires_in":   float64(3600),
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		n := dataHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-tok" {
			t.Errorf("data Authorization = %q", got)
		}
		body := map[string]any{"events": []any{map[string]any{"id": "e"}}}
		if n < 3 {
			body["next_cursor"] = "page"
		}
		writeJSON(w, http.StatusOK, body)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: ptrValue(mustInterp(server.URL + "/oauth/token"))}
	doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("test-client"))}
	doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("test-secret"))}
	doc.State["next_token"] = schema.FieldDecl{Type: "string"}
	doc.Requests[0].URL = mustInterp("${state.url}/data")
	doc.Pagination = schema.Pagination{CursorToken: &schema.CursorTokenPagination{
		From: mustPath("response.body.next_cursor"),
		To:   mustPath("state.next_token"),
	}}
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
	if dataHits.Load() < 2 {
		t.Fatalf("dataHits = %d, want >= 2 (pagination loop didn't fire)", dataHits.Load())
	}
	if tokenHits.Load() != 1 {
		t.Errorf("tokenHits = %d, want 1 (cache HIT on subsequent iterations of the same drain)", tokenHits.Load())
	}
}

// TestAuth_OAuth2_CacheNotPersisted pins the post-redesign semantic: a
// fresh Runner does NOT inherit the previous run's cache slot. Cache is
// process memory only; a runner restart forces a token re-fetch.
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
		doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: ptrValue(mustInterp(server.URL + "/oauth/token"))}
		doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("c"))}
		doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("s"))}
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
		// Persistent shared store (FileStore-like behaviour).
		return &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), Store: &MemoryStore{}}
	}

	// Each fresh runner forces a token re-fetch because cache.* never
	// rides through the store.
	for i := 0; i < 3; i++ {
		if err := build().Drain(context.Background()); err != nil {
			t.Fatalf("Drain %d: %v", i, err)
		}
	}
	if tokenHits.Load() != 3 {
		t.Errorf("tokenHits = %d, want 3 (each fresh Runner re-fetches)", tokenHits.Load())
	}
}

// TestAuth_OAuth2_PasswordGrant pins the password_grant variant: the
// token endpoint receives grant_type=password and the user credentials
// in the form body.
func TestAuth_OAuth2_PasswordGrant(t *testing.T) {
	var form url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(bs))
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "pw-tok",
			"expires_in":   float64(3600),
		})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: ptrValue(mustInterp(server.URL + "/oauth/token"))}
	doc.State["user"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("alice"))}
	doc.State["pass"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("hunter2"))}
	doc.Requests[0].URL = mustInterp("${state.url}/data")
	doc.Auth = schema.Auth{OAuth2: &schema.OAuth2Auth{
		PasswordGrant: &schema.PasswordGrant{
			TokenURL: vRef("state.token_url"),
			Username: vRef("state.user"),
			Password: vRef("state.pass"),
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
	if form.Get("grant_type") != "password" {
		t.Errorf("grant_type = %q, want password", form.Get("grant_type"))
	}
	if form.Get("username") != "alice" || form.Get("password") != "hunter2" {
		t.Errorf("password grant form: %v", form)
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
	doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: ptrValue(mustInterp(server.URL + "/oauth/token"))}
	doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("c"))}
	doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("s"))}
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
			"url":        {Type: "url", Default: ptrValue(vStr(server.URL))},
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
		Response: schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
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
// short expires_in means the buffer immediately invalidates the cache;
// every iteration re-fetches.
func TestRequestCache_ExpiryRefetches(t *testing.T) {
	var loginHits atomic.Int32
	var eventsHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		loginHits.Add(1)
		// expires_in=1s + buffer 60s → always stale at the next
		// iteration; every iteration's login step misses cache.
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
		Response: schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
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
	doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: ptrValue(mustInterp(server.URL + "/token"))}
	doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("c"))}
	doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("s"))}
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

// TestOAuth2_TokenResponseBodyBindForExpiresAt pins the cache.expires_at
// evaluation: the resolved body of the token endpoint feeds the
// expires_at Value. A response.body.expires_in ref reads from there.
func TestOAuth2_TokenResponseBodyBindForExpiresAt(t *testing.T) {
	tokenBody := map[string]any{
		"access_token": "tok",
		"expires_in":   float64(7200),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		bs, _ := json.Marshal(tokenBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bs)
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["token_url"] = schema.FieldDecl{Type: "url", Default: ptrValue(mustInterp(server.URL + "/token"))}
	doc.State["client_id"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("c"))}
	doc.State["client_secret"] = schema.FieldDecl{Type: "secret", Default: ptrValue(vStr("s"))}
	doc.Requests[0].URL = mustInterp("${state.url}/data")
	doc.Auth = schema.Auth{OAuth2: &schema.OAuth2Auth{
		ClientCredentials: &schema.ClientCredentialsGrant{
			TokenURL:     vRef("state.token_url"),
			ClientID:     vRef("state.client_id"),
			ClientSecret: vRef("state.client_secret"),
			Cache: &schema.Cache{
				To:        mustPath("cache.access_token"),
				ExpiresAt: vRef("response.body.expires_in"),
				Buffer:    "60s",
			},
		},
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// No assertion beyond "no error" — the proof is that the token's
	// expires_in body field was used; a regression here surfaces as
	// "cache.expires_at: cannot interpret <nil> as time".
}
