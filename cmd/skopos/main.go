package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
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
			os.Exit(1)
		}
	case "run":
		if err := runRun(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "skopos:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "skopos: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `skopos — spec runner for HTTP-pull integrations

Usage:
  skopos validate [-i path]
      Validate a spec document. Reads stdin when -i is omitted; writes
      diagnostics to stdout. Exits 1 on any error-severity diagnostic.

  skopos run -i path [--state path] [--once] [--interval 5m] [--out path] [--trace path]
      Execute a spec document in-process via the client backend. Emits
      events as JSONL to --out (default stdout). --once runs one drain;
      --interval polls continuously until SIGINT. --trace appends one
      redacted Exchange record per HTTP request/response pair.

Flags (validate):
  -i path     Input file (YAML or JSON). Omit or use "-" for stdin.

Flags (run):
  -i path           Input spec file. Required.
  --state path      State file (JSON). Optional; in-memory when omitted.
  --once            Run one drain and exit (default when --interval is unset).
  --interval dur    Sleep this duration between drains and repeat.
  --out path        JSONL event output (defaults to stdout).
  --trace path      Per-exchange JSONL trace (no default; required path).`)
}
