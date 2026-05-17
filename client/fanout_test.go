// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// fanOutListDoc builds a two-step Doc: a "list" step that returns the
// items the fan_out iterates over, and a "detail" step that fans out per
// item. The detail step is the implicit producer (last request); events
// flow through response.events_at = "" against the merged body.
func fanOutListDoc(baseURL, asName, merge string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: baseURL},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "list",
				Method: "GET",
				Path:   ptrValue(vStr("/api/v1/incidents")),
			},
			{
				ID:     "detail",
				Method: "GET",
				Path: ptrValue(vConcat(
					vStr("/api/v1/incidents/"),
					vRef(asName+".id"),
					vStr("/details"),
				)),
				FanOut: &schema.FanOut{
					Over:  vRef("steps.list.body.incidents"),
					As:    asName,
					Merge: merge,
				},
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
}

// TestFanOut_FlattenConcatenatesPerItemLists exercises the canonical
// filebeat-style pattern: list returns IDs, fan_out hits a detail endpoint
// per ID, each detail returns a list of events, merge: flatten concatenates
// them into one events stream that the producer iteration emits.
func TestFanOut_FlattenConcatenatesPerItemLists(t *testing.T) {
	var detailHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/incidents":
			writeJSON(w, http.StatusOK, map[string]any{
				"incidents": []map[string]any{
					{"id": "inc-1"},
					{"id": "inc-2"},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/incidents/") && strings.HasSuffix(r.URL.Path, "/details"):
			detailHits.Add(1)
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/incidents/"), "/details")
			writeJSON(w, http.StatusOK, []map[string]any{
				{"id": id + "-a", "incident": id},
				{"id": id + "-b", "incident": id},
			})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()

	sink := &captureSink{}
	r := &Runner{Doc: fanOutListDoc(server.URL, "incident", "flatten"), Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := detailHits.Load(); got != 2 {
		t.Fatalf("detail hits = %d, want 2 (one per incident)", got)
	}
	// flatten of two 2-element lists is one 4-element events stream.
	if len(sink.events) != 4 {
		t.Fatalf("emitted %d events, want 4 (2 incidents × 2 events each)", len(sink.events))
	}
	wantIDs := []string{"inc-1-a", "inc-1-b", "inc-2-a", "inc-2-b"}
	for i, ev := range sink.events {
		m, ok := ev.(map[string]any)
		if !ok {
			t.Fatalf("event[%d] type = %T, want map", i, ev)
		}
		if m["id"] != wantIDs[i] {
			t.Errorf("event[%d].id = %v, want %s", i, m["id"], wantIDs[i])
		}
	}
}

// TestFanOut_Wrap keeps per-item bodies intact as elements of a list.
// Useful when each per-item response is itself a single object (not a list).
func TestFanOut_Wrap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/incidents" {
			writeJSON(w, http.StatusOK, map[string]any{
				"incidents": []map[string]any{{"id": "a"}, {"id": "b"}},
			})
			return
		}
		// Per-item endpoint returns a single object (NOT a list).
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/incidents/"), "/details")
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "shape": "object"})
	}))
	defer server.Close()

	sink := &captureSink{}
	r := &Runner{Doc: fanOutListDoc(server.URL, "incident", "wrap"), Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// merge: wrap produces a 2-element list of per-item objects. events_at
	// is the body root, so each wrapped object emits as one event.
	if len(sink.events) != 2 {
		t.Fatalf("emitted %d events, want 2 (wrap → list of per-item bodies)", len(sink.events))
	}
}

// TestFanOut_FlattenRejectsNonListBody surfaces the flatten contract: each
// per-item response body must decode as a JSON list. A non-list shape is a
// template error, not a silent merge.
func TestFanOut_FlattenRejectsNonListBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/incidents" {
			writeJSON(w, http.StatusOK, map[string]any{
				"incidents": []map[string]any{{"id": "x"}},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"not": "a list"})
	}))
	defer server.Close()

	r := &Runner{Doc: fanOutListDoc(server.URL, "incident", "flatten"), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: nil error, want flatten-non-list rejection")
	}
	if !strings.Contains(err.Error(), "flatten requires") {
		t.Errorf("err = %v, want substring 'flatten requires'", err)
	}
}

// TestFanOut_EmptyOver tolerates an empty / unresolved over list: zero
// items, zero detail requests, merged body is an empty list, no events.
func TestFanOut_EmptyOver(t *testing.T) {
	var detailHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/incidents" {
			writeJSON(w, http.StatusOK, map[string]any{
				"incidents": []map[string]any{},
			})
			return
		}
		detailHits.Add(1)
		t.Errorf("detail endpoint hit on empty over: %s", r.URL.Path)
	}))
	defer server.Close()

	sink := &captureSink{}
	r := &Runner{Doc: fanOutListDoc(server.URL, "incident", "flatten"), Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := detailHits.Load(); got != 0 {
		t.Errorf("detail hits = %d, want 0 (empty over)", got)
	}
	if len(sink.events) != 0 {
		t.Errorf("emitted %d events, want 0", len(sink.events))
	}
}

// TestFanOut_OnStatusSkipPerItem asserts that an individual item that
// returns a non-success status with on_status: skip is dropped without
// failing the rest of the fan-out.
func TestFanOut_OnStatusSkipPerItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/incidents" {
			writeJSON(w, http.StatusOK, map[string]any{
				"incidents": []map[string]any{{"id": "ok"}, {"id": "bad"}, {"id": "ok2"}},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/bad/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/incidents/"), "/details")
		writeJSON(w, http.StatusOK, []map[string]any{{"id": id + "-ev"}})
	}))
	defer server.Close()

	doc := fanOutListDoc(server.URL, "incident", "flatten")
	doc.Requests[1].OnStatus = map[int]string{403: "skip"}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// "ok" and "ok2" contribute one event each; "bad" was skipped.
	if len(sink.events) != 2 {
		t.Fatalf("emitted %d events, want 2 (one bad item skipped)", len(sink.events))
	}
}

// TestFanOut_ItemBindingResolvesAuthorName confirms that the runtime
// resolves the author-chosen fan_out.as name (e.g. "incident") rather
// than a hardcoded "item". Each detail request must carry the per-item
// id in the path.
func TestFanOut_ItemBindingResolvesAuthorName(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/incidents" {
			writeJSON(w, http.StatusOK, map[string]any{
				"incidents": []map[string]any{{"id": "alpha"}, {"id": "beta"}},
			})
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/incidents/"), "/details")
		seen[id] = true
		writeJSON(w, http.StatusOK, []map[string]any{{"item_id": id}})
	}))
	defer server.Close()

	r := &Runner{Doc: fanOutListDoc(server.URL, "incident", "flatten"), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	for _, want := range []string{"alpha", "beta"} {
		if !seen[want] {
			t.Errorf("server never saw detail request for %q (fan_out.as binding %q did not resolve)", want, "incident")
		}
	}
}

// TestFanOut_ValidatorRejectsCacheCombination guards the schema decision:
// requests[].cache and fan_out are mutually exclusive. The validator must
// surface that with a clear error before the doc reaches the runner.
func TestFanOut_ValidatorRejectsCacheCombination(t *testing.T) {
	doc := fanOutListDoc("http://localhost:0", "incident", "flatten")
	doc.Requests[1].Cache = &schema.RequestCache{
		StoreIn:      "tok",
		ExpiryField:  mustPath("response.body.expires_in"),
		ExpiryBuffer: "60s",
	}
	err := schema.Validate(doc)
	if err == nil {
		t.Fatal("Validate: nil, want cache+fan_out rejection")
	}
	if !strings.Contains(fmt.Sprint(err), "mutually exclusive") {
		t.Errorf("err = %v, want substring 'mutually exclusive'", err)
	}
}
