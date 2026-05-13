// Command skopos is the CLI for the skopos spec runner.
//
// Usage:
//
//	skopos validate [-i input]
//	skopos run -i spec.yaml [--state state.json] [--once] [--interval 5m] [--out events.jsonl] [--trace trace.jsonl]
//
// Subcommands:
//
//	validate  Validate a spec document (YAML or JSON). Reads stdin when -i is
//	          omitted; writes diagnostics to stdout. Exits 1 when validation
//	          errors are present.
//
//	run       Execute a spec document in-process via the client backend.
//	          One-shot by default; --interval enables continuous polling.
package main
