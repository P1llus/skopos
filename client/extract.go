// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"

	"github.com/p1llus/skopos/schema"
)

// runExtracts applies extract[] to a step result. Each ExtractVar lands in
// either scope.extract (default target) or scope.cursor (target=cursor).
//
// The from Path's root selects the source:
//
//   - response.body.<path>     walks res.body
//   - response.header.<name>   reads res.headers via headerLookup
//   - steps.<id>.body.<path>   walks s.steps[id]
//   - steps.<id>.header.<name> reads s.stepHeaders[id] via headerLookup
//
// The codec + validator together guarantee one of these shapes; mismatches
// surface as an error rather than silently dropping the extract.
func (s *scope) runExtracts(res *stepResult, vars []schema.ExtractVar) error {
	for _, ev := range vars {
		raw, err := s.resolveExtractFrom(res, ev.From)
		if err != nil {
			return fmt.Errorf("extract.%s: %w", ev.Name, err)
		}

		if ev.Coerce != "" && raw != nil {
			coerced, err := applyFormat(ev.Coerce, raw)
			if err != nil {
				return fmt.Errorf("extract.%s coerce %s: %w", ev.Name, ev.Coerce, err)
			}
			raw = coerced
		}

		switch ev.Target {
		case "", "extract":
			s.extract[ev.Name] = raw
		case "cursor":
			s.cursor[ev.Name] = raw
		default:
			return fmt.Errorf("extract.%s: unknown target %q", ev.Name, ev.Target)
		}
	}
	return nil
}

// resolveExtractFrom walks p against the extract's source. Mirrors the four
// arms allowed by checkExtractFromPath: response.body / response.header /
// steps.<id>.body / steps.<id>.header. Returns nil for unresolved leaves so
// the caller stores explicit-null values the same way as missing fields.
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
