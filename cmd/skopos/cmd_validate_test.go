// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validDoc is a minimal IR document that schema.Validate accepts cleanly.
const validDoc = `ir_version: "1"
state:
  fields:
    url:
      type: url
      default: "http://example.invalid"
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
  events_at: events
pagination:
  none: {}
progress:
  latest_event_timestamp:
    event_time:
      path: timestamp
    initial:
      lookback: "24h"
`

// invalidDoc is missing the required `auth` discriminator entirely so
// schema.Validate produces an error-severity diagnostic.
const invalidDoc = `ir_version: "1"
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`

// TestValidate_HappyPath drives runValidate against a clean document
// and asserts no diagnostics print and no error returns. runValidate
// only calls os.Exit on error severity, so the happy path can run
// in-process.
func TestValidate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.yml")
	if err := os.WriteFile(path, []byte(validDoc), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runValidate([]string{"-i", path}); err != nil {
			t.Fatalf("runValidate: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("happy-path printed diagnostics: %q", out)
	}
}

// TestValidate_FailReturnsSentinel exercises the in-process path: an
// invalid document produces an errValidationFailed return + diagnostics
// on stdout. main.go then maps the sentinel to exit code 1.
func TestValidate_FailReturnsSentinel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yml")
	if err := os.WriteFile(path, []byte(invalidDoc), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	var err error
	out := captureStdout(t, func() {
		err = runValidate([]string{"-i", path})
	})
	if err == nil {
		t.Fatalf("expected errValidationFailed, got nil")
	}
	if !errors.Is(err, errValidationFailed) {
		t.Fatalf("expected errValidationFailed, got %v", err)
	}
	if !strings.Contains(out, "error") {
		t.Errorf("expected diagnostic on stdout, got: %q", out)
	}
}

// captureStdout reroutes os.Stdout for the duration of fn and returns
// what was written. Standard pattern used to test helpers that print
// directly to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
