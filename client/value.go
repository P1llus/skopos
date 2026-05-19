// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/p1llus/skopos/schema"
)

// evalValue evaluates v against the current scope and returns the runtime
// representation:
//
//	string | int64 | bool | time.Time | time.Duration | []any | map[string]any | nil
//
// The discriminator set mirrors schema.Value:
//
//	literal_string, literal_int, literal_bool, ref, now, concat, select,
//	format, base64, list, object, add, subtract, max, min, first, last,
//	count, regex
//
// Arithmetic and reducers obey the type pairs documented in
// docs/schema.md §Values:
//
//   - {add: [time, duration]} → time.Time
//   - {add: [duration, duration]} → time.Duration
//   - {add: [int, int]} → int64
//   - {subtract: [time, duration]} → time.Time
//   - {subtract: [time, time]} → time.Duration
//   - {subtract: [duration, duration]} → time.Duration
//   - {subtract: [int, int]} → int64
//
// Reducer inputs are list-shaped Values — either a {list: [...]} literal
// or a list-shaped projection such as {ref: events.*.timestamp}. max, min,
// first, and last skip nil entries; count counts every position (matching
// events.count). Empty / all-nil inputs surface as nil for the comparator
// reducers and 0 for count.
//
// Refs resolve through (*scope).resolveNamespaceRef; the closed root set
// (state | cache | events | extract | steps | response) plus any
// author-chosen fan_out.as alias active on the scope are the only legal
// path roots.
func (s *scope) evalValue(v schema.Value) (any, error) {
	switch {
	case v.IsZero:
		return nil, nil
	case v.LiteralString != nil:
		return *v.LiteralString, nil
	case v.LiteralInt != nil:
		return *v.LiteralInt, nil
	case v.LiteralBool != nil:
		return *v.LiteralBool, nil

	case v.Ref != nil:
		got, ok, err := s.resolveNamespaceRef(v.Ref.Path)
		if err != nil {
			return nil, fmt.Errorf("ref %s: %w", v.Ref.Path, err)
		}
		if !ok || isZeroRuntime(got) {
			if v.Ref.Default != nil {
				return s.evalValue(*v.Ref.Default)
			}
			return nil, nil
		}
		return got, nil

	case v.Now != nil:
		return s.now(), nil

	case v.Concat != nil:
		var b strings.Builder
		for i, elem := range v.Concat {
			got, err := s.evalValue(elem)
			if err != nil {
				return nil, fmt.Errorf("concat[%d]: %w", i, err)
			}
			if got == nil {
				continue
			}
			b.WriteString(toString(got))
		}
		return b.String(), nil

	case v.Select != nil:
		for i, br := range v.Select.Branches {
			ok, err := s.evalPredicate(br.When)
			if err != nil {
				return nil, fmt.Errorf("select.branches[%d].when: %w", i, err)
			}
			if ok {
				return s.evalValue(br.Value)
			}
		}
		return s.evalValue(v.Select.Default)

	case v.Format != nil:
		inner, err := s.evalValue(v.Format.Value)
		if err != nil {
			return nil, fmt.Errorf("format.value: %w", err)
		}
		return applyFormat(v.Format.Verb, inner)

	case v.Base64 != nil:
		inner, err := s.evalValue(*v.Base64)
		if err != nil {
			return nil, fmt.Errorf("base64: %w", err)
		}
		return base64.StdEncoding.EncodeToString([]byte(toString(inner))), nil

	case v.List != nil:
		out := make([]any, 0, len(v.List))
		for i, elem := range v.List {
			got, err := s.evalValue(elem)
			if err != nil {
				return nil, fmt.Errorf("list[%d]: %w", i, err)
			}
			out = append(out, got)
		}
		return out, nil

	case v.Object != nil:
		out := make(map[string]any, len(v.Object))
		for k, elem := range v.Object {
			got, err := s.evalValue(elem)
			if err != nil {
				return nil, fmt.Errorf("object.%s: %w", k, err)
			}
			out[k] = got
		}
		return out, nil

	case v.Add != nil:
		return s.evalArith("add", v.Add.Operands)
	case v.Subtract != nil:
		return s.evalArith("subtract", v.Subtract.Operands)

	case v.Max != nil:
		list, err := s.evalReducerInput("max", *v.Max)
		if err != nil {
			return nil, err
		}
		return reduceExtreme(list, true), nil
	case v.Min != nil:
		list, err := s.evalReducerInput("min", *v.Min)
		if err != nil {
			return nil, err
		}
		return reduceExtreme(list, false), nil
	case v.First != nil:
		list, err := s.evalReducerInput("first", *v.First)
		if err != nil {
			return nil, err
		}
		return firstNonNil(list), nil
	case v.Last != nil:
		list, err := s.evalReducerInput("last", *v.Last)
		if err != nil {
			return nil, err
		}
		return lastNonNil(list), nil
	case v.Count != nil:
		list, err := s.evalReducerInput("count", *v.Count)
		if err != nil {
			return nil, err
		}
		return int64(len(list)), nil

	case v.Regex != nil:
		from, err := s.evalValue(v.Regex.From)
		if err != nil {
			return nil, fmt.Errorf("regex.from: %w", err)
		}
		var def *schema.Value
		if v.Regex.Default != nil {
			def = v.Regex.Default
		}
		return s.applyRegex(v.Regex.Pattern, toString(from), v.Regex.Capture, def)
	}

	return nil, fmt.Errorf("schema.Value: no variant set (zero value); use IsZero for explicit absence or {literal_string: \"\"} for empty string")
}

// now returns the scope's idea of the current time.
func (s *scope) now() time.Time {
	if s.nowFn == nil {
		return time.Now()
	}
	return s.nowFn()
}

// evalArith resolves both operands of {add: ...} or {subtract: ...} and
// dispatches by their runtime type pair. Operand order is significant.
// Either operand resolving to nil short-circuits to nil so absent inputs
// stay absent rather than triggering a coercion error.
func (s *scope) evalArith(verb string, operands []schema.Value) (any, error) {
	if len(operands) != 2 {
		return nil, fmt.Errorf("%s: must have exactly 2 operands, got %d", verb, len(operands))
	}
	a, err := s.evalValue(operands[0])
	if err != nil {
		return nil, fmt.Errorf("%s.operands[0]: %w", verb, err)
	}
	b, err := s.evalValue(operands[1])
	if err != nil {
		return nil, fmt.Errorf("%s.operands[1]: %w", verb, err)
	}
	if a == nil || b == nil {
		return nil, nil
	}
	switch verb {
	case "add":
		return arithAdd(a, b)
	case "subtract":
		return arithSubtract(a, b)
	}
	return nil, fmt.Errorf("unknown arithmetic verb %q", verb)
}

// arithAdd implements add per the documented type pairs:
//
//	time + duration → time   (commutative)
//	duration + duration → duration
//	int + int → int64
//
// Strings are coerced via time.ParseDuration when an arithmetic context
// demands it (e.g. the canonical {add: [{now: true}, "1h"]} form).
func arithAdd(a, b any) (any, error) {
	if ta, ok := asTime(a); ok {
		d, err := toDuration(b)
		if err != nil {
			return nil, fmt.Errorf("add: time + non-duration %T: %w", b, err)
		}
		return ta.Add(d), nil
	}
	if tb, ok := asTime(b); ok {
		d, err := toDuration(a)
		if err != nil {
			return nil, fmt.Errorf("add: non-duration %T + time: %w", a, err)
		}
		return tb.Add(d), nil
	}
	if ia, ok := asInt64(a); ok {
		if ib, ok := asInt64(b); ok {
			return ia + ib, nil
		}
	}
	da, errA := toDuration(a)
	db, errB := toDuration(b)
	if errA == nil && errB == nil {
		return da + db, nil
	}
	return nil, fmt.Errorf("add: cannot interpret %T + %T as time/duration/int", a, b)
}

// arithSubtract implements subtract per the documented type pairs:
//
//	time - duration → time
//	time - time → duration
//	duration - duration → duration
//	int - int → int64
//
// time - time falls under subtraction only — there is no add pair that
// produces a duration from two times.
func arithSubtract(a, b any) (any, error) {
	ta, isTimeA := asTime(a)
	tb, isTimeB := asTime(b)
	if isTimeA && isTimeB {
		return ta.Sub(tb), nil
	}
	if isTimeA {
		d, err := toDuration(b)
		if err != nil {
			return nil, fmt.Errorf("subtract: time - non-duration %T: %w", b, err)
		}
		return ta.Add(-d), nil
	}
	if isTimeB {
		return nil, fmt.Errorf("subtract: cannot subtract time from %T", a)
	}
	if ia, ok := asInt64(a); ok {
		if ib, ok := asInt64(b); ok {
			return ia - ib, nil
		}
	}
	da, errA := toDuration(a)
	db, errB := toDuration(b)
	if errA == nil && errB == nil {
		return da - db, nil
	}
	return nil, fmt.Errorf("subtract: cannot interpret %T - %T as time/duration/int", a, b)
}

// evalReducerInput evaluates a reducer's operand to a list. The codec
// normalises {max: [...]} to {max: {list: [...]}}; {max: {ref: events.*.x}}
// resolves through the events-projection arm in resolveNamespaceRef. An
// absent operand (nil) is treated as an empty list so empty pages don't
// crash the reducer.
func (s *scope) evalReducerInput(verb string, operand schema.Value) ([]any, error) {
	got, err := s.evalValue(operand)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", verb, err)
	}
	if got == nil {
		return nil, nil
	}
	list, ok := got.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: operand resolved to %T, want list", verb, got)
	}
	return list, nil
}

// reduceExtreme returns the largest (greatest=true) or smallest element
// in list, skipping nil entries. Returns nil when the list is empty or
// contains only nils. Uses orderedCompare so timestamps, durations, and
// numbers all participate in the same ordering.
func reduceExtreme(list []any, greatest bool) any {
	var pick any
	for _, el := range list {
		if el == nil {
			continue
		}
		if pick == nil {
			pick = el
			continue
		}
		c, err := orderedCompare(el, pick)
		if err != nil {
			// Heterogeneous elements skip past — the reducer is
			// best-effort over the comparable subset.
			continue
		}
		if greatest && c > 0 {
			pick = el
		}
		if !greatest && c < 0 {
			pick = el
		}
	}
	return pick
}

// firstNonNil returns the first non-nil element of list, or nil when
// every entry is nil / the list is empty.
func firstNonNil(list []any) any {
	for _, el := range list {
		if el != nil {
			return el
		}
	}
	return nil
}

// lastNonNil returns the last non-nil element of list, or nil when every
// entry is nil / the list is empty.
func lastNonNil(list []any) any {
	for _, l := range slices.Backward(list) {
		if l != nil {
			return l
		}
	}
	return nil
}

// applyRegex compiles pattern and applies it to in. capture selects the
// returned group: 0 (or unset) returns the full match; n returns the
// n-th submatch, 1-based. When no match, def (if set) is evaluated;
// absent both match and def, the result is nil.
func (s *scope) applyRegex(pattern, in string, capture int, def *schema.Value) (any, error) {
	if capture < 0 {
		return nil, fmt.Errorf("regex: capture must be >= 0, got %d", capture)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("regex: compile %q: %w", pattern, err)
	}
	m := re.FindStringSubmatch(in)
	if m == nil {
		if def != nil {
			return s.evalValue(*def)
		}
		return nil, nil
	}
	if capture >= len(m) {
		return nil, fmt.Errorf("regex: capture group %d out of range (pattern has %d group(s))", capture, len(m)-1)
	}
	return m[capture], nil
}

// isZeroRuntime reports whether v is the runtime equivalent of a "zero
// Value" — used by Ref to decide whether to fall back to {default: ...}.
// nil, empty string, empty list, empty map all count as zero. Numbers and
// bools do NOT — `0` and `false` are valid resolved values.
func isZeroRuntime(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// toString stringifies a runtime value for use in Concat / Format(string).
// Mirrors the format verb 'string': numbers/bools fmt.Sprint, times to
// RFC 3339, nil to empty, everything else fmt.Sprint.
func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case time.Duration:
		return x.String()
	}
	return fmt.Sprint(v)
}

// toDuration coerces a value into a time.Duration. Strings are parsed with
// time.ParseDuration; integers are treated as nanoseconds.
func toDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case time.Duration:
		return x, nil
	case string:
		return time.ParseDuration(x)
	case int64:
		return time.Duration(x), nil
	case int:
		return time.Duration(x), nil
	case float64:
		return time.Duration(int64(x)), nil
	}
	return 0, fmt.Errorf("cannot interpret %T as duration", v)
}

// toInt coerces a value into an int64.
func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case string:
		return strconv.ParseInt(x, 10, 64)
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("cannot interpret %T as int", v)
}

// toBool coerces a value into a bool. Strings "true"/"false" parse; other
// types coerce by zero-ness.
func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		return strconv.ParseBool(x)
	case int:
		return x != 0, nil
	case int64:
		return x != 0, nil
	case float64:
		return x != 0, nil
	}
	return false, fmt.Errorf("cannot interpret %T as bool", v)
}

// toTime coerces a value into a time.Time. Strings are parsed as RFC 3339
// (with or without nanos); int/int64/float64 are treated as unix seconds.
func toTime(v any) (time.Time, error) {
	switch x := v.(type) {
	case time.Time:
		return x, nil
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t, nil
		}
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t, nil
		}
		// Report byte length only: the input may be a secret-typed state
		// field fed through format:rfc3339.
		return time.Time{}, fmt.Errorf("cannot parse %d-byte string as RFC 3339", len(x))
	case int64:
		return time.Unix(x, 0).UTC(), nil
	case int:
		return time.Unix(int64(x), 0).UTC(), nil
	case float64:
		sec, nsec := int64(x), int64((x-float64(int64(x)))*1e9)
		return time.Unix(sec, nsec).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cannot interpret %T as time", v)
}

// applyFormat implements the closed format-verb set.
func applyFormat(verb string, v any) (any, error) {
	switch verb {
	case "string":
		return toString(v), nil
	case "int":
		return toInt(v)
	case "bool":
		return toBool(v)
	case "rfc3339":
		t, err := toTime(v)
		if err != nil {
			return nil, err
		}
		return t.UTC().Format(time.RFC3339), nil
	case "rfc3339nano":
		t, err := toTime(v)
		if err != nil {
			return nil, err
		}
		return t.UTC().Format(time.RFC3339Nano), nil
	case "unix_seconds":
		t, err := toTime(v)
		if err != nil {
			return nil, err
		}
		return t.Unix(), nil
	case "unix_millis":
		t, err := toTime(v)
		if err != nil {
			return nil, err
		}
		return t.UnixMilli(), nil
	case "duration":
		d, err := toDuration(v)
		if err != nil {
			return nil, err
		}
		return d.String(), nil
	case "url_encode":
		return url.QueryEscape(toString(v)), nil
	case "parse_duration":
		d, err := toDuration(v)
		if err != nil {
			return nil, err
		}
		return d.Nanoseconds(), nil
	}
	return nil, fmt.Errorf("unknown format verb %q", verb)
}
