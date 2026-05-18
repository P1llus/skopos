// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"reflect"
	"time"

	"github.com/p1llus/skopos/schema"
)

// evalPredicate evaluates p against the current scope.
//
// Forms: eq, gt, lt, gte, lte, present, and, or, not, literal_bool.
func (s *scope) evalPredicate(p schema.Predicate) (bool, error) {
	switch {
	case p.LiteralBool != nil:
		return *p.LiteralBool, nil

	case p.Present != nil:
		got, ok, err := s.resolveNamespaceRef(*p.Present)
		if err != nil {
			return false, fmt.Errorf("present: %w", err)
		}
		return ok && !isZeroRuntime(got), nil

	case p.Eq != nil:
		return s.compare("eq", p.Eq)
	case p.Gt != nil:
		return s.compare("gt", p.Gt)
	case p.Lt != nil:
		return s.compare("lt", p.Lt)
	case p.Gte != nil:
		return s.compare("gte", p.Gte)
	case p.Lte != nil:
		return s.compare("lte", p.Lte)

	case p.And != nil:
		for i, sub := range p.And {
			ok, err := s.evalPredicate(sub)
			if err != nil {
				return false, fmt.Errorf("and[%d]: %w", i, err)
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil

	case p.Or != nil:
		for i, sub := range p.Or {
			ok, err := s.evalPredicate(sub)
			if err != nil {
				return false, fmt.Errorf("or[%d]: %w", i, err)
			}
			if ok {
				return true, nil
			}
		}
		return false, nil

	case p.Not != nil:
		ok, err := s.evalPredicate(*p.Not)
		if err != nil {
			return false, fmt.Errorf("not: %w", err)
		}
		return !ok, nil
	}

	return false, fmt.Errorf("schema.Predicate: no variant set")
}

// compare implements the ordered-comparison verbs (gt, lt, gte, lte) and
// equality (eq). The LHS is a namespace ref; the RHS is any Value.
//
// Every predicate path is namespace-rooted. response.body.<path> reads
// through scope.body via the same body-walk helper as response.body
// refs; state.<name>, events.*, extract.*, steps.<id>.*, cache.<name>,
// and the fan_out.as alias all resolve through resolveNamespaceRef.
// Absent paths surface as nil; comparisons against nil are false rather
// than erroring, matching the absent-tolerant predicate policy.
func (s *scope) compare(verb string, pe *schema.PredicateEq) (bool, error) {
	lhs, ok, err := s.resolveNamespaceRef(pe.Path)
	if err != nil {
		return false, fmt.Errorf("%s.path %s: %w", verb, pe.Path, err)
	}
	if !ok {
		lhs = nil
	}
	rhs, err := s.evalValue(pe.Value)
	if err != nil {
		return false, fmt.Errorf("%s.value: %w", verb, err)
	}

	if verb == "eq" {
		return equal(lhs, rhs), nil
	}
	// Ordered comparison.
	c, err := orderedCompare(lhs, rhs)
	if err != nil {
		return false, fmt.Errorf("%s: %w", verb, err)
	}
	switch verb {
	case "gt":
		return c > 0, nil
	case "lt":
		return c < 0, nil
	case "gte":
		return c >= 0, nil
	case "lte":
		return c <= 0, nil
	}
	return false, fmt.Errorf("unknown verb %q", verb)
}

// equal returns true when a and b are semantically equal under the IR's
// equality rule.
//
// The rule is JSON-loose: reflect.DeepEqual matches first, and otherwise
// both sides are coerced to their string representation and compared.
// This lets authors write {eq: {path: response.body.flag, value: "true"}}
// when the API returns either bool true or the string "true" without
// the template caring which shape the server picked.
//
// Strict edge cases (NOT covered by the loose rule):
//
//   - nil (missing path) is equal ONLY to nil. nil DOES NOT compare equal
//     to the empty string, the integer zero, or the boolean false. A
//     missing field is always semantically distinct from a present zero —
//     authors who want "missing OR zero" should use {or: [{not: present},
//     {eq: ..., value: 0}]}.
//   - reflect.DeepEqual handles same-type comparisons (string=="x",
//     int64==int64, []any deep-walk). The string fallback only fires when
//     types differ AND both sides round-trip through toString.
func equal(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		// A missing path is NOT equal to the empty string / zero int /
		// false bool. toString(nil) returns "" which would otherwise
		// silently equal {literal_string: ""}; that's a footgun for any
		// template guarding "is this field set?".
		return false
	}
	if reflect.DeepEqual(a, b) {
		return true
	}
	return toString(a) == toString(b)
}

// orderedCompare returns -1 / 0 / 1 for a < b, a == b, a > b. Numbers,
// durations (parsed from strings), and RFC 3339 timestamps are supported.
// Strings fall back to lexicographic comparison only when both sides are
// non-numeric strings.
func orderedCompare(a, b any) (int, error) {
	// Try time.
	if ta, ok := asTime(a); ok {
		tb, ok := asTime(b)
		if !ok {
			return 0, fmt.Errorf("cannot compare time with %T", b)
		}
		switch {
		case ta.Before(tb):
			return -1, nil
		case ta.After(tb):
			return 1, nil
		}
		return 0, nil
	}
	// Try number / duration.
	if na, ok := asInt64(a); ok {
		nb, ok := asInt64(b)
		if !ok {
			return 0, fmt.Errorf("cannot compare int with %T", b)
		}
		switch {
		case na < nb:
			return -1, nil
		case na > nb:
			return 1, nil
		}
		return 0, nil
	}
	if sa, ok := a.(string); ok {
		sb, ok := b.(string)
		if !ok {
			return 0, fmt.Errorf("cannot compare string with %T", b)
		}
		switch {
		case sa < sb:
			return -1, nil
		case sa > sb:
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("cannot order-compare %T", a)
}

// asTime attempts time coercion without erroring. Returns (zero, false) on
// failure — used by orderedCompare to pick a comparison kind.
func asTime(v any) (time.Time, bool) {
	if t, ok := v.(time.Time); ok {
		return t, true
	}
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// asInt64 attempts int64 coercion without erroring.
func asInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	}
	return 0, false
}
