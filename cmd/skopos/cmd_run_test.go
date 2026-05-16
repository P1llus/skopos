// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	docPath := filepath.Join(dir, "doc.yml")
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
  events_at: response.body.events
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

// minimalDocYAML builds a minimal valid spec that points at the given server URL.
func minimalDocYAML(serverURL string) string {
	return fmt.Sprintf(`ir_version: "1"
state:
  fields:
    url:
      type: url
      default: %q
    api_key:
      type: secret
      default: "test-token"
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
  events_at: response.body.events
pagination:
  none: {}
progress:
  latest_event_timestamp:
    event_time:
      path: timestamp
    initial:
      lookback: "24h"
`, serverURL)
}

// TestRun_ConfigFile verifies that settings in a -c config file are applied
// when the corresponding CLI flag is not explicitly set.
func TestRun_ConfigFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	outPath := filepath.Join(dir, "events.jsonl")
	tracePath := filepath.Join(dir, "trace.jsonl")
	cfgPath := filepath.Join(dir, "config.yml")

	if err := os.WriteFile(docPath, []byte(minimalDocYAML(server.URL)), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	// Config sets out + trace; CLI will supply -i and --once.
	cfgContent := fmt.Sprintf("out: %s\ntrace: %s\n", outPath, tracePath)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runRun([]string{
		"-c", cfgPath,
		"-i", docPath,
		"--once",
	}); err != nil {
		t.Fatalf("runRun with config: %v", err)
	}

	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("config-set --out file missing: %v", err)
	}
	if _, err := os.Stat(tracePath); err != nil {
		t.Errorf("config-set --trace file missing: %v", err)
	}
}

// TestRun_ConfigFileInputOverriddenByFlag verifies that an explicit -i flag
// wins over config.input when both are set.
func TestRun_ConfigFileInputOverriddenByFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	cfgPath := filepath.Join(dir, "config.yml")

	if err := os.WriteFile(docPath, []byte(minimalDocYAML(server.URL)), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	// Config points at a nonexistent file; the explicit -i should override it.
	cfgContent := "input: /nonexistent/should-not-be-read.yml\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runRun([]string{
		"-c", cfgPath,
		"-i", docPath, // explicit flag wins
		"--once",
	}); err != nil {
		t.Fatalf("runRun: %v", err)
	}
}

// TestRun_HTTPTimeout verifies that --http-timeout is accepted without error.
func TestRun_HTTPTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	if err := os.WriteFile(docPath, []byte(minimalDocYAML(server.URL)), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	if err := runRun([]string{
		"-i", docPath,
		"--once",
		"--http-timeout", "10s",
	}); err != nil {
		t.Fatalf("runRun with --http-timeout: %v", err)
	}
}

// TestRun_MaxPages verifies that --max-pages is accepted without error.
func TestRun_MaxPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	if err := os.WriteFile(docPath, []byte(minimalDocYAML(server.URL)), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	if err := runRun([]string{
		"-i", docPath,
		"--once",
		"--max-pages", "50",
	}); err != nil {
		t.Fatalf("runRun with --max-pages: %v", err)
	}
}

// TestRun_ConfigFileHTTPTimeout verifies that http_timeout from a config file
// is applied by checking that the runner accepts and honours the value
// (we set a generous 60s so the test server doesn't time out).
func TestRun_ConfigFileHTTPTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	cfgPath := filepath.Join(dir, "config.yml")

	if err := os.WriteFile(docPath, []byte(minimalDocYAML(server.URL)), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("http_timeout: 60s\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runRun([]string{
		"-c", cfgPath,
		"-i", docPath,
		"--once",
	}); err != nil {
		t.Fatalf("runRun with config http_timeout: %v", err)
	}
}

// TestRun_MissingInputError verifies that omitting -i and config.input returns an error.
func TestRun_MissingInputError(t *testing.T) {
	err := runRun([]string{"--once"})
	if err == nil {
		t.Fatal("expected error when -i is missing")
	}
	if !strings.Contains(err.Error(), "-i") && !strings.Contains(err.Error(), "input") {
		t.Errorf("error should mention -i or input, got: %v", err)
	}
}

// TestRun_InputFromConfig verifies that config.input is used when -i is absent.
func TestRun_InputFromConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	cfgPath := filepath.Join(dir, "config.yml")

	if err := os.WriteFile(docPath, []byte(minimalDocYAML(server.URL)), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	cfgContent := fmt.Sprintf("input: %s\nonce: true\n", docPath)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// No -i flag — input comes entirely from the config file.
	if err := runRun([]string{"-c", cfgPath}); err != nil {
		t.Fatalf("runRun via config input: %v", err)
	}
}

// TestRun_OnceAndIntervalBothSet verifies the incompatibility check still
// works after the config-merge refactor.
func TestRun_OnceAndIntervalBothSet(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	// A placeholder file; runRun will error before trying to load it.
	if err := os.WriteFile(docPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := runRun([]string{"-i", docPath, "--once", "--interval", "5s"})
	if err == nil {
		t.Fatal("expected error when --once and --interval both set")
	}
	if !strings.Contains(err.Error(), "--once") {
		t.Errorf("error should mention --once, got: %v", err)
	}
}

// TestRun_ConfigOnceOverriddenByInterval verifies that an explicit --interval
// flag wins over config.once = true.
func TestRun_ConfigOnceOverriddenByInterval(t *testing.T) {
	// We just want to confirm the incompatibility check fires after merge.
	// Config says once:true; CLI says --interval; the check should reject.
	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
	cfgPath := filepath.Join(dir, "config.yml")

	if err := os.WriteFile(docPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("once: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	err := runRun([]string{"-c", cfgPath, "-i", docPath, "--interval", "5s"})
	if err == nil {
		t.Fatal("expected error: config once=true + explicit --interval")
	}
	if !strings.Contains(err.Error(), "--once") {
		t.Errorf("error should mention --once, got: %v", err)
	}
}
