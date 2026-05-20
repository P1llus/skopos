// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// erroringSink fails the first Emit with err; subsequent calls succeed.
// Used to pin emit-before-progress-before-commit ordering.
type erroringSink struct {
	err error
}

func (e *erroringSink) Emit(any) error { return e.err }
func (*erroringSink) Flush() error     { return nil }

// TestApplyProgress_SnapshotThenWrite pins the pre-write snapshot
// semantics: every from: expression reads the OLD state, even when an
// earlier write would have overwritten the source. The canonical
// sliding-window pattern relies on this.
func TestApplyProgress_SnapshotThenWrite(t *testing.T) {
	s := newTestScope(t, map[string]any{
		"window_end": "2026-01-01T00:00:00Z",
	})

	writes := schema.Progress{
		{
			To:   mustPath("state.window_start"),
			From: vRef("state.window_end"),
		},
		{
			To:   mustPath("state.window_end"),
			From: vNow(),
		},
	}

	if err := applyProgress(s, writes); err != nil {
		t.Fatalf("applyProgress: %v", err)
	}
	if got := s.state["window_start"]; got != "2026-01-01T00:00:00Z" {
		t.Errorf("window_start = %v, want pre-write window_end", got)
	}
	if s.state["window_end"] == "2026-01-01T00:00:00Z" {
		t.Error("window_end should have advanced to now()")
	}
}

// TestApplyProgress_CoerceAndRegex pins the per-entry transform chain:
// regex first (group 0 is the full match), then coerce.
func TestApplyProgress_CoerceAndRegex(t *testing.T) {
	s := newTestScope(t, map[string]any{
		"raw_etag": `W/"v42"`,
	})

	writes := schema.Progress{
		{
			To:     mustPath("state.parsed_version"),
			From:   vRef("state.raw_etag"),
			Regex:  `\d+`,
			Coerce: "int",
		},
	}
	if err := applyProgress(s, writes); err != nil {
		t.Fatalf("applyProgress: %v", err)
	}
	got := s.state["parsed_version"]
	want := int64(42)
	if got != want {
		t.Errorf("parsed_version = %#v (%T); want %#v (int64) — regex must run first, then coerce", got, got, want)
	}
}

// TestApplyProgress_PersistsAcrossDrains pins the persistence contract:
// progress writes show up in the next drain's loaded snapshot.
func TestApplyProgress_PersistsAcrossDrains(t *testing.T) {
	var seenSince []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSince = append(seenSince, r.URL.Query().Get("since"))
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"ts": "2026-01-01T00:00:01Z"},
		}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["last_timestamp"] = schema.FieldDecl{Type: "timestamp"}
	doc.Requests[0].Query = map[string]schema.Value{
		"since": vRefDefault("state.last_timestamp", vStr("")),
	}
	doc.Progress = schema.Progress{
		{
			To:   mustPath("state.last_timestamp"),
			From: schema.Value{Max: new(vRef("events.*.ts"))},
		},
	}

	store := &MemoryStore{}
	// First drain.
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	// Second drain — should send ?since=2026-01-01T00:00:01Z.
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}
	if len(seenSince) != 2 {
		t.Fatalf("server calls = %d, want 2", len(seenSince))
	}
	if seenSince[0] != "" {
		t.Errorf("first since = %q, want empty (no prior state)", seenSince[0])
	}
	if seenSince[1] != "2026-01-01T00:00:01Z" {
		t.Errorf("second since = %q, want last_timestamp from drain 1", seenSince[1])
	}
}

// TestApplyProgress_SinkFailureSkipsCommit pins the emit-before-progress-
// before-commit ordering: when Sink.Emit returns an error, applyProgress
// must NOT have run for that page, so the deferred Save captures the
// pre-emit state. Without this ordering, an event that the sink rejected
// could still advance the high-water mark and silently drop on the next
// drain.
func TestApplyProgress_SinkFailureSkipsCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{
			map[string]any{"ts": "2026-01-01T00:00:42Z"},
		}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["last_timestamp"] = schema.FieldDecl{
		Type:    "timestamp",
		Default: new(vStr("seed-value")),
	}
	doc.Progress = schema.Progress{
		{
			To:   mustPath("state.last_timestamp"),
			From: schema.Value{Max: new(vRef("events.*.ts"))},
		},
	}

	store := &MemoryStore{}
	sink := &erroringSink{err: errors.New("downstream wedged")}
	r := &Runner{Doc: doc, Sink: sink, Store: store, Now: fixedNow(), Client: server.Client()}

	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: expected error from sink.Emit; got nil")
	}

	snap, _ := store.Load()
	got := snap.State["last_timestamp"]
	// Seed value survives; the progress write did NOT commit.
	if got != "seed-value" {
		t.Errorf("last_timestamp = %v; want %q (sink failure must not commit progress)", got, "seed-value")
	}
}

// TestApplyProgress_WarnDoesNotFire pins the iterWarn semantics: an
// error.mode: warn dispatch does NOT fire progress writes for the bad
// page, but pagination still advances.
func TestApplyProgress_WarnDoesNotFire(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
		calls.Add(1)
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Error = &schema.ErrorBlock{Mode: "warn"}
	doc.State["last_seen_at"] = schema.FieldDecl{Type: "timestamp"}
	doc.Progress = schema.Progress{
		{To: mustPath("state.last_seen_at"), From: vNow()},
	}

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain (warn mode): %v", err)
	}
	snap, _ := store.Load()
	if got := snap.State["last_seen_at"]; got != nil {
		t.Errorf("last_seen_at = %v, want nil (warn skips progress)", got)
	}
}

// TestApplyProgress_RejectsNonStateTo pins the destination check: a
// progress write must target state.<name>. (The validator enforces this
// at parse time too; the runtime defensive arm is the last line of
// defence.)
func TestApplyProgress_RejectsNonStateTo(t *testing.T) {
	s := newTestScope(t, nil)
	writes := schema.Progress{
		{
			To:   mustPath("cache.bad_destination"),
			From: vStr("x"),
		},
	}
	err := applyProgress(s, writes)
	if err == nil {
		t.Errorf("expected error for non-state to:, got nil")
	}
}

// TestApplyProgress_RegexMissReturnsNil pins the regex-no-match
// behaviour: an unmatched pattern returns nil; the staged write fires
// (writing nil clears the field).
func TestApplyProgress_RegexMissReturnsNil(t *testing.T) {
	s := newTestScope(t, map[string]any{"raw": "no-match-here"})
	writes := schema.Progress{
		{
			To:    mustPath("state.parsed"),
			From:  vRef("state.raw"),
			Regex: `^\d{4}$`,
		},
	}
	if err := applyProgress(s, writes); err != nil {
		t.Fatalf("applyProgress: %v", err)
	}
	if got, ok := s.state["parsed"]; got != nil || !ok {
		t.Errorf("parsed = (%v, %v), want (nil, true)", got, ok)
	}
}

// TestPersistentVsScratchClassification confirms the validator's
// lifetime inference: a field declared and written by progress is
// persistent (snapshot survives drains); a field declared and written
// by pagination is per-drain scratch (snapshot drops it).
func TestPersistentVsScratchClassification(t *testing.T) {
	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"persistent": {Type: "string"},
			"scratch":    {Type: "string"},
			"opconfig":   {Type: "string", Default: new(vStr("hi"))},
		},
		Progress: schema.Progress{
			{To: mustPath("state.persistent"), From: vStr("x")},
		},
		Pagination: schema.Pagination{CursorToken: &schema.CursorTokenPagination{
			From: mustPath("response.body.next"),
			To:   mustPath("state.scratch"),
		}},
	}

	got := persistentStateFields(doc)
	if _, ok := got["persistent"]; !ok {
		t.Errorf("persistent field missing from persistent set: %v", got)
	}
	if _, ok := got["scratch"]; ok {
		t.Errorf("scratch field incorrectly classified as persistent: %v", got)
	}

	got = perDrainScratchFields(doc)
	if _, ok := got["scratch"]; !ok {
		t.Errorf("scratch field missing from scratch set: %v", got)
	}

	// opconfig is neither — it's operator-config (default-only, no
	// writes). It must NOT be in either set.
	if _, ok := persistentStateFields(doc)["opconfig"]; ok {
		t.Errorf("opconfig classified as persistent: %v", persistentStateFields(doc))
	}
	if _, ok := perDrainScratchFields(doc)["opconfig"]; ok {
		t.Errorf("opconfig classified as scratch: %v", perDrainScratchFields(doc))
	}

	// classifyStateField wraps both sets and returns the lifetime enum.
	if lt, ok := classifyStateField(doc, "persistent"); !ok || lt != lifetimePersistent {
		t.Errorf("classify(persistent) = (%v, %v); want (lifetimePersistent, true)", lt, ok)
	}
	if lt, ok := classifyStateField(doc, "scratch"); !ok || lt != lifetimePerDrainScratch {
		t.Errorf("classify(scratch) = (%v, %v); want (lifetimePerDrainScratch, true)", lt, ok)
	}
	if lt, ok := classifyStateField(doc, "opconfig"); !ok || lt != lifetimeOperatorConfig {
		t.Errorf("classify(opconfig) = (%v, %v); want (lifetimeOperatorConfig, true)", lt, ok)
	}
}

// TestSnapshot_ExcludesScratchFields confirms the persist-time filter:
// scratch fields are stripped from the persisted Snapshot.
func TestSnapshot_ExcludesScratchFields(t *testing.T) {
	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"keep": {Type: "string"},
			"drop": {Type: "string"},
		},
		Progress: schema.Progress{
			{To: mustPath("state.keep"), From: vStr("kept")},
		},
		Pagination: schema.Pagination{CursorToken: &schema.CursorTokenPagination{
			From: mustPath("response.body.next"),
			To:   mustPath("state.drop"),
		}},
	}

	s, err := newScope(doc, Snapshot{}, fixedNow())
	if err != nil {
		t.Fatalf("newScope: %v", err)
	}
	s.state["keep"] = "persisted-value"
	s.state["drop"] = "scratch-value"

	snap := s.snapshot()
	if snap.State["keep"] != "persisted-value" {
		t.Errorf("snapshot.State[keep] = %v; want persisted-value", snap.State["keep"])
	}
	if _, found := snap.State["drop"]; found {
		t.Errorf("scratch field 'drop' leaked into snapshot: %v", snap.State)
	}
}

// TestSeedDefaults pins newScope's seed step: declared defaults land in
// scope.state, then the loaded snapshot overlays. Snapshot values win
// over defaults; both win over a never-set field.
func TestSeedDefaults(t *testing.T) {
	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"defaulted":     {Type: "string", Default: new(vStr("from-default"))},
			"from_snapshot": {Type: "string", Default: new(vStr("default-replaced"))},
			"snapshot_only": {Type: "string"},
			"unset":         {Type: "string"},
		},
	}
	snap := Snapshot{State: map[string]any{
		"from_snapshot": "from-snapshot",
		"snapshot_only": "snap-only",
	}}
	s, err := newScope(doc, snap, fixedNow())
	if err != nil {
		t.Fatalf("newScope: %v", err)
	}
	wantState := map[string]any{
		"defaulted":     "from-default",
		"from_snapshot": "from-snapshot",
		"snapshot_only": "snap-only",
	}
	if !reflect.DeepEqual(s.state, wantState) {
		t.Errorf("scope.state = %#v, want %#v", s.state, wantState)
	}
	if _, found := s.state["unset"]; found {
		t.Errorf("unset field appeared in scope.state: %v", s.state["unset"])
	}
}
