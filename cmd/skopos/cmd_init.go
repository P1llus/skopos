// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// configTemplate is the YAML document written by "skopos init". It includes
// a comment for every field so the generated file is self-documenting. The
// values shown are the built-in defaults; omitting a field is equivalent to
// leaving it at the default.
const configTemplate = `# skopos run configuration
# All fields are optional. CLI flags take precedence over values set here.
# Omit a field to use the built-in default.

# input: path to the spec file to run. Equivalent to -i / --input.
# input: spec.yml

# state: path to the JSON state file for persisting state.* between runs.
# Equivalent to --state. Omit to use an in-memory store (state lost on exit).
# state: state.json

# out: path to write JSONL events to. Equivalent to --out.
# Defaults to stdout when omitted.
# out: events.jsonl

# trace: path to write per-exchange JSONL trace records to.
# Equivalent to --trace. Trace is disabled when omitted.
# trace: trace.jsonl

# once: run a single drain and exit. Equivalent to --once.
# once: false

# interval: sleep this duration between drains in continuous mode.
# Equivalent to --interval. Omit (or set to 0) for one-shot mode.
# interval: 30s

# http_timeout: per-request HTTP timeout. Defaults to 30s when omitted.
# http_timeout: 30s

# max_pages: cap on the number of pagination iterations per drain.
# Defaults to 10000 when omitted.
# max_pages: 10000
`

// runInit is the entry point for "skopos init".
//
//	skopos init [-o path] [--force]
//
// Writes a commented default config to the given path (or stdout when -o is
// omitted or "-"). --force allows overwriting an existing file.
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	output := fs.String("o", "-", "Output path for the config file. Use - for stdout (the default).")
	force := fs.Bool("force", false, "Overwrite the output file if it already exists.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var w io.Writer
	if *output == "" || *output == "-" {
		w = os.Stdout
	} else {
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if !*force {
			flags |= os.O_EXCL
		}
		f, err := os.OpenFile(*output, flags, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("skopos init: %s already exists (use --force to overwrite)", *output)
			}
			return fmt.Errorf("skopos init: create %s: %w", *output, err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	if _, err := fmt.Fprint(w, configTemplate); err != nil {
		return fmt.Errorf("skopos init: write: %w", err)
	}
	return nil
}
