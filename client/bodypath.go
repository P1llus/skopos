// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"strconv"
)

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
