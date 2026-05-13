// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"

	"github.com/p1llus/skopos/schema"
)

// runExtracts applies extract[] to a step result. Each ExtractVar lands in
// either scope.extract (default target) or scope.cursor (target=cursor).
// The path is body-relative for source=body; for source=header the value
// comes from the named response header.
func (s *scope) runExtracts(res *stepResult, vars []schema.ExtractVar) error {
	for _, ev := range vars {
		var raw any
		switch src := ev.Source; src {
		case "", "body":
			parts := pathParts(ev.Path)
			got, ok, err := lookupBodyPath(res.body, parts)
			if err != nil {
				return fmt.Errorf("extract.%s: %w", ev.Name, err)
			}
			if !ok {
				raw = nil
			} else {
				raw = got
			}
		case "header":
			raw = res.headers.Get(ev.Header)
		default:
			return fmt.Errorf("extract.%s: unknown source %q", ev.Name, src)
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

// pathParts returns the Path's segments, or nil when the path is empty.
func pathParts(p schema.Path) []string {
	if p.IsEmpty() {
		return nil
	}
	return p.Parts
}
