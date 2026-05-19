// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1llus/skopos/schema"
)

// TestDrain_EmitsEvents pins the happy-path: one drain, one accepted
// page, every event walked through the sink in declared order.
func TestDrain_EmitsEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"id": "e1"},
			map[string]any{"id": "e2"},
		}})
	}))
	defer server.Close()

	sink := &captureSink{}
	r := &Runner{Doc: minimalDoc(server.URL), Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 2 {
		t.Errorf("events = %d, want 2", len(sink.events))
	}
	if sink.flushN != 1 {
		t.Errorf("flushN = %d, want 1", sink.flushN)
	}
}

// TestDrain_EmptyPageStillFiresProgress pins the redesigned contract:
// progress fires once per accepted page-response INCLUDING empty pages.
// docs/runtime.md §5.
func TestDrain_EmptyPageStillFiresProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["last_seen_at"] = schema.FieldDecl{Type: "timestamp"}
	doc.Progress = schema.Progress{
		{To: mustPath("state.last_seen_at"), From: vNow()},
	}

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	snap, _ := store.Load()
	if got := snap.State["last_seen_at"]; got == nil {
		t.Errorf("progress write did not fire on empty page; snapshot=%#v", snap.State)
	}
}

// TestDrain_StandardModeKeepsProgress pins the "deferred Save runs on
// every exit" contract: progress writes that already fired on completed
// pages survive a mid-drain error. docs/runtime.md §6.
func TestDrain_StandardModeKeepsProgress(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			writeJSON(w, http.StatusOK, map[string]any{
				"events":      []any{map[string]any{"id": "e1", "ts": "2026-01-01T00:00:01Z"}},
				"next_cursor": "page-2",
			})
			return
		}
		// Second page: 500.
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["next_token"] = schema.FieldDecl{Type: "string"}
	doc.State["last_timestamp"] = schema.FieldDecl{Type: "timestamp"}
	doc.Requests[0].URL = mustInterp("${state.url}/events?cursor=${state.next_token|}")
	doc.Pagination = schema.Pagination{CursorToken: &schema.CursorTokenPagination{
		From: mustPath("response.body.next_cursor"),
		To:   mustPath("state.next_token"),
	}}
	doc.Progress = schema.Progress{
		{To: mustPath("state.last_timestamp"), From: vRef("events.last.ts")},
	}

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain (standard mode): %v", err)
	}

	snap, _ := store.Load()
	if got := snap.State["last_timestamp"]; got != "2026-01-01T00:00:01Z" {
		t.Errorf("last_timestamp = %v, want progress write from first page to survive", got)
	}
}

// TestDrain_FailModeReturnsError pins error.mode: fail surfacing through
// the Drain return.
func TestDrain_FailModeReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Error = &schema.ErrorBlock{Mode: "fail"}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err == nil {
		t.Error("Drain: expected error in fail mode, got nil")
	}
}

// TestDrain_OnStatusSkip pins on_status: <code>: skip — log the skip,
// emit no events, but advance progress and pagination.
func TestDrain_OnStatusSkip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Requests[0].OnStatus = map[int]string{http.StatusTooManyRequests: "skip"}
	doc.State["last_seen_at"] = schema.FieldDecl{Type: "timestamp"}
	doc.Progress = schema.Progress{
		{To: mustPath("state.last_seen_at"), From: vNow()},
	}

	store := &MemoryStore{}
	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Errorf("Drain: %v, want nil (skip)", err)
	}
	if len(sink.events) != 0 {
		t.Errorf("skip should emit no events; got %d", len(sink.events))
	}
	snap, _ := store.Load()
	if snap.State["last_seen_at"] == nil {
		t.Error("skip should still fire progress writes")
	}
}

// TestDrain_OnStatusFail pins on_status: <code>: fail — the drain aborts
// with an error regardless of error.mode.
func TestDrain_OnStatusFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Requests[0].OnStatus = map[int]string{http.StatusGone: "fail"}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err == nil {
		t.Error("on_status: fail should surface a Drain error")
	}
}

// TestDrain_TerminateWhenLoop pins the request-level loop primitive: a
// step with terminate_when: re-fires until the predicate evaluates true.
// docs/runtime.md §4 — the three-request submit/poll/fetch pattern.
func TestDrain_TerminateWhenLoop(t *testing.T) {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"export_id": "exp-1"})
	})
	mux.HandleFunc("/poll", func(w http.ResponseWriter, _ *http.Request) {
		n := polls.Add(1)
		status := "running"
		if n >= 3 {
			status = "complete"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     status,
			"result_url": "/fetch",
		})
	})
	mux.HandleFunc("/fetch", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"id": "e1"},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url":        {Type: "url", Default: ptrValue(vStr(server.URL))},
			"export_id":  {Type: "string"},
			"result_url": {Type: "url"},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "submit",
				Method: "POST",
				URL:    mustInterp("${state.url}/submit"),
				Extract: []schema.ExtractVar{
					{To: mustPath("state.export_id"), From: mustPath("response.body.export_id")},
				},
			},
			{
				ID:     "poll",
				Method: "GET",
				URL:    mustInterp("${state.url}/poll"),
				TerminateWhen: &schema.Predicate{Eq: &schema.PredicateEq{
					Path:  mustPath("response.body.status"),
					Value: vStr("complete"),
				}},
				Extract: []schema.ExtractVar{
					{To: mustPath("state.result_url"), From: mustPath("response.body.result_url")},
				},
			},
			{
				ID:             "fetch",
				Method:         "GET",
				URL:            mustInterp("${state.url}/fetch"),
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if polls.Load() != 3 {
		t.Errorf("poll fired %d times, want 3 (re-fire until status=complete)", polls.Load())
	}
	if len(sink.events) != 1 {
		t.Errorf("events = %d, want 1 (from fetch)", len(sink.events))
	}
}

// TestMaxPagesGuardrail pins the iteration-cap behaviour: a pagination
// loop that never terminates is bounded by Runner.MaxPages.
func TestMaxPagesGuardrail(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		// Always return a next_cursor so cursor_token pagination never
		// terminates on its own.
		writeJSON(w, http.StatusOK, map[string]any{
			"events":      []any{},
			"next_cursor": "tok",
		})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["next_token"] = schema.FieldDecl{Type: "string"}
	doc.Pagination = schema.Pagination{CursorToken: &schema.CursorTokenPagination{
		From: mustPath("response.body.next_cursor"),
		To:   mustPath("state.next_token"),
	}}

	r := &Runner{
		Doc:      doc,
		Sink:     &captureSink{},
		Now:      fixedNow(),
		Client:   server.Client(),
		MaxPages: 5,
	}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: expected MaxPages cap error, got nil")
	}
	if !errors.Is(err, errMaxPagesExceeded) {
		t.Errorf("err = %v, want errMaxPagesExceeded", err)
	}
	if calls.Load() != 5 {
		t.Errorf("server calls = %d, want 5 (MaxPages)", calls.Load())
	}
}

// TestDrain_ContextCancellationStops pins the ctx-cancellation contract:
// Drain returns the ctx.Err() and the deferred Save still fires.
func TestDrain_ContextCancellationStops(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done

	r := &Runner{Doc: minimalDoc(server.URL), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Drain returned %v, want context.Canceled", err)
	}
}

// TestDrain_RequiresDocAndSink pins the structural preconditions: Doc
// and Sink are required; Store / Client / Logger / MaxPages get
// defaults.
func TestDrain_RequiresDocAndSink(t *testing.T) {
	t.Run("missing_doc", func(t *testing.T) {
		r := &Runner{Sink: &captureSink{}}
		err := r.Drain(context.Background())
		if err == nil || !strings.Contains(err.Error(), "Doc") {
			t.Errorf("missing Doc err = %v", err)
		}
	})
	t.Run("missing_sink", func(t *testing.T) {
		r := &Runner{Doc: &schema.Doc{IRVersion: "1"}}
		err := r.Drain(context.Background())
		if err == nil || !strings.Contains(err.Error(), "Sink") {
			t.Errorf("missing Sink err = %v", err)
		}
	})
}

// TestDrain_HTTPTimeoutFires pins the default client timeout: a hung
// server cannot wedge Drain indefinitely.
func TestDrain_HTTPTimeoutFires(t *testing.T) {
	// httptest server that hangs forever (ctx-based block).
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hung.Close()

	doc := minimalDoc(hung.URL)
	doc.Error = &schema.ErrorBlock{Mode: "fail"}

	r := &Runner{
		Doc:    doc,
		Sink:   &captureSink{},
		Now:    fixedNow(),
		Client: &http.Client{Timeout: 100 * time.Millisecond},
	}
	start := time.Now()
	err := r.Drain(context.Background())
	if err == nil {
		t.Error("Drain on hung server: expected timeout error, got nil")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Drain did not honour 100ms client timeout; elapsed %s", time.Since(start))
	}
}

// TestDrain_ProducerByIndex pins the "implicit producer = last request"
// fallback: two unlabelled requests are distinguished by slice index, not
// any string-identity match.
func TestDrain_ProducerByIndex(t *testing.T) {
	var firstHit, secondHit atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/first", func(w http.ResponseWriter, _ *http.Request) {
		firstHit.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"id": "from-first"},
		}})
	})
	mux.HandleFunc("/second", func(w http.ResponseWriter, _ *http.Request) {
		secondHit.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"id": "from-second"},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Requests = []schema.Request{
		{Method: "GET", URL: mustInterp("${state.url}/first")},
		{Method: "GET", URL: mustInterp("${state.url}/second")},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if firstHit.Load() != 1 || secondHit.Load() != 1 {
		t.Errorf("first=%d second=%d, want both 1", firstHit.Load(), secondHit.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1 (from last request)", len(sink.events))
	}
	if got, _ := sink.events[0].(map[string]any)["id"].(string); got != "from-second" {
		t.Errorf("events[0].id = %q, want from-second", got)
	}
}

// TestDrain_ProducesEventsTrue pins the explicit producer override: the
// request with produces_events:true wins, even if it's not last.
func TestDrain_ProducesEventsTrue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"id": "the-events"},
		}})
	})
	mux.HandleFunc("/sentinel", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Requests = []schema.Request{
		{ID: "data", Method: "GET", URL: mustInterp("${state.url}/data"), ProducesEvents: true},
		{ID: "sentinel", Method: "GET", URL: mustInterp("${state.url}/sentinel")},
	}

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1 (the-events from non-last producer)", len(sink.events))
	}
}

// TestDrain_ExtractToStatePersistsAcrossDrains pins the persistence
// classification: an extract whose to: is state.<name> is recorded in
// the snapshot.
func TestDrain_ExtractToStatePersistsAcrossDrains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v42"`)
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["etag"] = schema.FieldDecl{Type: "string"}
	doc.Requests[0].Extract = []schema.ExtractVar{
		{To: mustPath("state.etag"), From: mustPath("response.header.ETag")},
	}

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	snap, _ := store.Load()
	if got, _ := snap.State["etag"].(string); got != `"v42"` {
		t.Errorf("state.etag = %v, want \"v42\"", snap.State["etag"])
	}
}

// TestDrain_BodyMetadataRedactsContent pins the redaction-safe error
// path: a decode error never includes the raw response body — only
// length and content classification.
func TestDrain_BodyMetadataRedactsContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Embed an obvious secret in the body that must not appear in
		// the error.
		_, _ = w.Write([]byte("not-json-but-contains-SECRET-TOKEN-xyz"))
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Error = &schema.ErrorBlock{Mode: "fail"}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: expected decode error, got nil")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN-xyz") {
		t.Errorf("err leaks raw body: %v", err)
	}
}
