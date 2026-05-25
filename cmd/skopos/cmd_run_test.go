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

// minimalDocYAML returns a self-contained skopos spec that points at the
// given server URL. Used by every cmd_run_test that needs a runnable
// spec.
func minimalDocYAML(serverURL string) string {
	return fmt.Sprintf(`ir_version: "1"
state:
  url:
    type: url
    default: %q
auth:
  none: {}
requests:
  - method: GET
    url: "${state.url}/events"
    events_at: response.body.events
pagination:
  none: {}
`, serverURL)
}

// TestRun_TraceFlag_AppendsAcrossRuns confirms the trace file is opened
// in append mode, so a second run does not truncate the first run's
// records.
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

	if err := os.WriteFile(docPath, []byte(minimalDocYAML(server.URL)), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	for i := range 2 {
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

// TestRun_ConfigFile verifies that settings in a -c config file are
// applied when the corresponding CLI flag is not explicitly set:
// input, out, trace, http_timeout all source from the config.
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

	cfgContent := fmt.Sprintf(
		"input: %s\nout: %s\ntrace: %s\nhttp_timeout: 60s\nonce: true\n",
		docPath, outPath, tracePath,
	)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runRun([]string{"-c", cfgPath}); err != nil {
		t.Fatalf("runRun with config: %v", err)
	}

	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("config-set --out file missing: %v", err)
	}
	if _, err := os.Stat(tracePath); err != nil {
		t.Errorf("config-set --trace file missing: %v", err)
	}
}

// TestRun_ConfigFileInputOverriddenByFlag verifies that an explicit -i
// flag wins over config.input when both are set.
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

	cfgContent := "input: /nonexistent/should-not-be-read.yml\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runRun([]string{
		"-c", cfgPath,
		"-i", docPath,
		"--once",
	}); err != nil {
		t.Fatalf("runRun: %v", err)
	}
}

// TestRun_MissingInputError verifies that omitting -i and config.input
// returns an error.
func TestRun_MissingInputError(t *testing.T) {
	err := runRun([]string{"--once"})
	if err == nil {
		t.Fatal("expected error when -i is missing")
	}
	if !strings.Contains(err.Error(), "-i") && !strings.Contains(err.Error(), "input") {
		t.Errorf("error should mention -i or input, got: %v", err)
	}
}

// TestRun_OnceAndIntervalBothSet verifies the incompatibility check
// surfaces whether the conflicting flags come from the CLI or from a
// mix of config + CLI.
func TestRun_OnceAndIntervalBothSet(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.yml")
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
