// SPDX-License-Identifier: Apache-2.0

package client

import "sync"

// emitter delivers drained events to a Sink. Two implementations back the
// Runner: a synchronous emitter that calls Sink.Emit inline on the Drain
// goroutine (the default), and a buffered emitter that hands events to a
// single consumer goroutine over a bounded channel so a slow Sink does not
// stall the next page fetch.
//
// Both honour the Sink contract: Emit is invoked serially, in the order the
// runner produced the events. The buffered emitter changes only which
// goroutine makes the call, never the ordering.
type emitter interface {
	// emit delivers one event. The synchronous emitter calls Sink.Emit
	// directly; the buffered emitter enqueues the event, blocking once the
	// queue is full (the backpressure point). It returns the first Sink.Emit
	// error seen so far so the Drain loop can abort promptly instead of
	// pushing more work at a failing Sink.
	emit(ev any) error
	// barrier blocks until every event handed to emit so far has reached
	// Sink.Emit, then returns the first Sink.Emit error. After it returns the
	// consumer is idle, so the caller may call Sink.Flush / Store.Save without
	// racing the consumer. The synchronous emitter has nothing to wait for.
	barrier() error
	// close drains every queued event, stops the consumer, and returns the
	// first Sink.Emit error. After close returns the consumer goroutine has
	// exited.
	close() error
}

// syncEmitter calls Sink.Emit inline on the calling goroutine. It is the
// default — zero extra goroutines, identical behaviour to delivering events
// straight from the pagination loop.
type syncEmitter struct{ sink Sink }

func (e syncEmitter) emit(ev any) error { return e.sink.Emit(ev) }
func (e syncEmitter) barrier() error    { return nil }
func (e syncEmitter) close() error      { return nil }

// sinkMsg is one item on the buffered emitter's channel. A non-nil done marks
// a barrier marker rather than an event: the consumer closes it once it has
// caught up.
type sinkMsg struct {
	ev   any
	done chan struct{}
}

// asyncEmitter feeds a single consumer goroutine over a bounded channel. The
// bound is the source of backpressure: once the Sink falls far enough behind
// that the channel fills, emit blocks, recoupling the producer to the Sink —
// the only safe response to a Sink that cannot keep up without growing memory
// without bound.
type asyncEmitter struct {
	sink Sink
	ch   chan sinkMsg
	done chan struct{}

	mu  sync.Mutex
	err error
}

// newAsyncEmitter starts the consumer goroutine and returns the emitter.
// buffer is the channel depth and must be > 0.
func newAsyncEmitter(sink Sink, buffer int) *asyncEmitter {
	e := &asyncEmitter{
		sink: sink,
		ch:   make(chan sinkMsg, buffer),
		done: make(chan struct{}),
	}
	go e.consume()
	return e
}

// consume is the single consumer goroutine. It calls Sink.Emit for every
// event in channel order and records the first error. After an error it keeps
// draining the channel (without emitting) so the producer never blocks on a
// full channel; the recorded error surfaces through emit / barrier / close.
func (e *asyncEmitter) consume() {
	defer close(e.done)
	for msg := range e.ch {
		if msg.done != nil {
			close(msg.done)
			continue
		}
		if e.firstErr() != nil {
			continue
		}
		if err := e.sink.Emit(msg.ev); err != nil {
			e.setErr(err)
		}
	}
}

func (e *asyncEmitter) firstErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *asyncEmitter) setErr(err error) {
	e.mu.Lock()
	if e.err == nil {
		e.err = err
	}
	e.mu.Unlock()
}

func (e *asyncEmitter) emit(ev any) error {
	if err := e.firstErr(); err != nil {
		return err
	}
	e.ch <- sinkMsg{ev: ev}
	return nil
}

func (e *asyncEmitter) barrier() error {
	done := make(chan struct{})
	e.ch <- sinkMsg{done: done}
	<-done
	return e.firstErr()
}

func (e *asyncEmitter) close() error {
	close(e.ch)
	<-e.done
	return e.firstErr()
}
