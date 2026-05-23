// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// cursorPagesServer returns an httptest server that serves the given pages
// keyed by the ?cursor= query value. The empty cursor is the first page; each
// page advances via response.body.next, and a page without "next" terminates
// the cursor_token loop.
func cursorPagesServer(pages map[string]map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, pages[r.URL.Query().Get("cursor")])
	}))
}

// cursorDoc wires minimalDoc to cursor_token pagination over response.body.next.
func cursorDoc(baseURL string) *schema.Doc {
	doc := minimalDoc(baseURL)
	doc.State["next_token"] = schema.FieldDecl{Type: "string"}
	doc.Requests[0].URL = mustInterp("${state.url}/events?cursor=${state.next_token|}")
	doc.Pagination = schema.Pagination{CursorToken: &schema.CursorTokenPagination{
		From: mustPath("response.body.next"),
		To:   mustPath("state.next_token"),
	}}
	return doc
}

func ev(id string) any { return map[string]any{"id": id} }

func eventIDs(t *testing.T, events []any) []string {
	t.Helper()
	ids := make([]string, 0, len(events))
	for _, e := range events {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("event %#v is not an object", e)
		}
		ids = append(ids, m["id"].(string))
	}
	return ids
}

// TestDrain_SinkBufferDeliversInOrder pins that buffered delivery
// (SinkBuffer > 0) yields exactly the same events, in the same order, as the
// synchronous path — only the delivering goroutine changes.
func TestDrain_SinkBufferDeliversInOrder(t *testing.T) {
	server := cursorPagesServer(map[string]map[string]any{
		"":   {"events": []any{ev("e1"), ev("e2")}, "next": "p2"},
		"p2": {"events": []any{ev("e3"), ev("e4")}, "next": "p3"},
		"p3": {"events": []any{ev("e5"), ev("e6")}},
	})
	defer server.Close()

	sink := &captureSink{}
	r := &Runner{
		Doc:        cursorDoc(server.URL),
		Sink:       sink,
		Store:      &MemoryStore{},
		Now:        fixedNow(),
		Client:     server.Client(),
		SinkBuffer: 2,
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	got := eventIDs(t, sink.events)
	want := []string{"e1", "e2", "e3", "e4", "e5", "e6"}
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	if sink.flushN == 0 {
		t.Error("Flush was never called")
	}
}

// TestDrain_SinkBufferEmitErrorAborts pins that an Emit failure on the
// buffered consumer goroutine still surfaces as a non-nil Drain error.
func TestDrain_SinkBufferEmitErrorAborts(t *testing.T) {
	server := cursorPagesServer(map[string]map[string]any{
		"":   {"events": []any{ev("e1"), ev("e2")}, "next": "p2"},
		"p2": {"events": []any{ev("e3"), ev("e4")}},
	})
	defer server.Close()

	r := &Runner{
		Doc:        cursorDoc(server.URL),
		Sink:       &erroringSink{err: errors.New("sink down")},
		Store:      &MemoryStore{},
		Now:        fixedNow(),
		Client:     server.Client(),
		SinkBuffer: 2,
	}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: expected sink.Emit error, got nil")
	}
}

// countingStore counts Save calls so a test can observe mid-drain checkpoints.
type countingStore struct {
	mu    sync.Mutex
	snap  Snapshot
	saves int
}

func (c *countingStore) Load() (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap, nil
}

func (c *countingStore) Save(s Snapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap = s
	c.saves++
	return nil
}

// TestDrain_CheckpointPagesPersistsMidDrain pins that CheckpointPages commits
// state per N pages mid-drain (here: after each of the two advancing pages),
// in addition to the end-of-drain Save. The terminating page does not
// checkpoint (it returns before the checkpoint site).
func TestDrain_CheckpointPagesPersistsMidDrain(t *testing.T) {
	pages := map[string]map[string]any{
		"":   {"events": []any{ev("e1")}, "next": "p2"},
		"p2": {"events": []any{ev("e2")}, "next": "p3"},
		"p3": {"events": []any{ev("e3")}},
	}

	t.Run("off", func(t *testing.T) {
		server := cursorPagesServer(pages)
		defer server.Close()
		store := &countingStore{}
		r := &Runner{Doc: cursorDoc(server.URL), Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client()}
		if err := r.Drain(context.Background()); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if store.saves != 1 {
			t.Errorf("saves = %d, want 1 (end-of-drain only)", store.saves)
		}
	})

	t.Run("every_page", func(t *testing.T) {
		server := cursorPagesServer(pages)
		defer server.Close()
		store := &countingStore{}
		r := &Runner{Doc: cursorDoc(server.URL), Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client(), CheckpointPages: 1}
		if err := r.Drain(context.Background()); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		// 2 advancing pages → 2 checkpoints, plus 1 end-of-drain Save.
		if store.saves != 3 {
			t.Errorf("saves = %d, want 3 (2 checkpoints + final)", store.saves)
		}
	})
}

// opRecorder implements both Sink and Store, recording the order of Emit /
// Flush / Save so a test can assert the durability ordering.
type opRecorder struct {
	mu  sync.Mutex
	ops []string
}

func (r *opRecorder) Emit(any) error          { r.add("emit"); return nil }
func (r *opRecorder) Flush() error            { r.add("flush"); return nil }
func (r *opRecorder) Load() (Snapshot, error) { return Snapshot{}, nil }
func (r *opRecorder) Save(Snapshot) error     { r.add("save"); return nil }

func (r *opRecorder) add(s string) {
	r.mu.Lock()
	r.ops = append(r.ops, s)
	r.mu.Unlock()
}

// TestDrain_TeardownFlushesBeforeSave pins the at-least-once ordering: every
// event is emitted, then Flush makes them durable, and only then does Save
// commit state past them. Exercised through the buffered path to also cover
// the queue-drain barrier.
func TestDrain_TeardownFlushesBeforeSave(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{ev("e1"), ev("e2")}})
	}))
	defer server.Close()

	rec := &opRecorder{}
	r := &Runner{Doc: minimalDoc(server.URL), Sink: rec, Store: rec, Now: fixedNow(), Client: server.Client(), SinkBuffer: 4}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	flushIdx, saveIdx := -1, -1
	for i, op := range rec.ops {
		switch op {
		case "flush":
			flushIdx = i
		case "save":
			saveIdx = i
		case "emit":
			if flushIdx != -1 {
				t.Errorf("emit at %d ran after flush at %d; ops=%v", i, flushIdx, rec.ops)
			}
		}
	}
	if flushIdx == -1 || saveIdx == -1 {
		t.Fatalf("missing flush/save; ops=%v", rec.ops)
	}
	if flushIdx > saveIdx {
		t.Errorf("flush (%d) ran after save (%d); ops=%v", flushIdx, saveIdx, rec.ops)
	}
}
