// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Path is a typed identifier for a dotted-string reference into one of the
// IR's defined namespaces. The closed set of namespace roots is:
//
//	state | cache | events | extract | steps | response
//
// Any other first segment is treated as an author-chosen fan_out.as alias
// at parse time and is accepted; the validator binds the alias to a
// concrete fan-out step at use-site time. Three names — cursor, body, item
// — are explicitly rejected at parse time as historical reserved roots
// that have been removed.
//
// The events root carries a small vocabulary of declared-order shortcuts:
//
//	events.first.<field>   first event in the active page
//	events.last.<field>    last event in the active page
//	events.<int>.<field>   positional access (zero-based)
//	events.count           cardinality of the active page
//	events.*.<field>       projection across every event
//
// These shortcut segments parse like any other identifier; the validator
// is what enforces their semantics.
//
// Primary form (dotted string):
//
//	events_at: response.body.data.issues.nodes
//	ref: events.last.timestamp
//
// Segment-escape form for field names containing dots or other special chars:
//
//	ref: {parts: ["steps", "my.weird.id", "body"]}
//
// The zero value of Path represents an empty / unset path (IsZero == true).
type Path struct {
	// Parts holds the parsed path segments. For the dotted string
	// "a.b.c" this is ["a", "b", "c"]. For an explicit {parts: [...]}
	// map it holds exactly the provided list.
	Parts []string

	// IsZero is true when the path was set to an explicit null
	// (YAML null / JSON null). Use IsEmpty to also cover the
	// never-decoded "Parts is nil" case.
	IsZero bool
}

// pathClosedRoots is the closed set of namespace-root names. The validator
// (and the client-side scope resolver) mirror this set; any change here
// must be reflected at every mirroring site.
var pathClosedRoots = map[string]struct{}{
	"state":    {},
	"cache":    {},
	"events":   {},
	"extract":  {},
	"steps":    {},
	"response": {},
}

// pathRemovedRoots names roots that were once legal but have been removed
// from the IR. Each entry's value is the migration hint surfaced at parse
// time so authors of stale templates get a precise diagnostic.
var pathRemovedRoots = map[string]string{
	"cursor": "declare each cursor field under state.<name> (lifetimes are inferred from write sites; there is no cursor namespace any more)",
	"body":   "use response.body.<path> (or steps.<id>.body.<path> for a prior step's response)",
	"item":   "reference the per-iteration value through the author-chosen fan_out.as alias",
}

// pathLegalRootsList is the canonical English list of legal roots used in
// the parse-time error message.
const pathLegalRootsList = "state, cache, events, extract, steps, response, or a fan_out.as alias"

// validatePathRoot returns nil when root is a legal first-segment name and
// an error otherwise. The closed six roots and any syntactically valid
// identifier (a potential fan_out.as alias) are accepted; the three
// removed-reserved roots are rejected with a migration hint; anything else
// is rejected as a malformed root.
func validatePathRoot(root string) error {
	if _, ok := pathClosedRoots[root]; ok {
		return nil
	}
	if hint, ok := pathRemovedRoots[root]; ok {
		return fmt.Errorf("namespace root %q is not recognised (legal roots: %s): %s",
			root, pathLegalRootsList, hint)
	}
	if !isIdentifier(root) {
		return fmt.Errorf("namespace root %q is not recognised (legal roots: %s)",
			root, pathLegalRootsList)
	}
	return nil
}

// isIdentifier reports whether s matches the bare identifier shape
// fan_out.as aliases use: a non-empty run of ASCII letters, digits, and
// underscores with a non-digit first character.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Root returns the first segment of the path (the namespace root), or ""
// when the path is empty or zero.
func (p Path) Root() string {
	if len(p.Parts) == 0 {
		return ""
	}
	return p.Parts[0]
}

// IsEmpty reports whether p is empty for any reason — either the explicit
// null form (IsZero true) or the never-decoded form (Parts nil/zero-length).
// Both forms are equivalent at every reference site; this helper exists so
// validator and codec call sites don't repeat the open-coded "||" check.
func (p Path) IsEmpty() bool {
	return p.IsZero || len(p.Parts) == 0
}

// String returns the canonical dotted representation of the path.
func (p Path) String() string {
	return strings.Join(p.Parts, ".")
}

// ParsePath parses a dotted-string path into a Path value.
// An empty string produces a zero Path.
func ParsePath(s string) (Path, error) {
	if s == "" {
		return Path{IsZero: true}, nil
	}
	parts := strings.Split(s, ".")
	for _, seg := range parts {
		if seg == "" {
			return Path{}, fmt.Errorf("schema.Path: empty segment in %q", s)
		}
	}
	if err := validatePathRoot(parts[0]); err != nil {
		return Path{}, fmt.Errorf("schema.Path %q: %w", s, err)
	}
	return Path{Parts: parts}, nil
}

// UnmarshalYAML implements yaml.Unmarshaler. Accepts two forms:
//
//	"a.b.c"                   → dotted-string primary
//	{parts: ["a", "b", "c"]} → segment-escape map form
//	null                      → zero Path
func (p *Path) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			p.IsZero = true
			return nil
		}
		parsed, err := ParsePath(node.Value)
		if err != nil {
			return fmt.Errorf("line %d: %w", node.Line, err)
		}
		*p = parsed
		return nil

	case yaml.MappingNode:
		var m struct {
			Parts []string `yaml:"parts"`
		}
		if err := node.Decode(&m); err != nil {
			return fmt.Errorf("schema.Path map at line %d: %w", node.Line, err)
		}
		if len(m.Parts) == 0 {
			return fmt.Errorf("schema.Path map at line %d: parts must be non-empty", node.Line)
		}
		for _, seg := range m.Parts {
			if seg == "" {
				return fmt.Errorf("schema.Path map at line %d: empty segment in parts", node.Line)
			}
		}
		if err := validatePathRoot(m.Parts[0]); err != nil {
			return fmt.Errorf("schema.Path map at line %d: %w", node.Line, err)
		}
		p.Parts = m.Parts
		return nil

	default:
		return fmt.Errorf("schema.Path: unsupported YAML node kind %v at line %d", node.Kind, node.Line)
	}
}

// MarshalYAML emits the canonical YAML representation.
// Zero path (IsZero or nil Parts) → null.
// Paths whose segments contain '.' are emitted in the {parts: [...]} escape
// form so they round-trip safely through the dotted parser. All other paths
// → dotted string.
func (p Path) MarshalYAML() (interface{}, error) {
	if p.IsEmpty() {
		return nil, nil
	}
	if p.needsEscape() {
		return map[string]interface{}{"parts": p.Parts}, nil
	}
	return p.String(), nil
}

// needsEscape reports whether p must be emitted in {parts: [...]} form to
// round-trip safely through ParsePath. The dotted parser splits on '.', so any
// segment containing '.' would otherwise be mis-parsed.
func (p Path) needsEscape() bool {
	for _, seg := range p.Parts {
		if strings.Contains(seg, ".") {
			return true
		}
	}
	return false
}

// UnmarshalJSON implements json.Unmarshaler with the same rules as YAML.
func (p *Path) UnmarshalJSON(data []byte) error {
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("schema.Path: %w", err)
	}
	switch v := raw.(type) {
	case nil:
		p.IsZero = true
		return nil
	case string:
		parsed, err := ParsePath(v)
		if err != nil {
			return err
		}
		*p = parsed
		return nil
	case map[string]interface{}:
		raw, ok := v["parts"]
		if !ok {
			return fmt.Errorf("schema.Path: JSON object must have a 'parts' key")
		}
		arr, ok := raw.([]interface{})
		if !ok {
			return fmt.Errorf("schema.Path: 'parts' must be an array")
		}
		if len(arr) == 0 {
			return fmt.Errorf("schema.Path: 'parts' must be non-empty")
		}
		parts := make([]string, len(arr))
		for i, el := range arr {
			s, ok := el.(string)
			if !ok || s == "" {
				return fmt.Errorf("schema.Path: 'parts' element %d must be a non-empty string", i)
			}
			parts[i] = s
		}
		if err := validatePathRoot(parts[0]); err != nil {
			return fmt.Errorf("schema.Path: %w", err)
		}
		p.Parts = parts
		return nil
	default:
		return fmt.Errorf("schema.Path: unexpected JSON type %T", raw)
	}
}

// MarshalJSON emits the dotted string form (or null for zero/empty), or the
// {parts: [...]} escape form when any segment contains '.'.
func (p Path) MarshalJSON() ([]byte, error) {
	if p.IsEmpty() {
		return []byte("null"), nil
	}
	if p.needsEscape() {
		out := struct {
			Parts []string `json:"parts"`
		}{Parts: p.Parts}
		return json.Marshal(out)
	}
	return json.Marshal(p.String())
}
