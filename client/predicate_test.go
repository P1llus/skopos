package client

import (
	"testing"

	"github.com/p1llus/skopos/schema"
)

// helpers for building schema.Predicate forms tersely.
func pEq(path string, rhs schema.Value) schema.Predicate {
	return schema.Predicate{Eq: &schema.PredicateEq{Path: mustPath(path), Equal: rhs}}
}
func pGt(path string, rhs schema.Value) schema.Predicate {
	return schema.Predicate{Gt: &schema.PredicateEq{Path: mustPath(path), Equal: rhs}}
}
func pLt(path string, rhs schema.Value) schema.Predicate {
	return schema.Predicate{Lt: &schema.PredicateEq{Path: mustPath(path), Equal: rhs}}
}
func pGte(path string, rhs schema.Value) schema.Predicate {
	return schema.Predicate{Gte: &schema.PredicateEq{Path: mustPath(path), Equal: rhs}}
}
func pLte(path string, rhs schema.Value) schema.Predicate {
	return schema.Predicate{Lte: &schema.PredicateEq{Path: mustPath(path), Equal: rhs}}
}
func pPresent(path string) schema.Predicate {
	p := mustPath(path)
	return schema.Predicate{Present: &p}
}
func pAnd(parts ...schema.Predicate) schema.Predicate { return schema.Predicate{And: parts} }
func pOr(parts ...schema.Predicate) schema.Predicate  { return schema.Predicate{Or: parts} }
func pNot(p schema.Predicate) schema.Predicate        { return schema.Predicate{Not: &p} }
func pLit(b bool) schema.Predicate                    { return schema.Predicate{LiteralBool: &b} }

// TestEvalPredicate covers every Predicate discriminator, including the
// strict-nil and JSON-loose-stringify rules pinned by equal().
func TestEvalPredicate(t *testing.T) {
	tests := []struct {
		name    string
		state   map[string]any
		cursor  map[string]any
		body    any
		pred    schema.Predicate
		want    bool
		wantErr bool
	}{
		{name: "literal_bool_true", pred: pLit(true), want: true},
		{name: "literal_bool_false", pred: pLit(false), want: false},

		{
			name:  "eq_same_string",
			state: map[string]any{"mode": "bearer"},
			pred:  pEq("state.mode", vStr("bearer")),
			want:  true,
		},
		{
			name:  "eq_string_neq",
			state: map[string]any{"mode": "bearer"},
			pred:  pEq("state.mode", vStr("api_key")),
			want:  false,
		},
		{
			// scroll_id fixture pattern: body returns bool, equal is string.
			// JSON-loose stringification keeps this match working.
			name: "eq_bool_loose_against_string",
			body: map[string]any{"flag": true},
			pred: pEq("body.flag", vStr("true")),
			want: true,
		},
		{
			// The fix-this-now rule: a missing path is NEVER equal to the
			// empty string. The previous implementation returned true via
			// the toString fallback (toString(nil)=="", toString("")=="").
			name: "eq_nil_vs_empty_string_is_false",
			pred: pEq("state.absent", vStr("")),
			want: false,
		},
		{
			name: "eq_nil_vs_zero_int_is_false",
			pred: pEq("state.absent", vInt(0)),
			want: false,
		},
		{
			name: "eq_nil_vs_nil_is_true",
			pred: pEq("state.absent", schema.Value{IsZero: true}),
			want: true,
		},
		{
			// Pinned behavior: numeric stringification crosses types. This
			// is JSON-loose and documented; templates depend on it for
			// bool/string comparisons. Authors who want strict typing must
			// pin both sides explicitly.
			name:  "eq_int_loose_against_string",
			state: map[string]any{"code": int64(202)},
			pred:  pEq("state.code", vStr("202")),
			want:  true,
		},

		{
			name:   "gt_int",
			cursor: map[string]any{"page": int64(5)},
			pred:   pGt("cursor.page", vInt(3)),
			want:   true,
		},
		{
			name:   "lt_int",
			cursor: map[string]any{"page": int64(5)},
			pred:   pLt("cursor.page", vInt(3)),
			want:   false,
		},
		{
			name:   "gte_equal_is_true",
			cursor: map[string]any{"page": int64(5)},
			pred:   pGte("cursor.page", vInt(5)),
			want:   true,
		},
		{
			name:   "lte_equal_is_true",
			cursor: map[string]any{"page": int64(5)},
			pred:   pLte("cursor.page", vInt(5)),
			want:   true,
		},
		{
			name:   "lte_greater_is_false",
			cursor: map[string]any{"page": int64(5)},
			pred:   pLte("cursor.page", vInt(4)),
			want:   false,
		},
		{
			name:   "gt_rfc3339_timestamps",
			cursor: map[string]any{"last_timestamp": "2026-05-12T10:00:00Z"},
			pred:   pGt("cursor.last_timestamp", vStr("2026-05-12T09:00:00Z")),
			want:   true,
		},

		{
			name:  "present_field_set",
			state: map[string]any{"etag": "abc"},
			pred:  pPresent("state.etag"),
			want:  true,
		},
		{
			name: "present_field_unset",
			pred: pPresent("state.etag"),
			want: false,
		},
		{
			name:  "present_empty_string_is_false",
			state: map[string]any{"etag": ""},
			pred:  pPresent("state.etag"),
			want:  false,
		},

		{
			name:  "and_short_circuits_on_false",
			state: map[string]any{"a": "x"},
			pred:  pAnd(pEq("state.a", vStr("x")), pEq("state.b", vStr("y"))),
			want:  false,
		},
		{
			name:  "and_all_true",
			state: map[string]any{"a": "x", "b": "y"},
			pred:  pAnd(pEq("state.a", vStr("x")), pEq("state.b", vStr("y"))),
			want:  true,
		},
		{
			name:  "or_short_circuits_on_true",
			state: map[string]any{"a": "x"},
			pred:  pOr(pEq("state.a", vStr("x")), pEq("state.b", vStr("y"))),
			want:  true,
		},
		{
			name: "or_all_false",
			pred: pOr(pEq("state.a", vStr("x")), pEq("state.b", vStr("y"))),
			want: false,
		},
		{
			name: "not_inverts",
			pred: pNot(pLit(true)),
			want: false,
		},
		{
			name:    "empty_predicate_errors",
			pred:    schema.Predicate{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestScope(t, tc.state, tc.cursor)
			if tc.body != nil {
				s.body = tc.body
			}
			got, err := s.evalPredicate(tc.pred)
			if (err != nil) != tc.wantErr {
				t.Fatalf("evalPredicate err: got %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got != tc.want {
				t.Errorf("evalPredicate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEqualRule pins the JSON-loose-with-strict-nil rule in equal() so a
// future refactor cannot silently regress it.
func TestEqualRule(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"nil_nil", nil, nil, true},
		{"nil_empty_string", nil, "", false},
		{"nil_zero_int", nil, int64(0), false},
		{"nil_false", nil, false, false},
		{"same_string", "x", "x", true},
		{"string_loose_int", "202", int64(202), true},
		{"bool_loose_string", true, "true", true},
		{"different_strings", "a", "b", false},
		{"deep_equal_maps", map[string]any{"k": "v"}, map[string]any{"k": "v"}, true},
		// Cross-type numeric coercion via toString: DeepEqual rejects (one
		// is int64, one is float64) but both stringify to "1", so equal
		// returns true. This is the documented JSON-loose rule; do not
		// silently change it.
		{"int_vs_float_same", int64(1), float64(1.0), true},
	}

	for _, tc := range cases {
		if got := equal(tc.a, tc.b); got != tc.want {
			t.Errorf("equal(%#v, %#v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
