// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"

	"github.com/p1llus/skopos/schema"
)

// Progress evaluates the document's progress write list against the
// current scope and writes each entry's resolved value to its
// state.<name> destination.
//
// applyProgress fires once per accepted page-response, including empty
// pages — the runner places the call site between events-to-sink
// delivery and the pagination advance. An empty (or nil) writes list
// short-circuits to a no-op.
//
// # Snapshot-then-write semantics
//
// All from: expressions resolve against the same pre-write snapshot of
// state.*. The function stages every from: result into a slice first
// and only then commits the to: destinations, so cross-entry
// references always read the OLD value regardless of declaration
// order. The canonical sliding-window pattern
//
//	progress:
//	  - to: state.window_start
//	    from: {ref: state.window_end, default: ...}
//	  - to: state.window_end
//	    from: {now: true}
//
// is correct without authors having to reason about evaluation order:
// the first entry's {ref: state.window_end} sees the prior page's
// value, not the now-being-written one.
//
// # Per-entry transforms
//
// Each entry may carry regex: and / or coerce:; both run after the
// from: expression resolves but before the staged write is committed.
// regex returns the full match (capture group 0) and falls through to
// nil when the pattern does not match — staged values that landed at
// nil are written as nil. coerce: dispatches through applyFormat.
//
// # Cumulative high-water marks
//
// The runner does not merge writes. Cumulative semantics are author-
// written via the reducer Value forms — typically
// {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}.
//
// # First-run seeding
//
// First-run bootstrapping is owned by the destination field's default:
// declaration under state:. The per-drain wipe re-evaluates that
// default when the field is per-drain scratch; persistent fields read
// off the snapshot. applyProgress never inspects what was there
// before.
//
// # Error wrapping
//
// Errors carry a progress[<i>].<slot>: prefix so a failure in the
// third entry's coerce verb surfaces as
//
//	progress[2].coerce int: ...
func applyProgress(s *scope, writes schema.Progress) error {
	if len(writes) == 0 {
		return nil
	}

	staged := make([]any, len(writes))
	for i, w := range writes {
		got, err := s.evalValue(w.From)
		if err != nil {
			return fmt.Errorf("progress[%d].from: %w", i, err)
		}
		if w.Regex != "" && got != nil {
			matched, err := s.applyRegex(w.Regex, toString(got), 0, nil)
			if err != nil {
				return fmt.Errorf("progress[%d].regex: %w", i, err)
			}
			got = matched
		}
		if w.Coerce != "" && got != nil {
			coerced, err := applyFormat(w.Coerce, got)
			if err != nil {
				return fmt.Errorf("progress[%d].coerce %s: %w", i, w.Coerce, err)
			}
			got = coerced
		}
		staged[i] = got
	}

	for i, w := range writes {
		name, ok := stateFieldName(w.To)
		if !ok {
			return fmt.Errorf("progress[%d].to: must be state.<name>, got %q", i, w.To.String())
		}
		s.state[name] = staged[i]
	}
	return nil
}
