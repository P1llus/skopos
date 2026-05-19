// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestFileStore_RoundTrip pins the round-trip contract: Save then Load
// returns a Snapshot semantically equal to the one written. The on-disk
// shape is a single {"state": {...}} object — cache, events, extract,
// steps, response, and per-evaluation roots all live in process memory
// only.
func TestFileStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(filepath.Join(dir, "state.json"))

	in := Snapshot{
		State: map[string]any{
			"last_timestamp": "2026-05-12T08:00:00Z",
			"page":           float64(3),
			"window_start":   "2026-05-12T07:55:00Z",
		},
	}
	if err := fs.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := fs.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(out.State, in.State) {
		t.Errorf("State round-trip mismatch: got %#v, want %#v", out.State, in.State)
	}
}

// TestFileStore_LoadMissingIsZero pins the "absent file → zero Snapshot"
// contract used to bootstrap a first run.
func TestFileStore_LoadMissingIsZero(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(filepath.Join(dir, "never-written.json"))
	got, err := fs.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v, want nil", err)
	}
	if got.State != nil {
		t.Errorf("Load on missing returned %#v, want zero Snapshot", got)
	}
}

// TestFileStore_AtomicWrite pins the atomic-rename contract: after Save
// returns, the on-disk file is always a complete, valid Snapshot. We also
// assert no temp file is left behind once Save succeeds — the "temp +
// rename" sequence must clean up after itself.
func TestFileStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	fs := NewFileStore(path)

	for i := range 5 {
		in := Snapshot{
			State: map[string]any{"page": float64(i)},
		}
		if err := fs.Save(in); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		var got Snapshot
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("partial / invalid JSON observed after Save %d: %v; raw=%q", i, err, string(raw))
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file leaked after Save: %s", e.Name())
		}
	}
}

// TestFileStore_LoadDropsUnknownKeys pins the codec's "unknown top-level
// keys are silently dropped" contract: a file written by an out-of-band
// rev that carried an extra top-level key still loads cleanly under the
// current Snapshot.
func TestFileStore_LoadDropsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	raw := []byte(`{"state": {"page": 1}, "obsolete": {"x": 1}}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fs := NewFileStore(path)
	snap, err := fs.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if page, _ := snap.State["page"].(float64); page != 1 {
		t.Errorf("State.page = %v, want 1", snap.State["page"])
	}
}
