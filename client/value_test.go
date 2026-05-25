// SPDX-License-Identifier: Apache-2.0

package client

import (
	"maps"
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

func vFormat(verb string, inner schema.Value) schema.Value {
	return schema.Value{Format: &schema.FormatValue{Verb: verb, Value: inner}}
}
func vList(items ...schema.Value) schema.Value { return schema.Value{List: items} }
func vBase64(inner schema.Value) schema.Value  { return schema.Value{Base64: &inner} }

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestEvalValue covers the Value discriminators and the secret-relevant
// Ref defaults that goldens cannot easily reach (error paths, empty
// behaviour, default precedence). Discriminator forms exercised by
// integration goldens via interpolation, body.json, headers, etc. are
// not re-tested here.
func TestEvalValue(t *testing.T) {
	tests := []struct {
		name    string
		state   map[string]any
		cache   map[string]any
		val     schema.Value
		want    any
		wantErr bool
	}{
		{name: "is_zero_returns_nil", val: schema.Value{IsZero: true}, want: nil},
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
			// "${state.absent|fallback}" desugars to a Ref with a default.
			// When state.absent is unset at runtime, evalValue must return
			// the parsed default string, not nil.
			name: "interpolation_pipe_default_fires_on_absent",
			val:  mustInterp(`"${state.absent|fallback}"`),
			want: "fallback",
		},
		{
			name:  "interpolation_pipe_default_skipped_when_present",
			state: map[string]any{"x": "real"},
			val:   mustInterp(`"${state.x|fallback}"`),
			want:  "real",
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
			name:    "empty_value_errors",
			val:     schema.Value{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestScope(t, tc.state)
			maps.Copy(s.cache, tc.cache)
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

// TestEvalValueArithmetic pins the pure-function arithmetic forms.
// Time-based add/subtract is exercised end-to-end via the cursor_token
// integration golden's composite default.
func TestEvalValueArithmetic(t *testing.T) {
	s := newTestScope(t, nil)

	cases := []struct {
		name string
		v    schema.Value
		want any
	}{
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
// over both a literal {list: [...]} operand and a list-shaped Ref
// projection (e.g. {ref: events.*.timestamp}). The literal-list and
// projection paths dispatch differently, so each gets one representative
// case.
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
			v:    schema.Value{Max: new(vList(vInt(3), vInt(7), vInt(5)))},
			want: int64(7),
		},
		{
			name: "count_literal_list",
			v:    schema.Value{Count: new(vList(vInt(3), vInt(7), vInt(5)))},
			want: int64(3),
		},
		{
			name: "max_over_events_projection",
			v:    schema.Value{Max: new(vRef("events.*.ts"))},
			want: "2026-01-01T00:00:03Z",
		},
		{
			name: "first_over_events_projection",
			v:    schema.Value{First: new(vRef("events.*.ts"))},
			want: "2026-01-01T00:00:01Z",
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

// TestEvalValueSlice covers the {slice: {<operand>, from?, to?}} form: the
// worklist pop (drop the head), bounded windows, the clamping contract for
// out-of-range / inverted bounds, and the empty-operand case. It also pins
// the reducers (first/count) reading a list straight out of state, the
// emptiness-test the worklist relies on.
func TestEvalValueSlice(t *testing.T) {
	queue := func() []any { return []any{"a", "b", "c", "d"} }
	s := newTestScope(t, map[string]any{"queue": queue()})

	cases := []struct {
		name string
		v    schema.Value
		want any
	}{
		{
			name: "drop_head",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), From: new(1)}},
			want: []any{"b", "c", "d"},
		},
		{
			name: "bounded_window",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), From: new(1), To: new(3)}},
			want: []any{"b", "c"},
		},
		{
			name: "to_only",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), To: new(2)}},
			want: []any{"a", "b"},
		},
		{
			name: "from_beyond_len_clamps_empty",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), From: new(9)}},
			want: []any{},
		},
		{
			name: "to_beyond_len_clamps_to_len",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), From: new(2), To: new(99)}},
			want: []any{"c", "d"},
		},
		{
			name: "inverted_bounds_clamp_empty",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), From: new(3), To: new(1)}},
			want: []any{},
		},
		{
			name: "negative_from_clamps_to_zero",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), From: new(-1), To: new(2)}},
			want: []any{"a", "b"},
		},
		{
			name: "absent_operand_yields_empty_list",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.missing"), From: new(1)}},
			want: []any{},
		},
		{
			name: "slice_over_literal_list",
			v:    schema.Value{Slice: &schema.SliceExpr{Operand: vList(vInt(1), vInt(2), vInt(3)), From: new(1)}},
			want: []any{int64(2), int64(3)},
		},
		// Reducers reading a list straight out of state — the worklist's head
		// read and emptiness test.
		{
			name: "first_over_state_list",
			v:    schema.Value{First: new(vRef("state.queue"))},
			want: "a",
		},
		{
			name: "count_over_state_list",
			v:    schema.Value{Count: new(vRef("state.queue"))},
			want: int64(4),
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

// TestEvalValueSliceComposes pins that a slice result is itself a list, so it
// can feed a reducer (count of a slice) — the property that lets slice nest
// inside other list-consumers.
func TestEvalValueSliceComposes(t *testing.T) {
	s := newTestScope(t, map[string]any{"queue": []any{"a", "b", "c"}})
	v := schema.Value{Count: new(schema.Value{Slice: &schema.SliceExpr{Operand: vRef("state.queue"), From: new(1)}})}
	got, err := s.evalValue(v)
	if err != nil {
		t.Fatalf("evalValue: %v", err)
	}
	if got != int64(2) {
		t.Errorf("count of slice = %#v, want 2", got)
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

// TestResolveNamespaceRef covers the leaf-level resolver behaviour the
// integration goldens cannot easily exercise: case-insensitive header
// lookup, the events.* shortcut shape, fan_out alias binding, the
// zero-events count, and the unknown-root error path.
func TestResolveNamespaceRef(t *testing.T) {
	t.Run("response_header_case_insensitive", func(t *testing.T) {
		s := newTestScope(t, nil)
		s.responseHeaders = http.Header{}
		s.responseHeaders.Set("ETag", "abc-1")

		for _, ref := range []string{"response.header.ETag", "response.header.etag"} {
			got, ok, err := s.resolveNamespaceRef(mustPath(ref))
			if err != nil || !ok || got != "abc-1" {
				t.Errorf("resolve(%s) = (%v, %v, %v); want (\"abc-1\", true, nil)", ref, got, ok, err)
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
