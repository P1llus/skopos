// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"

	"github.com/p1llus/skopos/schema"
)

// Pagination drives the per-drain page loop. The active variant is chosen
// by a discriminated union on the document's pagination: block — four
// named variants plus the primitive custom: form:
//
//	none           Exactly one page per drain. No advance writes.
//	cursor_token   Read a next-cursor token from a body or header field
//	               and write it to state.<name>. Default termination:
//	               the source field resolves to absent / empty.
//	next_url       Read a fully-formed next-page URL from a body or
//	               header field, optionally apply a regex / capture
//	               group, and write to state.<name>. Default
//	               termination: the (post-regex) value resolves to
//	               absent / empty.
//	counter        Maintain a client-incremented counter in state.<name>:
//	               (current or start) + step per accepted page. Default
//	               termination: events.count < step (short-page detection).
//	custom         A list of {to, from, regex?, coerce?} writes plus an
//	               author-supplied terminate_when predicate (required).
//	               No default termination.
//
// # Per-page execution order
//
// One pagination iteration is one page. The runner binds scope.events,
// scope.body, and scope.responseHeaders against the producer step's
// response before calling advance. Each advance call follows the fixed
// order documented in docs/runtime.md §3:
//
//  1. Evaluate terminate_when (author-supplied or variant default)
//     against response.* and the pre-advance state.*. If true, return
//     terminate=true — the runner stops the loop, no advance writes fire
//     for this page.
//  2. Otherwise, apply the variant's writes to state.<name>. The runner
//     issues the next request and the cycle repeats.
//
// # Per-drain scratch state
//
// Every named variant's to: destination — and every custom.advance[].to —
// is a per-drain state.<name> field. The runner wipes those fields at
// drain start via (*scope).resetPerDrainScratch; pagination itself does
// not seed. A drain that fails mid-page therefore re-bootstraps on the
// next start.
//
// # MaxPages cap
//
// The runner enforces the document-level MaxPages ceiling. A pagination
// plan that loops indefinitely (buggy server, mis-spelled termination
// path) surfaces as an "iteration cap exceeded" diagnostic from the
// runner; the plans here keep their error wraps prefixed by their
// variant name so the eventual diagnostic identifies which plan was
// active.

// paginationPlan describes the active pagination variant for one drain.
// advance is called once per page-response, after the runner has bound
// the producer step's events, body, and headers into the scope. It
// returns terminate=true when the loop should exit before the next page
// request, or terminate=false after writing the variant's advance
// writes to scope.state.
type paginationPlan interface {
	advance(s *scope) (terminate bool, err error)
}

// makePaginationPlan returns the driver for the document's active
// pagination variant. The validator guarantees exactly one variant key
// is set; the defensive default arm here surfaces a clear error if a
// future Pagination field is added without a matching plan.
func makePaginationPlan(doc *schema.Doc) (paginationPlan, error) {
	switch {
	case doc.Pagination.None != nil:
		return &nonePagination{}, nil
	case doc.Pagination.CursorToken != nil:
		return &cursorTokenPagination{cfg: doc.Pagination.CursorToken}, nil
	case doc.Pagination.NextURL != nil:
		return &nextURLPagination{cfg: doc.Pagination.NextURL}, nil
	case doc.Pagination.Counter != nil:
		return &counterPagination{cfg: doc.Pagination.Counter}, nil
	case doc.Pagination.Custom != nil:
		return &customPagination{cfg: doc.Pagination.Custom}, nil
	}
	return nil, fmt.Errorf("pagination: no variant set")
}

// ---- none ----

// nonePagination yields exactly one page per drain. advance always
// returns terminate=true so the runner stops after the first response.
// No writes fire.
type nonePagination struct{}

func (p *nonePagination) advance(_ *scope) (bool, error) {
	return true, nil
}

// ---- cursor_token ----

// cursorTokenPagination reads a next-cursor token from response.body,
// response.header, or steps.<id>.{body|header} and writes it to
// state.<name>. The default termination predicate fires when the source
// path resolves to absent / empty; authors override with terminate_when
// when the server signals end-of-stream by other means.
//
// On every accepted page the loop reads state.<name> back via a
// {ref: state.<name>, default: ...} Value at the request's slot of
// choice — the first iteration falls through to the ref's default
// because the per-drain wipe leaves state.<name> unset at drain start.
type cursorTokenPagination struct {
	cfg *schema.CursorTokenPagination
}

func (p *cursorTokenPagination) advance(s *scope) (bool, error) {
	got, ok, err := s.resolveNamespaceRef(p.cfg.From)
	if err != nil {
		return false, fmt.Errorf("pagination.cursor_token.from: %w", err)
	}

	done, err := p.terminate(s, got, ok)
	if err != nil {
		return false, err
	}
	if done {
		return true, nil
	}

	name, err := paginationStateField(p.cfg.To)
	if err != nil {
		return false, fmt.Errorf("pagination.cursor_token.to: %w", err)
	}
	s.state[name] = got
	return false, nil
}

func (p *cursorTokenPagination) terminate(s *scope, from any, fromOK bool) (bool, error) {
	if p.cfg.TerminateWhen != nil {
		done, err := s.evalPredicate(*p.cfg.TerminateWhen)
		if err != nil {
			return false, fmt.Errorf("pagination.cursor_token.terminate_when: %w", err)
		}
		return done, nil
	}
	return !fromOK || from == nil || isZeroRuntime(from), nil
}

// ---- next_url ----

// nextURLPagination reads a fully-formed next-page URL from a body or
// header field. When regex: is set, the resolved string is reduced to
// the named capture group before writing — the canonical use is the
// `<url>; rel="next"` Link-header shape, where capture: 1 picks the URL.
//
// The default termination predicate fires when the post-regex value
// resolves to absent / empty: an unmatched regex therefore terminates
// the loop the same way a missing field does. Authors who want to
// distinguish "header missing" from "regex didn't match" override with
// terminate_when.
type nextURLPagination struct {
	cfg *schema.NextURLPagination
}

func (p *nextURLPagination) advance(s *scope) (bool, error) {
	got, ok, err := s.resolveNamespaceRef(p.cfg.From)
	if err != nil {
		return false, fmt.Errorf("pagination.next_url.from: %w", err)
	}

	if p.cfg.Regex != "" && got != nil {
		matched, err := s.applyRegex(p.cfg.Regex, toString(got), p.cfg.Capture, nil)
		if err != nil {
			return false, fmt.Errorf("pagination.next_url.regex: %w", err)
		}
		got = matched
		ok = got != nil
	}

	done, err := p.terminate(s, got, ok)
	if err != nil {
		return false, err
	}
	if done {
		return true, nil
	}

	name, err := paginationStateField(p.cfg.To)
	if err != nil {
		return false, fmt.Errorf("pagination.next_url.to: %w", err)
	}
	s.state[name] = got
	return false, nil
}

func (p *nextURLPagination) terminate(s *scope, from any, fromOK bool) (bool, error) {
	if p.cfg.TerminateWhen != nil {
		done, err := s.evalPredicate(*p.cfg.TerminateWhen)
		if err != nil {
			return false, fmt.Errorf("pagination.next_url.terminate_when: %w", err)
		}
		return done, nil
	}
	return !fromOK || from == nil || isZeroRuntime(from), nil
}

// ---- counter ----

// counterPagination maintains a client-incremented counter at
// state.<name>. Each accepted page writes (current or start) + step;
// the first iteration treats an unset state field as start, so authors
// can choose between declaring state.<name>.default: <start> (wipe
// seeds the field) or reading it back with {ref: state.<name>,
// default: <start>} at the request slot. Both shapes converge on the
// same iteration sequence.
//
// The default termination predicate is short-page detection:
// events.count < step. The events.count arm in resolveNamespaceRef
// returns int64(0) when no page is bound yet, so the predicate is
// safe to evaluate before the first response if a future caller
// rewires the order.
type counterPagination struct {
	cfg *schema.CounterPagination
}

func (p *counterPagination) advance(s *scope) (bool, error) {
	step, err := p.resolveInt(s, p.cfg.Step, "step", 1)
	if err != nil {
		return false, err
	}

	done, err := p.terminate(s, step)
	if err != nil {
		return false, err
	}
	if done {
		return true, nil
	}

	start, err := p.resolveInt(s, p.cfg.Start, "start", 1)
	if err != nil {
		return false, err
	}

	name, err := paginationStateField(p.cfg.To)
	if err != nil {
		return false, fmt.Errorf("pagination.counter.to: %w", err)
	}

	cur, ok := s.state[name]
	var n int64
	if !ok || cur == nil {
		n = start
	} else {
		coerced, err := toInt(cur)
		if err != nil {
			return false, fmt.Errorf("pagination.counter: state.%s: %w", name, err)
		}
		n = coerced
	}
	s.state[name] = n + step
	return false, nil
}

func (p *counterPagination) terminate(s *scope, step int64) (bool, error) {
	if p.cfg.TerminateWhen != nil {
		done, err := s.evalPredicate(*p.cfg.TerminateWhen)
		if err != nil {
			return false, fmt.Errorf("pagination.counter.terminate_when: %w", err)
		}
		return done, nil
	}
	got, _, err := s.resolveNamespaceRef(schema.Path{Parts: []string{"events", "count"}})
	if err != nil {
		return false, fmt.Errorf("pagination.counter.terminate_when: events.count: %w", err)
	}
	count, _ := asInt64(got)
	return count < step, nil
}

// resolveInt evaluates an optional *Value field into an int64, falling
// back to def when the field is unset. field names the slot for error
// wrapping (start / step).
func (p *counterPagination) resolveInt(s *scope, v *schema.Value, field string, def int64) (int64, error) {
	if v == nil {
		return def, nil
	}
	got, err := s.evalValue(*v)
	if err != nil {
		return 0, fmt.Errorf("pagination.counter.%s: %w", field, err)
	}
	n, err := toInt(got)
	if err != nil {
		return 0, fmt.Errorf("pagination.counter.%s: %w", field, err)
	}
	return n, nil
}

// ---- custom ----

// customPagination is the author-controlled primitive form: a list of
// per-page {to, from, regex?, coerce?} writes plus an author-supplied
// terminate_when predicate (required, no default).
//
// All from: expressions are resolved against the same pre-write
// snapshot of state.* before any to: destination is written. This
// matches the snapshot-then-write semantics of applyProgress and lets
// authors write two entries that reference each other without seeing a
// half-applied state mid-page.
type customPagination struct {
	cfg *schema.CustomPagination
}

func (p *customPagination) advance(s *scope) (bool, error) {
	done, err := s.evalPredicate(p.cfg.TerminateWhen)
	if err != nil {
		return false, fmt.Errorf("pagination.custom.terminate_when: %w", err)
	}
	if done {
		return true, nil
	}

	staged := make([]any, len(p.cfg.Advance))
	for i, w := range p.cfg.Advance {
		got, err := s.evalValue(w.From)
		if err != nil {
			return false, fmt.Errorf("pagination.custom.advance[%d].from: %w", i, err)
		}
		if w.Regex != "" && got != nil {
			matched, err := s.applyRegex(w.Regex, toString(got), 0, nil)
			if err != nil {
				return false, fmt.Errorf("pagination.custom.advance[%d].regex: %w", i, err)
			}
			got = matched
		}
		if w.Coerce != "" && got != nil {
			coerced, err := applyFormat(w.Coerce, got)
			if err != nil {
				return false, fmt.Errorf("pagination.custom.advance[%d].coerce %s: %w", i, w.Coerce, err)
			}
			got = coerced
		}
		staged[i] = got
	}

	for i, w := range p.cfg.Advance {
		name, err := paginationStateField(w.To)
		if err != nil {
			return false, fmt.Errorf("pagination.custom.advance[%d].to: %w", i, err)
		}
		s.state[name] = staged[i]
	}
	return false, nil
}

// paginationStateField unpacks a pagination To path into its
// state.<name> field name. The validator guarantees the shape; the
// defensive check here keeps a future contract violation from silently
// writing into the wrong slot.
func paginationStateField(p schema.Path) (string, error) {
	if len(p.Parts) != 2 || p.Parts[0] != "state" {
		return "", fmt.Errorf("to must be state.<name>, got %q", p.String())
	}
	return p.Parts[1], nil
}
