// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	// The zero value is the default — runner uses its own internal defaults
	// when these are zero.
	if cfg.Input != "" {
		t.Errorf("Input = %q, want empty", cfg.Input)
	}
	if cfg.HTTPTimeout != 0 {
		t.Errorf("HTTPTimeout = %v, want 0 (use runner default)", cfg.HTTPTimeout)
	}
	if cfg.MaxPages != 0 {
		t.Errorf("MaxPages = %d, want 0 (use runner default)", cfg.MaxPages)
	}
	if cfg.Once {
		t.Error("Once should default to false")
	}
	if cfg.Interval != 0 {
		t.Errorf("Interval = %v, want 0", cfg.Interval)
	}
}

func TestLoadConfig_RoundTrip(t *testing.T) {
	content := `
input: spec.yaml
state: state.json
out: events.jsonl
trace: trace.jsonl
once: true
interval: 5m
http_timeout: 45s
max_pages: 500
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.Input != "spec.yaml" {
		t.Errorf("Input = %q, want spec.yaml", cfg.Input)
	}
	if cfg.State != "state.json" {
		t.Errorf("State = %q, want state.json", cfg.State)
	}
	if cfg.Out != "events.jsonl" {
		t.Errorf("Out = %q, want events.jsonl", cfg.Out)
	}
	if cfg.Trace != "trace.jsonl" {
		t.Errorf("Trace = %q, want trace.jsonl", cfg.Trace)
	}
	if !cfg.Once {
		t.Error("Once should be true")
	}
	if cfg.Interval != 5*time.Minute {
		t.Errorf("Interval = %v, want 5m", cfg.Interval)
	}
	if cfg.HTTPTimeout != 45*time.Second {
		t.Errorf("HTTPTimeout = %v, want 45s", cfg.HTTPTimeout)
	}
	if cfg.MaxPages != 500 {
		t.Errorf("MaxPages = %d, want 500", cfg.MaxPages)
	}
}

func TestLoadConfig_Partial(t *testing.T) {
	content := `input: myspec.yaml`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Input != "myspec.yaml" {
		t.Errorf("Input = %q, want myspec.yaml", cfg.Input)
	}
	// Omitted fields remain zero.
	if cfg.State != "" {
		t.Errorf("State = %q, want empty", cfg.State)
	}
	if cfg.HTTPTimeout != 0 {
		t.Errorf("HTTPTimeout = %v, want 0", cfg.HTTPTimeout)
	}
}

func TestLoadConfig_UnknownKeyRejected(t *testing.T) {
	content := `intterval: 5m` // typo
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown key 'intterval', got nil")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := loadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	content := `input: [unclosed`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}
