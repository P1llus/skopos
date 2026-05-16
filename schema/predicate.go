// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Predicate is the boolean condition type used in:
//   - requests[].if
//   - auth.multi_mode.branches[].when
//   - Value.select.branches[].when
//   - async_job.poll.complete_when
//   - pagination.scroll_id.complete_when
//
// It is a discriminated union — exactly one form is active.
//
// Forms:
//
//	{eq:  {path: cursor.phase,  value: "submit"}}
//	{gt:  {path: cursor.page,   value: {ref: state.total_pages}}}
//	{lt:  {path: cursor.page,   value: {ref: state.total_pages}}}
//	{gte: {path: state.retries, value: 3}}
//	{lte: {path: state.retries, value: 3}}
//	{present: state.etag}
//	{and: [{eq: ...}, {present: ...}]}
//	{or:  [{eq: ...}, {eq: ...}]}
//	{not: {eq: ...}}
//	{literal_bool: true}
//
// gt / lt / gte / lte share the PredicateEq shape — they compare a
// namespace-rooted Path against a Value. The verbs are ordered comparisons
// over numbers, durations (as ns), and RFC 3339 timestamps; targets that
// cannot type-check both operands (e.g. when the path resolves to a string)
// must surface a lowering error rather than silently coercing.
type Predicate struct {
	// Eq is the {eq: {path, value}} form: equality comparison.
	Eq *PredicateEq
	// Gt is the {gt: {path, value}} form: greater-than comparison.
	Gt *PredicateEq
	// Lt is the {lt: {path, value}} form: less-than comparison.
	Lt *PredicateEq
	// Gte is the {gte: {path, value}} form: greater-than-or-equal.
	Gte *PredicateEq
	// Lte is the {lte: {path, value}} form: less-than-or-equal.
	Lte *PredicateEq
	// Present is the {present: <path>} form: true when the path
	// resolves to a non-nil value.
	Present *Path
	// And is the {and: [...]} form: conjunction over its sub-predicates.
	And []Predicate
	// Or is the {or: [...]} form: disjunction over its sub-predicates.
	Or []Predicate
	// Not is the {not: <predicate>} form: negation.
	Not *Predicate
	// LiteralBool is the {literal_bool: true|false} form: a constant
	// predicate value (useful in select branches).
	LiteralBool *bool
}

// PredicateEq is the {<verb>: {path: <Path>, value: <Value>}} shape, shared
// by eq / gt / lt / gte / lte. The Go type name keeps the "Eq" prefix for
// historical reasons (eq was the first verb to use this shape); the right-
// hand-side field is named Value (renamed from Equal in slice 6) so the
// shape reads naturally under the ordered verbs too.
type PredicateEq struct {
	// Path is the left-hand-side namespace-rooted locator.
	Path Path `yaml:"path" json:"path"`
	// Value is the right-hand-side Value compared against Path.
	Value Value `yaml:"value" json:"value"`
}

// predicateEqRaw is the on-wire shadow of PredicateEq, used by the codec to
// detect the legacy `equal:` key (renamed to `value:` in slice 6) and
// surface a precise migration hint at parse time.
type predicateEqRaw struct {
	Path  Path  `yaml:"path"  json:"path"`
	Value Value `yaml:"value" json:"value"`
}

const predicateEqEqualRenamedHint = "predicate eq.equal was renamed to eq.value in slice 6 (shared across eq / gt / lt / gte / lte); use value: instead"

// UnmarshalYAML implements yaml.Unmarshaler. The legacy `equal:` key (renamed
// to `value:` in slice 6) is rejected at parse time with a migration hint.
func (pe *PredicateEq) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("schema.PredicateEq at line %d: expected a mapping, got %v", node.Line, node.Kind)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Value == "equal" {
			return fmt.Errorf("schema.PredicateEq at line %d: %s", key.Line, predicateEqEqualRenamedHint)
		}
	}
	var raw predicateEqRaw
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("schema.PredicateEq at line %d: %w", node.Line, err)
	}
	pe.Path = raw.Path
	pe.Value = raw.Value
	return nil
}

// UnmarshalJSON implements json.Unmarshaler. The legacy `equal:` key is
// rejected with the same migration hint as the YAML form.
func (pe *PredicateEq) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("schema.PredicateEq: expected an object")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("schema.PredicateEq: %w", err)
	}
	if _, ok := probe["equal"]; ok {
		return fmt.Errorf("schema.PredicateEq: %s", predicateEqEqualRenamedHint)
	}
	var raw predicateEqRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("schema.PredicateEq: %w", err)
	}
	pe.Path = raw.Path
	pe.Value = raw.Value
	return nil
}

// predicateDiscriminatorKeys is the closed set of map keys that select a
// Predicate variant. A Predicate mapping must carry exactly one of these.
var predicateDiscriminatorKeys = []string{
	"eq",
	"gt",
	"lt",
	"gte",
	"lte",
	"present",
	"and",
	"or",
	"not",
	"literal_bool",
}

// predicateVariantAllowedKeys names every map key that may appear alongside a
// given Predicate discriminator. Any other sibling key is a parse error.
var predicateVariantAllowedKeys = map[string]map[string]struct{}{
	"eq":           {"eq": {}},
	"gt":           {"gt": {}},
	"lt":           {"lt": {}},
	"gte":          {"gte": {}},
	"lte":          {"lte": {}},
	"present":      {"present": {}},
	"and":          {"and": {}},
	"or":           {"or": {}},
	"not":          {"not": {}},
	"literal_bool": {"literal_bool": {}},
}

func checkPredicateSiblingKeys(disc string, keys map[string]struct{}) error {
	allowed := predicateVariantAllowedKeys[disc]
	for k := range keys {
		if _, ok := allowed[k]; ok {
			continue
		}
		return fmt.Errorf("unknown key %q in %s form; allowed: %s",
			k, disc, sortedAllowedKeys(allowed))
	}
	return nil
}

// predicateVariants returns the (name, payload) pairs for every Predicate
// variant that is currently set.
func predicateVariants(p Predicate) (names []string, payloads []any) {
	add := func(name string, payload any, set bool) {
		if set {
			names = append(names, name)
			payloads = append(payloads, payload)
		}
	}
	add("eq", p.Eq, p.Eq != nil)
	add("gt", p.Gt, p.Gt != nil)
	add("lt", p.Lt, p.Lt != nil)
	add("gte", p.Gte, p.Gte != nil)
	add("lte", p.Lte, p.Lte != nil)
	add("present", p.Present, p.Present != nil)
	add("and", p.And, len(p.And) > 0)
	add("or", p.Or, len(p.Or) > 0)
	add("not", p.Not, p.Not != nil)
	add("literal_bool", p.LiteralBool, p.LiteralBool != nil)
	return names, payloads
}

// Variant returns the active Predicate variant name and payload.
func (p Predicate) Variant() (string, any) {
	names, payloads := predicateVariants(p)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set Predicate form.
func (p Predicate) VariantNames() []string {
	names, _ := predicateVariants(p)
	return names
}

// pickPredicateDiscriminator returns the single Predicate discriminator key
// present in keys, or an error when zero or more than one are present.
func pickPredicateDiscriminator(keys map[string]struct{}) (string, error) {
	matches := make([]string, 0, 2)
	for _, k := range predicateDiscriminatorKeys {
		if _, ok := keys[k]; ok {
			matches = append(matches, k)
		}
	}
	switch len(matches) {
	case 0:
		if _, ok := keys["in"]; ok {
			return "", fmt.Errorf("predicate verb \"in\" is deferred; express as nested {or: [...]} of eq comparisons")
		}
		if _, ok := keys["matches"]; ok {
			return "", fmt.Errorf("predicate verb \"matches\" is deferred; no portable regex Value form yet")
		}
		return "", fmt.Errorf("no recognised discriminator key (want eq|gt|lt|gte|lte|present|and|or|not|literal_bool)")
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple discriminator keys present (%v); a Predicate mapping must carry exactly one", matches)
	}
}

// ---- YAML ----

// UnmarshalYAML implements yaml.Unmarshaler.
func (p *Predicate) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("schema.Predicate: expected a mapping at line %d, got %v", node.Line, node.Kind)
	}

	keys := mapKeys(node)
	disc, err := pickPredicateDiscriminator(keys)
	if err != nil {
		return fmt.Errorf("schema.Predicate at line %d: %w", node.Line, err)
	}
	if err := checkPredicateSiblingKeys(disc, keys); err != nil {
		return fmt.Errorf("schema.Predicate at line %d: %w", node.Line, err)
	}

	switch disc {
	case "eq", "gt", "lt", "gte", "lte":
		var raw map[string]PredicateEq
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Predicate.%s at line %d: %w", disc, node.Line, err)
		}
		pe := raw[disc]
		switch disc {
		case "eq":
			p.Eq = &pe
		case "gt":
			p.Gt = &pe
		case "lt":
			p.Lt = &pe
		case "gte":
			p.Gte = &pe
		case "lte":
			p.Lte = &pe
		}
		return nil

	case "present":
		var raw struct {
			Present Path `yaml:"present"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Predicate.present at line %d: %w", node.Line, err)
		}
		p.Present = &raw.Present
		return nil

	case "and":
		var raw struct {
			And []Predicate `yaml:"and"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Predicate.and at line %d: %w", node.Line, err)
		}
		if len(raw.And) == 0 {
			return fmt.Errorf("schema.Predicate.and at line %d: must contain at least one operand; for a constant predicate use {literal_bool: true}", node.Line)
		}
		p.And = raw.And
		return nil

	case "or":
		var raw struct {
			Or []Predicate `yaml:"or"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Predicate.or at line %d: %w", node.Line, err)
		}
		if len(raw.Or) == 0 {
			return fmt.Errorf("schema.Predicate.or at line %d: must contain at least one operand; for a constant predicate use {literal_bool: false}", node.Line)
		}
		p.Or = raw.Or
		return nil

	case "not":
		var raw struct {
			Not Predicate `yaml:"not"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Predicate.not at line %d: %w", node.Line, err)
		}
		p.Not = &raw.Not
		return nil

	case "literal_bool":
		var raw struct {
			LiteralBool bool `yaml:"literal_bool"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("schema.Predicate.literal_bool at line %d: %w", node.Line, err)
		}
		p.LiteralBool = &raw.LiteralBool
		return nil
	}

	return fmt.Errorf("schema.Predicate at line %d: unreachable discriminator %q", node.Line, disc)
}

// MarshalYAML emits the canonical YAML form.
func (p Predicate) MarshalYAML() (interface{}, error) {
	switch {
	case p.Eq != nil:
		return map[string]interface{}{"eq": p.Eq}, nil
	case p.Gt != nil:
		return map[string]interface{}{"gt": p.Gt}, nil
	case p.Lt != nil:
		return map[string]interface{}{"lt": p.Lt}, nil
	case p.Gte != nil:
		return map[string]interface{}{"gte": p.Gte}, nil
	case p.Lte != nil:
		return map[string]interface{}{"lte": p.Lte}, nil
	case p.Present != nil:
		return map[string]interface{}{"present": p.Present}, nil
	case len(p.And) > 0:
		return map[string]interface{}{"and": p.And}, nil
	case len(p.Or) > 0:
		return map[string]interface{}{"or": p.Or}, nil
	case p.Not != nil:
		return map[string]interface{}{"not": p.Not}, nil
	case p.LiteralBool != nil:
		return map[string]interface{}{"literal_bool": *p.LiteralBool}, nil
	}
	return nil, fmt.Errorf("schema.Predicate: zero value cannot be marshalled")
}

// ---- JSON ----

// UnmarshalJSON implements json.Unmarshaler.
func (p *Predicate) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("schema.Predicate: %w", err)
	}

	keys := make(map[string]struct{}, len(raw))
	for k := range raw {
		keys[k] = struct{}{}
	}
	disc, err := pickPredicateDiscriminator(keys)
	if err != nil {
		return fmt.Errorf("schema.Predicate: %w", err)
	}
	if err := checkPredicateSiblingKeys(disc, keys); err != nil {
		return fmt.Errorf("schema.Predicate: %w", err)
	}

	switch disc {
	case "eq", "gt", "lt", "gte", "lte":
		var pe PredicateEq
		if err := json.Unmarshal(raw[disc], &pe); err != nil {
			return fmt.Errorf("schema.Predicate.%s: %w", disc, err)
		}
		switch disc {
		case "eq":
			p.Eq = &pe
		case "gt":
			p.Gt = &pe
		case "lt":
			p.Lt = &pe
		case "gte":
			p.Gte = &pe
		case "lte":
			p.Lte = &pe
		}
		return nil

	case "present":
		var path Path
		if err := json.Unmarshal(raw["present"], &path); err != nil {
			return fmt.Errorf("schema.Predicate.present: %w", err)
		}
		p.Present = &path
		return nil

	case "and":
		var and []Predicate
		if err := json.Unmarshal(raw["and"], &and); err != nil {
			return fmt.Errorf("schema.Predicate.and: %w", err)
		}
		if len(and) == 0 {
			return fmt.Errorf("schema.Predicate.and: must contain at least one operand; for a constant predicate use {literal_bool: true}")
		}
		p.And = and
		return nil

	case "or":
		var or []Predicate
		if err := json.Unmarshal(raw["or"], &or); err != nil {
			return fmt.Errorf("schema.Predicate.or: %w", err)
		}
		if len(or) == 0 {
			return fmt.Errorf("schema.Predicate.or: must contain at least one operand; for a constant predicate use {literal_bool: false}")
		}
		p.Or = or
		return nil

	case "not":
		var not Predicate
		if err := json.Unmarshal(raw["not"], &not); err != nil {
			return fmt.Errorf("schema.Predicate.not: %w", err)
		}
		p.Not = &not
		return nil

	case "literal_bool":
		var b bool
		if err := json.Unmarshal(raw["literal_bool"], &b); err != nil {
			return fmt.Errorf("schema.Predicate.literal_bool: %w", err)
		}
		p.LiteralBool = &b
		return nil
	}

	return fmt.Errorf("schema.Predicate: unreachable discriminator %q", disc)
}

// MarshalJSON emits the canonical JSON form.
func (p Predicate) MarshalJSON() ([]byte, error) {
	switch {
	case p.Eq != nil:
		return json.Marshal(map[string]*PredicateEq{"eq": p.Eq})
	case p.Gt != nil:
		return json.Marshal(map[string]*PredicateEq{"gt": p.Gt})
	case p.Lt != nil:
		return json.Marshal(map[string]*PredicateEq{"lt": p.Lt})
	case p.Gte != nil:
		return json.Marshal(map[string]*PredicateEq{"gte": p.Gte})
	case p.Lte != nil:
		return json.Marshal(map[string]*PredicateEq{"lte": p.Lte})
	case p.Present != nil:
		type out struct {
			Present Path `json:"present"`
		}
		return json.Marshal(out{Present: *p.Present})
	case len(p.And) > 0:
		type out struct {
			And []Predicate `json:"and"`
		}
		return json.Marshal(out{And: p.And})
	case len(p.Or) > 0:
		type out struct {
			Or []Predicate `json:"or"`
		}
		return json.Marshal(out{Or: p.Or})
	case p.Not != nil:
		type out struct {
			Not *Predicate `json:"not"`
		}
		return json.Marshal(out{Not: p.Not})
	case p.LiteralBool != nil:
		type out struct {
			LiteralBool bool `json:"literal_bool"`
		}
		return json.Marshal(out{LiteralBool: *p.LiteralBool})
	}
	return nil, fmt.Errorf("schema.Predicate: zero value cannot be marshalled")
}

// IsZero reports whether p is the zero (unset) value.
func (p Predicate) IsZero() bool {
	return p.Eq == nil && p.Gt == nil && p.Lt == nil &&
		p.Gte == nil && p.Lte == nil && p.Present == nil &&
		len(p.And) == 0 && len(p.Or) == 0 &&
		p.Not == nil && p.LiteralBool == nil
}
