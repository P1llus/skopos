// SPDX-License-Identifier: Apache-2.0

// Package bench holds Go benchmarks for the in-process Runner. They boot
// one shared httptest.Server backed by every scenario in
// internal/testserver, load each bundled template into a *schema.Doc, and
// drive Runner.Drain in tight loops to surface per-scenario ns/op +
// allocs/op and per-fleet sequential-vs-concurrent throughput.
//
// Usage:
//
//	go test -bench=. -benchmem ./bench
//	go test -bench=BenchmarkScenarios/fanout -benchmem -memprofile=mem.out ./bench
//	go test -bench=BenchmarkFleet -benchmem -cpuprofile=cpu.out ./bench
//
// The benchmarks intentionally write events to a no-op sink so the
// numbers reflect Runner + HTTP + JSON cost only, not disk I/O. For the
// I/O-coupled story, see the `make bench` CLI wrapper which drives
// bench/fleet.yml against the same scenarios with a real JSONLSink.
package bench

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/p1llus/skopos/client"
	"github.com/p1llus/skopos/internal/testserver"
	"github.com/p1llus/skopos/schema"
)

// benchScenarios is the list of (name, template path) pairs the
// benchmarks iterate over. Paths are repo-relative; `go test` from this
// directory makes them resolvable as "../templates/...".
//
// The list duplicates bench/fleet.yml on purpose: the benchmark has no
// reason to parse YAML at startup and the two artifacts answer
// different questions (Go bench vs CLI soak test). Keep them in sync
// when adding a template.
var benchScenarios = []struct {
	name string
	path string
}{
	{"api_key_auth", "../templates/api_key_auth.yml"},
	{"async_poll", "../templates/async_poll.yml"},
	{"async_poll_latest_ts", "../templates/async_poll_latest_ts.yml"},
	{"async_poll_stateless", "../templates/async_poll_stateless.yml"},
	{"basic_auth", "../templates/basic_auth.yml"},
	{"bearer_simple", "../templates/bearer_simple.yml"},
	{"cursor_token", "../templates/cursor_token.yml"},
	{"custom_auth", "../templates/custom_auth.yml"},
	{"etag_conditional", "../templates/etag_conditional.yml"},
	{"etag_conditional_middle", "../templates/etag_conditional_middle.yml"},
	{"fanout", "../templates/fanout.yml"},
	{"link_header", "../templates/link_header.yml"},
	{"multi_mode_auth", "../templates/multi_mode_auth.yml"},
	{"ndjson_response", "../templates/ndjson_response.yml"},
	{"next_url_in_body", "../templates/next_url_in_body.yml"},
	{"oauth2_client_credentials", "../templates/oauth2_client_credentials.yml"},
	{"oauth2_password_grant", "../templates/oauth2_password_grant.yml"},
	{"oauth2_relay", "../templates/oauth2_relay.yml"},
	{"offset_pagination", "../templates/offset_pagination.yml"},
	{"page_number", "../templates/page_number.yml"},
	{"post_form_body", "../templates/post_form_body.yml"},
	{"post_json_body", "../templates/post_json_body.yml"},
	{"post_raw_body", "../templates/post_raw_body.yml"},
	{"scroll_id", "../templates/scroll_id.yml"},
	{"session_cookie", "../templates/session_cookie.yml"},
	{"session_login_cached", "../templates/session_login_cached.yml"},
	{"simple_get_object", "../templates/simple_get_object.yml"},
}

// sharedServer is built once in TestMain and torn down after the
// benchmarks return. Every scenario is registered so a single server URL
// works for every template — matching what `make bench` does with the
// stand-alone testserver binary.
var (
	sharedServer *httptest.Server
	serverURL    string
)

// TestMain boots the shared scenarios server. It also serves as the
// guard against accidentally running the package as a normal test suite
// — there are no actual TestXxx functions, just benchmarks.
func TestMain(m *testing.M) {
	// internal/testserver logs every request via the default logger.
	// At benchmark rate (thousands of requests per scenario) that
	// floods stderr and skews timings via the log mutex. The CLI
	// soak-test wrapper still gets the request log because it uses
	// the standalone binary, not this in-process server.
	log.SetOutput(io.Discard)

	srv := testserver.New(testserver.Options{}, testserver.AllScenarios()...)
	sharedServer = httptest.NewServer(srv.Handler())
	serverURL = sharedServer.URL
	code := m.Run()
	sharedServer.Close()
	os.Exit(code)
}

// discardSink is a Sink that drops every event. Used in benchmarks so
// JSON encoding, file I/O, and the JSONLSink mutex stay out of the
// numbers — the goal is to measure the runner, not the sink.
type discardSink struct{}

// Emit drops the event.
func (discardSink) Emit(any) error { return nil }

// Flush is a no-op.
func (discardSink) Flush() error { return nil }

// loadScenarioDoc parses and validates one template, failing the
// benchmark on any error so iteration setup cost is bounded.
func loadScenarioDoc(tb testing.TB, path string) *schema.Doc {
	tb.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		tb.Fatalf("read %s: %v", path, err)
	}
	doc, err := schema.Parse(data)
	if err != nil {
		tb.Fatalf("parse %s: %v", path, err)
	}
	if diags := schema.Validate(doc); len(diags) > 0 {
		for _, d := range diags {
			if d.Severity == "error" {
				tb.Fatalf("validate %s: %v", path, d)
			}
		}
	}
	return doc
}

// silentLogger returns a *log.Logger that drops everything. Drains
// otherwise emit operational diagnostics (skipped steps, auth retries,
// pagination-loop terminations on 401, etc.) onto stderr at b.N rate —
// fine in single-run mode, but it floods the benchmark output and skews
// timings.
func silentLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// newScenarioRunner returns a Runner ready to drain `url`, with a fresh
// in-memory store seeded with state.url. The runner shares the package
// http.DefaultClient via Runner's default fallback — explicit clients
// would just add allocations the bench isn't trying to measure.
func newScenarioRunner(doc *schema.Doc, url string) *client.Runner {
	store := &client.MemoryStore{}
	_ = store.Save(client.Snapshot{State: map[string]any{"url": url}})
	return &client.Runner{
		Doc:    doc,
		Store:  store,
		Sink:   discardSink{},
		Logger: silentLogger(),
	}
}

// resetRunnerStore reseats the runner's store back to a fresh snapshot
// with only state.url. The runner's deferred Save mutates the snapshot
// (cursor tokens, last_timestamp, etc.); without a reset, iteration N+1
// would start from the cursor N left behind and the per-op cost would
// drift as pagination terminated earlier on later iters.
func resetRunnerStore(r *client.Runner, url string) {
	store := &client.MemoryStore{}
	_ = store.Save(client.Snapshot{State: map[string]any{"url": url}})
	r.Store = store
}

// BenchmarkScenarios runs one sub-benchmark per template. Each iteration
// is one full Drain (which may itself span N pages of pagination).
// ns/op is per-Drain, NOT per-event — paginated scenarios are
// intentionally heavier than single-request ones, and the comparison
// across rows is the point.
func BenchmarkScenarios(b *testing.B) {
	ctx := context.Background()
	for _, sc := range benchScenarios {
		b.Run(sc.name, func(b *testing.B) {
			doc := loadScenarioDoc(b, sc.path)
			runner := newScenarioRunner(doc, serverURL)
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				resetRunnerStore(runner, serverURL)
				b.StartTimer()
				if err := runner.Drain(ctx); err != nil {
					b.Fatalf("drain: %v", err)
				}
			}
		})
	}
}

// BenchmarkFleetSequential drains every scenario once per iteration, in
// declaration order. ns/op is per-full-sweep, so dividing by 27 gives an
// approximate per-scenario number that includes any serial overhead.
// Compare against BenchmarkFleetConcurrent to see what concurrency
// actually buys.
func BenchmarkFleetSequential(b *testing.B) {
	ctx := context.Background()
	runners := make([]*client.Runner, len(benchScenarios))
	for i, sc := range benchScenarios {
		doc := loadScenarioDoc(b, sc.path)
		runners[i] = newScenarioRunner(doc, serverURL)
	}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		for _, r := range runners {
			resetRunnerStore(r, serverURL)
		}
		b.StartTimer()
		for _, r := range runners {
			if err := r.Drain(ctx); err != nil {
				b.Fatalf("drain: %v", err)
			}
		}
	}
}

// BenchmarkFleetConcurrent drains every scenario in parallel — one
// goroutine per scenario per iteration. The per-op number is the
// wall-clock for one full sweep, so the comparison vs Sequential
// reveals scheduler contention, lock cost in the shared sink (we use
// discard here, so this is mostly runner / HTTP client / GC), and any
// false sharing in the runner code path.
func BenchmarkFleetConcurrent(b *testing.B) {
	ctx := context.Background()
	runners := make([]*client.Runner, len(benchScenarios))
	for i, sc := range benchScenarios {
		doc := loadScenarioDoc(b, sc.path)
		runners[i] = newScenarioRunner(doc, serverURL)
	}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		for _, r := range runners {
			resetRunnerStore(r, serverURL)
		}
		b.StartTimer()
		var wg sync.WaitGroup
		for _, r := range runners {
			wg.Go(func() {
				if err := r.Drain(ctx); err != nil {
					b.Errorf("drain: %v", err)
				}
			})
		}
		wg.Wait()
	}
}
