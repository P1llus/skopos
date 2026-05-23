// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	os.Exit(Main())
}

// Main is the testable entry point for the skopos CLI. It returns the process
// exit code so testscript.RunMain can invoke it in-process.
func Main() int {
	if len(os.Args) < 2 {
		usage()
		return 1
	}

	switch os.Args[1] {
	case "validate":
		if err := runValidate(os.Args[2:]); err != nil {
			// errValidationFailed already printed diagnostics to stdout;
			// surface only the non-zero exit so the operator surface stays
			// diagnostic-only (no duplicate "skopos: validation failed").
			if !errors.Is(err, errValidationFailed) {
				fmt.Fprintln(os.Stderr, "skopos:", err)
			}
			return 1
		}
	case "run":
		if err := runRun(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "skopos:", err)
			return 1
		}
	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "skopos:", err)
			return 1
		}
	case "template":
		if err := runTemplate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "skopos:", err)
			return 1
		}
	case "version", "--version", "-V":
		if err := runVersion(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "skopos:", err)
			return 1
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "skopos: unknown subcommand %q\n", os.Args[1])
		usage()
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `skopos — spec runner for HTTP-pull integrations

Usage:
  skopos validate [-i path]
      Validate a spec document. Reads stdin when -i is omitted; writes
      diagnostics to stdout. Exits 1 on any error-severity diagnostic.

  skopos run [-c config.yml] -i path [--state path] [--once] [--interval 5m]
             [--out path] [--trace path] [--http-timeout dur] [--max-pages n]
             [--sink-buffer n]
      Execute a spec document in-process via the client backend. Emits
      events as JSONL to --out (default stdout). --once runs one drain;
      --interval polls continuously until SIGINT. --trace appends one
      redacted Exchange record per HTTP request/response pair.
      -c supplies defaults from a YAML config; explicit flags override it.

  skopos init [-o path] [--force]
      Write a commented default run config to -o path (default stdout).
      Use --force to overwrite an existing file. Pipe into a file and
      then edit it, or pass it to "skopos run -c".

  skopos template list
      List the names of all bundled spec templates.

  skopos template show [-o path] <name>
      Print a bundled spec template to stdout (or write to -o path).
      The name may be given with or without the .yml extension.
          skopos template show -o spec.yml bearer_simple

  skopos version
      Print build metadata (version, commit, build date, Go toolchain,
      OS/arch). Also accessible via --version / -V. Paste the output
      into issue reports so the maintainer can reproduce against the
      same revision.

Flags (validate):
  -i path     Input file (YAML or JSON). Omit or use "-" for stdin.

Flags (run):
  -c path           Config file (YAML). Optional. Flags override config values.
  -i path           Input spec file. Required (or set via config.input).
  --state path      State file (JSON). Optional; in-memory when omitted.
  --once            Run one drain and exit (default when --interval is unset).
  --interval dur    Sleep this duration between drains and repeat.
  --out path        JSONL event output (defaults to stdout).
  --trace path      Per-exchange JSONL trace (no default; required path).
  --http-timeout dur  Per-request HTTP timeout (default 30s).
  --max-pages n     Pagination cap per drain (default 10000).
  --sink-buffer n   Buffer events through a consumer goroutine (default 0, off).

Flags (init):
  -o path     Output file for the config. Defaults to stdout ("-").
  --force     Overwrite the output file if it already exists.`)
}
