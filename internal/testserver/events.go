// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"sync"
	"time"
)

// Event is a single synthetic event emitted by a scenario handler.
type Event struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	SeqNum    int       `json:"seq_num"`
}

// EventStore is a thread-safe, append-only event window for one scenario.
// It holds the events for the current drain; Replenish refills it at the
// start of each new drain.
type EventStore struct {
	mu     sync.Mutex
	events []Event
	nextID int
}

// Replenish resets the window and appends n fresh events, each timestamped
// 1 second apart starting from now.
func (s *EventStore) Replenish(now time.Time, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = s.events[:0]
	for i := 0; i < n; i++ {
		s.nextID++
		s.events = append(s.events, Event{
			ID:        fmt.Sprintf("evt-%06d", s.nextID),
			Timestamp: now.Add(time.Duration(i) * time.Second).UTC(),
			SeqNum:    s.nextID,
		})
	}
}

// Slice returns up to limit events starting at offset. Returns a nil slice
// when offset is beyond the window.
func (s *EventStore) Slice(offset, limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if offset >= len(s.events) {
		return nil
	}
	end := offset + limit
	if end > len(s.events) {
		end = len(s.events)
	}
	out := make([]Event, end-offset)
	copy(out, s.events[offset:end])
	return out
}

// Len returns the number of events currently in the window.
func (s *EventStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}
