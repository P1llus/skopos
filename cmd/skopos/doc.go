// SPDX-License-Identifier: Apache-2.0

// Command skopos is the CLI for the skopos spec runner.
//
// Usage:
//
//	skopos validate [-i input]
//	skopos run [-c config.yaml] -i spec.yaml [--state state.json] [--once] [--interval 5m] [--out events.jsonl] [--trace trace.jsonl] [--http-timeout dur] [--max-pages n]
//	skopos init [-o path] [--force]
//	skopos template list
//	skopos template show <name>
//
// Subcommands:
//
//	validate  Validate a spec document (YAML or JSON). Reads stdin when -i is
//	          omitted; writes diagnostics to stdout. Exits 1 when validation
//	          errors are present.
//
//	run       Execute a spec document in-process via the client backend.
//	          One-shot by default; --interval enables continuous polling.
//	          -c supplies run settings from a YAML config file; explicit
//	          flags take precedence over config values.
//
//	init      Write a fully-commented default run config to a file or stdout.
//	          Edit the result, then pass it to "skopos run -c <path>".
//
//	template  Browse and print bundled spec templates. Use "template list"
//	          to see available names and "template show <name>" to print one.
package main
