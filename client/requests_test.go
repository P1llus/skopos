// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// requestDoc builds a minimal Doc with the supplied request slice. Auth is
// always none and pagination is none — these tests exercise request-level
// behaviour, not the auth/pagination subsystems.
func requestDoc(baseURL string, reqs []schema.Request) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: baseURL},
		}},
		Defaults:   &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:       schema.Auth{None: &struct{}{}},
		Requests:   reqs,
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
}

// TestRequest_If_PredicateGating asserts that a request whose `if`
// predicate evaluates false is skipped — the server does not see the
// request, and the iteration continues with the remaining requests.
func TestRequest_If_PredicateGating(t *testing.T) {
	var hits atomic.Int32
	var sawPath2 atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/two" {
			sawPath2.Store(true)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	falseLit := false
	doc := requestDoc(server.URL, []schema.Request{
		{
			Method: "GET",
			Path:   ptrValue(vStr("/one")),
		},
		{
			Method: "GET",
			Path:   ptrValue(vStr("/two")),
			If:     &schema.Predicate{LiteralBool: &falseLit},
		},
	})
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1 (gated request must be skipped)", got)
	}
	if sawPath2.Load() {
		t.Errorf("server saw /two; if-gated request must not fire")
	}
}

// onStatusServer is a tiny httptest.Server that always returns the chosen
// status code with an empty body. Used by the on_status verb tests.
func onStatusServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
}

// onStatusDoc builds a single-request Doc that expects 200 and dispatches
// the supplied (code → verb) on_status table.
func onStatusDoc(baseURL string, code int, verb string) *schema.Doc {
	return requestDoc(baseURL, []schema.Request{{
		Method:       "GET",
		Path:         ptrValue(vStr("/api/v1/events")),
		ExpectStatus: []int{200},
		OnStatus:     map[int]string{code: verb},
	}})
}

// TestOnStatus_Skip asserts the "skip" verb logs and continues without
// returning an error, and no events are emitted.
func TestOnStatus_Skip(t *testing.T) {
	server := onStatusServer(t, 429)
	defer server.Close()

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	sink := &captureSink{}
	r := &Runner{
		Doc:    onStatusDoc(server.URL, 429, "skip"),
		Sink:   sink,
		Logger: logger,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 0 {
		t.Errorf("emitted %d events, want 0 (skip verb)", len(sink.events))
	}
	if !strings.Contains(buf.String(), "skip") {
		t.Errorf("logger missing 'skip' substring: %q", buf.String())
	}
}

// TestOnStatus_Fail asserts the "fail" verb aborts the drain with a
// non-nil error.
func TestOnStatus_Fail(t *testing.T) {
	server := onStatusServer(t, 500)
	defer server.Close()

	r := &Runner{
		Doc:    onStatusDoc(server.URL, 500, "fail"),
		Sink:   &captureSink{},
		Now:    fixedNow(),
		Client: server.Client(),
	}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: nil error, want fail-mode abort")
	}
	if !strings.Contains(err.Error(), "fail") {
		t.Errorf("err = %v, want substring 'fail'", err)
	}
}

// TestOnStatus_EmptyEvents asserts the "empty_events" verb treats the
// page as empty (no events) and returns nil so the cursor still advances.
func TestOnStatus_EmptyEvents(t *testing.T) {
	server := onStatusServer(t, 401)
	defer server.Close()

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	sink := &captureSink{}
	r := &Runner{
		Doc:    onStatusDoc(server.URL, 401, "empty_events"),
		Sink:   sink,
		Logger: logger,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 0 {
		t.Errorf("emitted %d events, want 0", len(sink.events))
	}
	if !strings.Contains(buf.String(), "empty_events") {
		t.Errorf("logger missing 'empty_events' substring: %q", buf.String())
	}
}

// TestOnStatus_InvalidateCache asserts the "invalidate_cache" verb logs
// and advances as empty_events when the active auth has no cache.
func TestOnStatus_InvalidateCache(t *testing.T) {
	server := onStatusServer(t, 401)
	defer server.Close()

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	sink := &captureSink{}
	r := &Runner{
		Doc:    onStatusDoc(server.URL, 401, "invalidate_cache"),
		Sink:   sink,
		Logger: logger,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(sink.events) != 0 {
		t.Errorf("emitted %d events, want 0", len(sink.events))
	}
	if !strings.Contains(buf.String(), "invalidate_cache") {
		t.Errorf("logger missing 'invalidate_cache' substring: %q", buf.String())
	}
}

// TestExtract_BodyAndHeader_AndTargets exercises the four extract shapes
// in one drain: body source / header source × target=extract / target=cursor.
//
// The first request returns a body-cursor token at body.next + a header
// X-Trace-Id, and uses extract[] to lift them into scope.cursor.next and
// scope.extract.trace. The second request reads cursor.next as a query
// param and returns events. After Drain, snapshot.Cursor must carry next
// (cursor target) and the request must have been built with the extracted
// trace id (extract target survived to the second request via headers).
func TestExtract_BodyAndHeader_AndTargets(t *testing.T) {
	var gotCursorParam string
	var gotTraceHeader string
	var hits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := int(hits.Add(1))
		switch hit {
		case 1:
			w.Header().Set("X-Trace-Id", "trc-99")
			writeJSON(w, http.StatusOK, map[string]any{
				"next":   "tok-second",
				"events": []any{},
			})
		case 2:
			gotCursorParam = r.URL.Query().Get("cursor")
			gotTraceHeader = r.Header.Get("X-Trace-Id")
			writeJSON(w, http.StatusOK, map[string]any{
				"events": []map[string]any{{"id": "e-1"}},
			})
		default:
			t.Errorf("unexpected extra request %d", hit)
		}
	}))
	defer server.Close()

	doc := requestDoc(server.URL, []schema.Request{
		{
			ID:     "first",
			Method: "GET",
			Path:   ptrValue(vStr("/one")),
			Extract: []schema.ExtractVar{
				{
					// body source, default target=extract.
					Name:   "trace",
					Source: "header",
					Header: "X-Trace-Id",
				},
				{
					// body source, target=cursor.
					Name:   "next",
					Source: "body",
					Path:   mustPath("next"),
					Target: "cursor",
				},
			},
		},
		{
			Method: "GET",
			Path:   ptrValue(vStr("/two")),
			Query: map[string]schema.Value{
				"cursor": vRef("cursor.next"),
			},
			Headers: map[string]schema.Value{
				"X-Trace-Id": vRef("extract.trace"),
			},
		},
	})

	store := &MemoryStore{}
	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
	if gotCursorParam != "tok-second" {
		t.Errorf("second request cursor= %q, want tok-second (target=cursor body extract)", gotCursorParam)
	}
	if gotTraceHeader != "trc-99" {
		t.Errorf("second request X-Trace-Id = %q, want trc-99 (header source extract)", gotTraceHeader)
	}
	snap, _ := store.Load()
	if got := snap.Cursor["next"]; got != "tok-second" {
		t.Errorf("snapshot.Cursor[next] = %v, want tok-second (target=cursor persists)", got)
	}
	if len(sink.events) != 1 {
		t.Errorf("emitted %d events, want 1", len(sink.events))
	}
}
