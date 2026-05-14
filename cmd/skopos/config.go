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
// Every field corresponds to a CLI flag; the merge order is:
//
//	built-in defaults → config file → explicit CLI flags (highest priority)
//
// An explicit flag always wins; an omitted flag falls back to the config
// value, which itself falls back to the built-in default.
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
