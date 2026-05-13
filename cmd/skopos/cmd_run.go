package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
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
//	skopos run -i spec.yaml [--state state.json] [--once] [--interval 5m] [--out events.jsonl] [--trace trace.jsonl]
//
// Loads + validates the spec document, builds a client.Runner, and drives
// it: --once for a single drain, --interval for continuous polling.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("i", "", "Input spec file (YAML or JSON). Required.")
	statePath := fs.String("state", "", "State file path (JSON). Optional; in-memory when omitted.")
	once := fs.Bool("once", false, "Run a single drain and exit (default when --interval is unset).")
	interval := fs.Duration("interval", 0, "When set, sleep this duration between drains and repeat until SIGINT.")
	outPath := fs.String("out", "", "Event output file (JSONL). Defaults to stdout.")
	tracePath := fs.String("trace", "", "Per-exchange trace output file (JSONL). When set, one redacted Exchange record is appended per HTTP request/response pair. Defaults to no trace.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("skopos run: -i is required")
	}
	if *once && *interval != 0 {
		return errors.New("skopos run: --once is incompatible with --interval; pick one")
	}

	doc, err := loadDoc(*input)
	if err != nil {
		return err
	}
	if diags := schema.Validate(doc); hasErrorSeverity(diags) {
		for _, d := range diags {
			fmt.Fprintln(os.Stderr, formatDiag(d, *input))
		}
		return errors.New("skopos run: spec validation failed")
	}

	out, closeOut, err := openOutput(*outPath)
	if err != nil {
		return err
	}
	defer closeOut()

	var tracer client.Tracer
	if *tracePath != "" {
		tf, closeTrace, err := openTrace(*tracePath)
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
	if *statePath != "" {
		if err := preflightStatePath(*statePath); err != nil {
			return err
		}
		store = client.NewFileStore(*statePath)
	}

	logger := log.New(os.Stderr, "skopos: ", log.LstdFlags|log.Lmsgprefix)
	runner := &client.Runner{
		Doc:    doc,
		Store:  store,
		Sink:   client.NewJSONLSink(out),
		Logger: logger,
		Tracer: tracer,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --once OR (--interval unset → implicit one-shot)
	if *once || *interval == 0 {
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
			logger.Printf("drain error: %v (retrying after %s)", client.RedactURLError(err), *interval)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
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
