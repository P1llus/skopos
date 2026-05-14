// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/p1llus/skopos/internal/testserver"
)

// TestSmoke boots the testserver handler and asserts that the bearer_simple
// scenario returns 200 with a non-empty events array.
func TestSmoke(t *testing.T) {
	opts := testserver.Options{
		PageSize:       2,
		EventsPerDrain: 5,
		Now:            func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	srv := testserver.New(opts, testserver.AllScenarios()...)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/bearer_simple/events", nil)
	req.Header.Set("Authorization", "Bearer "+testserver.DefaultBearer)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /bearer_simple/events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	events, ok := body["events"].([]any)
	if !ok || len(events) == 0 {
		t.Errorf("expected non-empty events, got %v", body["events"])
	}
}

// TestFilterScenarios checks that filterScenarios correctly selects by name.
func TestFilterScenarios(t *testing.T) {
	all := testserver.AllScenarios()
	got := filterScenarios(all, "bearer_simple,cursor_token")
	if len(got) != 2 {
		t.Errorf("filterScenarios returned %d scenarios, want 2", len(got))
	}
	if got[0].Name() != "bearer_simple" {
		t.Errorf("got[0].Name() = %q, want bearer_simple", got[0].Name())
	}
	if got[1].Name() != "cursor_token" {
		t.Errorf("got[1].Name() = %q, want cursor_token", got[1].Name())
	}
}

// TestFilterScenarios_Empty returns all when filter is empty.
func TestFilterScenarios_Empty(t *testing.T) {
	all := testserver.AllScenarios()
	got := filterScenarios(all, "")
	if len(got) != len(all) {
		t.Errorf("filterScenarios('') returned %d, want %d", len(got), len(all))
	}
}
