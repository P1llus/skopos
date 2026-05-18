// SPDX-License-Identifier: Apache-2.0

package client

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/p1llus/skopos/schema"
)

// newTestScope builds a *scope wired with a minimal *schema.Doc, the
// supplied state seed values, and a fixed clock.
func newTestScope(t *testing.T, state map[string]any) *scope {
	t.Helper()
	doc := &schema.Doc{IRVersion: "1"}
	s, err := newScope(doc, Snapshot{State: state}, fixedNow())
	if err != nil {
		t.Fatalf("newScope: %v", err)
	}
	return s
}

func vConcat(parts ...schema.Value) schema.Value { return schema.Value{Concat: parts} }
func vFormat(verb string, inner schema.Value) schema.Value {
	return schema.Value{Format: &schema.FormatValue{Verb: verb, Value: inner}}
}
func vList(items ...schema.Value) schema.Value       { return schema.Value{List: items} }
func vObject(m map[string]schema.Value) schema.Value { return schema.Value{Object: m} }
func vBase64(inner schema.Value) schema.Value        { return schema.Value{Base64: &inner} }

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestEvalValue covers each Value discriminator and the secret-relevant
// Ref defaults.
func TestEvalValue(t *testing.T) {
	tests := []struct {
		name    string
		state   map[string]any
		cache   map[string]any
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
			name:  "ref_cache_slot",
			cache: map[string]any{"access_token": "tok-1"},
			val:   vRef("cache.access_token"),
			want:  "tok-1",
		},

		{
			name: "now",
			val:  vNow(),
			want: mustTime("2026-01-01T00:00:00Z"),
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
			s := newTestScope(t, tc.state)
			for k, v := range tc.cache {
				s.cache[k] = v
			}
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

// TestEvalValueSelect pins the {select} branch evaluation: the first
// branch whose predicate evaluates true wins; otherwise the default.
func TestEvalValueSelect(t *testing.T) {
	s := newTestScope(t, map[string]any{"mode": "bearer"})

	bearerBranch := schema.SelectBranch{
		When: schema.Predicate{Eq: &schema.PredicateEq{
			Path:  mustPath("state.mode"),
			Value: vStr("bearer"),
		}},
		Value: vStr("bearer-path"),
	}
	apikeyBranch := schema.SelectBranch{
		When: schema.Predicate{Eq: &schema.PredicateEq{
			Path:  mustPath("state.mode"),
			Value: vStr("api_key"),
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

	s.state["mode"] = "other"
	got, err = s.evalValue(sel)
	if err != nil {
		t.Fatalf("evalValue: %v", err)
	}
	if got != "default-path" {
		t.Errorf("select picked %v, want default-path", got)
	}
}

// TestEvalValueArithmetic covers the {add, subtract} forms over each
// supported type pair.
func TestEvalValueArithmetic(t *testing.T) {
	s := newTestScope(t, nil)

	cases := []struct {
		name string
		v    schema.Value
		want any
	}{
		{
			name: "add_time_duration",
			v: schema.Value{Add: &schema.ArithExpr{Operands: []schema.Value{
				vNow(), vStr("1h"),
			}}},
			want: mustTime("2026-01-01T01:00:00Z"),
		},
		{
			name: "subtract_time_duration",
			v: schema.Value{Subtract: &schema.ArithExpr{Operands: []schema.Value{
				vNow(), vStr("24h"),
			}}},
			want: mustTime("2025-12-31T00:00:00Z"),
		},
		{
			name: "add_int_int",
			v: schema.Value{Add: &schema.ArithExpr{Operands: []schema.Value{
				vInt(5), vInt(7),
			}}},
			want: int64(12),
		},
		{
			name: "subtract_duration_duration",
			v: schema.Value{Subtract: &schema.ArithExpr{Operands: []schema.Value{
				vStr("2h"), vStr("30m"),
			}}},
			want: 90 * time.Minute,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.evalValue(tc.v)
			if err != nil {
				t.Fatalf("evalValue: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestEvalValueReducers covers the {max, min, first, last, count} forms
// over both literal {list: [...]} operands and list-shaped Ref
// projections (e.g. {ref: events.*.timestamp}).
func TestEvalValueReducers(t *testing.T) {
	s := newTestScope(t, nil)
	s.events = []any{
		map[string]any{"ts": "2026-01-01T00:00:01Z"},
		map[string]any{"ts": "2026-01-01T00:00:03Z"},
		map[string]any{"ts": "2026-01-01T00:00:02Z"},
	}

	cases := []struct {
		name string
		v    schema.Value
		want any
	}{
		{
			name: "max_literal_list",
			v:    schema.Value{Max: ptrValue(vList(vInt(3), vInt(7), vInt(5)))},
			want: int64(7),
		},
		{
			name: "min_literal_list",
			v:    schema.Value{Min: ptrValue(vList(vInt(3), vInt(7), vInt(5)))},
			want: int64(3),
		},
		{
			name: "count_literal_list",
			v:    schema.Value{Count: ptrValue(vList(vInt(3), vInt(7), vInt(5)))},
			want: int64(3),
		},
		{
			name: "first_literal_list",
			v:    schema.Value{First: ptrValue(vList(vInt(3), vInt(7), vInt(5)))},
			want: int64(3),
		},
		{
			name: "last_literal_list",
			v:    schema.Value{Last: ptrValue(vList(vInt(3), vInt(7), vInt(5)))},
			want: int64(5),
		},
		{
			name: "max_over_events_projection",
			v:    schema.Value{Max: ptrValue(vRef("events.*.ts"))},
			want: "2026-01-01T00:00:03Z",
		},
		{
			name: "count_over_events_projection",
			v:    schema.Value{Count: ptrValue(vRef("events.*.ts"))},
			want: int64(3),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.evalValue(tc.v)
			if err != nil {
				t.Fatalf("evalValue: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestToString pins the stringification used by Concat and equality
// helpers.
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

// TestResolveNamespaceRef walks the resolver across each closed root and
// the fan_out alias path. The closed-root set is the post-redesign one:
// state / cache / events / extract / steps / response (no body, no
// cursor, no item).
func TestResolveNamespaceRef(t *testing.T) {
	t.Run("response_body_resolves_paths", func(t *testing.T) {
		s := newTestScope(t, nil)
		s.body = map[string]any{
			"status": "complete",
			"meta":   map[string]any{"page": int64(7)},
			"items":  []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}},
		}

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
		s := newTestScope(t, nil)
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

		got, ok, err := s.resolveNamespaceRef(mustPath("response.header.X-Missing"))
		if err != nil {
			t.Fatalf("absent resolve: %v", err)
		}
		if ok || got != nil {
			t.Errorf("absent header should resolve as (nil,false); got (%v,%v)", got, ok)
		}
	})

	t.Run("steps_id_body_and_header", func(t *testing.T) {
		s := newTestScope(t, nil)
		s.steps["login"] = map[string]any{"session_id": "xyz"}
		h := http.Header{}
		h.Set("Set-Cookie", "session=xyz")
		s.stepHeaders["login"] = h

		got, ok, err := s.resolveNamespaceRef(mustPath("steps.login.body.session_id"))
		if err != nil || !ok || got != "xyz" {
			t.Errorf("steps.login.body.session_id = (%v, %v, %v); want (\"xyz\", true, nil)", got, ok, err)
		}

		got, ok, err = s.resolveNamespaceRef(mustPath("steps.login.header.Set-Cookie"))
		if err != nil || !ok || got != "session=xyz" {
			t.Errorf("steps.login.header.Set-Cookie = (%v, %v, %v)", got, ok, err)
		}

		got, ok, err = s.resolveNamespaceRef(mustPath("steps.unknown.header.X"))
		if err != nil {
			t.Fatalf("unknown step: %v", err)
		}
		if ok || got != nil {
			t.Errorf("unknown step should resolve as (nil,false); got (%v,%v)", got, ok)
		}
	})

	t.Run("events_shortcuts", func(t *testing.T) {
		s := newTestScope(t, nil)
		s.events = []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		}

		cases := []struct {
			ref  string
			want any
		}{
			{"events.count", int64(3)},
			{"events.first.id", "a"},
			{"events.last.id", "c"},
			{"events.1.id", "b"},
		}
		for _, c := range cases {
			got, ok, err := s.resolveNamespaceRef(mustPath(c.ref))
			if err != nil || !ok {
				t.Errorf("resolve(%s): ok=%v err=%v", c.ref, ok, err)
				continue
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("resolve(%s) = %#v, want %#v", c.ref, got, c.want)
			}
		}

		// events.* projection.
		got, _, err := s.resolveNamespaceRef(mustPath("events.*.id"))
		if err != nil {
			t.Fatalf("projection: %v", err)
		}
		want := []any{"a", "b", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("events.*.id = %#v, want %#v", got, want)
		}
	})

	t.Run("events_count_returns_zero_when_unbound", func(t *testing.T) {
		s := newTestScope(t, nil)
		got, ok, err := s.resolveNamespaceRef(mustPath("events.count"))
		if err != nil || !ok || got != int64(0) {
			t.Errorf("events.count unbound = (%v,%v,%v); want (0, true, nil)", got, ok, err)
		}
	})

	t.Run("fanout_alias_resolves_against_item", func(t *testing.T) {
		s := newTestScope(t, nil)
		s.itemBinding = "incident"
		s.item = map[string]any{"id": "INC-42"}

		got, ok, err := s.resolveNamespaceRef(mustPath("incident.id"))
		if err != nil || !ok || got != "INC-42" {
			t.Errorf("incident.id = (%v,%v,%v)", got, ok, err)
		}
	})

	t.Run("unknown_root_errors", func(t *testing.T) {
		s := newTestScope(t, nil)
		_, _, err := s.resolveNamespaceRef(schema.Path{Parts: []string{"nope", "x"}})
		if err == nil {
			t.Error("expected error for unknown namespace root")
		}
	})
}
