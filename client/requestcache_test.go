// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1llus/skopos/schema"
)

// sessionLoginCachedDoc builds a two-step "POST /login → ride the token"
// Doc: a login step carrying requests[].cache, then an events step that
// rides the cached token as Bearer auth. The runtime counterpart of
// templates/session_login_cached.yml.
//
// The bearer ref carries a default so the login step itself (auth is
// applied to every request) does not fail on the first drain before the
// cache slot is populated; the login mock ignores the Authorization header.
func sessionLoginCachedDoc(baseURL string, cache *schema.RequestCache) *schema.Doc {
	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":      {Type: "url", Default: baseURL},
			"username": {Type: "string", Default: "admin"},
			"password": {Type: "secret", Default: "test-password"},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth: schema.Auth{Bearer: &schema.BearerAuth{
			Token: vRefDefault("state.session_token", vStr("pending")),
		}},
		Requests: []schema.Request{
			{
				ID:     "login",
				Method: "POST",
				Path:   ptrValue(vStr("/api/v1/login")),
				Body: &schema.Body{JSON: map[string]schema.Value{
					"username": vRef("state.username"),
					"password": vRef("state.password"),
				}},
				Cache: cache,
			},
			{
				ID:             "events",
				Method:         "GET",
				Path:           ptrValue(vStr("/api/v1/events")),
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
	// Emulate the IR validator's auto-registration of the cache.store_in slot
	// as a runtime-mutable state field so snapshot() persists it across drains.
	if cache != nil && cache.StoreIn != "" {
		doc.State.Fields[cache.StoreIn] = schema.FieldDecl{Type: "string", Mutability: "runtime"}
	}
	return doc
}

// sessionLoginServers wires a login mock + an events mock and returns both
// with atomic call counters. The login mock returns sessionToken + a
// duration-shaped expires_in; the events mock validates the Bearer token.
type sessionLoginServers struct {
	login      *httptest.Server
	events     *httptest.Server
	loginCalls atomic.Int32
	eventCalls atomic.Int32
}

func newSessionLoginServers(t *testing.T, sessionToken string, expiresIn int) *sessionLoginServers {
	t.Helper()
	s := &sessionLoginServers{}
	s.login = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.loginCalls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("login method = %s, want POST", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session_token": sessionToken,
			"expires_in":    expiresIn,
		})
	}))
	t.Cleanup(s.login.Close)

	s.events = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.eventCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+sessionToken {
			t.Errorf("events Authorization = %q, want Bearer %s", got, sessionToken)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	t.Cleanup(s.events.Close)
	return s
}

// TestRequestCache_LoginFiresThenCached drives two drains over the same
// Store: drain 1 runs the login step and caches the token; drain 2 finds
// the cache still inside its expiry buffer and skips the login step while
// still riding the cached token on the events request.
func TestRequestCache_LoginFiresThenCached(t *testing.T) {
	srv := newSessionLoginServers(t, "session-tok-1", 3600)

	doc := sessionLoginCachedDoc(srv.events.URL, &schema.RequestCache{
		StoreIn:      "session_token",
		ExpiryField:  mustPath("expires_in"),
		ExpiryBuffer: "60s",
		ExpiryFormat: "duration",
	})
	// Point the login step at its own mock (the events mock is the base_url).
	doc.Requests[0].URL = ptrValue(vStr(srv.login.URL + "/api/v1/login"))
	doc.Requests[0].Path = nil

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: srv.events.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}

	if got := srv.loginCalls.Load(); got != 1 {
		t.Errorf("login calls = %d, want 1 (cache hit on drain 2)", got)
	}
	if got := srv.eventCalls.Load(); got != 2 {
		t.Errorf("events calls = %d, want 2 (events step runs every drain)", got)
	}

	snap, _ := store.Load()
	if got, ok := snap.State["session_token"].(string); !ok || got != "session-tok-1" {
		t.Errorf("state.session_token = %v (ok=%v), want session-tok-1", snap.State["session_token"], ok)
	}
	if _, ok := snap.Cursor["__step_session_token_expires_at"].(string); !ok {
		t.Errorf("cursor.__step_session_token_expires_at = %v, want RFC 3339 string",
			snap.Cursor["__step_session_token_expires_at"])
	}
}

// TestRequestCache_ExpiredRefetch advances the clock past the cached
// token's expiry buffer and asserts the next drain re-runs the login step.
func TestRequestCache_ExpiredRefetch(t *testing.T) {
	srv := newSessionLoginServers(t, "session-tok-fresh", 60)

	doc := sessionLoginCachedDoc(srv.events.URL, &schema.RequestCache{
		StoreIn:      "session_token",
		ExpiryField:  mustPath("expires_in"),
		ExpiryBuffer: "30s",
		ExpiryFormat: "duration",
	})
	doc.Requests[0].URL = ptrValue(vStr(srv.login.URL + "/api/v1/login"))
	doc.Requests[0].Path = nil

	clock := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	advance := time.Duration(0)
	now := func() time.Time { return clock.Add(advance) }

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: now, Client: srv.events.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	// Token valid for 60s; buffer 30s. Advance 45s → now+buffer (75s) >
	// expiry (60s) → the login step re-fires.
	advance = 45 * time.Second
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}

	if got := srv.loginCalls.Load(); got != 2 {
		t.Errorf("login calls = %d, want 2 (cached token expired)", got)
	}
}

// TestRequestCache_ExpiredViaStaleCursor force-expires the cache by priming
// the Store with a stale __step_<store_in>_expires_at cursor key, then
// asserts the drain re-runs the login step rather than trusting the slot.
func TestRequestCache_ExpiredViaStaleCursor(t *testing.T) {
	srv := newSessionLoginServers(t, "session-tok-restamp", 3600)

	doc := sessionLoginCachedDoc(srv.events.URL, &schema.RequestCache{
		StoreIn:      "session_token",
		ExpiryField:  mustPath("expires_in"),
		ExpiryBuffer: "60s",
		ExpiryFormat: "duration",
	})
	doc.Requests[0].URL = ptrValue(vStr(srv.login.URL + "/api/v1/login"))
	doc.Requests[0].Path = nil

	// Prime a populated-but-stale cache: a token slot plus an expiry stamp
	// already in the past relative to fixedNow().
	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	store := &MemoryStore{}
	_ = store.Save(Snapshot{
		State:  map[string]any{"session_token": "stale-token"},
		Cursor: map[string]any{"__step_session_token_expires_at": stale},
	})

	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: srv.events.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if got := srv.loginCalls.Load(); got != 1 {
		t.Errorf("login calls = %d, want 1 (stale cursor stamp forces re-login)", got)
	}
	snap, _ := store.Load()
	if got := snap.State["session_token"]; got != "session-tok-restamp" {
		t.Errorf("state.session_token = %v, want session-tok-restamp (re-login overwrites stale slot)", got)
	}
}

// TestRequestCache_InvalidateCache asserts on_status:{401: invalidate_cache}
// clears the step cache and forces a fresh login on the next drain. Drain 1
// logs in, the events request 401s and invalidate_cache drops the slot;
// drain 2 must re-run the login step rather than reusing the cleared token.
func TestRequestCache_InvalidateCache(t *testing.T) {
	var loginCalls, eventCalls atomic.Int32
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		loginCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"session_token": "session-tok-roundtrip",
			"expires_in":    3600,
		})
	}))
	defer login.Close()

	events := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := eventCalls.Add(1)
		// First drain: pretend the cached session was revoked → 401.
		// Second drain: serve events normally.
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer events.Close()

	doc := sessionLoginCachedDoc(events.URL, &schema.RequestCache{
		StoreIn:      "session_token",
		ExpiryField:  mustPath("expires_in"),
		ExpiryBuffer: "60s",
		ExpiryFormat: "duration",
	})
	doc.Requests[0].URL = ptrValue(vStr(login.URL + "/api/v1/login"))
	doc.Requests[0].Path = nil
	doc.Requests[1].ExpectStatus = []int{200}
	doc.Requests[1].OnStatus = map[int]string{401: "invalidate_cache"}

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: events.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	// After drain 1 the step cache must be cleared by invalidate_cache.
	snap, _ := store.Load()
	if _, ok := snap.State["session_token"]; ok {
		t.Errorf("state.session_token = %v after invalidate_cache; want missing", snap.State["session_token"])
	}
	if _, ok := snap.Cursor["__step_session_token_expires_at"]; ok {
		t.Errorf("cursor.__step_session_token_expires_at = %v after invalidate_cache; want missing",
			snap.Cursor["__step_session_token_expires_at"])
	}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}
	if got := loginCalls.Load(); got != 2 {
		t.Errorf("login calls = %d, want 2 (invalidate_cache forced a re-login)", got)
	}
}

// TestStepCacheExpiry_Formats pins how expiry_format interprets the value at
// expiry_field: a remaining lifetime (duration / default) versus an absolute
// instant (unix_seconds, rfc3339).
func TestStepCacheExpiry_Formats(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	abs := time.Date(2026, 5, 12, 13, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		format string
		field  string
		body   map[string]any
		want   time.Time
	}{
		{
			name:   "duration_default",
			format: "",
			field:  "expires_in",
			body:   map[string]any{"expires_in": float64(3600)},
			want:   now.Add(time.Hour),
		},
		{
			name:   "duration_explicit",
			format: "duration",
			field:  "ttl",
			body:   map[string]any{"ttl": "1h"},
			want:   now.Add(time.Hour),
		},
		{
			name:   "unix_seconds",
			format: "unix_seconds",
			field:  "expires_at",
			body:   map[string]any{"expires_at": float64(abs.Unix())},
			want:   abs,
		},
		{
			name:   "rfc3339",
			format: "rfc3339",
			field:  "expires_at",
			body:   map[string]any{"expires_at": abs.Format(time.RFC3339)},
			want:   abs,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := &schema.RequestCache{
				StoreIn:      "session_token",
				ExpiryField:  mustPath(tc.field),
				ExpiryBuffer: "60s",
				ExpiryFormat: tc.format,
			}
			got, err := stepCacheExpiry(now, tc.body, cache)
			if err != nil {
				t.Fatalf("stepCacheExpiry: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("stepCacheExpiry = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStoreStepValue_MissingField asserts a cache block whose store_in does
// not name a field in the response body fails loudly rather than caching an
// empty slot.
func TestStoreStepValue_MissingField(t *testing.T) {
	s := newTestScope(t, nil, nil)
	cache := &schema.RequestCache{
		StoreIn:      "session_token",
		ExpiryField:  mustPath("expires_in"),
		ExpiryBuffer: "60s",
	}
	err := s.storeStepValue(cache, map[string]any{"expires_in": float64(3600)})
	if err == nil {
		t.Fatal("storeStepValue returned nil, want missing-field error")
	}
}
