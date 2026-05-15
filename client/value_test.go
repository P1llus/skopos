// SPDX-License-Identifier: Apache-2.0

package client

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/p1llus/skopos/schema"
)

// fixedNow returns a deterministic clock at 2026-05-12T12:00:00Z so tests
// asserting on {now} / lookback math are stable.
func fixedNow() func() time.Time {
	t, _ := time.Parse(time.RFC3339, "2026-05-12T12:00:00Z")
	return func() time.Time { return t }
}

// newTestScope builds a *scope wired with a minimal *schema.Doc, the supplied
// state/cursor seed values, and a fixed clock. Useful as the foundation of
// the value/predicate/extract tables.
func newTestScope(t *testing.T, state, cursor map[string]any) *scope {
	t.Helper()
	doc := &schema.Doc{IRVersion: "1"}
	s, err := newScope(doc, Snapshot{State: state, Cursor: cursor}, fixedNow())
	if err != nil {
		t.Fatalf("newScope: %v", err)
	}
	return s
}

// helpers for building schema.Value pointers tersely.
func vStr(s string) schema.Value { return schema.Value{LiteralString: &s} }
func vInt(i int64) schema.Value  { return schema.Value{LiteralInt: &i} }
func vBool(b bool) schema.Value  { return schema.Value{LiteralBool: &b} }
func vRef(p string) schema.Value { return schema.Value{Ref: &schema.RefValue{Path: mustPath(p)}} }
func vRefDefault(p string, d schema.Value) schema.Value {
	return schema.Value{Ref: &schema.RefValue{Path: mustPath(p), Default: &d}}
}
func vConcat(parts ...schema.Value) schema.Value { return schema.Value{Concat: parts} }
func vFromPag(name string) schema.Value          { return schema.Value{FromPagination: name} }
func vFromProg(name string) schema.Value         { return schema.Value{FromProgress: name} }
func vNow(offset *schema.Value) schema.Value {
	return schema.Value{Now: &schema.NowValue{Offset: offset}}
}
func vFormat(verb string, inner schema.Value) schema.Value {
	return schema.Value{Format: &schema.FormatValue{Verb: verb, Value: inner}}
}
func vList(items ...schema.Value) schema.Value       { return schema.Value{List: items} }
func vObject(m map[string]schema.Value) schema.Value { return schema.Value{Object: m} }
func vBase64(inner schema.Value) schema.Value        { return schema.Value{Base64: &inner} }

func mustPath(s string) schema.Path {
	p, err := schema.ParsePath(s)
	if err != nil {
		panic(err)
	}
	return p
}

// TestEvalValue table covers each discriminator in schema.Value plus the
// secret-relevant Ref defaults and the explicit raw-rejection contract.
func TestEvalValue(t *testing.T) {
	tests := []struct {
		name    string
		state   map[string]any
		cursor  map[string]any
		setup   func(s *scope)
		val     schema.Value
		want    any
		wantErr bool
	}{
		{name: "literal_string", val: vStr("hi"), want: "hi"},
		{name: "literal_int", val: vInt(42), want: int64(42)},
		{name: "literal_bool_true", val: vBool(true), want: true},
		{name: "literal_bool_false", val: vBool(false), want: false},
		{name: "is_zero_returns_nil", val: schema.Value{IsZero: true}, want: nil},

		{
			name:  "ref_state_hit",
			state: map[string]any{"api_key": "secret-1"},
			val:   vRef("state.api_key"),
			want:  "secret-1",
		},
		{
			name: "ref_missing_no_default_returns_nil",
			val:  vRef("state.absent"),
			want: nil,
		},
		{
			name: "ref_missing_uses_default",
			val:  vRefDefault("state.absent", vStr("fallback")),
			want: "fallback",
		},
		{
			name:  "ref_empty_string_takes_default",
			state: map[string]any{"x": ""},
			val:   vRefDefault("state.x", vStr("fallback")),
			want:  "fallback",
		},
		{
			name:   "ref_cursor_nested",
			cursor: map[string]any{"meta": map[string]any{"phase": "poll"}},
			val:    vRef("cursor.meta.phase"),
			want:   "poll",
		},

		{
			name: "now",
			val:  vNow(nil),
			want: mustTime("2026-05-12T12:00:00Z"),
		},
		{
			name: "now_with_offset_string",
			val:  vNow(ptrValue(vStr("-1h"))),
			want: mustTime("2026-05-12T11:00:00Z"),
		},

		{
			name:  "concat_strings",
			state: map[string]any{"a": "x", "b": "y"},
			val:   vConcat(vRef("state.a"), vStr("-"), vRef("state.b")),
			want:  "x-y",
		},
		{
			name:  "concat_skips_nil_values",
			state: map[string]any{"a": "x"},
			val:   vConcat(vStr("p="), vRef("state.a"), vRef("state.absent"), vStr("?")),
			want:  "p=x?",
		},

		{
			name:  "from_pagination_set",
			setup: func(s *scope) { s.fromPagination["token"] = "tok-1" },
			val:   vFromPag("token"),
			want:  "tok-1",
		},
		{
			name: "from_pagination_unset_returns_nil",
			val:  vFromPag("token"),
			want: nil,
		},
		{
			name:  "from_progress_set",
			setup: func(s *scope) { s.fromProgress["latest_timestamp"] = "2026-05-12T00:00:00Z" },
			val:   vFromProg("latest_timestamp"),
			want:  "2026-05-12T00:00:00Z",
		},

		{
			name: "format_string_int",
			val:  vFormat("string", vInt(7)),
			want: "7",
		},
		{
			name: "format_int_from_string",
			val:  vFormat("int", vStr("42")),
			want: int64(42),
		},
		{
			name: "format_rfc3339_from_unix",
			val:  vFormat("rfc3339", vInt(1700000000)),
			want: "2023-11-14T22:13:20Z",
		},
		{
			name: "format_url_encode",
			val:  vFormat("url_encode", vStr("a b&c=d")),
			want: "a+b%26c%3Dd",
		},
		{
			name:    "format_unknown_verb_errors",
			val:     vFormat("bogus", vStr("x")),
			wantErr: true,
		},

		{
			name: "format_bool_from_string",
			val:  vFormat("bool", vStr("true")),
			want: true,
		},
		{
			name: "format_unix_millis_from_rfc3339",
			val:  vFormat("unix_millis", vStr("2023-11-14T22:13:20Z")),
			want: int64(1700000000000),
		},
		{
			name: "format_duration_stringifies",
			// toDuration parses "1h30m" → 90m, then duration verb
			// returns the canonical Go duration string.
			val:  vFormat("duration", vStr("1h30m")),
			want: "1h30m0s",
		},
		{
			name: "format_parse_duration_returns_nanos",
			val:  vFormat("parse_duration", vStr("1h")),
			want: int64(time.Hour),
		},

		{
			name: "base64_encodes_string",
			val:  vBase64(vStr("user:pass")),
			want: "dXNlcjpwYXNz",
		},

		{
			name: "list_evaluates_each_element",
			val:  vList(vInt(1), vInt(2), vInt(3)),
			want: []any{int64(1), int64(2), int64(3)},
		},
		{
			name: "object_evaluates_each_value",
			val:  vObject(map[string]schema.Value{"a": vInt(1), "b": vStr("x")}),
			want: map[string]any{"a": int64(1), "b": "x"},
		},

		{
			name:    "empty_value_errors",
			val:     schema.Value{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestScope(t, tc.state, tc.cursor)
			if tc.setup != nil {
				tc.setup(s)
			}
			got, err := s.evalValue(tc.val)
			if (err != nil) != tc.wantErr {
				t.Fatalf("evalValue err: got %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("evalValue = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestEvalValueSelect covers the {select} discriminator separately because
// its predicate shape is awkward in the same table as the simpler verbs.
func TestEvalValueSelect(t *testing.T) {
	s := newTestScope(t, map[string]any{"mode": "bearer"}, nil)

	bearerBranch := schema.SelectBranch{
		When: schema.Predicate{Eq: &schema.PredicateEq{
			Path:  mustPath("state.mode"),
			Equal: vStr("bearer"),
		}},
		Value: vStr("bearer-path"),
	}
	apikeyBranch := schema.SelectBranch{
		When: schema.Predicate{Eq: &schema.PredicateEq{
			Path:  mustPath("state.mode"),
			Equal: vStr("api_key"),
		}},
		Value: vStr("apikey-path"),
	}
	sel := schema.Value{Select: &schema.SelectValue{
		Branches: []schema.SelectBranch{apikeyBranch, bearerBranch},
		Default:  vStr("default-path"),
	}}

	got, err := s.evalValue(sel)
	if err != nil {
		t.Fatalf("evalValue: %v", err)
	}
	if got != "bearer-path" {
		t.Errorf("select picked %v, want bearer-path", got)
	}

	// Switch state.mode → default branch wins.
	s.state["mode"] = "other"
	got, err = s.evalValue(sel)
	if err != nil {
		t.Fatalf("evalValue: %v", err)
	}
	if got != "default-path" {
		t.Errorf("select picked %v, want default-path", got)
	}
}

// TestToString pins the stringification used by Concat and equal().
func TestToString(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"x", "x"},
		{true, "true"},
		{false, "false"},
		{int(7), "7"},
		{int64(7), "7"},
		{float64(1.5), "1.5"},
		{mustTime("2026-05-12T00:00:00Z"), "2026-05-12T00:00:00Z"},
		{time.Hour, "1h0m0s"},
	}
	for _, tc := range tests {
		if got := toString(tc.in); got != tc.want {
			t.Errorf("toString(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func ptrValue(v schema.Value) *schema.Value { return &v }

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestSliceOneResponseAndStepHeaderRefs covers runtime resolution of the
// namespace-rooted body/header roots: response.body.<path>,
// response.header.<name>, and steps.<id>.header.<name>.
//
// Slice 2 retired the legacy body.<path> root, so the interchangeability
// subtest from slice 1 was reframed to assert the response.body.<path>
// resolver returns the expected scalar/list/missing values directly.
func TestSliceOneResponseAndStepHeaderRefs(t *testing.T) {
	t.Run("response_body_resolves_expected_values", func(t *testing.T) {
		s := newTestScope(t, nil, nil)
		body := map[string]any{
			"status": "complete",
			"meta":   map[string]any{"page": int64(7)},
			"items":  []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}},
		}
		s.body = body

		cases := []struct {
			label    string
			response string
			want     any
			wantOK   bool
		}{
			{"top-level scalar", "response.body.status", "complete", true},
			{"nested object", "response.body.meta.page", int64(7), true},
			{"list index", "response.body.items.1.id", "b", true},
			{"missing leaf", "response.body.meta.missing", nil, false},
		}
		for _, c := range cases {
			t.Run(c.label, func(t *testing.T) {
				got, ok, err := s.resolveNamespaceRef(mustPath(c.response))
				if err != nil {
					t.Fatalf("resolve(%s): %v", c.response, err)
				}
				if ok != c.wantOK {
					t.Fatalf("ok = %v, want %v", ok, c.wantOK)
				}
				if !reflect.DeepEqual(got, c.want) {
					t.Errorf("value = %#v, want %#v", got, c.want)
				}
			})
		}
	})

	t.Run("response_header_case_insensitive", func(t *testing.T) {
		s := newTestScope(t, nil, nil)
		s.responseHeaders = http.Header{}
		s.responseHeaders.Set("ETag", "abc-1")
		s.responseHeaders.Set("X-Total-Count", "42")

		cases := []struct {
			ref  string
			want any
		}{
			{"response.header.ETag", "abc-1"},
			{"response.header.etag", "abc-1"},
			{"response.header.X-Total-Count", "42"},
			{"response.header.x-total-count", "42"},
		}
		for _, c := range cases {
			got, ok, err := s.resolveNamespaceRef(mustPath(c.ref))
			if err != nil {
				t.Fatalf("resolve(%s): %v", c.ref, err)
			}
			if !ok || got != c.want {
				t.Errorf("resolve(%s) = (%v, %v); want (%v, true)", c.ref, got, ok, c.want)
			}
		}

		// Absent header resolves as nil/!ok (the {default: ...} path on Ref
		// handles first-iteration absence at the Value layer).
		got, ok, err := s.resolveNamespaceRef(mustPath("response.header.X-Missing"))
		if err != nil {
			t.Fatalf("absent resolve: %v", err)
		}
		if ok || got != nil {
			t.Errorf("absent header should resolve as (nil,false); got (%v,%v)", got, ok)
		}
	})

	t.Run("steps_id_header_resolves_from_stepHeaders", func(t *testing.T) {
		s := newTestScope(t, nil, nil)
		h := http.Header{}
		h.Set("Set-Cookie", "session=xyz")
		h.Set("ETag", "v1")
		s.stepHeaders["login"] = h

		got, ok, err := s.resolveNamespaceRef(mustPath("steps.login.header.Set-Cookie"))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !ok || got != "session=xyz" {
			t.Errorf("steps.login.header.Set-Cookie = (%v, %v); want (\"session=xyz\", true)", got, ok)
		}

		// Case-insensitive match.
		got, ok, err = s.resolveNamespaceRef(mustPath("steps.login.header.etag"))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !ok || got != "v1" {
			t.Errorf("steps.login.header.etag = (%v, %v); want (\"v1\", true)", got, ok)
		}

		// Unknown step id resolves as (nil, false).
		got, ok, err = s.resolveNamespaceRef(mustPath("steps.unknown.header.ETag"))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if ok || got != nil {
			t.Errorf("unknown step header should resolve as (nil,false); got (%v,%v)", got, ok)
		}
	})

	t.Run("response_body_unset_resolves_as_nil", func(t *testing.T) {
		s := newTestScope(t, nil, nil)
		// Outside complete_when, s.body is nil — every response.body ref
		// resolves as (nil, false). The validator already rejects such use
		// at parse time; the runtime guard is the second line of defence.
		got, ok, err := s.resolveNamespaceRef(mustPath("response.body.anything"))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if ok || got != nil {
			t.Errorf("response.body with no body context should resolve as (nil,false); got (%v,%v)", got, ok)
		}
	})
}
