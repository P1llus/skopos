package client

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/p1llus/skopos/schema"
)

// evalValue evaluates v against the current scope and returns the runtime
// representation: string | int64 | bool | []any | map[string]any | time.Time | nil.
//
// The discriminator set mirrors schema.Value:
//
//	literal_string, literal_int, literal_bool, ref, now, concat, select,
//	from_pagination, from_progress, format, base64, list, object
//
// from_pagination and from_progress role names are strategy-specific and
// catalogued in paginationPlan / progressPlan godoc (pagination.go,
// progress.go). An unknown role resolves to nil — the runtime does not
// validate role names against the active plan, since that's the IR
// validator's job before Drain ever runs.
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
		t := s.now()
		if v.Now.Offset != nil {
			off, err := s.evalValue(*v.Now.Offset)
			if err != nil {
				return nil, fmt.Errorf("now.offset: %w", err)
			}
			d, err := toDuration(off)
			if err != nil {
				return nil, fmt.Errorf("now.offset: %w", err)
			}
			t = t.Add(d)
		}
		return t, nil

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

	case v.FromPagination != "":
		got, ok := s.fromPagination[v.FromPagination]
		if !ok {
			return nil, nil
		}
		return got, nil

	case v.FromProgress != "":
		got, ok := s.fromProgress[v.FromProgress]
		if !ok {
			return nil, nil
		}
		return got, nil

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
