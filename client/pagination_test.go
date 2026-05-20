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

// TestPagination_CustomTerminateBeforeAdvance pins the DESIGN §3.6
// execution order: terminate_when is evaluated against pre-advance
// state.* so the predicate can read a state field that advance: itself
// will overwrite. If the order were inverted, the predicate would see
// the post-advance value on iteration 1 and terminate one page early.
func TestPagination_CustomTerminateBeforeAdvance(t *testing.T) {
	pages := []map[string]any{
		{"events": []any{map[string]any{"id": "e1"}}, "next": "n2"},
		{"events": []any{map[string]any{"id": "e2"}}, "next": "n3"},
	}
	var idx atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(idx.Add(1) - 1)
		if i >= len(pages) {
			t.Errorf("server called %d times, want at most %d", i+1, len(pages))
			http.Error(w, "exhausted", http.StatusGone)
			return
		}
		writeJSON(w, http.StatusOK, pages[i])
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["cursor"] = schema.FieldDecl{Type: "string"}
	cursorPath := mustPath("state.cursor")
	doc.Pagination = schema.Pagination{Custom: &schema.CustomPagination{
		Advance: []schema.AdvanceWrite{
			{To: mustPath("state.cursor"), From: vRef("response.body.next")},
		},
		// Predicate reads state.cursor — which advance: writes on every
		// iteration. If terminate_when fires AFTER advance, page 1
		// terminates the loop because state.cursor became "n2".
		// Correct order: pre-advance state, so page 1 sees absent and
		// continues.
		TerminateWhen: schema.Predicate{Present: &cursorPath},
	}}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Iter 1: state.cursor absent → terminate=false → advance writes "n2".
	// Iter 2: state.cursor="n2" → terminate=true → stop after iter 2's response.
	if got := idx.Load(); got != 2 {
		t.Errorf("server calls = %d, want 2 (terminate_when must read pre-advance state)", got)
	}
	if len(sink.events) != 2 {
		t.Errorf("events = %d, want 2", len(sink.events))
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
