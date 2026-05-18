// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"

	"github.com/p1llus/skopos/schema"
)

// runExtracts applies extract[] to a step result. Each ExtractVar's
// destination is its To Path: state.<name> writes to scope.state (the
// field must be declared under state:); extract.<name> writes to
// scope.extract (per-iteration, no declaration required). The namespace
// prefix is the sole persistence selector — there is no separate target
// switch.
//
// The from Path's root selects the source:
//
//   - response.body.<path>     walks res.body
//   - response.header.<name>   reads res.headers via headerLookup
//   - steps.<id>.body.<path>   walks s.steps[id]
//   - steps.<id>.header.<name> reads s.stepHeaders[id] via headerLookup
//
// Regex (if set) is applied to the resolved string of the source value
// first; Coerce (if set) then applies a format verb to the result. Both
// run only when the resolved value is non-nil — absent sources flow
// through unchanged so callers store explicit-null the same way as
// missing fields.
func (s *scope) runExtracts(res *stepResult, vars []schema.ExtractVar) error {
	for _, ev := range vars {
		dest, name, err := extractDest(ev.To)
		if err != nil {
			return fmt.Errorf("extract: %w", err)
		}

		raw, err := s.resolveExtractFrom(res, ev.From)
		if err != nil {
			return fmt.Errorf("extract %s.%s: %w", dest, name, err)
		}

		if ev.Regex != "" && raw != nil {
			matched, err := s.applyRegex(ev.Regex, toString(raw), 0, nil)
			if err != nil {
				return fmt.Errorf("extract %s.%s regex: %w", dest, name, err)
			}
			raw = matched
		}

		if ev.Coerce != "" && raw != nil {
			coerced, err := applyFormat(ev.Coerce, raw)
			if err != nil {
				return fmt.Errorf("extract %s.%s coerce %s: %w", dest, name, ev.Coerce, err)
			}
			raw = coerced
		}

		switch dest {
		case "state":
			s.state[name] = raw
		case "extract":
			s.extract[name] = raw
		}
	}
	return nil
}

// extractDest unpacks an ExtractVar.To path into its destination
// namespace ("state" or "extract") and field name. The validator
// guarantees the path is one of state.<name> or extract.<name>; the
// runtime check here is the defensive last line.
func extractDest(p schema.Path) (string, string, error) {
	if len(p.Parts) != 2 {
		return "", "", fmt.Errorf("to must be state.<name> or extract.<name>, got %q", p.String())
	}
	root := p.Parts[0]
	if root != "state" && root != "extract" {
		return "", "", fmt.Errorf("to root must be state or extract, got %q", root)
	}
	return root, p.Parts[1], nil
}

// resolveExtractFrom walks p against the extract's source. Mirrors the
// four arms allowed by the validator: response.body / response.header /
// steps.<id>.body / steps.<id>.header. Returns nil for unresolved leaves
// so callers store explicit-null values the same way as missing fields.
func (s *scope) resolveExtractFrom(res *stepResult, p schema.Path) (any, error) {
	if p.IsEmpty() {
		return nil, fmt.Errorf("from is required")
	}
	switch p.Parts[0] {
	case "response":
		if len(p.Parts) < 2 {
			return nil, fmt.Errorf("response ref requires a kind segment: response.body.<path> or response.header.<name>")
		}
		switch p.Parts[1] {
		case "body":
			got, _, err := lookupBodyPath(res.body, p.Parts[2:])
			return got, err
		case "header":
			if len(p.Parts) < 3 {
				return nil, fmt.Errorf("response.header ref requires a header name")
			}
			got, _, err := headerLookup(res.headers, p.Parts[2])
			return got, err
		default:
			return nil, fmt.Errorf("response ref must be response.body.<path> or response.header.<name>")
		}
	case "steps":
		if len(p.Parts) < 3 {
			return nil, fmt.Errorf("steps ref requires steps.<id>.body.<path> or steps.<id>.header.<name>")
		}
		stepID := p.Parts[1]
		switch p.Parts[2] {
		case "body":
			body, ok := s.steps[stepID]
			if !ok {
				return nil, nil
			}
			got, _, err := lookupBodyPath(body, p.Parts[3:])
			return got, err
		case "header":
			if len(p.Parts) < 4 {
				return nil, fmt.Errorf("steps.<id>.header ref requires a header name")
			}
			h, ok := s.stepHeaders[stepID]
			if !ok {
				return nil, nil
			}
			got, _, err := headerLookup(h, p.Parts[3])
			return got, err
		default:
			return nil, fmt.Errorf("steps ref must be steps.<id>.body.<path> or steps.<id>.header.<name>")
		}
	default:
		return nil, fmt.Errorf("extract.from %q must be namespace-rooted (response.body.<path>, response.header.<name>, or steps.<id>.{body|header}.<...>)", p.String())
	}
}
