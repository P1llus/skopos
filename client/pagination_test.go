// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestPagination_NoneSinglePage pins the none variant: exactly one page
// per drain.
func TestPagination_NoneSinglePage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"id": "e1"},
		}})
	}))
	defer server.Close()

	sink := &captureSink{}
	r := &Runner{Doc: minimalDoc(server.URL), Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("server calls = %d, want 1 (none variant)", calls.Load())
	}
}

// TestPagination_CursorTokenAdvancesAndStops walks two pages with a
// cursor_token plan, then a third page with no next_cursor terminates
// the loop.
func TestPagination_CursorTokenAdvancesAndStops(t *testing.T) {
	pages := []map[string]any{
		{"events": []any{map[string]any{"id": "e1"}}, "next_cursor": "page-2"},
		{"events": []any{map[string]any{"id": "e2"}}, "next_cursor": "page-3"},
		{"events": []any{map[string]any{"id": "e3"}}}, // no next_cursor → terminate
	}
	var idx atomic.Int32
	var seenCursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCursors = append(seenCursors, r.URL.Query().Get("cursor"))
		i := idx.Add(1) - 1
		writeJSON(w, http.StatusOK, pages[i])
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["next_token"] = schema.FieldDecl{Type: "string"}
	doc.Requests[0].URL = mustInterp("${state.url}/events")
	doc.Requests[0].Query = map[string]schema.Value{
		"cursor": vRefDefault("state.next_token", vStr("")),
	}
	doc.Pagination = schema.Pagination{CursorToken: &schema.CursorTokenPagination{
		From: mustPath("response.body.next_cursor"),
		To:   mustPath("state.next_token"),
	}}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 3 {
		t.Errorf("events = %d, want 3", len(sink.events))
	}
	if len(seenCursors) != 3 {
		t.Fatalf("cursor query observed %d times, want 3", len(seenCursors))
	}
	if got := seenCursors[0]; got != "" {
		t.Errorf("page 1 cursor = %q, want empty (bootstrap default)", got)
	}
	if got := seenCursors[1]; got != "page-2" {
		t.Errorf("page 2 cursor = %q, want page-2", got)
	}
}

// TestPagination_NextURLLinkHeader pins the Link-header shape: the
// regex extracts the URL between < and >; rel="next" picks the right
// header value when multiple links exist.
func TestPagination_NextURLLinkHeader(t *testing.T) {
	var idx atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := idx.Add(1) - 1
		if i == 0 {
			w.Header().Set("Link", `<`+server.URL+`/events?p=2>; rel="next", <`+server.URL+`/events?p=1>; rel="self"`)
			writeJSON(w, http.StatusOK, map[string]any{"events": []any{
				map[string]any{"id": "e1"},
			}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"id": "e2"},
		}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["next_url"] = schema.FieldDecl{Type: "url"}
	doc.Requests[0].URL = vRefDefault("state.next_url", mustInterp("${state.url}/events"))
	doc.Pagination = schema.Pagination{NextURL: &schema.NextURLPagination{
		From:    mustPath("response.header.Link"),
		To:      mustPath("state.next_url"),
		Regex:   `<([^>]+)>;\s*rel="next"`,
		Capture: 1,
	}}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 2 {
		t.Errorf("events = %d, want 2", len(sink.events))
	}
}

// TestPagination_CounterShortPageTerminates pins the counter variant's
// default terminate_when (events.count < step). Page-number style with
// start: 1, step: 1.
func TestPagination_CounterShortPageTerminates(t *testing.T) {
	var idx atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		i := idx.Add(1)
		_ = i
		// step:1 — terminate fires on the first short page (zero
		// events), so the page-1 response carries one event and page-2
		// (zero events) ends the loop.
		var events []any
		if page == 1 {
			events = []any{map[string]any{"id": "e1"}}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["page"] = schema.FieldDecl{Type: "int"}
	doc.Requests[0].URL = mustInterp("${state.url}/events")
	doc.Requests[0].Query = map[string]schema.Value{
		"page": vRefDefault("state.page", vInt(1)),
	}
	doc.Pagination = schema.Pagination{Counter: &schema.CounterPagination{
		To: mustPath("state.page"),
	}}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 1 {
		t.Errorf("events = %d, want 1 (page 1 returns one event, page 2 empty → terminate)", len(sink.events))
	}
	if idx.Load() != 2 {
		t.Errorf("server calls = %d, want 2", idx.Load())
	}
}

// TestPagination_CounterWithExplicitTerminate pins overriding the
// default short-page predicate with an explicit has_next signal.
func TestPagination_CounterWithExplicitTerminate(t *testing.T) {
	var idx atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		idx.Add(1)
		body := map[string]any{
			"events": []any{map[string]any{"id": fmt.Sprintf("e-page-%d", page)}},
			"meta":   map[string]any{"has_next": page < 3},
		}
		writeJSON(w, http.StatusOK, body)
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["page"] = schema.FieldDecl{Type: "int"}
	doc.Requests[0].Query = map[string]schema.Value{
		"page": vRefDefault("state.page", vInt(1)),
	}
	hasNextPath := mustPath("response.body.meta.has_next")
	doc.Pagination = schema.Pagination{Counter: &schema.CounterPagination{
		To: mustPath("state.page"),
		TerminateWhen: &schema.Predicate{Not: &schema.Predicate{
			Eq: &schema.PredicateEq{Path: hasNextPath, Value: vBool(true)},
		}},
	}}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if idx.Load() != 3 {
		t.Errorf("server calls = %d, want 3 (pages 1-3 all return events, has_next:false on 3)", idx.Load())
	}
	if len(sink.events) != 3 {
		t.Errorf("events = %d, want 3", len(sink.events))
	}
}

// TestPagination_CustomAdvancesAndTerminates pins the author-controlled
// custom variant: explicit advance writes, explicit terminate_when.
func TestPagination_CustomAdvancesAndTerminates(t *testing.T) {
	pages := []map[string]any{
		{"events": []any{map[string]any{"id": "e1"}}, "next": "n2"},
		{"events": []any{map[string]any{"id": "e2"}}, "next": "n3"},
		{"events": []any{map[string]any{"id": "e3"}}}, // no next
	}
	var idx atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := idx.Add(1) - 1
		writeJSON(w, http.StatusOK, pages[i])
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["cursor"] = schema.FieldDecl{Type: "string"}
	nextPath := mustPath("response.body.next")
	doc.Pagination = schema.Pagination{Custom: &schema.CustomPagination{
		Advance: []schema.AdvanceWrite{
			{To: mustPath("state.cursor"), From: vRef("response.body.next")},
		},
		TerminateWhen: schema.Predicate{Not: &schema.Predicate{Present: &nextPath}},
	}}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 3 {
		t.Errorf("events = %d, want 3", len(sink.events))
	}
}

// TestPagination_PerDrainWipeResetsScratch pins the per-drain wipe: a
// drain that ended mid-page does not re-use last drain's pagination
// cursor.
func TestPagination_PerDrainWipeResetsScratch(t *testing.T) {
	var seenCursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCursors = append(seenCursors, r.URL.Query().Get("cursor"))
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["next_token"] = schema.FieldDecl{Type: "string"}
	doc.Requests[0].Query = map[string]schema.Value{
		"cursor": vRefDefault("state.next_token", vStr("")),
	}
	doc.Pagination = schema.Pagination{CursorToken: &schema.CursorTokenPagination{
		From: mustPath("response.body.next_cursor"),
		To:   mustPath("state.next_token"),
	}}

	store := &MemoryStore{}
	// Pre-seed the store with a stale next_token; the per-drain wipe
	// should reset it back to its declared default (unset → bootstrap
	// default kicks in).
	_ = store.Save(Snapshot{State: map[string]any{"next_token": "stale-tok"}})

	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(seenCursors) != 1 {
		t.Fatalf("server calls = %d, want 1", len(seenCursors))
	}
	if seenCursors[0] != "" {
		t.Errorf("first-iteration cursor = %q, want empty (per-drain wipe should have reset state.next_token)", seenCursors[0])
	}
}
