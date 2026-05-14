// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_Stdout(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runInit([]string{}); err != nil {
			t.Fatalf("runInit stdout: %v", err)
		}
	})

	if !strings.Contains(out, "skopos run configuration") {
		t.Errorf("stdout output missing header comment; got:\n%s", out)
	}
	if !strings.Contains(out, "http_timeout") {
		t.Errorf("stdout output missing http_timeout field; got:\n%s", out)
	}
}

func TestInit_ToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := runInit([]string{"-o", path}); err != nil {
		t.Fatalf("runInit to file: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "skopos run configuration") {
		t.Errorf("file missing header comment")
	}
	if !strings.Contains(content, "input:") {
		t.Errorf("file missing input field")
	}
	if !strings.Contains(content, "http_timeout:") {
		t.Errorf("file missing http_timeout field")
	}
	if !strings.Contains(content, "max_pages:") {
		t.Errorf("file missing max_pages field")
	}
}

func TestInit_FailsIfFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := runInit([]string{"-o", path})
	if err == nil {
		t.Fatal("expected error when output file exists without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists', got: %v", err)
	}

	// File must not have been truncated.
	data, _ := os.ReadFile(path)
	if string(data) != "existing" {
		t.Errorf("existing file was modified")
	}
}

func TestInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := runInit([]string{"-o", path, "--force"}); err != nil {
		t.Fatalf("runInit --force: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) == "old content" {
		t.Error("--force did not overwrite the file")
	}
	if !strings.Contains(string(data), "skopos run configuration") {
		t.Errorf("overwritten file missing expected content")
	}
}

func TestInit_OutputIsValidLoadableConfig(t *testing.T) {
	// The generated template (with all lines commented out) must be
	// loadable without error even though every field is absent.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := runInit([]string{"-o", path}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig on generated config: %v", err)
	}
	// All fields should be zero (all commented out in the template).
	if cfg.Input != "" || cfg.State != "" || cfg.Out != "" {
		t.Errorf("unexpected non-zero fields in generated config: %+v", cfg)
	}
}
