// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"

	"github.com/p1llus/skopos/schema"
)

// predEq builds {<verb>: {path, value}} for one of eq/gt/lt/gte/lte. The
// verb selector lets one helper drive every comparison test.
func predEq(verb, path string, value schema.Value) schema.Predicate {
	pe := &schema.PredicateEq{Path: mustPath(path), Value: value}
	switch verb {
	case "eq":
		return schema.Predicate{Eq: pe}
	case "gt":
		return schema.Predicate{Gt: pe}
	case "lt":
		return schema.Predicate{Lt: pe}
	case "gte":
		return schema.Predicate{Gte: pe}
	case "lte":
		return schema.Predicate{Lte: pe}
	}
	panic("predEq: unknown verb " + verb)
}

func predPresent(path string) schema.Predicate {
	p := mustPath(path)
	return schema.Predicate{Present: &p}
}

func predNot(p schema.Predicate) schema.Predicate { return schema.Predicate{Not: &p} }
func predAnd(ps ...schema.Predicate) schema.Predicate {
	return schema.Predicate{And: ps}
}
func predOr(ps ...schema.Predicate) schema.Predicate {
	return schema.Predicate{Or: ps}
}
func predBool(b bool) schema.Predicate {
	return schema.Predicate{LiteralBool: &b}
}

// TestEvalPredicate covers the verb dispatchers (eq / present / not /
// and / or / literal_bool) and the gt arm as the canonical ordered
// comparison. The other ordered verbs share the same dispatcher and are
// covered by TestEvalPredicate_AbsenceTolerantOrdered + the
// predicate_composition schema fixture.
func TestEvalPredicate(t *testing.T) {
	cases := []struct {
		name  string
		state map[string]any
		pred  schema.Predicate
		want  bool
	}{
		{
			name:  "eq_state_string",
			state: map[string]any{"mode": "bearer"},
			pred:  predEq("eq", "state.mode", vStr("bearer")),
			want:  true,
		},
		{
			name:  "gt_int",
			state: map[string]any{"page": int64(5)},
			pred:  predEq("gt", "state.page", vInt(3)),
			want:  true,
		},
		{
			name:  "present_set",
			state: map[string]any{"etag": "abc"},
			pred:  predPresent("state.etag"),
			want:  true,
		},
		{
			name: "present_unset",
			pred: predPresent("state.etag"),
			want: false,
		},
		{
			name:  "not_inverts",
			state: map[string]any{"x": "y"},
			pred:  predNot(predEq("eq", "state.x", vStr("y"))),
			want:  false,
		},
		{
			name:  "and_short_circuits_false",
			state: map[string]any{"x": "y"},
			pred: predAnd(
				predEq("eq", "state.x", vStr("y")),
				predEq("eq", "state.x", vStr("z")),
			),
			want: false,
		},
		{
			name:  "or_first_true",
			state: map[string]any{"x": "y"},
			pred: predOr(
				predEq("eq", "state.x", vStr("y")),
				predEq("eq", "state.x", vStr("z")),
			),
			want: true,
		},
		{
			name: "literal_bool_true",
			pred: predBool(true),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestScope(t, tc.state)
			got, err := s.evalPredicate(tc.pred)
			if err != nil {
				t.Fatalf("evalPredicate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEvalPredicate_TimestampComparison pins the time-typed gt/lt arms:
// both sides parse as RFC 3339 and the resulting nanosecond comparison
// drives the predicate.
func TestEvalPredicate_TimestampComparison(t *testing.T) {
	s := newTestScope(t, map[string]any{"last_seen": "2026-01-01T00:01:00Z"})

	pred := predEq("gt", "state.last_seen", vStr("2026-01-01T00:00:00Z"))
	got, err := s.evalPredicate(pred)
	if err != nil {
		t.Fatalf("evalPredicate: %v", err)
	}
	if !got {
		t.Errorf("gt for later timestamp = false; want true")
	}

	earlier := predEq("lt", "state.last_seen", vStr("2026-01-01T00:00:00Z"))
	got, err = s.evalPredicate(earlier)
	if err != nil {
		t.Fatalf("evalPredicate: %v", err)
	}
	if got {
		t.Errorf("lt for later timestamp = true; want false")
	}
}

// TestEvalPredicate_AbsenceTolerantEq pins eq's absence-tolerance: an
// absent path compared to anything returns false, never an error. The
// {present: <path>} verb is the right tool when the author actually
// cares about presence.
func TestEvalPredicate_AbsenceTolerantEq(t *testing.T) {
	s := newTestScope(t, nil)

	pred := predEq("eq", "state.missing", vInt(0))
	got, err := s.evalPredicate(pred)
	if err != nil {
		t.Errorf("absent path returned error: %v", err)
	}
	if got {
		t.Errorf("absent path returned true; want false")
	}

	// present: cleanly reports the absence without error.
	got, err = s.evalPredicate(predPresent("state.missing"))
	if err != nil {
		t.Errorf("present absent: %v", err)
	}
	if got {
		t.Errorf("present absent returned true; want false")
	}
}

// TestEvalPredicate_AbsenceTolerantOrdered pins the absence-tolerance
// contract for ordered comparisons: an absent operand (either side)
// yields false rather than an error. gt is representative — the four
// verbs (gt/lt/gte/lte) share a single dispatcher.
func TestEvalPredicate_AbsenceTolerantOrdered(t *testing.T) {
	t.Run("absent_lhs", func(t *testing.T) {
		s := newTestScope(t, nil)
		got, err := s.evalPredicate(predEq("gt", "state.missing", vInt(5)))
		if err != nil {
			t.Errorf("absent lhs returned error: %v", err)
		}
		if got {
			t.Errorf("absent lhs returned true; want false")
		}
	})

	t.Run("absent_rhs", func(t *testing.T) {
		s := newTestScope(t, map[string]any{"x": int64(5)})
		got, err := s.evalPredicate(predEq("gt", "state.x", vRef("state.absent")))
		if err != nil {
			t.Errorf("absent rhs returned error: %v", err)
		}
		if got {
			t.Errorf("absent rhs returned true; want false")
		}
	})
}
