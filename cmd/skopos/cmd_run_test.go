// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_TraceFlag exercises the --trace flag end-to-end: a minimal IR
// doc is pulled against an httptest server, runRun is invoked with
// --trace pointing at a tempfile, and the resulting JSONL is decoded
// and checked for the redaction-safe Exchange shape (status, redacted
// Authorization header, body metadata, no raw query).
func TestRun_TraceFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer trace-test-token" {
			t.Errorf("Authorization = %q, want Bearer trace-test-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"events":[{"id":"e1","timestamp":"2026-05-12T08:00:00Z"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "doc.yaml")
	tracePath := filepath.Join(dir, "trace.jsonl")
	outPath := filepath.Join(dir, "events.jsonl")

	docYAML := fmt.Sprintf(`ir_version: "1"
state:
  fields:
    url:
      type: url
      default: %q
    api_key:
      type: secret
      default: "trace-test-token"
defaults:
  base_url: {ref: state.url}
auth:
  bearer:
    token: {ref: state.api_key}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  latest_event_timestamp:
    event_time:
      path: timestamp
    initial:
      lookback: "24h"
`, server.URL)
	if err := os.WriteFile(docPath, []byte(docYAML), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	if err := runRun([]string{
		"-i", docPath,
		"--once",
		"--out", outPath,
		"--trace", tracePath,
	}); err != nil {
		t.Fatalf("runRun: %v", err)
	}

	traceData, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(traceData), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("trace lines = %d, want 1; data=%q", len(lines), string(traceData))
	}

	var ex map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ex); err != nil {
		t.Fatalf("decode exchange: %v; line=%q", err, lines[0])
	}

	if got := ex["status"]; got != float64(http.StatusOK) {
		t.Errorf("status = %v, want 200", got)
	}
	if got := ex["method"]; got != "GET" {
		t.Errorf("method = %v, want GET", got)
	}
	if got, _ := ex["url"].(string); !strings.HasSuffix(got, "/api/v1/events") {
		t.Errorf("url = %v, want suffix /api/v1/events", got)
	}
	headers, _ := ex["request_headers"].(map[string]any)
	if got := headers["Authorization"]; got != "<redacted>" {
		t.Errorf("Authorization header = %v, want <redacted>", got)
	}
	if got, _ := ex["response_body"].(string); !strings.Contains(got, "object-like") {
		t.Errorf("response_body = %v, want object-like classification", got)
	}

	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("event output missing: %v", err)
	}
}

// TestRun_TraceFlag_AppendsAcrossRuns confirms the trace file is opened
// in append mode, so a second run does not truncate the first run's
// records. This is the property an operator relies on when re-running a
// drain after fixing a transient error.
func TestRun_TraceFlag_AppendsAcrossRuns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "doc.yaml")
	tracePath := filepath.Join(dir, "trace.jsonl")
	outPath := filepath.Join(dir, "events.jsonl")

	docYAML := fmt.Sprintf(`ir_version: "1"
state:
  fields:
    url:
      type: url
      default: %q
    api_key:
      type: secret
      default: "tok"
defaults:
  base_url: {ref: state.url}
auth:
  bearer:
    token: {ref: state.api_key}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  latest_event_timestamp:
    event_time:
      path: timestamp
    initial:
      lookback: "24h"
`, server.URL)
	if err := os.WriteFile(docPath, []byte(docYAML), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := runRun([]string{
			"-i", docPath,
			"--once",
			"--out", outPath,
			"--trace", tracePath,
		}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("trace lines after 2 runs = %d, want 2; data=%q", len(lines), string(data))
	}
}
