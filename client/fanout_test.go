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

// TestFanOut_FlattenMerge pins the default merge=flatten behaviour: each
// per-item response is a list, and the merged body is the concatenation.
func TestFanOut_FlattenMerge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"incidents": []any{
			map[string]any{"id": "INC-1"},
			map[string]any{"id": "INC-2"},
		}})
	})
	mux.HandleFunc("/incidents/INC-1/events", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []any{
			map[string]any{"id": "e1-a"},
			map[string]any{"id": "e1-b"},
		})
	})
	mux.HandleFunc("/incidents/INC-2/events", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []any{
			map[string]any{"id": "e2-a"},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				URL:    mustInterp("${state.url}/incidents"),
			},
			{
				ID:     "events",
				Method: "GET",
				URL:    mustInterp("${state.url}/incidents/${incident.id}/events"),
				FanOut: &schema.FanOut{
					Over: vRef("steps.list.body.incidents"),
					As:   "incident",
				},
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("")},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 3 {
		t.Errorf("events = %d, want 3 (2 from INC-1 + 1 from INC-2 via flatten)", len(sink.events))
	}
}

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
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
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
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("")},
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
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
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
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("")},
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
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
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
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("")},
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

// TestFanOut_ItemRefResolves pins the per-item binding: refs rooted at
// the fan_out.as alias resolve to the active item.
func TestFanOut_ItemRefResolves(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{
			map[string]any{"id": "x", "label": "X-mark"},
			map[string]any{"id": "y", "label": "Y-mark"},
		}})
	})
	mux.HandleFunc("/detail/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		writeJSON(w, http.StatusOK, []any{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
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
				URL:    mustInterp("${state.url}/detail/${rec.id}-${rec.label}"),
				FanOut: &schema.FanOut{
					Over: vRef("steps.list.body.items"),
					As:   "rec",
				},
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("")},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("seen = %v, want two requests", seen)
	}
	if !strings.HasSuffix(seen[0], "/x-X-mark") {
		t.Errorf("first request path = %q, want /detail/x-X-mark", seen[0])
	}
	if !strings.HasSuffix(seen[1], "/y-Y-mark") {
		t.Errorf("second request path = %q, want /detail/y-Y-mark", seen[1])
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
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
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
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("")},
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
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
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
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("")},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Error("Drain: expected fail on item b")
	}
}
