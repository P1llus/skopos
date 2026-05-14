// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1llus/skopos/schema"
)

// TestStatelessProgress: every operation is a no-op.
func TestStatelessProgress(t *testing.T) {
	p := &statelessProgress{}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if p.shouldSkipForPhase(schema.Request{}, "") {
		t.Errorf("stateless shouldSkipForPhase = true, want false")
	}
}

// TestLatestTimestampProgress_SeedFirstRun: no cursor.last_timestamp + an
// initial.lookback → fromProgress[latest_timestamp] is "now − lookback".
func TestLatestTimestampProgress_SeedFirstRun(t *testing.T) {
	p := &latestTimestampProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
		Initial: &schema.Initial{
			Lookback: vStr("24h"),
		},
	}}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// fixedNow = 2026-05-12T12:00:00Z → 24h earlier = 2026-05-11T12:00:00Z
	want := "2026-05-11T12:00:00Z"
	if got := s.fromProgress["latest_timestamp"]; got != want {
		t.Errorf("fromProgress[latest_timestamp] = %v, want %v", got, want)
	}
	if got := s.cursor["last_timestamp"]; got != want {
		t.Errorf("cursor.last_timestamp = %v, want %v (initial-lookback persists)", got, want)
	}
}

// TestLatestTimestampProgress_SeedResume: cursor.last_timestamp already
// set → fromProgress mirrors it. Initial.lookback is NOT re-applied (the
// window-start must be stable across resumes).
func TestLatestTimestampProgress_SeedResume(t *testing.T) {
	p := &latestTimestampProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
		Initial: &schema.Initial{
			Lookback: vStr("24h"),
		},
	}}
	s := newTestScope(t, nil, map[string]any{"last_timestamp": "2026-05-12T10:00:00Z"})
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := s.fromProgress["latest_timestamp"]; got != "2026-05-12T10:00:00Z" {
		t.Errorf("fromProgress[latest_timestamp] = %v, want resume value", got)
	}
}

// TestLatestTimestampProgress_AdvancePicksMax walks events, picks the max
// timestamp, writes cursor.last_timestamp.
func TestLatestTimestampProgress_AdvancePicksMax(t *testing.T) {
	p := &latestTimestampProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
	}}
	s := newTestScope(t, nil, nil)

	events := []any{
		map[string]any{"created_at": "2026-05-12T08:00:00Z"},
		map[string]any{"created_at": "2026-05-12T10:30:00Z"},
		map[string]any{"created_at": "2026-05-12T09:15:00Z"},
	}
	if err := p.advance(s, events); err != nil {
		t.Fatalf("advance: %v", err)
	}
	want := "2026-05-12T10:30:00Z"
	if got := s.cursor["last_timestamp"]; got != want {
		t.Errorf("cursor.last_timestamp = %v, want %v", got, want)
	}
}

// TestLatestTimestampProgress_AdvanceEmpty: no events → cursor stays put.
func TestLatestTimestampProgress_AdvanceEmpty(t *testing.T) {
	p := &latestTimestampProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
	}}
	s := newTestScope(t, nil, map[string]any{"last_timestamp": "2026-05-12T08:00:00Z"})
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got := s.cursor["last_timestamp"]; got != "2026-05-12T08:00:00Z" {
		t.Errorf("empty-events advance moved cursor: got %v", got)
	}
}

// TestAsyncJobProgress_FirstPhaseAndShouldSkip pins the phase-machine
// gating: only the request matching the current phase runs.
func TestAsyncJobProgress_FirstPhaseAndShouldSkip(t *testing.T) {
	cfg := &schema.AsyncJobProgress{
		Submit: &schema.AsyncSubmitStep{Step: "submit"},
		Poll:   &schema.AsyncPollStep{Step: "poll"},
		Fetch:  &schema.AsyncFetchStep{Step: "fetch"},
	}
	p := &asyncJobProgress{cfg: cfg}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := p.currentPhase(s); got != "submit" {
		t.Fatalf("first phase = %v, want submit", got)
	}
	if !p.shouldSkipForPhase(schema.Request{ID: "poll"}, "submit") {
		t.Errorf("expected poll to be skipped while phase=submit")
	}
	if p.shouldSkipForPhase(schema.Request{ID: "submit"}, "submit") {
		t.Errorf("expected submit NOT to be skipped while phase=submit")
	}
}

// TestAsyncJobProgress_PhaseTransition walks submit → poll → fetch:
//   - submit extracts export_id, advances phase to poll, wantMore=true.
//   - poll with complete_when unsatisfied stays in poll, wantMore=false.
//   - poll with complete_when satisfied advances to fetch, wantMore=true.
//   - fetch resets phase to submit, applies on_complete (use_now).
func TestAsyncJobProgress_PhaseTransition(t *testing.T) {
	completeWhen := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.status"),
		Equal: vStr("complete"),
	}}
	cfg := &schema.AsyncJobProgress{
		Submit: &schema.AsyncSubmitStep{
			Step: "submit",
			Extract: map[string]schema.AsyncExtract{
				"export_id": {Path: mustPath("export_id")},
			},
		},
		Poll: &schema.AsyncPollStep{
			Step:         "poll",
			CompleteWhen: &completeWhen,
			Extract: map[string]schema.AsyncExtract{
				"result_url": {Path: mustPath("result_url")},
			},
		},
		Fetch: &schema.AsyncFetchStep{Step: "fetch"},
		OnComplete: &schema.AsyncOnComplete{
			CursorUpdate: &schema.CursorUpdateDirective{Kind: "use_now"},
		},
	}
	p := &asyncJobProgress{cfg: cfg}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// submit → poll, captures export_id.
	submitRes := &stepResult{statusCode: 202, body: map[string]any{"export_id": "exp-123"}}
	next, more, _, err := p.phaseTransition(s, "submit", submitRes)
	if err != nil {
		t.Fatalf("submit transition: %v", err)
	}
	if next != "poll" || !more {
		t.Errorf("submit→ got (%s, more=%v), want (poll, true)", next, more)
	}
	if s.cursor["export_id"] != "exp-123" {
		t.Errorf("export_id not captured: %v", s.cursor["export_id"])
	}

	// poll unsatisfied → stays in poll, wantMore=false.
	pollPending := &stepResult{statusCode: 200, body: map[string]any{"status": "pending"}}
	next, more, _, err = p.phaseTransition(s, "poll", pollPending)
	if err != nil {
		t.Fatalf("poll-pending transition: %v", err)
	}
	if next != "poll" || more {
		t.Errorf("poll-pending → got (%s, more=%v), want (poll, false)", next, more)
	}

	// poll satisfied → fetch, captures result_url.
	pollDone := &stepResult{statusCode: 200, body: map[string]any{
		"status":     "complete",
		"result_url": "http://example/data",
	}}
	next, more, _, err = p.phaseTransition(s, "poll", pollDone)
	if err != nil {
		t.Fatalf("poll-done transition: %v", err)
	}
	if next != "fetch" || !more {
		t.Errorf("poll-done → got (%s, more=%v), want (fetch, true)", next, more)
	}
	if s.cursor["result_url"] != "http://example/data" {
		t.Errorf("result_url not captured: %v", s.cursor["result_url"])
	}

	// fetch → reset, on_complete=use_now writes cursor.last_timestamp.
	fetchRes := &stepResult{statusCode: 200, body: map[string]any{"items": []any{}}}
	_, _, _, err = p.phaseTransition(s, "fetch", fetchRes)
	if err != nil {
		t.Fatalf("fetch transition: %v", err)
	}
	if got := s.cursor["phase"]; got != "submit" {
		t.Errorf("after fetch phase = %v, want submit", got)
	}
	if got := s.cursor["last_timestamp"]; got != "2026-05-12T12:00:00Z" {
		t.Errorf("on_complete.use_now = %v, want fixedNow", got)
	}
	// extract-derived cursor fields are cleared.
	if _, ok := s.cursor["export_id"]; ok {
		t.Errorf("export_id should be cleared after fetch")
	}
	if _, ok := s.cursor["result_url"]; ok {
		t.Errorf("result_url should be cleared after fetch")
	}
}

// TestUnsupportedProgressVariants asserts that a Progress block with no
// variant set fails at plan construction with a clear message.
func TestUnsupportedProgressVariants(t *testing.T) {
	doc := &schema.Doc{Progress: schema.Progress{}}
	if _, err := makeProgressPlan(doc); err == nil {
		t.Errorf("makeProgressPlan(empty Progress): expected error, got nil")
	}
}

// TestUseNowProgress_AdvanceWritesNowMinusLookback: advance subtracts the
// optional lookback Value from s.now() and writes the result as an RFC 3339
// string to cursor.last_timestamp.
func TestUseNowProgress_AdvanceWritesNowMinusLookback(t *testing.T) {
	lookback := vStr("5m")
	p := &useNowProgress{cfg: &schema.UseNowProgress{Lookback: &lookback}}
	s := newTestScope(t, nil, nil)
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	// fixedNow = 2026-05-12T12:00:00Z; minus 5m = 2026-05-12T11:55:00Z.
	want := "2026-05-12T11:55:00Z"
	if got := s.cursor["last_timestamp"]; got != want {
		t.Errorf("cursor.last_timestamp = %v, want %v", got, want)
	}
}

// TestUseNowProgress_AdvanceWithoutLookback: with cfg.Lookback == nil the
// cursor is written to exactly s.now() in RFC 3339.
func TestUseNowProgress_AdvanceWithoutLookback(t *testing.T) {
	p := &useNowProgress{cfg: &schema.UseNowProgress{}}
	s := newTestScope(t, nil, nil)
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	want := "2026-05-12T12:00:00Z"
	if got := s.cursor["last_timestamp"]; got != want {
		t.Errorf("cursor.last_timestamp = %v, want %v", got, want)
	}
}

// TestUseNowProgress_SeedMirrorsExistingCursor: on resume seed mirrors the
// existing cursor.last_timestamp into fromProgress[latest_timestamp]; on
// first drain (no cursor) fromProgress is the empty string. use_now has no
// initial.lookback, so seed never writes the cursor itself.
func TestUseNowProgress_SeedMirrorsExistingCursor(t *testing.T) {
	p := &useNowProgress{cfg: &schema.UseNowProgress{}}

	t.Run("resume", func(t *testing.T) {
		s := newTestScope(t, nil, map[string]any{"last_timestamp": "2026-05-12T10:00:00Z"})
		if err := p.seed(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if got := s.fromProgress["latest_timestamp"]; got != "2026-05-12T10:00:00Z" {
			t.Errorf("fromProgress[latest_timestamp] = %v, want resume value", got)
		}
	})

	t.Run("first_drain", func(t *testing.T) {
		s := newTestScope(t, nil, nil)
		if err := p.seed(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if got := s.fromProgress["latest_timestamp"]; got != "" {
			t.Errorf("fromProgress[latest_timestamp] = %v, want empty string on first drain", got)
		}
		if _, ok := s.cursor["last_timestamp"]; ok {
			t.Errorf("seed wrote cursor.last_timestamp on first drain (use_now has no initial.lookback)")
		}
	})
}

// TestUseNowProgress_FromProgressResolvesNextDrain pins the multi-drain
// shape: seed → advance → re-seed simulates the next drain; the second
// seed's fromProgress[latest_timestamp] equals the advance-written value.
func TestUseNowProgress_FromProgressResolvesNextDrain(t *testing.T) {
	lookback := vStr("5m")
	p := &useNowProgress{cfg: &schema.UseNowProgress{Lookback: &lookback}}
	s := newTestScope(t, nil, nil)

	if err := p.seed(s); err != nil {
		t.Fatalf("seed-1: %v", err)
	}
	if got := s.fromProgress["latest_timestamp"]; got != "" {
		t.Errorf("first-drain fromProgress = %v, want empty", got)
	}
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	advanced := s.cursor["last_timestamp"]
	if advanced != "2026-05-12T11:55:00Z" {
		t.Fatalf("advance wrote %v, want 2026-05-12T11:55:00Z", advanced)
	}

	// Re-seed (simulates the next Drain entering against the same scope).
	if err := p.seed(s); err != nil {
		t.Fatalf("seed-2: %v", err)
	}
	if got := s.fromProgress["latest_timestamp"]; got != advanced {
		t.Errorf("drain-2 fromProgress[latest_timestamp] = %v, want previous advance %v", got, advanced)
	}
}

// TestMaxEventFieldProgress_SeedFirstRun: no cursor.last_timestamp + an
// initial.lookback → fromProgress[latest_timestamp] is "now − lookback" AND
// the cursor is seeded so the first since=<...> matches the persisted
// high-water mark.
func TestMaxEventFieldProgress_SeedFirstRun(t *testing.T) {
	p := &maxEventFieldProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
		Initial: &schema.Initial{
			Lookback: vStr("24h"),
		},
	}}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// fixedNow = 2026-05-12T12:00:00Z → 24h earlier = 2026-05-11T12:00:00Z.
	want := "2026-05-11T12:00:00Z"
	if got := s.fromProgress["latest_timestamp"]; got != want {
		t.Errorf("fromProgress[latest_timestamp] = %v, want %v", got, want)
	}
	if got := s.cursor["last_timestamp"]; got != want {
		t.Errorf("cursor.last_timestamp = %v, want %v (initial-lookback persists)", got, want)
	}
}

// TestMaxEventFieldProgress_SeedResume: cursor.last_timestamp already set →
// fromProgress mirrors it. initial.lookback is NOT re-applied (the
// window-start must be stable across resumes).
func TestMaxEventFieldProgress_SeedResume(t *testing.T) {
	p := &maxEventFieldProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
		Initial: &schema.Initial{
			Lookback: vStr("24h"),
		},
	}}
	s := newTestScope(t, nil, map[string]any{"last_timestamp": "2026-05-12T10:00:00Z"})
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := s.fromProgress["latest_timestamp"]; got != "2026-05-12T10:00:00Z" {
		t.Errorf("fromProgress[latest_timestamp] = %v, want resume value", got)
	}
}

// TestMaxEventFieldProgress_AdvancePicksMax: walks unordered events, picks
// the max timestamp regardless of position in the list, and writes
// cursor.last_timestamp.
func TestMaxEventFieldProgress_AdvancePicksMax(t *testing.T) {
	p := &maxEventFieldProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
	}}
	s := newTestScope(t, nil, nil)

	// Deliberately out-of-order: the max is in the middle, not the last.
	events := []any{
		map[string]any{"created_at": "2026-05-12T08:00:00Z"},
		map[string]any{"created_at": "2026-05-12T11:45:00Z"},
		map[string]any{"created_at": "2026-05-12T09:15:00Z"},
		map[string]any{"created_at": "2026-05-12T10:30:00Z"},
	}
	if err := p.advance(s, events); err != nil {
		t.Fatalf("advance: %v", err)
	}
	want := "2026-05-12T11:45:00Z"
	if got := s.cursor["last_timestamp"]; got != want {
		t.Errorf("cursor.last_timestamp = %v, want %v", got, want)
	}
}

// TestMaxEventFieldProgress_AdvanceEmpty: no events → cursor stays put.
func TestMaxEventFieldProgress_AdvanceEmpty(t *testing.T) {
	p := &maxEventFieldProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
	}}
	s := newTestScope(t, nil, map[string]any{"last_timestamp": "2026-05-12T08:00:00Z"})
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got := s.cursor["last_timestamp"]; got != "2026-05-12T08:00:00Z" {
		t.Errorf("empty-events advance moved cursor: got %v", got)
	}
}

// TestMaxEventFieldProgress_AdvanceLookback: the per-iteration lookback is
// subtracted from the chosen max on every advance (the "lag for late events"
// pattern shared with latest_event_timestamp).
func TestMaxEventFieldProgress_AdvanceLookback(t *testing.T) {
	lookback := vStr("5m")
	p := &maxEventFieldProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
		Lookback:  &lookback,
	}}
	s := newTestScope(t, nil, nil)
	events := []any{
		map[string]any{"created_at": "2026-05-12T10:00:00Z"},
		map[string]any{"created_at": "2026-05-12T10:30:00Z"},
	}
	if err := p.advance(s, events); err != nil {
		t.Fatalf("advance: %v", err)
	}
	// max = 10:30Z; minus 5m lookback = 10:25Z.
	want := "2026-05-12T10:25:00Z"
	if got := s.cursor["last_timestamp"]; got != want {
		t.Errorf("cursor.last_timestamp = %v, want %v", got, want)
	}
}

// TestMaxEventFieldProgress_AdvanceSkipsUnparseable: events whose value at
// EventTime.Path is missing, non-string, or not RFC 3339 are skipped — the
// max comes from the parseable subset. This pins the "partial event quality"
// contract documented on maxEventTime.
func TestMaxEventFieldProgress_AdvanceSkipsUnparseable(t *testing.T) {
	p := &maxEventFieldProgress{cfg: &schema.TimestampProgress{
		EventTime: schema.EventTime{Path: mustPath("created_at")},
	}}
	s := newTestScope(t, nil, nil)
	events := []any{
		map[string]any{"created_at": "2026-05-12T08:00:00Z"},
		map[string]any{}, // missing field
		map[string]any{"created_at": "not-a-timestamp"}, // unparseable
		map[string]any{"created_at": "2026-05-12T09:30:00Z"},
	}
	if err := p.advance(s, events); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got := s.cursor["last_timestamp"]; got != "2026-05-12T09:30:00Z" {
		t.Errorf("cursor.last_timestamp = %v, want max-of-parseable", got)
	}
}

// TestEndToEnd_MaxEventField drives the max_event_field variant against a
// live httptest server. Two drains, asserting (a) drain-1 with no cursor
// computes since = now-24h and the server sees that, (b) drain-1 advances
// cursor.last_timestamp to the max event timestamp in the response (NOT the
// last item — the server returns events out of order to make the difference
// visible), and (c) drain-2 sends since=<drain-1 max>.
func TestEndToEnd_MaxEventField(t *testing.T) {
	var sinceParams []string
	var hitCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitCount, 1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		sinceParams = append(sinceParams, r.URL.Query().Get("since"))
		// Out-of-order events: max is in the middle. max_event_field MUST
		// walk all of them; "last item's timestamp" would write the wrong
		// cursor.
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []map[string]any{
				{"id": "evt-1", "created_at": "2026-05-12T08:00:00Z"},
				{"id": "evt-2", "created_at": "2026-05-12T11:45:00Z"},
				{"id": "evt-3", "created_at": "2026-05-12T09:15:00Z"},
			},
		})
	}))
	defer server.Close()

	store := &MemoryStore{}
	sink := &captureSink{}

	r1 := &Runner{
		Doc:    maxEventFieldDoc(server.URL),
		Sink:   sink,
		Store:  store,
		Now:    constNow(t, "2026-05-12T12:00:00Z"),
		Client: server.Client(),
	}
	if err := r1.Drain(context.Background()); err != nil {
		t.Fatalf("Drain-1: %v", err)
	}

	r2 := &Runner{
		Doc:    maxEventFieldDoc(server.URL),
		Sink:   sink,
		Store:  store,
		Now:    constNow(t, "2026-05-12T13:00:00Z"),
		Client: server.Client(),
	}
	if err := r2.Drain(context.Background()); err != nil {
		t.Fatalf("Drain-2: %v", err)
	}

	if got := atomic.LoadInt32(&hitCount); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
	if len(sinceParams) != 2 {
		t.Fatalf("captured %d sinces, want 2", len(sinceParams))
	}
	if sinceParams[0] != "2026-05-11T12:00:00Z" {
		t.Errorf("drain-1 since = %q, want initial 24h lookback 2026-05-11T12:00:00Z", sinceParams[0])
	}
	if sinceParams[1] != "2026-05-12T11:45:00Z" {
		t.Errorf("drain-2 since = %q, want max of drain-1 events 2026-05-12T11:45:00Z", sinceParams[1])
	}

	// Final snapshot carries the high-water mark forward.
	snap, _ := store.Load()
	if got := snap.Cursor["last_timestamp"]; got != "2026-05-12T11:45:00Z" {
		t.Errorf("snapshot cursor.last_timestamp = %v, want drain-2 max 2026-05-12T11:45:00Z", got)
	}
}

// maxEventFieldDoc builds a minimal Doc: GET with since=<from_progress
// latest_timestamp> in the query, pagination.none, max_event_field with a 24h
// initial lookback.
func maxEventFieldDoc(baseURL string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: baseURL},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
			Query: map[string]schema.Value{
				"since": vFromProg("latest_timestamp"),
			},
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress: schema.Progress{MaxEventField: &schema.TimestampProgress{
			EventTime: schema.EventTime{Path: mustPath("created_at")},
			Initial:   &schema.Initial{Lookback: vStr("24h")},
		}},
	}
}

// TestTimeWindowProgress_SeedFirstDrain: no cursor → start = now() -
// initial_offset, end = now(); both exposed via fromProgress AND persisted to
// cursor under the configured format (default rfc3339).
func TestTimeWindowProgress_SeedFirstDrain(t *testing.T) {
	p := &timeWindowProgress{cfg: &schema.TimeWindowProgress{
		InitialOffset: vStr("24h"),
	}}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// fixedNow = 2026-05-12T12:00:00Z; lookback 24h → 2026-05-11T12:00:00Z.
	wantStart := "2026-05-11T12:00:00Z"
	wantEnd := "2026-05-12T12:00:00Z"
	if got := s.fromProgress["window_start"]; got != wantStart {
		t.Errorf("fromProgress[window_start] = %v, want %v", got, wantStart)
	}
	if got := s.fromProgress["window_end"]; got != wantEnd {
		t.Errorf("fromProgress[window_end] = %v, want %v", got, wantEnd)
	}
	if got := s.cursor["window_start"]; got != wantStart {
		t.Errorf("cursor.window_start = %v, want %v (cursor must mirror fromProgress)", got, wantStart)
	}
	if got := s.cursor["window_end"]; got != wantEnd {
		t.Errorf("cursor.window_end = %v, want %v", got, wantEnd)
	}
}

// TestTimeWindowProgress_SeedResume: cursor.window_start already set → seed
// carries it forward (initial_offset is NOT re-applied) and recomputes
// window_end against the current now(). This is the natural "drain pauses
// and resumes later — the new window covers the gap" shape.
func TestTimeWindowProgress_SeedResume(t *testing.T) {
	p := &timeWindowProgress{cfg: &schema.TimeWindowProgress{
		InitialOffset: vStr("24h"),
	}}
	// Pretend the previous drain left a window_start in the past.
	s := newTestScope(t, nil, map[string]any{
		"window_start": "2026-05-12T09:00:00Z",
		"window_end":   "2026-05-12T10:00:00Z",
	})
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := s.fromProgress["window_start"]; got != "2026-05-12T09:00:00Z" {
		t.Errorf("fromProgress[window_start] = %v, want carry-forward value", got)
	}
	// window_end refreshes against fixedNow (12:00Z), widening the window.
	if got := s.fromProgress["window_end"]; got != "2026-05-12T12:00:00Z" {
		t.Errorf("fromProgress[window_end] = %v, want fixedNow", got)
	}
}

// TestTimeWindowProgress_Advance: the just-finished window_end becomes the
// next drain's window_start.
func TestTimeWindowProgress_Advance(t *testing.T) {
	p := &timeWindowProgress{cfg: &schema.TimeWindowProgress{
		InitialOffset: vStr("24h"),
	}}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	endBefore := s.cursor["window_end"]
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got := s.cursor["window_start"]; got != endBefore {
		t.Errorf("advance: cursor.window_start = %v, want previous window_end %v", got, endBefore)
	}
}

// TestTimeWindowProgress_AdvanceThenSeed_NextDrain pins the multi-drain
// shape: one drain ends, the next drain's seed sees window_start carried
// forward to the just-finished window_end.
func TestTimeWindowProgress_AdvanceThenSeed_NextDrain(t *testing.T) {
	p := &timeWindowProgress{cfg: &schema.TimeWindowProgress{
		InitialOffset: vStr("24h"),
	}}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed-1: %v", err)
	}
	if err := p.advance(s, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	// Re-seed (simulating the next Drain call against the same clock).
	if err := p.seed(s); err != nil {
		t.Fatalf("seed-2: %v", err)
	}
	// window_start is now what was window_end on drain-1 (fixedNow). On the
	// same clock, the new window collapses to zero width — which is the
	// correct "no time has passed, no new events" behaviour.
	if got := s.fromProgress["window_start"]; got != "2026-05-12T12:00:00Z" {
		t.Errorf("drain-2 fromProgress[window_start] = %v, want previous end %v", got, "2026-05-12T12:00:00Z")
	}
	if got := s.fromProgress["window_end"]; got != "2026-05-12T12:00:00Z" {
		t.Errorf("drain-2 fromProgress[window_end] = %v, want fixedNow", got)
	}
}

// TestTimeWindowProgress_Format_RFC3339Nano asserts the configured format
// verb is applied to both the cursor and the fromProgress values.
func TestTimeWindowProgress_Format_RFC3339Nano(t *testing.T) {
	p := &timeWindowProgress{cfg: &schema.TimeWindowProgress{
		InitialOffset: vStr("1h"),
		Format:        "rfc3339nano",
	}}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// fixedNow has zero nanos so rfc3339 and rfc3339nano render identically;
	// the contract under test is "the format verb runs", so assert against
	// the rendered form rather than spying on the verb.
	wantStart := "2026-05-12T11:00:00Z"
	if got := s.fromProgress["window_start"]; got != wantStart {
		t.Errorf("fromProgress[window_start] = %v (%T), want %v", got, got, wantStart)
	}
}

// TestTimeWindowProgress_Format_UnixSeconds verifies a numeric verb stores
// int64s in both cursor and fromProgress so a body builder doing
// {format: int, value: {from_progress: window_start}} sees a number.
func TestTimeWindowProgress_Format_UnixSeconds(t *testing.T) {
	p := &timeWindowProgress{cfg: &schema.TimeWindowProgress{
		InitialOffset: vStr("1h"),
		Format:        "unix_seconds",
	}}
	s := newTestScope(t, nil, nil)
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// fixedNow = 2026-05-12T12:00:00Z → unix seconds = 1778587200.
	const wantEndSec int64 = 1778587200
	const wantStartSec int64 = wantEndSec - 3600
	if got, ok := s.fromProgress["window_start"].(int64); !ok || got != wantStartSec {
		t.Errorf("fromProgress[window_start] = %v (%T), want int64 %d", s.fromProgress["window_start"], s.fromProgress["window_start"], wantStartSec)
	}
	if got, ok := s.fromProgress["window_end"].(int64); !ok || got != wantEndSec {
		t.Errorf("fromProgress[window_end] = %v (%T), want int64 %d", s.fromProgress["window_end"], s.fromProgress["window_end"], wantEndSec)
	}
}

// TestTimeWindowProgress_ForwardClockClamp: if a persisted cursor sits in
// the future (clock skew, restored backup), window_end clamps to
// window_start so the window stays valid rather than inverting.
func TestTimeWindowProgress_ForwardClockClamp(t *testing.T) {
	p := &timeWindowProgress{cfg: &schema.TimeWindowProgress{
		InitialOffset: vStr("24h"),
	}}
	// window_start = now() + 1h → end clamps to start.
	s := newTestScope(t, nil, map[string]any{
		"window_start": "2026-05-12T13:00:00Z",
	})
	if err := p.seed(s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := s.fromProgress["window_start"]; got != "2026-05-12T13:00:00Z" {
		t.Errorf("fromProgress[window_start] = %v, want carry-forward future timestamp", got)
	}
	if got := s.fromProgress["window_end"]; got != "2026-05-12T13:00:00Z" {
		t.Errorf("fromProgress[window_end] = %v, want clamped-to-start", got)
	}
}

// TestEndToEnd_TimeWindow drives the time_window variant against a live
// httptest server. Mirrors templates/post_json_body.yaml: the
// request is a POST whose JSON body carries from_date / to_date plucked
// from {from_progress: window_start / window_end}. Two drains, asserting
// (a) drain-1 sends the initial [now-24h, now] window, (b) drain-2 sends
// the resumed [previous-end, now] window after a clock tick, and (c) the
// final snapshot carries window_start forward for the (hypothetical) third
// drain.
func TestEndToEnd_TimeWindow(t *testing.T) {
	type captured struct {
		from string
		to   string
	}
	var hits []captured
	var hitCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitCount, 1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		var body struct {
			FromDate string `json:"from_date"`
			ToDate   string `json:"to_date"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		hits = append(hits, captured{from: body.FromDate, to: body.ToDate})
		// One event per drain, no pagination tail (pagination.none).
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []map[string]any{
				{"id": "evt", "created_at": body.FromDate},
			},
		})
	}))
	defer server.Close()

	store := &MemoryStore{}
	sink := &captureSink{}

	// Drain 1: at 12:00Z, initial 24h window → [11:00:00 the day before, 12:00Z].
	r1 := &Runner{
		Doc:    timeWindowDoc(server.URL),
		Sink:   sink,
		Store:  store,
		Now:    constNow(t, "2026-05-12T12:00:00Z"),
		Client: server.Client(),
	}
	if err := r1.Drain(context.Background()); err != nil {
		t.Fatalf("Drain-1: %v", err)
	}
	// Drain 2: at 13:00Z, resumed window → [12:00Z, 13:00Z].
	r2 := &Runner{
		Doc:    timeWindowDoc(server.URL),
		Sink:   sink,
		Store:  store,
		Now:    constNow(t, "2026-05-12T13:00:00Z"),
		Client: server.Client(),
	}
	if err := r2.Drain(context.Background()); err != nil {
		t.Fatalf("Drain-2: %v", err)
	}

	if got := atomic.LoadInt32(&hitCount); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
	if len(hits) != 2 {
		t.Fatalf("captured %d hits, want 2", len(hits))
	}
	if hits[0].from != "2026-05-11T12:00:00Z" || hits[0].to != "2026-05-12T12:00:00Z" {
		t.Errorf("drain-1 window = [%s, %s], want [2026-05-11T12:00:00Z, 2026-05-12T12:00:00Z]", hits[0].from, hits[0].to)
	}
	if hits[1].from != "2026-05-12T12:00:00Z" || hits[1].to != "2026-05-12T13:00:00Z" {
		t.Errorf("drain-2 window = [%s, %s], want [2026-05-12T12:00:00Z, 2026-05-12T13:00:00Z]", hits[1].from, hits[1].to)
	}

	// Final snapshot: cursor.window_start carries the drain-2 end forward
	// so the next drain would pick up from 13:00Z.
	snap, _ := store.Load()
	if got := snap.Cursor["window_start"]; got != "2026-05-12T13:00:00Z" {
		t.Errorf("snapshot cursor.window_start = %v, want drain-2 end 2026-05-12T13:00:00Z", got)
	}
}

// timeWindowDoc mirrors templates/post_json_body.yaml: POST with
// a JSON body whose from_date / to_date come from {from_progress: ...}.
// Pagination is none so each drain runs exactly one iteration, keeping the
// test focused on the time_window plumbing.
func timeWindowDoc(baseURL string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: baseURL},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{{
			Method: "POST",
			Path:   ptrValue(vStr("/api/v1/search")),
			Body: &schema.Body{JSON: map[string]schema.Value{
				"from_date": vFromProg("window_start"),
				"to_date":   vFromProg("window_end"),
			}},
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress: schema.Progress{TimeWindow: &schema.TimeWindowProgress{
			InitialOffset: vStr("24h"),
			Format:        "rfc3339",
		}},
	}
}

// constNow returns a func() time.Time pinned to the given RFC 3339 string —
// used by tests that simulate the same Runner re-entering Drain at a later
// wall-clock time.
func constNow(t *testing.T, s string) func() time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return func() time.Time { return tm }
}

// TestAsyncJobProgress_OnCompleteLatestEventTimestamp pins the events-walk
// happy path: fetch body carries events at the doc's events_at path; the
// directive's event_time.path locates per-event timestamps; the max across
// the (out-of-order) events lands in cursor.last_timestamp.
func TestAsyncJobProgress_OnCompleteLatestEventTimestamp(t *testing.T) {
	completeWhen := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.status"),
		Equal: vStr("complete"),
	}}
	cfg := &schema.AsyncJobProgress{
		Submit: &schema.AsyncSubmitStep{Step: "submit"},
		Poll: &schema.AsyncPollStep{
			Step:         "poll",
			CompleteWhen: &completeWhen,
		},
		Fetch: &schema.AsyncFetchStep{Step: "fetch"},
		OnComplete: &schema.AsyncOnComplete{
			CursorUpdate: &schema.CursorUpdateDirective{
				Kind:      "latest_event_timestamp",
				EventTime: &schema.EventTime{Path: mustPath("created_at")},
			},
		},
	}
	p := &asyncJobProgress{cfg: cfg, eventsAt: mustPath("items")}
	s := newTestScope(t, nil, map[string]any{"phase": "fetch"})

	// Out-of-order events: max is in the middle, not the last item.
	fetchBody := map[string]any{
		"items": []any{
			map[string]any{"id": "evt-1", "created_at": "2026-05-12T08:00:00Z"},
			map[string]any{"id": "evt-2", "created_at": "2026-05-12T11:45:00Z"},
			map[string]any{"id": "evt-3", "created_at": "2026-05-12T09:15:00Z"},
		},
	}
	fetchRes := &stepResult{statusCode: 200, body: fetchBody}
	if _, _, _, err := p.phaseTransition(s, "fetch", fetchRes); err != nil {
		t.Fatalf("fetch transition: %v", err)
	}
	if got := s.cursor["last_timestamp"]; got != "2026-05-12T11:45:00Z" {
		t.Errorf("cursor.last_timestamp = %v, want 2026-05-12T11:45:00Z (max across events)", got)
	}
}

// TestAsyncJobProgress_OnCompleteLatestEventTimestamp_Lookback: the optional
// per-iteration lookback is subtracted from the chosen max.
func TestAsyncJobProgress_OnCompleteLatestEventTimestamp_Lookback(t *testing.T) {
	lookback := vStr("5m")
	cfg := &schema.AsyncJobProgress{
		Fetch: &schema.AsyncFetchStep{Step: "fetch"},
		OnComplete: &schema.AsyncOnComplete{
			CursorUpdate: &schema.CursorUpdateDirective{
				Kind:      "latest_event_timestamp",
				EventTime: &schema.EventTime{Path: mustPath("created_at")},
				Lookback:  &lookback,
			},
		},
	}
	p := &asyncJobProgress{cfg: cfg, eventsAt: mustPath("items")}
	s := newTestScope(t, nil, map[string]any{"phase": "fetch"})

	fetchRes := &stepResult{statusCode: 200, body: map[string]any{
		"items": []any{
			map[string]any{"created_at": "2026-05-12T10:00:00Z"},
			map[string]any{"created_at": "2026-05-12T10:30:00Z"},
		},
	}}
	if _, _, _, err := p.phaseTransition(s, "fetch", fetchRes); err != nil {
		t.Fatalf("fetch transition: %v", err)
	}
	// max = 10:30Z; minus 5m = 10:25Z.
	if got := s.cursor["last_timestamp"]; got != "2026-05-12T10:25:00Z" {
		t.Errorf("cursor.last_timestamp = %v, want 2026-05-12T10:25:00Z (max − 5m)", got)
	}
}

// TestAsyncJobProgress_OnCompleteLatestEventTimestamp_EmptyEvents: a fetch
// that returns no events leaves the cursor untouched — matching the
// timestamp-progress contract (empty drains don't move the high-water mark).
func TestAsyncJobProgress_OnCompleteLatestEventTimestamp_EmptyEvents(t *testing.T) {
	cfg := &schema.AsyncJobProgress{
		Fetch: &schema.AsyncFetchStep{Step: "fetch"},
		OnComplete: &schema.AsyncOnComplete{
			CursorUpdate: &schema.CursorUpdateDirective{
				Kind:      "latest_event_timestamp",
				EventTime: &schema.EventTime{Path: mustPath("created_at")},
			},
		},
	}
	p := &asyncJobProgress{cfg: cfg, eventsAt: mustPath("items")}
	s := newTestScope(t, nil, map[string]any{
		"phase":          "fetch",
		"last_timestamp": "2026-05-12T08:00:00Z",
	})

	fetchRes := &stepResult{statusCode: 200, body: map[string]any{"items": []any{}}}
	if _, _, _, err := p.phaseTransition(s, "fetch", fetchRes); err != nil {
		t.Fatalf("fetch transition: %v", err)
	}
	if got := s.cursor["last_timestamp"]; got != "2026-05-12T08:00:00Z" {
		t.Errorf("empty-events advance moved cursor: got %v", got)
	}
}

// TestAsyncJobProgress_OnCompleteLatestEventTimestamp_MissingEventTime: a
// directive declared with kind=latest_event_timestamp but no event_time
// surfaces a clear error at apply time. The IR validator rejects this shape
// upstream, but the runtime guard pins the contract for a directive that
// somehow reached the runner without validation.
func TestAsyncJobProgress_OnCompleteLatestEventTimestamp_MissingEventTime(t *testing.T) {
	cfg := &schema.AsyncJobProgress{
		Fetch: &schema.AsyncFetchStep{Step: "fetch"},
		OnComplete: &schema.AsyncOnComplete{
			CursorUpdate: &schema.CursorUpdateDirective{
				Kind: "latest_event_timestamp",
				// EventTime intentionally missing.
			},
		},
	}
	p := &asyncJobProgress{cfg: cfg, eventsAt: mustPath("items")}
	s := newTestScope(t, nil, map[string]any{"phase": "fetch"})
	fetchRes := &stepResult{statusCode: 200, body: map[string]any{"items": []any{}}}
	if _, _, _, err := p.phaseTransition(s, "fetch", fetchRes); err == nil {
		t.Fatalf("expected error for missing event_time, got nil")
	}
}

// TestEndToEnd_AsyncJob_LatestEventTimestamp drives the full submit → poll →
// fetch → on_complete loop against a live httptest server, asserting that
// (a) the fetch body's max event timestamp lands in cursor.last_timestamp,
// (b) the next drain's submit body carries that timestamp via
// {from_progress: latest_timestamp}, and (c) the persisted snapshot mirrors
// the high-water mark.
func TestEndToEnd_AsyncJob_LatestEventTimestamp(t *testing.T) {
	var submitSinces []string
	var hitCount int32
	var serverURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitCount, 1)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/exports":
			var body struct {
				Since string `json:"since"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode submit body: %v", err)
				return
			}
			submitSinces = append(submitSinces, body.Since)
			writeJSON(w, http.StatusAccepted, map[string]any{"export_id": "exp-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/exports/exp-1/status":
			writeJSON(w, http.StatusOK, map[string]any{
				"status":     "complete",
				"result_url": serverURL + "/api/v1/exports/exp-1/data",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/exports/exp-1/data":
			// Out-of-order events so "last item's timestamp" would write the
			// wrong cursor; max_event semantics MUST walk all of them.
			writeJSON(w, http.StatusOK, map[string]any{
				"items": []map[string]any{
					{"id": "evt-1", "created_at": "2026-05-12T08:00:00Z"},
					{"id": "evt-2", "created_at": "2026-05-12T11:45:00Z"},
					{"id": "evt-3", "created_at": "2026-05-12T09:15:00Z"},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	store := &MemoryStore{}
	sink := &captureSink{}

	// Drain 1: no cursor → submit body carries empty "since".
	r1 := &Runner{
		Doc:    asyncLatestEventTimestampDoc(server.URL),
		Sink:   sink,
		Store:  store,
		Now:    constNow(t, "2026-05-12T12:00:00Z"),
		Client: server.Client(),
	}
	if err := r1.Drain(context.Background()); err != nil {
		t.Fatalf("Drain-1: %v", err)
	}

	// Drain 2: cursor.last_timestamp = drain-1's max → submit body carries it.
	r2 := &Runner{
		Doc:    asyncLatestEventTimestampDoc(server.URL),
		Sink:   sink,
		Store:  store,
		Now:    constNow(t, "2026-05-12T13:00:00Z"),
		Client: server.Client(),
	}
	if err := r2.Drain(context.Background()); err != nil {
		t.Fatalf("Drain-2: %v", err)
	}

	if got := atomic.LoadInt32(&hitCount); got != 6 {
		t.Fatalf("server saw %d requests, want 6 (3 per drain)", got)
	}
	if len(submitSinces) != 2 {
		t.Fatalf("captured %d submits, want 2", len(submitSinces))
	}
	if submitSinces[0] != "" {
		t.Errorf("drain-1 since = %q, want empty (no initial lookback)", submitSinces[0])
	}
	if submitSinces[1] != "2026-05-12T11:45:00Z" {
		t.Errorf("drain-2 since = %q, want drain-1 max 2026-05-12T11:45:00Z", submitSinces[1])
	}

	snap, _ := store.Load()
	if got := snap.Cursor["last_timestamp"]; got != "2026-05-12T11:45:00Z" {
		t.Errorf("snapshot cursor.last_timestamp = %v, want drain-2 max 2026-05-12T11:45:00Z", got)
	}
}

// asyncLatestEventTimestampDoc mirrors templates/async_poll.yaml
// but swaps the cursor_update kind from use_now to latest_event_timestamp so
// the fetch body's max event timestamp drives the next drain's window.
func asyncLatestEventTimestampDoc(baseURL string) *schema.Doc {
	completeWhen := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.status"),
		Equal: vStr("complete"),
	}}
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: baseURL},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:           "submit",
				Method:       "POST",
				Path:         ptrValue(vStr("/api/v1/exports")),
				Body:         &schema.Body{JSON: map[string]schema.Value{"since": vFromProg("latest_timestamp")}},
				ExpectStatus: []int{http.StatusAccepted},
			},
			{
				ID:     "poll",
				Method: "GET",
				Path: ptrValue(vConcat(
					vStr("/api/v1/exports/"),
					vRef("cursor.export_id"),
					vStr("/status"),
				)),
			},
			{
				ID:     "fetch",
				Method: "GET",
				URL:    ptrValue(vRef("cursor.result_url")),
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("items")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress: schema.Progress{AsyncJob: &schema.AsyncJobProgress{
			Submit: &schema.AsyncSubmitStep{
				Step:    "submit",
				Extract: map[string]schema.AsyncExtract{"export_id": {Path: mustPath("export_id")}},
			},
			Poll: &schema.AsyncPollStep{
				Step:         "poll",
				CompleteWhen: &completeWhen,
				Extract:      map[string]schema.AsyncExtract{"result_url": {Path: mustPath("result_url")}},
			},
			Fetch: &schema.AsyncFetchStep{Step: "fetch"},
			OnComplete: &schema.AsyncOnComplete{
				CursorUpdate: &schema.CursorUpdateDirective{
					Kind:      "latest_event_timestamp",
					EventTime: &schema.EventTime{Path: mustPath("created_at")},
				},
			},
		}},
	}
}
