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

// invalidDoc is missing the required `auth` discriminator entirely so
// schema.Validate produces an error-severity diagnostic.
const invalidDoc = `ir_version: "1"
requests:
  - method: GET
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`

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

// TestValidate_CleanDocReturnsNil pins the happy path: a structurally
// valid document returns nil and prints no diagnostics.
func TestValidate_CleanDocReturnsNil(t *testing.T) {
	clean := `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "good.yml")
	if err := os.WriteFile(path, []byte(clean), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	var err error
	out := captureStdout(t, func() {
		err = runValidate([]string{"-i", path})
	})
	if err != nil {
		t.Fatalf("clean doc: %v", err)
	}
	if strings.Contains(out, "error") {
		t.Errorf("expected no error diagnostics; got %q", out)
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
