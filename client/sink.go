package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
)

// Sink is the event-output contract.
//
// # Concurrency
//
// A single Drain calls Emit serially (one event at a time, in the order
// locateEvents returned them). A Sink implementation backing exactly one
// Runner therefore does not need internal synchronisation. A Sink shared
// between multiple Runners (i.e. multiple goroutines concurrently calling
// Drain) MUST be safe for concurrent Emit and Flush — the runner does not
// gate that for the implementer. The bundled JSONLSink is safe under
// either model (mutex-guarded encoder).
type Sink interface {
	// Emit is called once per drained event. The runner does not buffer
	// or batch — each event is delivered as soon as it leaves
	// locateEvents.
	Emit(event any) error
	// Flush is called once at the end of a Drain so file-backed sinks
	// can sync to disk.
	Flush() error
}

// JSONLSink writes one JSON object per line to w. Concurrent Emit calls
// are serialised by the embedded mutex so output stays line-aligned.
type JSONLSink struct {
	mu sync.Mutex
	w  io.Writer
	// reuse a single encoder so we don't reallocate it per event.
	enc *json.Encoder
}

// NewJSONLSink returns a Sink that writes JSONL to w.
func NewJSONLSink(w io.Writer) *JSONLSink {
	return &JSONLSink{
		w:   w,
		enc: json.NewEncoder(w),
	}
}

// Emit writes one event as a single JSON line.
func (s *JSONLSink) Emit(event any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enc.Encode(event); err != nil {
		return fmt.Errorf("jsonl emit: %w", err)
	}
	return nil
}

// Flush drains any buffered events before returning.
//
// Dispatch:
//   - If w wraps a buffered writer (bufio.Writer or anything else satisfying
//     `interface{ Flush() error }`), call its Flush so in-memory data reaches
//     the underlying sink — otherwise up to one buffer's worth of events is
//     lost on shutdown.
//   - If the underlying writer is an *os.File backed by a regular file,
//     fsync it.
//
// Pipes / ttys / sockets return EINVAL on Sync — that's not a failure,
// so we silently ignore those.
func (s *JSONLSink) Flush() error {
	if fl, ok := s.w.(interface{ Flush() error }); ok {
		if err := fl.Flush(); err != nil {
			return err
		}
	}
	f, ok := s.w.(*os.File)
	if !ok {
		return nil
	}
	err := f.Sync()
	if err == nil {
		return nil
	}
	// EINVAL on a non-regular file (tty, pipe, socket) is benign.
	if errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}
