// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestFanOut_WrapMerge pins the wrap variant: each per-item body lands
// as-is in the merged list (one element per item).
func TestFanOut_WrapMerge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ids": []any{"a", "b"}})
	})
	mux.HandleFunc("/detail/a", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "a", "name": "Alpha"})
	})
	mux.HandleFunc("/detail/b", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "b", "name": "Beta"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: new(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				URL:    mustInterp("${state.url}/list"),
			},
			{
				ID:     "detail",
				Method: "GET",
				URL:    mustInterp("${state.url}/detail/${id}"),
				FanOut: &schema.FanOut{
					Over:  vRef("steps.list.body.ids"),
					As:    "id",
					Merge: "wrap",
				},
				EventsAt: pathPtr(""),
			},
		},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 2 {
		t.Errorf("events = %d, want 2 (one per item in wrap mode)", len(sink.events))
	}
}

// TestFanOut_EmptyOverIsNoop pins the no-items case: an empty fan_out
// list runs the step zero times and surfaces no events.
func TestFanOut_EmptyOverIsNoop(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ids": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: new(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				URL:    mustInterp("${state.url}/list"),
			},
			{
				ID:     "detail",
				Method: "GET",
				URL:    mustInterp("${state.url}/detail/${id}"),
				FanOut: &schema.FanOut{
					Over: vRef("steps.list.body.ids"),
					As:   "id",
				},
				EventsAt: pathPtr(""),
			},
		},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 0 {
		t.Errorf("events = %d, want 0 (empty fan-out)", len(sink.events))
	}
}

// TestFanOut_PerItemSkipDoesNotStopChain pins the on_status:skip
// per-item dispatch: one item's 404 doesn't block the others.
func TestFanOut_PerItemSkipDoesNotStopChain(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ids": []any{"a", "b", "c"}})
	})
	mux.HandleFunc("/detail/a", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits["a"]++
		mu.Unlock()
		writeJSON(w, http.StatusOK, []any{map[string]any{"id": "ea"}})
	})
	mux.HandleFunc("/detail/b", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits["b"]++
		mu.Unlock()
		http.Error(w, "gone", http.StatusNotFound)
	})
	mux.HandleFunc("/detail/c", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits["c"]++
		mu.Unlock()
		writeJSON(w, http.StatusOK, []any{map[string]any{"id": "ec"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: new(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				URL:    mustInterp("${state.url}/list"),
			},
			{
				ID:       "detail",
				Method:   "GET",
				URL:      mustInterp("${state.url}/detail/${id}"),
				OnStatus: map[int]string{http.StatusNotFound: "skip"},
				FanOut: &schema.FanOut{
					Over: vRef("steps.list.body.ids"),
					As:   "id",
				},
				EventsAt: pathPtr(""),
			},
		},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if hits["a"] != 1 || hits["b"] != 1 || hits["c"] != 1 {
		t.Errorf("hits = %v, want each item once", hits)
	}
	if len(sink.events) != 2 {
		t.Errorf("events = %d, want 2 (a + c; b skipped)", len(sink.events))
	}
}

// TestFanOut_FlattenRejectsNonListBody pins the flatten contract: every
// per-item body MUST decode as a list. A non-list body is a hard error.
func TestFanOut_FlattenRejectsNonListBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ids": []any{"a"}})
	})
	mux.HandleFunc("/detail/a", func(w http.ResponseWriter, _ *http.Request) {
		// Object body, not a list — flatten can't merge.
		writeJSON(w, http.StatusOK, map[string]any{"hello": "world"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: new(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				URL:    mustInterp("${state.url}/list"),
			},
			{
				ID:     "detail",
				Method: "GET",
				URL:    mustInterp("${state.url}/detail/${id}"),
				FanOut: &schema.FanOut{
					Over: vRef("steps.list.body.ids"),
					As:   "id",
				},
				EventsAt: pathPtr(""),
			},
		},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Error("Drain: expected flatten error on non-list body, got nil")
	}
}

// TestFanOut_PerItemFailFatal pins the fail dispatch on a fan-out step:
// drain aborts on the first item that hits the fail verb.
func TestFanOut_PerItemFailFatal(t *testing.T) {
	var seen atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ids": []any{"a", "b", "c"}})
	})
	mux.HandleFunc("/detail/", func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if strings.HasSuffix(r.URL.Path, "/b") {
			http.Error(w, "gone", http.StatusGone)
			return
		}
		writeJSON(w, http.StatusOK, []any{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: new(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				URL:    mustInterp("${state.url}/list"),
			},
			{
				ID:       "detail",
				Method:   "GET",
				URL:      mustInterp("${state.url}/detail/${id}"),
				OnStatus: map[int]string{http.StatusGone: "fail"},
				FanOut: &schema.FanOut{
					Over: vRef("steps.list.body.ids"),
					As:   "id",
				},
				EventsAt: pathPtr(""),
			},
		},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Error("Drain: expected fail on item b")
	}
}

// TestFanOut_PerItemInvalidateCacheSkipsPaginationAdvance pins the
// iterInvalidate contract for fan_out: a per-item on_status:invalidate_cache
// must drop reachable cache slots, stop the fan-out, AND skip pagination
// advance — so the same page retries on the next iteration with the
// freshly-evicted caches. Without the skip, pagination would advance past
// the poisoned page and miss events.
func TestFanOut_PerItemInvalidateCacheSkipsPaginationAdvance(t *testing.T) {
	var tokenHits atomic.Int32
	var listHits atomic.Int32
	var detailHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		tokenHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "tok",
			"expires_in":   float64(3600),
		})
	})
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		listHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ids": []any{"a", "b"}})
	})
	mux.HandleFunc("/detail/", func(w http.ResponseWriter, _ *http.Request) {
		n := detailHits.Add(1)
		if n == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, []any{map[string]any{"id": "e"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url":           {Type: "url", Default: new(vStr(server.URL))},
			"token_url":     {Type: "url", Default: new(mustInterp(server.URL + "/oauth/token"))},
			"client_id":     {Type: "string", Default: new(vStr("c"))},
			"client_secret": {Type: "secret", Default: new(vStr("s"))},
		},
		Auth: schema.Auth{OAuth2: &schema.OAuth2Auth{
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
		}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				URL:    mustInterp("${state.url}/list"),
			},
			{
				ID:       "detail",
				Method:   "GET",
				URL:      mustInterp("${state.url}/detail/${id}"),
				OnStatus: map[int]string{http.StatusUnauthorized: "invalidate_cache"},
				FanOut: &schema.FanOut{
					Over: vRef("steps.list.body.ids"),
					As:   "id",
				},
				EventsAt: pathPtr(""),
			},
		},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if got := tokenHits.Load(); got != 2 {
		t.Errorf("tokenHits = %d, want 2 (initial + after invalidate_cache)", got)
	}
	if got := listHits.Load(); got != 2 {
		t.Errorf("listHits = %d, want 2 (page retried after invalidate_cache)", got)
	}
	// detail: first iteration's item "a" gets 401 (1 hit, fan-out stops),
	// second iteration runs both items successfully (2 hits). Total 3.
	if got := detailHits.Load(); got != 3 {
		t.Errorf("detailHits = %d, want 3 (1 poisoned + 2 retried)", got)
	}
	// Both items from the retry iteration emit one event each.
	if len(sink.events) != 2 {
		t.Errorf("events = %d, want 2 (both items emitted after retry)", len(sink.events))
	}
}
