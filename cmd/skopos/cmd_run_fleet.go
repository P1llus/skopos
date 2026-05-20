// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/p1llus/skopos/client"
	"github.com/p1llus/skopos/schema"
)

// runFleet executes every entry in cfg.Runs concurrently, one Runner per
// entry, with one shared JSONLSink (or per-entry sinks when an entry sets
// its own Out). Drain errors are logged and the loop keeps going — the
// fleet matches "always retry on next interval" semantics from the
// single-run continuous mode. On SIGINT/SIGTERM it cancels all runners,
// waits for them to settle, and prints a per-run + totals summary to
// stderr.
func runFleet(cfg Config) error {
	if cfg.Once && cfg.Interval != 0 {
		return errors.New("skopos run: --once is incompatible with --interval; pick one")
	}

	entries, err := resolveFleetEntries(cfg)
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "skopos: ", log.LstdFlags|log.Lmsgprefix)

	// Open the shared sink once. Entries that set their own Out get a
	// dedicated sink; everyone else shares this one. JSONLSink is safe for
	// concurrent Emit, so sharing is the cheap path.
	sharedOut, closeSharedOut, err := openOutput(cfg.Out)
	if err != nil {
		return err
	}
	defer closeSharedOut()
	sharedSink := client.NewJSONLSink(sharedOut)

	// Pre-build every Runner + counters before starting any goroutine, so
	// any setup failure aborts cleanly without leaving half a fleet
	// running.
	closers := []func(){}
	defer func() {
		for _, c := range closers {
			c()
		}
	}()

	runs := make([]*fleetRun, 0, len(entries))
	for _, ent := range entries {
		fr, closeFns, err := buildFleetRun(ent, sharedSink, logger)
		if err != nil {
			return err
		}
		closers = append(closers, closeFns...)
		runs = append(runs, fr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	logger.Printf("fleet: starting %d runners", len(runs))

	var wg sync.WaitGroup
	for _, fr := range runs {
		wg.Add(1)
		go func(fr *fleetRun) {
			defer wg.Done()
			driveFleetRun(ctx, fr, logger)
		}(fr)
	}
	wg.Wait()

	elapsed := time.Since(started)
	printFleetSummary(os.Stderr, runs, elapsed)
	return nil
}

// fleetRun bundles the per-entry state — Runner, counters, derived
// settings — used by the driver goroutine and the summary printer.
type fleetRun struct {
	name     string
	entry    resolvedEntry
	runner   *client.Runner
	counter  *countingSink
	drains   atomic.Uint64
	errors   atomic.Uint64
}

// resolvedEntry holds an entry after defaults have been inherited from
// the top-level Config and any derivations applied (state path).
type resolvedEntry struct {
	Input       string
	State       string
	Out         string
	Trace       string
	Once        bool
	Interval    time.Duration
	HTTPTimeout time.Duration
	MaxPages    int
}

// resolveFleetEntries inherits top-level Config defaults into each entry,
// derives state paths from StateDir when needed, and rejects duplicates.
// Trace is the one field that is *not* inherited — fleet runs assume
// trace is off unless an entry explicitly opts in.
func resolveFleetEntries(cfg Config) ([]resolvedEntry, error) {
	out := make([]resolvedEntry, 0, len(cfg.Runs))
	seenState := make(map[string]int)
	seenInput := make(map[string]int)

	for i, r := range cfg.Runs {
		if r.Input == "" {
			return nil, fmt.Errorf("skopos run: runs[%d]: input is required", i)
		}
		if r.Once && r.Interval != 0 {
			return nil, fmt.Errorf("skopos run: runs[%d]: once is incompatible with interval", i)
		}

		ent := resolvedEntry{
			Input:       r.Input,
			State:       r.State,
			Out:         r.Out,
			Trace:       r.Trace, // not inherited from top level
			Once:        r.Once || cfg.Once,
			Interval:    r.Interval,
			HTTPTimeout: r.HTTPTimeout,
			MaxPages:    r.MaxPages,
		}
		if ent.Interval == 0 {
			ent.Interval = cfg.Interval
		}
		if ent.HTTPTimeout == 0 {
			ent.HTTPTimeout = cfg.HTTPTimeout
		}
		if ent.MaxPages == 0 {
			ent.MaxPages = cfg.MaxPages
		}
		if ent.State == "" {
			if cfg.StateDir == "" {
				return nil, fmt.Errorf("skopos run: runs[%d]: state is unset and top-level state_dir is empty; set one of them", i)
			}
			ent.State = filepath.Join(cfg.StateDir, deriveStateName(r.Input))
		}

		if prev, dup := seenState[ent.State]; dup {
			return nil, fmt.Errorf("skopos run: runs[%d] and runs[%d] both resolve to state %q; state files must be unique", prev, i, ent.State)
		}
		seenState[ent.State] = i

		if prev, dup := seenInput[ent.Input]; dup {
			return nil, fmt.Errorf("skopos run: runs[%d] and runs[%d] both reference input %q", prev, i, ent.Input)
		}
		seenInput[ent.Input] = i

		out = append(out, ent)
	}
	if len(out) == 0 {
		return nil, errors.New("skopos run: runs: empty list")
	}
	return out, nil
}

// deriveStateName returns the basename of inputPath with its extension
// swapped for ".json". "templates/cursor_token.yml" → "cursor_token.json".
func deriveStateName(inputPath string) string {
	base := filepath.Base(inputPath)
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base + ".json"
}

// buildFleetRun constructs the Runner and ancillary plumbing for one
// entry. It returns the run plus any close functions the caller must
// invoke at fleet shutdown (output files, trace files).
func buildFleetRun(ent resolvedEntry, sharedSink client.Sink, logger *log.Logger) (*fleetRun, []func(), error) {
	doc, err := loadDoc(ent.Input)
	if err != nil {
		return nil, nil, err
	}
	if diags := schema.Validate(doc); hasErrorSeverity(diags) {
		for _, d := range diags {
			fmt.Fprintln(os.Stderr, formatDiag(d, ent.Input))
		}
		return nil, nil, fmt.Errorf("skopos run: %s: spec validation failed", ent.Input)
	}

	var closers []func()

	// Sink: shared by default, dedicated per-entry only when Out is set.
	// sharedSink is *JSONLSink; declaring the variable as the interface
	// lets the conditional branch swap in a per-entry sink without an
	// extra conversion.
	sink := client.Sink(sharedSink)
	if ent.Out != "" {
		f, closeOut, err := openOutput(ent.Out)
		if err != nil {
			return nil, closers, err
		}
		closers = append(closers, closeOut)
		sink = client.NewJSONLSink(f)
	}
	counter := &countingSink{inner: sink}

	if err := preflightStatePath(ent.State); err != nil {
		return nil, closers, err
	}
	store := client.NewFileStore(ent.State)

	var tracer client.Tracer
	if ent.Trace != "" {
		tf, closeTrace, err := openTrace(ent.Trace)
		if err != nil {
			return nil, closers, err
		}
		jt := client.NewJSONLTracer(tf)
		closers = append(closers, func() {
			_ = jt.Flush()
			closeTrace()
		})
		tracer = jt
	}

	runner := &client.Runner{
		Doc:    doc,
		Store:  store,
		Sink:   counter,
		Logger: logger,
		Tracer: tracer,
	}
	if now, ok := pinnedClock(); ok {
		runner.Now = now
	}
	if ent.HTTPTimeout > 0 {
		runner.Client = &http.Client{Timeout: ent.HTTPTimeout}
	}
	if ent.MaxPages != 0 {
		runner.MaxPages = ent.MaxPages
	}

	return &fleetRun{
		name:    fleetRunName(ent.Input),
		entry:   ent,
		runner:  runner,
		counter: counter,
	}, closers, nil
}

// fleetRunName returns the short label used in logs and the summary table.
// The base filename without extension is unambiguous enough for the
// summary while staying short.
func fleetRunName(inputPath string) string {
	base := filepath.Base(inputPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// driveFleetRun is the goroutine body for one runner. One-shot mode runs
// a single Drain; continuous mode loops Drain + sleep until ctx is done.
// Drain errors never propagate out of the goroutine — they are logged and
// counted so the fleet keeps making forward progress on the next
// interval.
func driveFleetRun(ctx context.Context, fr *fleetRun, logger *log.Logger) {
	for {
		fr.drains.Add(1)
		if err := fr.runner.Drain(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			fr.errors.Add(1)
			logger.Printf("fleet[%s] drain error: %v", fr.name, client.RedactURLError(err))
		}
		if ctx.Err() != nil {
			return
		}
		if fr.entry.Once || fr.entry.Interval == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(fr.entry.Interval):
		}
	}
}

// countingSink wraps a Sink and counts emitted events. The counter is
// owned per-run so the fleet summary can attribute events to the
// originating runner even when sinks are shared.
type countingSink struct {
	inner  client.Sink
	events atomic.Uint64
}

// Emit increments the per-run event counter and forwards the event.
func (c *countingSink) Emit(event any) error {
	c.events.Add(1)
	return c.inner.Emit(event)
}

// Flush forwards to the wrapped sink.
func (c *countingSink) Flush() error { return c.inner.Flush() }

// printFleetSummary writes a per-run table plus a totals row to w.
// Columns: name, drains attempted, events emitted, drain errors. The
// elapsed wall time is included in the header so the reader can divide
// for throughput without an extra column.
func printFleetSummary(w *os.File, runs []*fleetRun, elapsed time.Duration) {
	const nameWidth = 30
	header := fmt.Sprintf("\nskopos fleet summary (elapsed: %s)\n", elapsed.Round(time.Millisecond))
	_, _ = w.WriteString(header)
	_, _ = fmt.Fprintf(w, "%-*s  %8s  %8s  %8s\n", nameWidth, "RUN", "DRAINS", "EVENTS", "ERRORS")

	var totDrains, totEvents, totErrors uint64
	for _, fr := range runs {
		d := fr.drains.Load()
		ev := fr.counter.events.Load()
		er := fr.errors.Load()
		totDrains += d
		totEvents += ev
		totErrors += er
		_, _ = fmt.Fprintf(w, "%-*s  %8d  %8d  %8d\n", nameWidth, fr.name, d, ev, er)
	}
	_, _ = fmt.Fprintf(w, "%-*s  %8d  %8d  %8d\n", nameWidth, "TOTAL", totDrains, totEvents, totErrors)
}
