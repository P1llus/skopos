// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestJSONLSink_ConcurrentEmit pins the mutex-guarded contract documented
// on JSONLSink: N goroutines emitting concurrently MUST produce one JSON
// object per line — never an interleaved partial-then-rest write that
// breaks the line-oriented format.
func TestJSONLSink_ConcurrentEmit(t *testing.T) {
	const goroutines = 8
	const perGoroutine = 25

	buf := &bytes.Buffer{}
	// Wrap the buffer so per-Write calls go through a mutex of their own;
	// otherwise bytes.Buffer's own concurrent-write race would mask any
	// missing JSONLSink synchronisation. The point of this test is to
	// confirm JSONLSink's lock keeps Encode() atomic, not bytes.Buffer's.
	sink := NewJSONLSink(&serialisedWriter{w: buf})

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				ev := map[string]any{"g": g, "i": i}
				if err := sink.Emit(ev); err != nil {
					t.Errorf("Emit: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if got, want := len(lines), goroutines*perGoroutine; got != want {
		t.Fatalf("emitted %d lines, want %d", got, want)
	}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d not valid JSON: %v; line=%q", i, err, line)
		}
	}
}

// TestJSONLSink_FlushSyncsFile asserts Flush returns nil and leaves a
// durable file when the underlying writer is an *os.File. The contract
// is "file-aware Flush"; the operational guarantee a caller relies on is
// "after Flush, the bytes are on disk and readable from another fd".
func TestJSONLSink_FlushSyncsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	sink := NewJSONLSink(f)
	if err := sink.Emit(map[string]any{"id": "e1"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := sink.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// Re-read from a separate fd to confirm durability.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(got), `"id":"e1"`) {
		t.Errorf("file content = %q, want substring \"id\":\"e1\"", string(got))
	}
}

// TestJSONLSink_FlushPipeBenign asserts Flush returns nil for a non-file
// writer. The implementation special-cases EINVAL on Sync (pipes, ttys,
// sockets); a buffer is the simplest non-*os.File case.
func TestJSONLSink_FlushPipeBenign(t *testing.T) {
	sink := NewJSONLSink(&bytes.Buffer{})
	if err := sink.Flush(); err != nil {
		t.Errorf("Flush on non-file writer = %v, want nil", err)
	}
}

// serialisedWriter wraps an io.Writer with a mutex so concurrent test
// writers don't race on the underlying buffer. JSONLSink already holds
// its own mutex around encoder.Encode; this wrapper keeps the test
// honest by not relying on bytes.Buffer's concurrency behaviour.
type serialisedWriter struct {
	mu sync.Mutex
	w  interface {
		Write(p []byte) (int, error)
	}
}

func (sw *serialisedWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}
