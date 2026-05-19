// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/p1llus/skopos/client"
	"github.com/p1llus/skopos/schema"
)

// runRun is the entry point for "skopos run".
//
//	skopos run [-c config.yml] -i spec.yml [--state path] [--once] [--interval 5m]
//	           [--out path] [--trace path] [--http-timeout dur] [--max-pages n]
//
// Loads + validates the spec document, builds a client.Runner, and drives
// it: --once for a single drain, --interval for continuous polling.
//
// The effective configuration is built with increasing precedence:
//
//  1. built-in defaults (zero values where runner has its own defaults)
//  2. values from -c/--config file
//  3. explicit CLI flags (detected via fs.Visit so unset flags do not clobber
//     config-file values)
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("c", "", "Config file path (YAML). Optional; flags override config values.")
	fs.String("config", "", "Alias for -c.") // alias, consumed below
	input := fs.String("i", "", "Input spec file (YAML or JSON). Overrides config.input.")
	statePath := fs.String("state", "", "State file path (JSON). Overrides config.state.")
	once := fs.Bool("once", false, "Run a single drain and exit. Overrides config.once.")
	interval := fs.Duration("interval", 0, "Sleep this duration between drains and repeat. Overrides config.interval.")
	outPath := fs.String("out", "", "Event output file (JSONL). Overrides config.out.")
	tracePath := fs.String("trace", "", "Per-exchange trace output file (JSONL). Overrides config.trace.")
	httpTimeout := fs.Duration("http-timeout", 0, "Per-request HTTP timeout. Overrides config.http_timeout.")
	maxPages := fs.Int("max-pages", 0, "Max pagination iterations per drain. Overrides config.max_pages.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve -c / --config alias.
	if *configPath == "" {
		if v := fs.Lookup("config"); v != nil {
			*configPath = v.Value.String()
		}
	}

	// Track which flags were explicitly set by the user.
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	// Start from built-in defaults.
	cfg := DefaultConfig()

	// Layer in the config file if provided.
	if *configPath != "" {
		fileCfg, err := loadConfig(*configPath)
		if err != nil {
			return err
		}
		cfg = fileCfg
	}

	// Explicit flags override the config.
	if explicit["i"] {
		cfg.Input = *input
	}
	if explicit["state"] {
		cfg.State = *statePath
	}
	if explicit["once"] {
		cfg.Once = *once
	}
	if explicit["interval"] {
		cfg.Interval = *interval
	}
	if explicit["out"] {
		cfg.Out = *outPath
	}
	if explicit["trace"] {
		cfg.Trace = *tracePath
	}
	if explicit["http-timeout"] {
		cfg.HTTPTimeout = *httpTimeout
	}
	if explicit["max-pages"] {
		cfg.MaxPages = *maxPages
	}

	// Fleet mode: a config with `runs:` and no explicit -i fans out one
	// Runner per entry. Explicit -i forces single-run mode, so the two
	// modes never compete for the same invocation.
	if len(cfg.Runs) > 0 && !explicit["i"] {
		return runFleet(cfg)
	}

	// Validate the resolved config.
	if cfg.Input == "" {
		return errors.New("skopos run: -i (or config.input) is required")
	}
	if cfg.Once && cfg.Interval != 0 {
		return errors.New("skopos run: --once is incompatible with --interval; pick one")
	}

	doc, err := loadDoc(cfg.Input)
	if err != nil {
		return err
	}
	if diags := schema.Validate(doc); hasErrorSeverity(diags) {
		for _, d := range diags {
			fmt.Fprintln(os.Stderr, formatDiag(d, cfg.Input))
		}
		return errors.New("skopos run: spec validation failed")
	}

	out, closeOut, err := openOutput(cfg.Out)
	if err != nil {
		return err
	}
	defer closeOut()

	var tracer client.Tracer
	if cfg.Trace != "" {
		tf, closeTrace, err := openTrace(cfg.Trace)
		if err != nil {
			return err
		}
		jt := client.NewJSONLTracer(tf)
		defer func() {
			_ = jt.Flush()
			closeTrace()
		}()
		tracer = jt
	}

	var store client.Store
	if cfg.State != "" {
		if err := preflightStatePath(cfg.State); err != nil {
			return err
		}
		store = client.NewFileStore(cfg.State)
	}

	logger := log.New(os.Stderr, "skopos: ", log.LstdFlags|log.Lmsgprefix)

	runner := &client.Runner{
		Doc:    doc,
		Store:  store,
		Sink:   client.NewJSONLSink(out),
		Logger: logger,
		Tracer: tracer,
	}

	// Apply optional overrides that have non-zero values. When zero the
	// runner falls back to its own internal defaults (30s timeout, 10k pages).
	if cfg.HTTPTimeout > 0 {
		runner.Client = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	if cfg.MaxPages != 0 {
		runner.MaxPages = cfg.MaxPages
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --once OR (--interval unset → implicit one-shot)
	if cfg.Once || cfg.Interval == 0 {
		if err := runner.Drain(ctx); err != nil {
			// Mirror the continuous-mode unwind: SIGINT mid-drain is a
			// graceful end, not an error exit.
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		return nil
	}

	for {
		if err := runner.Drain(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Continuous-mode policy is "always retry": no fail-fast, no
			// backoff, no error budget. Operators who want fail-fast use
			// --once and let the orchestrator decide. Wrap with
			// client.RedactURLError at the log site so a future Drain path
			// that returns a *url.Error still scrubs before logging.
			logger.Printf("drain error: %v (retrying after %s)", client.RedactURLError(err), cfg.Interval)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.Interval):
		}
	}
}

func loadDoc(path string) (*schema.Doc, error) {
	data, err := readBytes(path)
	if err != nil {
		return nil, err
	}
	doc, err := schema.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, nil
}

// preflightStatePath verifies the --state path is writable before the runner
// starts, so a typo or permission error surfaces at flag-parse time rather
// than on the first deferred Save (where the failure is logged but the drain
// has already emitted events).
func preflightStatePath(path string) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("skopos run: --state parent %s: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".skopos-state-probe-*")
	if err != nil {
		return fmt.Errorf("skopos run: --state %s is not writable: %w", path, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

func openOutput(path string) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

// openTrace opens the per-exchange trace destination. Unlike --out, the
// trace flag has no stdout sentinel: stdout is typically already claimed
// by --out, and mixing redacted Exchange JSON into the event stream would
// silently corrupt downstream JSONL consumers. The file is opened
// O_APPEND so resuming a long-running drain after a crash continues the
// trace rather than truncating prior records.
func openTrace(path string) (io.Writer, func(), error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open trace %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}
