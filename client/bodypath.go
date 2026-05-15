// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"strconv"

	"github.com/p1llus/skopos/schema"
)

// pathParts returns the Path's segments, or nil when the path is empty.
// Used by per-event sub-path walks (progress.{latest_event_timestamp,
// max_event_field}.event_time.path) where the path is body-relative to
// each event object — no namespace root to strip.
func pathParts(p schema.Path) []string {
	if p.IsEmpty() {
		return nil
	}
	return p.Parts
}

// stripBodyRoot strips the body-root prefix from p and returns the
// body-relative parts to walk plus the step id (or "" when the local
// body is the target). The validator's body-rooted-Path contract feeds
// this helper; the defensive checks here keep a future contract
// violation from silently miswalking the wrong body.
//
// Accepted shapes:
//
//   - empty Path              → ([], "", nil)         caller walks the local body root
//   - response.body.<path>    → (parts[2:], "", nil)  caller walks the local body
//   - steps.<id>.body.<path>  → (parts[3:], id,  nil) caller walks the step's body
//
// Anything else (a bare body-relative dotted string, a non-body
// namespace, a header root) is a contract violation: the validator
// should have rejected it upstream, and the runtime surfaces it as a
// clear error instead of silently dropping segments.
func stripBodyRoot(p schema.Path) ([]string, string, error) {
	if p.IsEmpty() {
		return nil, "", nil
	}
	switch p.Parts[0] {
	case "response":
		if len(p.Parts) < 2 || p.Parts[1] != "body" {
			return nil, "", fmt.Errorf("body-rooted path must be response.body.<path>, got %q", p.String())
		}
		return p.Parts[2:], "", nil
	case "steps":
		if len(p.Parts) < 3 || p.Parts[2] != "body" {
			return nil, "", fmt.Errorf("body-rooted path must be steps.<id>.body.<path>, got %q", p.String())
		}
		return p.Parts[3:], p.Parts[1], nil
	}
	return nil, "", fmt.Errorf("body-rooted path must be response.body.<path> or steps.<id>.body.<path>, got %q", p.String())
}

// resolveBodyPath walks body (the local response body) or the named
// step's response body at the segments under p's body root, returning
// the value found there. Pairs stripBodyRoot with lookupBodyPath for
// every §1.2 read site that previously called
// `lookupBodyPath(body, pathParts(p))` against a bare dotted string.
//
// When p references a labelled prior step (steps.<id>.body.<path>),
// the named step's body is read from s.steps; missing step bodies
// surface as (nil, false, nil) so callers handle the absence the same
// way as a missing field.
func (s *scope) resolveBodyPath(body any, p schema.Path) (any, bool, error) {
	parts, stepID, err := stripBodyRoot(p)
	if err != nil {
		return nil, false, err
	}
	target := body
	if stepID != "" {
		v, ok := s.steps[stepID]
		if !ok {
			return nil, false, nil
		}
		target = v
	}
	return lookupBodyPath(target, parts)
}

// lookupBodyPath walks parts into body. body is a JSON-decoded value:
// map[string]any for objects, []any for arrays, scalars otherwise. Numeric
// path segments index into arrays; everything else indexes maps.
//
// Returns (nil, false, nil) when the path doesn't resolve. The IR spec
// declares "missing" and "explicit null" both as zero Values — the runtime
// surfaces them the same way (the caller can distinguish via the ok flag).
func lookupBodyPath(body any, parts []string) (any, bool, error) {
	v := body
	for _, seg := range parts {
		switch x := v.(type) {
		case map[string]any:
			next, ok := x[seg]
			if !ok {
				return nil, false, nil
			}
			v = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil {
				return nil, false, nil
			}
			if idx < 0 || idx >= len(x) {
				return nil, false, nil
			}
			v = x[idx]
		default:
			return nil, false, nil
		}
	}
	return v, true, nil
}

// locateEvents resolves the events_at Path against the producer step's
// decoded body. The zero Path means "body root": when the body is a list,
// the list IS the events stream; when it is an object, the body is wrapped
// in a single-element list (one event per drain).
//
// For NDJSON the body is already a []any of decoded lines, so events_at
// empty produces the line list directly; events_at non-empty applies the
// path to EACH line and concatenates the results.
func locateEvents(body any, eventsAt []string, ndjson bool) ([]any, error) {
	if ndjson {
		lines, ok := body.([]any)
		if !ok {
			return nil, fmt.Errorf("ndjson body must be a list of decoded lines, got %T", body)
		}
		if len(eventsAt) == 0 {
			return lines, nil
		}
		var out []any
		for i, line := range lines {
			got, ok, err := lookupBodyPath(line, eventsAt)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			arr, ok := got.([]any)
			if !ok {
				return nil, fmt.Errorf("ndjson line %d at %v: expected list, got %T", i, eventsAt, got)
			}
			out = append(out, arr...)
		}
		return out, nil
	}

	if len(eventsAt) == 0 {
		switch x := body.(type) {
		case []any:
			return x, nil
		case nil:
			return nil, nil
		default:
			return []any{body}, nil
		}
	}
	got, ok, err := lookupBodyPath(body, eventsAt)
	if err != nil {
		return nil, err
	}
	if !ok || got == nil {
		return nil, nil
	}
	arr, ok := got.([]any)
	if !ok {
		return nil, fmt.Errorf("events_at %v: expected list, got %T", eventsAt, got)
	}
	return arr, nil
}
