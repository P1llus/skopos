// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Value is the universal field type for any dynamic input. It is a
// discriminated union — exactly one form is active after unmarshalling.
//
// YAML authoring rules:
//
//	/api/v1/events        → LiteralString ("/api/v1/events")
//	${state.url}/path     → desugared to Concat with interleaved Refs
//	100                   → LiteralInt(100)
//	true                  → LiteralBool(true)
//	null                  → IsZero
//	{literal_string: "x"} → LiteralString("x")  (no interpolation scan)
//	{ref: state.url}                     → Ref
//	{ref: state.url, default: "http://…"} → Ref with Default
//	{now: true}                          → Now (current moment)
//	{concat: [<Value>, ...]}             → Concat
//	{select: {branches: [...], default: <Value>}} → Select
//	{format: <verb-or-layout>, value: <Value>}    → Format
//	{base64: <Value>}                    → Base64
//	{list: [<Value>, ...]}               → List
//	{object: {<key>: <Value>, ...}}      → Object (explicit; required for any
//	                                       map-shaped Value)
//	{add: [<Value>, <Value>]}            → Add (positional 2-operand)
//	{subtract: [<Value>, <Value>]}       → Subtract (positional 2-operand)
//	{max: <list-or-projection>}          → Max (reducer)
//	{min: <list-or-projection>}          → Min (reducer)
//	{first: <list-or-projection>}        → First (reducer)
//	{last: <list-or-projection>}         → Last (reducer)
//	{count: <list-or-projection>}        → Count (reducer)
//	{regex: {pattern, from, capture?, default?}} → Regex
//
// A map-shaped Value MUST carry exactly one discriminator key. There is no
// silent fallback for arbitrary mappings; authors who want a literal map
// must wrap it in {object: {...}}. This isolates Object as the only form whose
// inner keys are NOT re-interpreted as discriminators (inner keys are literal
// strings; inner values still recurse as Values).
//
// Any YAML/JSON string scalar in a Value position is scanned for ${<path>}
// interpolation segments and desugared into a Concat. A scalar with no '$'
// is preserved as a plain LiteralString on the fast path.
type Value struct {
	// LiteralString is the scalar string form, set when the YAML/JSON
	// node is a quoted or untagged scalar with no interpolation segments.
	LiteralString *string
	// LiteralInt is the scalar integer form, set when the YAML/JSON
	// node is an integer literal.
	LiteralInt *int64
	// LiteralBool is the scalar boolean form, set when the YAML/JSON
	// node is a boolean literal.
	LiteralBool *bool

	// IsZero is true when the node was explicitly null / absent.
	IsZero bool

	// Ref is the {ref: <path>, default?: <Value>} form.
	Ref *RefValue
	// Now is the {now: true} form.
	Now *NowValue
	// Concat is the {concat: [<Value>, ...]} form: concatenate the
	// resolved string representation of each element. Also produced by
	// the string-interpolation desugaring of any scalar containing
	// ${...} segments.
	Concat []Value
	// Select is the {select: {branches: [...], default: <Value>}} form.
	Select *SelectValue
	// Format is the {format: <verb-or-layout>, value: <Value>} form:
	// apply a format verb to the inner value. The verb set is closed
	// (string, int, bool, rfc3339, rfc3339nano, unix_seconds, unix_millis,
	// duration, url_encode, parse_duration); any other verb-position
	// string is treated as a Go date layout by downstream consumers.
	Format *FormatValue
	// Base64 is the {base64: <Value>} form: base64-encode the resolved
	// inner value.
	Base64 *Value
	// List is the {list: [<Value>, ...]} form.
	List []Value

	// Object is the explicit map-literal form. Inner keys are literal
	// strings (not re-interpreted as discriminators); inner values
	// recurse as Values. Encoded as {object: {<key>: <Value>, ...}}.
	Object map[string]Value

	// Add is the {add: [<Value>, <Value>]} form: positional 2-operand
	// arithmetic. Type pairs: time + duration → time;
	// duration + duration → duration; int + int → int.
	Add *ArithExpr
	// Subtract is the {subtract: [<Value>, <Value>]} form: positional
	// 2-operand arithmetic. Type pairs: time - duration → time;
	// time - time → duration; duration - duration → duration;
	// int - int → int.
	Subtract *ArithExpr
	// Max is the {max: <list-or-projection>} reducer: largest element.
	// The operand resolves to a list (either a literal {list: [...]} or
	// a list-shaped projection such as {ref: events.*.timestamp}).
	Max *Value
	// Min is the {min: <list-or-projection>} reducer: smallest element.
	Min *Value
	// First is the {first: <list-or-projection>} reducer: first element
	// in declared order.
	First *Value
	// Last is the {last: <list-or-projection>} reducer: last element
	// in declared order.
	Last *Value
	// Count is the {count: <list-or-projection>} reducer: cardinality.
	Count *Value

	// Regex is the {regex: {pattern, from, capture?, default?}} form:
	// apply a Go regular expression to the resolved string of From,
	// returning the matched substring (or the chosen capture group when
	// Capture is set).
	Regex *RegexExpr
}

// valueDiscriminatorKeys is the closed set of map keys that select a Value
// variant. A map-shaped Value may carry at most one of these keys.
var valueDiscriminatorKeys = []string{
	"literal_string",
	"ref",
	"now",
	"concat",
	"select",
	"format",
	"base64",
	"list",
	"object",
	"add",
	"subtract",
	"max",
	"min",
	"first",
	"last",
	"count",
	"regex",
}

// valueVariantAllowedKeys names every map key that may appear alongside a
// given discriminator in a Value mapping. Any other sibling key is a parse
// error: silent acceptance lets typos (e.g. {ref: state.x, defualt: "y"})
// drop into the void.
var valueVariantAllowedKeys = map[string]map[string]struct{}{
	"literal_string": {"literal_string": {}},
	"ref":            {"ref": {}, "default": {}},
	"now":            {"now": {}},
	"concat":         {"concat": {}},
	"select":         {"select": {}},
	"format":         {"format": {}, "value": {}},
	"base64":         {"base64": {}},
	"list":           {"list": {}},
	"object":         {"object": {}},
	"add":            {"add": {}},
	"subtract":       {"subtract": {}},
	"max":            {"max": {}},
	"min":            {"min": {}},
	"first":          {"first": {}},
	"last":           {"last": {}},
	"count":          {"count": {}},
	"regex":          {"regex": {}},
}

// checkValueSiblingKeys verifies that every key in keys is permitted alongside
// the chosen discriminator. The discriminator is always allowed; any sibling
// not in the variant's allow-list is rejected.
func checkValueSiblingKeys(disc string, keys map[string]struct{}) error {
	allowed := valueVariantAllowedKeys[disc]
	for k := range keys {
		if _, ok := allowed[k]; ok {
			continue
		}
		return fmt.Errorf("unknown key %q in %s form; allowed: %s",
			k, disc, sortedAllowedKeys(allowed))
	}
	return nil
}

// sortedAllowedKeys returns the keys of m as a deterministic comma-separated
// list (used only for error messages).
func sortedAllowedKeys(m map[string]struct{}) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return strings.Join(out, ", ")
}

// RefValue is the {ref: <path>} form with an optional default.
//
// When adding new Value-typed sub-fields, also extend schema.IsSecret (and
// predicateContainsSecret where relevant) so any state-ref reachable from
// the new field still propagates the secret marker.
type RefValue struct {
	// Path is the namespace-rooted locator.
	Path Path `yaml:"ref" json:"ref"`
	// Default, when set, is the fallback Value used when the reference
	// resolves to nil at evaluation time.
	Default *Value `yaml:"default,omitempty" json:"default,omitempty"`
}

// NowValue is the {now: true} form. To coerce a Now value to a specific
// representation, wrap it with a Format Value:
// {format: rfc3339, value: {now: true}}. To shift the moment by a duration,
// use Arithmetic: {add: [{now: true}, "1h"]} or
// {subtract: [{now: true}, "30m"]}.
type NowValue struct{}

// SelectBranch is one branch of a {select: ...} Value.
type SelectBranch struct {
	// When is the predicate that selects this branch.
	When Predicate `yaml:"when" json:"when"`
	// Value is the Value returned when the predicate is true.
	Value Value `yaml:"value" json:"value"`
}

// SelectValue is the {select: {branches: [...], default: <Value>}} form.
type SelectValue struct {
	// Branches is the ordered list of (when, value) arms. The first arm
	// whose predicate is true wins.
	Branches []SelectBranch `yaml:"branches" json:"branches"`
	// Default is the Value returned when no branch matches.
	Default Value `yaml:"default" json:"default"`
}

// FormatValue is the {format: <verb-or-layout>, value: <Value>} form.
type FormatValue struct {
	// Verb is the format-verb name or a Go date layout string.
	Verb string `yaml:"format" json:"format"`
	// Value is the inner Value the verb is applied to.
	Value Value `yaml:"value" json:"value"`
}

// ArithExpr is the operand pair for {add: [...]} and {subtract: [...]}. The
// codec enforces exactly two operands; operand order is significant.
type ArithExpr struct {
	// Operands holds the two positional operands.
	Operands []Value
}

// RegexExpr is the {regex: {pattern, from, capture?, default?}} form.
type RegexExpr struct {
	// Pattern is the Go regular expression source.
	Pattern string `yaml:"pattern" json:"pattern"`
	// From is the Value whose resolved string the pattern matches.
	From Value `yaml:"from" json:"from"`
	// Capture, when non-zero, selects the 1-based capture group to
	// return. Zero (or omitted) returns the full match.
	Capture int `yaml:"capture,omitempty" json:"capture,omitempty"`
	// Default, when set, is the Value returned when the pattern does
	// not match. Absent both match and default, the result is a zero
	// Value.
	Default *Value `yaml:"default,omitempty" json:"default,omitempty"`
}

// ---- YAML unmarshalling ----

// UnmarshalYAML implements yaml.Unmarshaler.
func (v *Value) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return v.unmarshalYAMLScalar(node)
	case yaml.MappingNode:
		return v.unmarshalYAMLMap(node)
	case yaml.SequenceNode:
		return fmt.Errorf("schema.Value: bare YAML sequence is not a valid Value at line %d; use {list: [...]}", node.Line)
	default:
		return fmt.Errorf("schema.Value: unsupported YAML node kind %v at line %d", node.Kind, node.Line)
	}
}

func (v *Value) unmarshalYAMLScalar(node *yaml.Node) error {
	switch node.Tag {
	case "!!null":
		v.IsZero = true
		return nil
	case "!!int":
		var n int64
		if err := node.Decode(&n); err != nil {
			return fmt.Errorf("schema.Value int at line %d: %w", node.Line, err)
		}
		v.LiteralInt = &n
		return nil
	case "!!bool":
		var b bool
		if err := node.Decode(&b); err != nil {
			return fmt.Errorf("schema.Value bool at line %d: %w", node.Line, err)
		}
		v.LiteralBool = &b
		return nil
	default:
		// All other scalars (!!str, !!float, untagged) flow through
		// the interpolation desugaring path. Strings with no '$' are
		// returned as plain LiteralString on the fast path.
		desugared, err := desugarInterpolation(node.Value)
		if err != nil {
			return fmt.Errorf("schema.Value at line %d: %w", node.Line, err)
		}
		*v = desugared
		return nil
	}
}

func (v *Value) unmarshalYAMLMap(node *yaml.Node) error {
	keys := mapKeys(node)
	disc, err := pickValueDiscriminator(keys)
	if err != nil {
		return fmt.Errorf("schema.Value at line %d: %w", node.Line, err)
	}
	if err := checkValueSiblingKeys(disc, keys); err != nil {
		return fmt.Errorf("schema.Value at line %d: %w", node.Line, err)
	}

	switch disc {
	case "literal_string":
		var raw struct {
			S string `yaml:"literal_string"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value literal_string at line %d: %w", node.Line, err)
		}
		v.LiteralString = &raw.S
		return nil

	case "ref":
		var r RefValue
		if err := node.Decode(&r); err != nil {
			return fmt.Errorf("schema.Value ref at line %d: %w", node.Line, err)
		}
		v.Ref = &r
		return nil

	case "now":
		var raw struct {
			Now bool `yaml:"now"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value now at line %d: %w", node.Line, err)
		}
		if !raw.Now {
			return fmt.Errorf("schema.Value: {now: false} is not valid at line %d", node.Line)
		}
		v.Now = &NowValue{}
		return nil

	case "concat":
		var raw struct {
			Concat []Value `yaml:"concat"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value concat at line %d: %w", node.Line, err)
		}
		if len(raw.Concat) < 2 {
			return fmt.Errorf("schema.Value concat at line %d: must contain at least 2 elements; for a single value use the value directly", node.Line)
		}
		v.Concat = raw.Concat
		return nil

	case "select":
		var raw struct {
			Select SelectValue `yaml:"select"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value select at line %d: %w", node.Line, err)
		}
		v.Select = &raw.Select
		return nil

	case "format":
		var raw struct {
			Verb  string `yaml:"format"`
			Value Value  `yaml:"value"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value format at line %d: %w", node.Line, err)
		}
		v.Format = &FormatValue{Verb: raw.Verb, Value: raw.Value}
		return nil

	case "base64":
		var raw struct {
			Inner Value `yaml:"base64"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value base64 at line %d: %w", node.Line, err)
		}
		v.Base64 = &raw.Inner
		return nil

	case "list":
		var raw struct {
			List []Value `yaml:"list"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value list at line %d: %w", node.Line, err)
		}
		if raw.List == nil {
			raw.List = []Value{}
		}
		v.List = raw.List
		return nil

	case "object":
		var raw struct {
			Object map[string]Value `yaml:"object"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value object at line %d: %w", node.Line, err)
		}
		if raw.Object == nil {
			raw.Object = map[string]Value{}
		}
		v.Object = raw.Object
		return nil

	case "add", "subtract":
		ops, err := decodeArithOperandsYAML(node, disc)
		if err != nil {
			return err
		}
		if disc == "add" {
			v.Add = &ArithExpr{Operands: ops}
		} else {
			v.Subtract = &ArithExpr{Operands: ops}
		}
		return nil

	case "max", "min", "first", "last", "count":
		inner, err := decodeReducerOperandYAML(node, disc)
		if err != nil {
			return err
		}
		switch disc {
		case "max":
			v.Max = &inner
		case "min":
			v.Min = &inner
		case "first":
			v.First = &inner
		case "last":
			v.Last = &inner
		case "count":
			v.Count = &inner
		}
		return nil

	case "regex":
		var raw struct {
			Regex RegexExpr `yaml:"regex"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value regex at line %d: %w", node.Line, err)
		}
		if raw.Regex.Pattern == "" {
			return fmt.Errorf("schema.Value regex at line %d: pattern is required", node.Line)
		}
		v.Regex = &raw.Regex
		return nil
	}

	return fmt.Errorf("schema.Value at line %d: unreachable discriminator %q", node.Line, disc)
}

// decodeArithOperandsYAML extracts the two-element operand list under disc
// from a YAML mapping node. Operand count is enforced at parse time.
func decodeArithOperandsYAML(node *yaml.Node, disc string) ([]Value, error) {
	var raw map[string][]Value
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("schema.Value %s at line %d: %w", disc, node.Line, err)
	}
	ops := raw[disc]
	if len(ops) != 2 {
		return nil, fmt.Errorf("schema.Value %s at line %d: must have exactly 2 operands, got %d", disc, node.Line, len(ops))
	}
	return ops, nil
}

// decodeReducerOperandYAML extracts the operand of a reducer (max/min/first/
// last/count). A bare sequence is sugar for {list: [...]}; any other shape
// decodes as a regular Value (typically a {ref: ...} projection).
func decodeReducerOperandYAML(node *yaml.Node, disc string) (Value, error) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Kind == yaml.ScalarNode && node.Content[i].Value == disc {
			inner := node.Content[i+1]
			if inner.Kind == yaml.SequenceNode {
				var items []Value
				if err := inner.Decode(&items); err != nil {
					return Value{}, fmt.Errorf("schema.Value %s at line %d: %w", disc, node.Line, err)
				}
				return Value{List: items}, nil
			}
			var v Value
			if err := inner.Decode(&v); err != nil {
				return Value{}, fmt.Errorf("schema.Value %s at line %d: %w", disc, node.Line, err)
			}
			return v, nil
		}
	}
	return Value{}, fmt.Errorf("schema.Value %s at line %d: missing operand", disc, node.Line)
}

// valueVariants returns the (name, payload) pairs for every Value variant
// that is currently set, in the declaration order of valueDiscriminatorKeys
// plus the bare-scalar variants. literal_int and literal_bool are listed
// here even though they are not in valueDiscriminatorKeys: those forms are
// reached from YAML int / bool scalars rather than via a map-key
// discriminator, so Variant() / VariantNames() must still surface them
// when set.
func valueVariants(v Value) (names []string, payloads []any) {
	add := func(name string, payload any, set bool) {
		if set {
			names = append(names, name)
			payloads = append(payloads, payload)
		}
	}
	add("literal_string", v.LiteralString, v.LiteralString != nil)
	add("literal_int", v.LiteralInt, v.LiteralInt != nil)
	add("literal_bool", v.LiteralBool, v.LiteralBool != nil)
	add("ref", v.Ref, v.Ref != nil)
	add("now", v.Now, v.Now != nil)
	add("concat", v.Concat, len(v.Concat) > 0)
	add("select", v.Select, v.Select != nil)
	add("format", v.Format, v.Format != nil)
	add("base64", v.Base64, v.Base64 != nil)
	add("list", v.List, v.List != nil)
	add("object", v.Object, v.Object != nil)
	add("add", v.Add, v.Add != nil)
	add("subtract", v.Subtract, v.Subtract != nil)
	add("max", v.Max, v.Max != nil)
	add("min", v.Min, v.Min != nil)
	add("first", v.First, v.First != nil)
	add("last", v.Last, v.Last != nil)
	add("count", v.Count, v.Count != nil)
	add("regex", v.Regex, v.Regex != nil)
	return names, payloads
}

// Variant returns the active Value variant name and payload. Returns
// ("", nil) when the Value is zero (no form set) or, for callers that
// hand-construct a Doc, when multiple forms are set at once. Documents
// loaded via schema.Load / schema.Parse have multi-form rejection
// enforced at codec time, so this fallback only matters for in-process
// builders.
func (v Value) Variant() (string, any) {
	names, payloads := valueVariants(v)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set Value form.
func (v Value) VariantNames() []string {
	names, _ := valueVariants(v)
	return names
}

// IsAbsent reports whether v is empty for any reason — either the explicit
// null form (IsZero true) or the never-decoded form (no discriminator set).
// Mirrors Path.IsEmpty so validator call sites don't repeat the open-coded
// check. yaml.v3 and encoding/json leave omitted Value fields at the Go zero
// Value{} without invoking UnmarshalYAML, so IsZero alone misses the omit
// case; required-field checks should use IsAbsent.
func (v Value) IsAbsent() bool {
	return v.IsZero || len(v.VariantNames()) == 0
}

// pickValueDiscriminator returns the single Value discriminator key present
// in keys, or an error when zero or more than one are present.
func pickValueDiscriminator(keys map[string]struct{}) (string, error) {
	matches := make([]string, 0, 2)
	for _, k := range valueDiscriminatorKeys {
		if _, ok := keys[k]; ok {
			matches = append(matches, k)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no recognised discriminator key " +
			"(want one of literal_string|ref|now|concat|select|" +
			"format|base64|list|object|add|subtract|max|min|first|" +
			"last|count|regex); wrap a literal map in {object: {...}}")
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple discriminator keys present (%v); a Value mapping must carry exactly one", matches)
	}
}

// mapKeys returns the set of top-level keys from a YAML MappingNode.
func mapKeys(node *yaml.Node) map[string]struct{} {
	keys := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Kind == yaml.ScalarNode {
			keys[node.Content[i].Value] = struct{}{}
		}
	}
	return keys
}

// ---- String interpolation ----

// desugarInterpolation scans s for ${<path>[|<default>]} segments and returns
// the equivalent Value. A string with no '$' is returned as a plain
// LiteralString on the fast path. A string with a single interpolation
// segment and no surrounding text is returned as that segment directly;
// otherwise the result is a Concat over the literal and ref segments.
func desugarInterpolation(s string) (Value, error) {
	if !strings.ContainsRune(s, '$') {
		lit := s
		return Value{LiteralString: &lit}, nil
	}

	var segments []Value
	var lit strings.Builder
	flushLit := func() {
		if lit.Len() == 0 {
			return
		}
		piece := lit.String()
		segments = append(segments, Value{LiteralString: &piece})
		lit.Reset()
	}

	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '$' {
			lit.WriteByte('$')
			i += 2
			continue
		}
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			flushLit()
			end, segText, err := scanInterpSegment(s, i+2)
			if err != nil {
				return Value{}, err
			}
			ref, err := parseInterpSegment(segText)
			if err != nil {
				return Value{}, fmt.Errorf("interpolation segment %q: %w", segText, err)
			}
			segments = append(segments, ref)
			i = end
			continue
		}
		lit.WriteByte(s[i])
		i++
	}
	flushLit()

	switch len(segments) {
	case 0:
		empty := ""
		return Value{LiteralString: &empty}, nil
	case 1:
		return segments[0], nil
	default:
		return Value{Concat: segments}, nil
	}
}

// scanInterpSegment reads the body of a ${...} segment starting at index start
// (just after the opening '{'). Returns the index just past the closing '}'
// and the unescaped segment body. A literal '{' inside the segment is written
// '\{'.
func scanInterpSegment(s string, start int) (end int, body string, err error) {
	var b strings.Builder
	i := start
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '{' {
			b.WriteByte('{')
			i += 2
			continue
		}
		if s[i] == '}' {
			return i + 1, b.String(), nil
		}
		b.WriteByte(s[i])
		i++
	}
	return 0, "", fmt.Errorf("unterminated ${...} segment")
}

// parseInterpSegment turns a segment body (path[|default]) into a Ref Value.
// The text before '|' is the path; the text after '|' (if any) is parsed as
// a YAML scalar so non-string defaults (int, bool) round-trip naturally.
func parseInterpSegment(body string) (Value, error) {
	pathStr := body
	var defaultStr string
	hasDefault := false
	if idx := strings.IndexByte(body, '|'); idx >= 0 {
		pathStr = body[:idx]
		defaultStr = body[idx+1:]
		hasDefault = true
	}
	pathStr = strings.TrimSpace(pathStr)
	if pathStr == "" {
		return Value{}, fmt.Errorf("empty path")
	}
	path, err := ParsePath(pathStr)
	if err != nil {
		return Value{}, err
	}
	ref := &RefValue{Path: path}
	if hasDefault {
		def, err := parseInterpDefault(defaultStr)
		if err != nil {
			return Value{}, fmt.Errorf("default %q: %w", defaultStr, err)
		}
		ref.Default = &def
	}
	return Value{Ref: ref}, nil
}

// parseInterpDefault decodes the text after '|' as a YAML scalar. An empty
// default is an explicit empty string (the canonical "${ref|}" form).
func parseInterpDefault(text string) (Value, error) {
	if text == "" {
		empty := ""
		return Value{LiteralString: &empty}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return Value{}, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		s := text
		return Value{LiteralString: &s}, nil
	}
	scalar := doc.Content[0]
	if scalar.Kind != yaml.ScalarNode {
		return Value{}, fmt.Errorf("default must be a scalar")
	}
	var v Value
	if err := v.unmarshalYAMLScalar(scalar); err != nil {
		return Value{}, err
	}
	return v, nil
}

// ---- YAML marshalling ----

// MarshalYAML emits the canonical YAML representation.
// Receiver is by value so map elements dispatch through this method.
func (v Value) MarshalYAML() (interface{}, error) {
	if v.IsZero {
		return nil, nil
	}
	if v.LiteralInt != nil {
		return *v.LiteralInt, nil
	}
	if v.LiteralBool != nil {
		return *v.LiteralBool, nil
	}
	if v.LiteralString != nil {
		return *v.LiteralString, nil
	}

	switch {
	case v.Ref != nil:
		if v.Ref.Default != nil {
			return map[string]interface{}{"ref": v.Ref.Path, "default": v.Ref.Default}, nil
		}
		return map[string]interface{}{"ref": v.Ref.Path}, nil

	case v.Now != nil:
		return map[string]interface{}{"now": true}, nil

	case len(v.Concat) > 0:
		return map[string]interface{}{"concat": v.Concat}, nil

	case v.Select != nil:
		return map[string]interface{}{"select": v.Select}, nil

	case v.Format != nil:
		return map[string]interface{}{"format": v.Format.Verb, "value": v.Format.Value}, nil

	case v.Base64 != nil:
		return map[string]interface{}{"base64": v.Base64}, nil

	case v.List != nil:
		return map[string]interface{}{"list": v.List}, nil

	case v.Object != nil:
		return map[string]interface{}{"object": v.Object}, nil

	case v.Add != nil:
		return map[string]interface{}{"add": v.Add.Operands}, nil

	case v.Subtract != nil:
		return map[string]interface{}{"subtract": v.Subtract.Operands}, nil

	case v.Max != nil:
		return map[string]interface{}{"max": *v.Max}, nil

	case v.Min != nil:
		return map[string]interface{}{"min": *v.Min}, nil

	case v.First != nil:
		return map[string]interface{}{"first": *v.First}, nil

	case v.Last != nil:
		return map[string]interface{}{"last": *v.Last}, nil

	case v.Count != nil:
		return map[string]interface{}{"count": *v.Count}, nil

	case v.Regex != nil:
		return map[string]interface{}{"regex": v.Regex}, nil
	}

	return nil, fmt.Errorf("schema.Value: zero value cannot be marshalled; for an explicit absent value set IsZero, for an empty string use literal_string: \"\"")
}

// ---- JSON unmarshalling ----

// UnmarshalJSON implements json.Unmarshaler with the same rules as YAML.
func (v *Value) UnmarshalJSON(data []byte) error {
	bs := bytes.TrimSpace(data)
	if len(bs) == 0 {
		return fmt.Errorf("schema.Value: empty JSON input")
	}
	switch bs[0] {
	case 'n':
		v.IsZero = true
		return nil
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(bs, &b); err != nil {
			return fmt.Errorf("schema.Value bool: %w", err)
		}
		v.LiteralBool = &b
		return nil
	case '"':
		var s string
		if err := json.Unmarshal(bs, &s); err != nil {
			return fmt.Errorf("schema.Value string: %w", err)
		}
		desugared, err := desugarInterpolation(s)
		if err != nil {
			return fmt.Errorf("schema.Value: %w", err)
		}
		*v = desugared
		return nil
	case '{':
		return v.unmarshalJSONMap(bs)
	case '[':
		return fmt.Errorf("schema.Value: bare JSON array is not a valid Value; use {list: [...]}")
	default:
		var n int64
		if err := json.Unmarshal(bs, &n); err != nil {
			return fmt.Errorf("schema.Value int: %w (only integer literals are supported)", err)
		}
		v.LiteralInt = &n
		return nil
	}
}

func (v *Value) unmarshalJSONMap(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("schema.Value object: %w", err)
	}

	keys := make(map[string]struct{}, len(raw))
	for k := range raw {
		keys[k] = struct{}{}
	}
	disc, err := pickValueDiscriminator(keys)
	if err != nil {
		return fmt.Errorf("schema.Value: %w", err)
	}
	if err := checkValueSiblingKeys(disc, keys); err != nil {
		return fmt.Errorf("schema.Value: %w", err)
	}

	switch disc {
	case "literal_string":
		var s string
		if err := json.Unmarshal(raw["literal_string"], &s); err != nil {
			return fmt.Errorf("schema.Value.literal_string: %w", err)
		}
		v.LiteralString = &s
		return nil

	case "ref":
		var r RefValue
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("schema.Value.ref: %w", err)
		}
		v.Ref = &r
		return nil

	case "now":
		var nv struct {
			Now bool `json:"now"`
		}
		if err := json.Unmarshal(data, &nv); err != nil {
			return fmt.Errorf("schema.Value.now: %w", err)
		}
		if !nv.Now {
			return fmt.Errorf("schema.Value: {now: false} is not valid")
		}
		v.Now = &NowValue{}
		return nil

	case "concat":
		var c struct {
			Concat []Value `json:"concat"`
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("schema.Value.concat: %w", err)
		}
		if len(c.Concat) < 2 {
			return fmt.Errorf("schema.Value.concat: must contain at least 2 elements; for a single value use the value directly")
		}
		v.Concat = c.Concat
		return nil

	case "select":
		var s struct {
			Select SelectValue `json:"select"`
		}
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("schema.Value.select: %w", err)
		}
		v.Select = &s.Select
		return nil

	case "format":
		var f struct {
			Verb  string `json:"format"`
			Value Value  `json:"value"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			return fmt.Errorf("schema.Value.format: %w", err)
		}
		v.Format = &FormatValue{Verb: f.Verb, Value: f.Value}
		return nil

	case "base64":
		var inner Value
		if err := json.Unmarshal(raw["base64"], &inner); err != nil {
			return fmt.Errorf("schema.Value.base64: %w", err)
		}
		v.Base64 = &inner
		return nil

	case "list":
		var l struct {
			List []Value `json:"list"`
		}
		if err := json.Unmarshal(data, &l); err != nil {
			return fmt.Errorf("schema.Value.list: %w", err)
		}
		if l.List == nil {
			l.List = []Value{}
		}
		v.List = l.List
		return nil

	case "object":
		var o struct {
			Object map[string]Value `json:"object"`
		}
		if err := json.Unmarshal(data, &o); err != nil {
			return fmt.Errorf("schema.Value.object: %w", err)
		}
		if o.Object == nil {
			o.Object = map[string]Value{}
		}
		v.Object = o.Object
		return nil

	case "add", "subtract":
		var ops []Value
		if err := json.Unmarshal(raw[disc], &ops); err != nil {
			return fmt.Errorf("schema.Value.%s: %w", disc, err)
		}
		if len(ops) != 2 {
			return fmt.Errorf("schema.Value.%s: must have exactly 2 operands, got %d", disc, len(ops))
		}
		if disc == "add" {
			v.Add = &ArithExpr{Operands: ops}
		} else {
			v.Subtract = &ArithExpr{Operands: ops}
		}
		return nil

	case "max", "min", "first", "last", "count":
		inner, err := decodeReducerOperandJSON(raw[disc])
		if err != nil {
			return fmt.Errorf("schema.Value.%s: %w", disc, err)
		}
		switch disc {
		case "max":
			v.Max = &inner
		case "min":
			v.Min = &inner
		case "first":
			v.First = &inner
		case "last":
			v.Last = &inner
		case "count":
			v.Count = &inner
		}
		return nil

	case "regex":
		var r RegexExpr
		if err := json.Unmarshal(raw["regex"], &r); err != nil {
			return fmt.Errorf("schema.Value.regex: %w", err)
		}
		if r.Pattern == "" {
			return fmt.Errorf("schema.Value.regex: pattern is required")
		}
		v.Regex = &r
		return nil
	}

	return fmt.Errorf("schema.Value: unreachable discriminator %q", disc)
}

// decodeReducerOperandJSON parses the operand of a reducer from a JSON
// RawMessage. A bare array is sugar for {list: [...]}; any other shape
// decodes as a regular Value.
func decodeReducerOperandJSON(raw json.RawMessage) (Value, error) {
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []Value
		if err := json.Unmarshal(raw, &items); err != nil {
			return Value{}, err
		}
		return Value{List: items}, nil
	}
	var inner Value
	if err := json.Unmarshal(raw, &inner); err != nil {
		return Value{}, err
	}
	return inner, nil
}

// ---- JSON marshalling ----

// MarshalJSON emits the canonical JSON representation.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.IsZero {
		return []byte("null"), nil
	}
	if v.LiteralInt != nil {
		return json.Marshal(*v.LiteralInt)
	}
	if v.LiteralBool != nil {
		return json.Marshal(*v.LiteralBool)
	}
	if v.LiteralString != nil {
		return json.Marshal(*v.LiteralString)
	}

	switch {
	case v.Ref != nil:
		type refOut struct {
			Path    Path   `json:"ref"`
			Default *Value `json:"default,omitempty"`
		}
		return json.Marshal(refOut{Path: v.Ref.Path, Default: v.Ref.Default})

	case v.Now != nil:
		type nowOut struct {
			Now bool `json:"now"`
		}
		return json.Marshal(nowOut{Now: true})

	case len(v.Concat) > 0:
		type concatOut struct {
			Concat []Value `json:"concat"`
		}
		return json.Marshal(concatOut{Concat: v.Concat})

	case v.Select != nil:
		type selectOut struct {
			Select *SelectValue `json:"select"`
		}
		return json.Marshal(selectOut{Select: v.Select})

	case v.Format != nil:
		type fmtOut struct {
			Verb  string `json:"format"`
			Value Value  `json:"value"`
		}
		return json.Marshal(fmtOut{Verb: v.Format.Verb, Value: v.Format.Value})

	case v.Base64 != nil:
		type b64Out struct {
			Inner Value `json:"base64"`
		}
		return json.Marshal(b64Out{Inner: *v.Base64})

	case v.List != nil:
		type listOut struct {
			List []Value `json:"list"`
		}
		return json.Marshal(listOut{List: v.List})

	case v.Object != nil:
		type objOut struct {
			Object map[string]Value `json:"object"`
		}
		return json.Marshal(objOut{Object: v.Object})

	case v.Add != nil:
		type addOut struct {
			Add []Value `json:"add"`
		}
		return json.Marshal(addOut{Add: v.Add.Operands})

	case v.Subtract != nil:
		type subOut struct {
			Subtract []Value `json:"subtract"`
		}
		return json.Marshal(subOut{Subtract: v.Subtract.Operands})

	case v.Max != nil:
		type maxOut struct {
			Max Value `json:"max"`
		}
		return json.Marshal(maxOut{Max: *v.Max})

	case v.Min != nil:
		type minOut struct {
			Min Value `json:"min"`
		}
		return json.Marshal(minOut{Min: *v.Min})

	case v.First != nil:
		type firstOut struct {
			First Value `json:"first"`
		}
		return json.Marshal(firstOut{First: *v.First})

	case v.Last != nil:
		type lastOut struct {
			Last Value `json:"last"`
		}
		return json.Marshal(lastOut{Last: *v.Last})

	case v.Count != nil:
		type countOut struct {
			Count Value `json:"count"`
		}
		return json.Marshal(countOut{Count: *v.Count})

	case v.Regex != nil:
		type regexOut struct {
			Regex *RegexExpr `json:"regex"`
		}
		return json.Marshal(regexOut{Regex: v.Regex})
	}

	return nil, fmt.Errorf("schema.Value: zero value cannot be marshalled; for an explicit absent value set IsZero, for an empty string use literal_string: \"\"")
}

// ---- Secret propagation ----

// IsSecret reports whether evaluating v could surface a secret-typed state
// field. It walks the Value recursively and returns true as soon as any
// reachable Ref resolves to a state.<name> path whose FieldDecl.Type is
// "secret".
//
// Targets MUST call IsSecret(d, v) on any Value before rendering it into a
// log line, error message, or other debug surface. A target that cannot
// redact a transitive composition (e.g. a Concat that includes a secret Ref)
// must treat the entire composed Value as secret.
//
// The traversal covers every container the IR can express:
//
//   - Ref (and Ref.Default)
//   - Concat elements
//   - Select branches (when, value) and Select default
//   - Format.Value
//   - Base64 (the wrapped Value)
//   - List elements
//   - Object map values
//   - Add / Subtract operand pairs
//   - Max / Min / First / Last / Count reducer operands
//   - Regex.From and Regex.Default
//
// Predicate.Eq.Path is checked the same way as a state.<name> Ref. Literal
// scalars and {now: true} are never secret.
func IsSecret(d *Doc, v Value) bool {
	switch {
	case v.Ref != nil:
		if isSecretStatePath(d, v.Ref.Path) {
			return true
		}
		if v.Ref.Default != nil && IsSecret(d, *v.Ref.Default) {
			return true
		}
	case v.Concat != nil:
		for _, e := range v.Concat {
			if IsSecret(d, e) {
				return true
			}
		}
	case v.Select != nil:
		for _, b := range v.Select.Branches {
			if predicateContainsSecret(d, b.When) || IsSecret(d, b.Value) {
				return true
			}
		}
		if IsSecret(d, v.Select.Default) {
			return true
		}
	case v.Format != nil:
		if IsSecret(d, v.Format.Value) {
			return true
		}
	case v.Base64 != nil:
		if IsSecret(d, *v.Base64) {
			return true
		}
	case v.List != nil:
		for _, e := range v.List {
			if IsSecret(d, e) {
				return true
			}
		}
	case v.Object != nil:
		for _, e := range v.Object {
			if IsSecret(d, e) {
				return true
			}
		}
	case v.Add != nil:
		for _, e := range v.Add.Operands {
			if IsSecret(d, e) {
				return true
			}
		}
	case v.Subtract != nil:
		for _, e := range v.Subtract.Operands {
			if IsSecret(d, e) {
				return true
			}
		}
	case v.Max != nil:
		if IsSecret(d, *v.Max) {
			return true
		}
	case v.Min != nil:
		if IsSecret(d, *v.Min) {
			return true
		}
	case v.First != nil:
		if IsSecret(d, *v.First) {
			return true
		}
	case v.Last != nil:
		if IsSecret(d, *v.Last) {
			return true
		}
	case v.Count != nil:
		if IsSecret(d, *v.Count) {
			return true
		}
	case v.Regex != nil:
		if IsSecret(d, v.Regex.From) {
			return true
		}
		if v.Regex.Default != nil && IsSecret(d, *v.Regex.Default) {
			return true
		}
	}
	return false
}

// isSecretStatePath reports whether p is rooted at state.<name> where the
// declared field type is "secret".
func isSecretStatePath(d *Doc, p Path) bool {
	if d == nil || d.State == nil || len(p.Parts) < 2 || p.Parts[0] != "state" {
		return false
	}
	fd, ok := d.State[p.Parts[1]]
	if !ok {
		return false
	}
	return fd.Type == "secret"
}

// predicateContainsSecret reports whether evaluating p could surface a
// secret-typed state field. Used by IsSecret to recurse through Select
// branches.
func predicateContainsSecret(d *Doc, p Predicate) bool {
	for _, pe := range []*PredicateEq{p.Eq, p.Gt, p.Lt, p.Gte, p.Lte} {
		if pe == nil {
			continue
		}
		if isSecretStatePath(d, pe.Path) || IsSecret(d, pe.Value) {
			return true
		}
	}
	if p.Present != nil && isSecretStatePath(d, *p.Present) {
		return true
	}
	for _, sub := range p.And {
		if predicateContainsSecret(d, sub) {
			return true
		}
	}
	for _, sub := range p.Or {
		if predicateContainsSecret(d, sub) {
			return true
		}
	}
	if p.Not != nil && predicateContainsSecret(d, *p.Not) {
		return true
	}
	return false
}
