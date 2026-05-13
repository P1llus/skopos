// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Path is a typed identifier for a dotted-string reference into one of the
// IR's defined namespaces (state, cursor, extract, steps, item, body).
//
// Primary form (dotted string):
//
//	events_at: data.issues.nodes
//	ref: cursor.last_timestamp
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
