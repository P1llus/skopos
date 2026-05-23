// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the optional run configuration read from a -c/--config file.
// Every top-level field corresponds to a CLI flag; the merge order is:
//
//	built-in defaults → config file → explicit CLI flags (highest priority)
//
// An explicit flag always wins; an omitted flag falls back to the config
// value, which itself falls back to the built-in default.
//
// When Runs is non-empty the file describes a fleet: one Runner per
// entry, fanned out across goroutines. Per-entry values override the
// top-level fields by inheritance — see resolveRunEntry.
type Config struct {
	// Input is the path to the spec file. Equivalent to -i.
	Input string `yaml:"input"`

	// State is the path to the JSON state file. Equivalent to --state.
	State string `yaml:"state"`

	// Out is the path for JSONL event output. Equivalent to --out.
	Out string `yaml:"out"`

	// Trace is the path for per-exchange JSONL trace output. Equivalent to --trace.
	Trace string `yaml:"trace"`

	// Once runs a single drain and exits. Equivalent to --once.
	Once bool `yaml:"once"`

	// Interval is the sleep duration between drains in continuous mode.
	// Equivalent to --interval.
	Interval time.Duration `yaml:"interval"`

	// HTTPTimeout caps every individual HTTP request. When zero, the
	// built-in default (30s) is used.
	HTTPTimeout time.Duration `yaml:"http_timeout"`

	// MaxPages caps the number of pagination iterations per drain.
	// When zero, the client default (10 000) is used.
	MaxPages int `yaml:"max_pages"`

	// SinkBuffer, when > 0, delivers events through a buffered consumer
	// goroutine of this depth so a slow sink does not stall the next page
	// fetch. Zero keeps synchronous delivery. Equivalent to --sink-buffer.
	SinkBuffer int `yaml:"sink_buffer"`

	// StateDir, when set, is the directory used to derive each fleet
	// entry's state file when the entry does not set State explicitly.
	// The derived path is {StateDir}/{basename(entry.Input) without ext}.json.
	// Ignored outside fleet mode.
	StateDir string `yaml:"state_dir"`

	// Runs lists fleet entries. When non-empty (and no explicit -i was
	// passed on the CLI), each entry runs in its own goroutine with its
	// own Runner. Per-entry fields override the top-level defaults; Trace
	// is NOT inherited from the top level — entries that want a trace
	// must set it explicitly.
	Runs []RunEntry `yaml:"runs"`
}

// RunEntry is one spec to execute in a fleet. Fields parallel the
// top-level Config flags; an unset field inherits from the top level
// except for Trace, which is opt-in per-entry.
type RunEntry struct {
	Input       string        `yaml:"input"`
	State       string        `yaml:"state"`
	Out         string        `yaml:"out"`
	Trace       string        `yaml:"trace"`
	Once        bool          `yaml:"once"`
	Interval    time.Duration `yaml:"interval"`
	HTTPTimeout time.Duration `yaml:"http_timeout"`
	MaxPages    int           `yaml:"max_pages"`
	SinkBuffer  int           `yaml:"sink_buffer"`
}

// DefaultConfig returns a Config populated with the built-in defaults.
// Fields that the runner treats as "zero means use internal default"
// (HTTPTimeout, MaxPages) are left at zero so the runner's own defaults
// continue to apply unless the user explicitly overrides them.
func DefaultConfig() Config {
	return Config{}
}

// loadConfig reads and strictly-decodes a YAML config file from path.
// Unknown keys in the file are rejected so typos surface immediately.
func loadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		// A file that contains only comments (or is empty) produces io.EOF
		// from the decoder — treat it as a valid empty config so that a
		// freshly-generated "skopos init" file can be loaded without error.
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	return cfg, nil
}
