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
//	100                   → LiteralInt(100)
//	true                  → LiteralBool(true)
//	null                  → IsZero
//	{literal_string: "x"} → LiteralString("x")  (explicit form)
//	{ref: cursor.last_timestamp}         → Ref
//	{ref: state.url, default: "http://…"} → Ref with Default
//	{now: true, offset: "-1h", format: rfc3339} → Now
//	{concat: [<Value>, ...]}             → Concat
//	{select: {branches: [...], default: <Value>}} → Select
//	{format: string, value: {ref: state.page_size}} → Format
//	{base64: {concat: [...]}}            → Base64
//	{list: [<Value>, ...]}               → List
//	{object: {<key>: <Value>, ...}}      → Object (explicit; required for any
//	                                       map-shaped Value)
//
// A map-shaped Value MUST carry exactly one discriminator key. There is no
// silent fallback for an arbitrary mapping; authors who want a literal map
// must wrap it in {object: {...}}. This isolates Object as the only form whose
// inner keys are NOT re-interpreted as discriminators (inner keys are literal
// strings; inner values still recurse as Values).
type Value struct {
	// LiteralString is the scalar string form, set when the YAML/JSON
	// node is a quoted or untagged scalar.
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
	// Now is the {now: true, offset?: <Value>} form.
	Now *NowValue
	// Concat is the {concat: [<Value>, ...]} form: concatenate the
	// resolved string representation of each element.
	Concat []Value
	// Select is the {select: {branches: [...], default: <Value>}} form.
	Select *SelectValue
	// Format is the {format: <verb>, value: <Value>} form: apply a
	// format verb (rfc3339, unix_seconds, ...) to the inner value.
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
}

// removedValueDiscriminatorKeys carries deleted discriminator keys whose
// presence in a YAML/JSON Value mapping should surface a precise migration
// hint instead of the generic "no recognised discriminator key" error.
// pickValueDiscriminator consults this map after a no-match outcome so
// authors who copy an old template see exactly which form replaced theirs.
var removedValueDiscriminatorKeys = map[string]string{
	"from_pagination": "{from_pagination: <role>} was removed in slice 4; use {ref: cursor.<name>} instead (e.g. {ref: cursor.token}, {ref: cursor.page}, {ref: cursor.offset}, {ref: cursor.scroll_id}, {ref: cursor.<cursor_var>} for graphql_relay)",
	"from_progress":   "{from_progress: <role>} was removed in slice 4; use {ref: cursor.<name>} instead (latest_timestamp → cursor.last_timestamp; window_start → cursor.window_start; window_end → cursor.window_end)",
}

// valueVariantAllowedKeys names every map key that may appear alongside a
// given discriminator in a Value mapping. Any other sibling key is a parse
// error: silent acceptance lets typos (e.g. {ref: state.x, defualt: "y"})
// drop into the void.
var valueVariantAllowedKeys = map[string]map[string]struct{}{
	"literal_string": {"literal_string": {}},
	"ref":            {"ref": {}, "default": {}},
	"now":            {"now": {}, "offset": {}},
	"concat":         {"concat": {}},
	"select":         {"select": {}},
	"format":         {"format": {}, "value": {}},
	"base64":         {"base64": {}},
	"list":           {"list": {}},
	"object":         {"object": {}},
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
	// Path is the namespace-rooted locator. Legal roots:
	// state.<name>, cursor.<name>, extract.<name>,
	// steps.<id>.body.<path>, steps.<id>.header.<name>,
	// response.body.<path>, response.header.<name>, item.<path>.
	// The response.<...> roots are contextual: valid only at the
	// call sites listed in docs/schema.md ("response.* call-site
	// table") — most commonly inside complete_when predicates.
	Path Path `yaml:"ref" json:"ref"`
	// Default, when set, is the fallback Value used when the reference
	// resolves to nil at evaluation time.
	Default *Value `yaml:"default,omitempty" json:"default,omitempty"`
}

// NowValue is the {now: true, offset?: <Value>} form. To coerce a Now value to
// a specific representation, wrap it with a Format Value:
// {format: rfc3339, value: {now: true}}. This keeps Now and Format orthogonal
// and avoids a discriminator-key clash between Now and the Format Value form.
type NowValue struct {
	// Offset, when set, is a duration Value added to (or subtracted
	// from) now() before returning. Use a negative duration to look
	// backwards (e.g. {offset: "-5m"}).
	Offset *Value `yaml:"offset,omitempty" json:"offset,omitempty"`
}

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

// FormatValue is the {format: <verb>, value: <Value>} form.
type FormatValue struct {
	// Verb is the format-verb name: string, int, bool, rfc3339,
	// rfc3339nano, unix_seconds, unix_millis, duration, url_encode,
	// parse_duration.
	Verb string `yaml:"format" json:"format"`
	// Value is the inner Value the verb is applied to.
	Value Value `yaml:"value" json:"value"`
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
		// All other scalars (!!str, !!float, untagged) become LiteralString.
		s := node.Value
		v.LiteralString = &s
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
			Now    bool   `yaml:"now"`
			Offset *Value `yaml:"offset,omitempty"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Value now at line %d: %w", node.Line, err)
		}
		if !raw.Now {
			return fmt.Errorf("schema.Value: {now: false} is not valid at line %d", node.Line)
		}
		v.Now = &NowValue{Offset: raw.Offset}
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
	}

	return fmt.Errorf("schema.Value at line %d: unreachable discriminator %q", node.Line, disc)
}

// valueVariants returns the (name, payload) pairs for every Value variant
// that is currently set, in the declaration order of valueDiscriminatorKeys.
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
	return names, payloads
}

// Variant returns the active Value variant name and payload. Returns
// ("", nil) when the Value is zero (IsZero or no form set) or, in the rare
// case targets call this on a hand-constructed Doc, when multiple forms are
// set. Documents loaded via schema.Load / schema.Parse have multi-form rejection
// enforced at codec time.
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

// pickValueDiscriminator returns the single Value discriminator key present in
// keys, or an error when zero or more than one are present. If keys contains a
// removed-but-still-recognisable discriminator (see removedValueDiscriminatorKeys)
// the error names the new replacement form so authors get a precise migration
// hint rather than the generic "no recognised discriminator key" message.
func pickValueDiscriminator(keys map[string]struct{}) (string, error) {
	matches := make([]string, 0, 2)
	for _, k := range valueDiscriminatorKeys {
		if _, ok := keys[k]; ok {
			matches = append(matches, k)
		}
	}
	switch len(matches) {
	case 0:
		for k := range keys {
			if hint, ok := removedValueDiscriminatorKeys[k]; ok {
				return "", fmt.Errorf("%s", hint)
			}
		}
		return "", fmt.Errorf("no recognised discriminator key " +
			"(want one of literal_string|ref|now|concat|select|" +
			"format|base64|list|object); wrap a literal map in {object: {...}}")
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
		m := map[string]interface{}{"now": true}
		if v.Now.Offset != nil {
			m["offset"] = v.Now.Offset
		}
		return m, nil

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
		v.LiteralString = &s
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
			Now    bool   `json:"now"`
			Offset *Value `json:"offset,omitempty"`
		}
		if err := json.Unmarshal(data, &nv); err != nil {
			return fmt.Errorf("schema.Value.now: %w", err)
		}
		if !nv.Now {
			return fmt.Errorf("schema.Value: {now: false} is not valid")
		}
		v.Now = &NowValue{Offset: nv.Offset}
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
	}

	return fmt.Errorf("schema.Value: unreachable discriminator %q", disc)
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
			Now    bool   `json:"now"`
			Offset *Value `json:"offset,omitempty"`
		}
		return json.Marshal(nowOut{Now: true, Offset: v.Now.Offset})

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
//   - Now.Offset
//   - Concat elements
//   - Select branches (when, value) and Select default
//   - Format.Value
//   - Base64 (the wrapped Value)
//   - List elements
//   - Object map values
//
// Predicate.Eq.Path is checked the same way as a state.<name> Ref. Literal
// scalars are never secret.
func IsSecret(d *Doc, v Value) bool {
	switch {
	case v.Ref != nil:
		if isSecretStatePath(d, v.Ref.Path) {
			return true
		}
		if v.Ref.Default != nil && IsSecret(d, *v.Ref.Default) {
			return true
		}
	case v.Now != nil:
		if v.Now.Offset != nil && IsSecret(d, *v.Now.Offset) {
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
	}
	return false
}

// isSecretStatePath reports whether p is rooted at state.<name> where the
// declared field type is "secret".
func isSecretStatePath(d *Doc, p Path) bool {
	if d == nil || d.State == nil || len(p.Parts) < 2 || p.Parts[0] != "state" {
		return false
	}
	fd, ok := d.State.Fields[p.Parts[1]]
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
