// SPDX-License-Identifier: Apache-2.0

package schema_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/p1llus/skopos/schema"
)

// TestSpecFixtures loads every YAML under schema/testdata/ (internal-only
// variants and matrix coverage) and templates/ (user-facing templates),
// validates it, and round-trips it through YAML and JSON to verify the
// codecs. Both roots share the same conformance bar so user-visible
// templates cannot drift away from the parser.
func TestSpecFixtures(t *testing.T) {
	roots := []struct {
		label string
		dir   string
	}{
		{"testdata", "testdata"},
		{"templates", filepath.Join("..", "templates")},
	}

	for _, r := range roots {
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			t.Fatalf("reading %s dir (%s): %v", r.label, r.dir, err)
		}
		if len(entries) == 0 {
			t.Fatalf("no YAML fixtures found in %s/", r.dir)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
				continue
			}
			name := e.Name()
			t.Run(r.label+"/"+name, func(t *testing.T) {
				path := filepath.Join(r.dir, name)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("reading %s: %v", path, err)
				}

				// Parse — skip v1 templates gracefully.
				doc, err := schema.Parse(data)
				if err != nil {
					if isVersionError(err) {
						t.Skipf("skipping v1 fixture %s (not yet migrated)", path)
					}
					t.Fatalf("Parse(%s): %v", path, err)
				}

				// Validate. Warnings are allowed (e.g. multi_field_cursor.yaml's
				// extract.path namespace-shadow warning); only error-severity
				// diagnostics fail the suite.
				diags := schema.Validate(doc)
				errs := 0
				for _, d := range diags {
					if d.Severity == "error" {
						errs++
						t.Errorf("Validate(%s): [%s] %s: %s", path, d.Severity, d.Path, d.Message)
					}
				}
				if errs > 0 {
					return
				}

				// Round-trip YAML: marshal → unmarshal → marshal must produce
				// byte-identical YAML.
				roundTripYAML(t, doc, path)

				// Round-trip JSON: marshal → unmarshal → marshal must produce
				// byte-identical JSON.
				roundTripJSON(t, doc, path)
			})
		}
	}
}

// roundTripYAML asserts: doc → marshal → parse → marshal → parse produces a
// structurally identical Doc and byte-identical output across both
// re-marshals. This is the invariant that catches the codec-class bugs the
// v2-tighten plan plugged (Path-escape regression, Object-key collision,
// multi-key Value mappings, empty AND/OR).
func roundTripYAML(t *testing.T, doc *schema.Doc, label string) {
	t.Helper()
	out1, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("yaml.Marshal(%s): %v", label, err)
	}
	var doc2 schema.Doc
	if err := yaml.Unmarshal(out1, &doc2); err != nil {
		t.Fatalf("yaml.Unmarshal(%s) round-trip: %v", label, err)
	}
	out2, err := yaml.Marshal(&doc2)
	if err != nil {
		t.Fatalf("yaml.Marshal(%s) round-trip 2: %v", label, err)
	}
	if !bytes.Equal(out1, out2) {
		t.Errorf("YAML round-trip mismatch for %s:\n--- first ---\n%s--- second ---\n%s", label, out1, out2)
	}
	var doc3 schema.Doc
	if err := yaml.Unmarshal(out2, &doc3); err != nil {
		t.Fatalf("yaml.Unmarshal(%s) round-trip 3: %v", label, err)
	}
	if !reflect.DeepEqual(&doc2, &doc3) {
		t.Errorf("YAML Doc structural mismatch across round-trips for %s", label)
	}
}

// roundTripJSON: the JSON twin of roundTripYAML. Same invariant.
func roundTripJSON(t *testing.T, doc *schema.Doc, label string) {
	t.Helper()
	out1, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal(%s): %v", label, err)
	}
	var doc2 schema.Doc
	if err := json.Unmarshal(out1, &doc2); err != nil {
		t.Fatalf("json.Unmarshal(%s) round-trip: %v", label, err)
	}
	out2, err := json.Marshal(&doc2)
	if err != nil {
		t.Fatalf("json.Marshal(%s) round-trip 2: %v", label, err)
	}
	if !bytes.Equal(out1, out2) {
		t.Errorf("JSON round-trip mismatch for %s:\n--- first ---\n%s\n--- second ---\n%s\n", label, out1, out2)
	}
	var doc3 schema.Doc
	if err := json.Unmarshal(out2, &doc3); err != nil {
		t.Fatalf("json.Unmarshal(%s) round-trip 3: %v", label, err)
	}
	if !reflect.DeepEqual(&doc2, &doc3) {
		t.Errorf("JSON Doc structural mismatch across round-trips for %s", label)
	}
}

// TestParseRejectsLegacyV1 verifies that a document using the legacy
// `version:` key (rather than `ir_version:`) is rejected. This is a different
// version-mismatch path from the IRVersion-string check.
func TestParseRejectsLegacyV1(t *testing.T) {
	v1 := []byte(`version: "1"
api:
  base_url: state.url
auth:
  type: bearer
  bearer:
    token: state.api_key
`)
	_, err := schema.Parse(v1)
	if err == nil {
		t.Fatal("expected error for v1 document, got nil")
	}
}

// TestParseRejectsEmpty verifies that empty input is rejected.
func TestParseRejectsEmpty(t *testing.T) {
	_, err := schema.Parse([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// TestParseJSON verifies that a JSON-formatted document is parsed correctly.
func TestParseJSON(t *testing.T) {
	data := []byte(`{
  "ir_version": "1",
  "auth": {"none": {}},
  "requests": [{"method": "GET", "path": "/api/v1/events"}],
  "response": {"decode": "json", "events_at": "events"},
  "pagination": {"none": {}},
  "progress": {"stateless": {}}
}`)
	doc, err := schema.Parse(data)
	if err != nil {
		t.Fatalf("Parse JSON: %v", err)
	}
	if doc.IRVersion != "1" {
		t.Errorf("expected ir_version 1, got %q", doc.IRVersion)
	}
}

// TestParseJSONIntDefault verifies that a JSON int default validates. Per A5:
// encoding/json decodes numbers into float64 when the destination is interface{},
// so the validator must accept whole-number float64s for type=int.
func TestParseJSONIntDefault(t *testing.T) {
	data := []byte(`{
  "ir_version": "1",
  "auth": {"none": {}},
  "state": {"fields": {"size": {"type": "int", "default": 5}}},
  "requests": [{"method": "GET", "path": "/api/v1/events"}],
  "response": {"decode": "json", "events_at": "events"},
  "pagination": {"none": {}},
  "progress": {"stateless": {}}
}`)
	doc, err := schema.Parse(data)
	if err != nil {
		t.Fatalf("Parse JSON with int default: %v", err)
	}
	for _, d := range schema.Validate(doc) {
		if d.Severity == "error" {
			t.Errorf("unexpected validate error: %s", d.Message)
		}
	}
}

// TestParseJSONIntDefaultRejectsFraction pins the new validator behaviour: a
// JSON number like 5.5 is rejected even though it decodes as float64.
func TestParseJSONIntDefaultRejectsFraction(t *testing.T) {
	data := []byte(`{
  "ir_version": "1",
  "auth": {"none": {}},
  "state": {"fields": {"size": {"type": "int", "default": 5.5}}},
  "requests": [{"method": "GET", "path": "/api/v1/events"}],
  "response": {"decode": "json", "events_at": "events"},
  "pagination": {"none": {}},
  "progress": {"stateless": {}}
}`)
	doc, err := schema.Parse(data)
	if err != nil {
		t.Fatalf("Parse JSON: %v", err)
	}
	diags := schema.Validate(doc)
	var got string
	for _, d := range diags {
		if d.Severity == "error" {
			got = d.Message
			break
		}
	}
	if got == "" {
		t.Fatalf("expected validate error for fractional default, got none")
	}
	if !strings.Contains(got, "non-integer") {
		t.Errorf("expected non-integer diagnostic, got: %s", got)
	}
}

// TestPathParsing tests Path parsing edge cases.
func TestPathParsing(t *testing.T) {
	tests := []struct {
		input   string
		wantStr string
		wantErr bool
	}{
		{"state.api_key", "state.api_key", false},
		{"data.issues.nodes", "data.issues.nodes", false},
		{"cursor.last_timestamp", "cursor.last_timestamp", false},
		{"", "", false}, // zero path
		{".bad", "", true},
		{"a..b", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			p, err := schema.ParsePath(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParsePath(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParsePath(%q): unexpected error: %v", tc.input, err)
				return
			}
			if p.String() != tc.wantStr {
				t.Errorf("ParsePath(%q).String() = %q, want %q", tc.input, p.String(), tc.wantStr)
			}
		})
	}
}

// TestPathParsing_JSONInvalidParts pins the JSON-side parts decoder error
// path: non-string elements and empty-string elements both reject with a
// pointed diagnostic. (A12 — test gap, validator behaviour was correct.)
func TestPathParsing_JSONInvalidParts(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"non_string_element", `{"parts": [1, 2]}`},
		{"empty_string_element", `{"parts": ["", "b"]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p schema.Path
			err := json.Unmarshal([]byte(tc.input), &p)
			if err == nil {
				t.Fatalf("expected error, got: %+v", p)
			}
			if !strings.Contains(err.Error(), "must be a non-empty string") {
				t.Errorf("expected non-empty-string diagnostic, got: %v", err)
			}
		})
	}
}

// TestValueObjectMarshalAlphabetisesKeys pins the implicit yaml.v3 default
// behaviour relied on by A24: Value variants that marshal as Go maps emit keys
// in alphabetical order. Catches a future refactor to *yaml.Node that quietly
// drops the contract.
func TestValueObjectMarshalAlphabetisesKeys(t *testing.T) {
	var v schema.Value
	if err := yaml.Unmarshal([]byte(`{object: {z: 1, a: 2, m: 3}}`), &v); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(out)
	aIdx := strings.Index(got, "a:")
	mIdx := strings.Index(got, "m:")
	zIdx := strings.Index(got, "z:")
	if aIdx < 0 || mIdx < 0 || zIdx < 0 || aIdx >= mIdx || mIdx >= zIdx {
		t.Errorf("expected keys a, m, z in alphabetical order, got:\n%s", got)
	}
}

// TestValueCodecs tests Value YAML/JSON round-trips for each form.
func TestValueCodecs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"literal_string", `"hello"`},
		{"literal_int", `42`},
		{"literal_bool", `true`},
		// Note: there is no top-level "null" case here. yaml.v3 short-circuits
		// the null tag before invoking UnmarshalYAML, so a YAML `null` cannot
		// round-trip to a Value with IsZero==true through the codec; it would
		// land as the all-zero Value{} which the marshaler now correctly
		// rejects as a programmer-construction error (see review-03 todo
		// codec_value_marshal_empty_fallback). Authors who want absence omit
		// the field entirely.
		{"ref_simple", `{ref: state.api_key}`},
		{"now", `{now: true, offset: "-1h"}`},
		{"now_formatted", `{format: rfc3339, value: {now: true, offset: "-1h"}}`},
		{"concat", `{concat: ["/api/", {ref: state.url}, "/v1"]}`},
		{"object", `{object: {key: value, n: 5, b: true}}`},
		{"from_pagination", `{from_pagination: token}`},
		{"from_progress", `{from_progress: latest_timestamp}`},
		{"format", `{format: string, value: {ref: state.page_size}}`},
		{"base64", `{base64: hello}`},
		{"select_value", `{select: {branches: [{when: {literal_bool: true}, value: /gov}], default: /commercial}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var v schema.Value
			if err := yaml.Unmarshal([]byte(tc.yaml), &v); err != nil {
				t.Fatalf("UnmarshalYAML(%s): %v", tc.name, err)
			}
			out, err := yaml.Marshal(v)
			if err != nil {
				t.Fatalf("MarshalYAML(%s): %v", tc.name, err)
			}
			var v2 schema.Value
			if err := yaml.Unmarshal(out, &v2); err != nil {
				t.Fatalf("UnmarshalYAML round-trip (%s): %v", tc.name, err)
			}
			out2, err := yaml.Marshal(v2)
			if err != nil {
				t.Fatalf("MarshalYAML round-trip 2 (%s): %v", tc.name, err)
			}
			if !bytes.Equal(out, out2) {
				t.Errorf("YAML round-trip mismatch for %s:\n  1: %s  2: %s", tc.name, out, out2)
			}
		})
	}
}

// TestPredicateCodecs tests Predicate YAML/JSON round-trips for each form.
func TestPredicateCodecs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"eq", `{eq: {path: state.auth_mode, equal: bearer}}`},
		{"present", `{present: state.etag}`},
		{"and", `{and: [{literal_bool: true}, {literal_bool: false}]}`},
		{"or", `{or: [{literal_bool: true}, {literal_bool: false}]}`},
		{"not", `{not: {literal_bool: true}}`},
		{"literal_bool_true", `{literal_bool: true}`},
		{"literal_bool_false", `{literal_bool: false}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p schema.Predicate
			if err := yaml.Unmarshal([]byte(tc.yaml), &p); err != nil {
				t.Fatalf("UnmarshalYAML(%s): %v", tc.name, err)
			}
			out, err := yaml.Marshal(p)
			if err != nil {
				t.Fatalf("MarshalYAML(%s): %v", tc.name, err)
			}
			var p2 schema.Predicate
			if err := yaml.Unmarshal(out, &p2); err != nil {
				t.Fatalf("UnmarshalYAML round-trip (%s): %v", tc.name, err)
			}
			out2, err := yaml.Marshal(p2)
			if err != nil {
				t.Fatalf("MarshalYAML round-trip 2 (%s): %v", tc.name, err)
			}
			if !bytes.Equal(out, out2) {
				t.Errorf("YAML round-trip mismatch for %s:\n  1: %s  2: %s", tc.name, out, out2)
			}
		})
	}
}

// TestCodecRegressions covers the classes of round-trip-corruption bugs the
// v2-tighten plan plugged: implicit Object fallback, multi-key Value /
// Predicate mappings, paths whose segments contain '.', and empty AND/OR.
func TestCodecRegressions(t *testing.T) {
	t.Run("object_required_for_unknown_keys", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{some: thing, other: stuff}`), &v)
		if err == nil {
			t.Fatalf("expected error for bare unrecognised mapping, got value: %+v", v)
		}
		if !strings.Contains(err.Error(), "object") {
			t.Errorf("error should mention {object: ...} wrapper hint, got: %v", err)
		}
	})

	t.Run("object_inner_keys_are_literal", func(t *testing.T) {
		// concat is a Value discriminator key; inside {object: ...} it must
		// stay literal and not get re-interpreted.
		var v schema.Value
		if err := yaml.Unmarshal([]byte(`{object: {concat: literal-value, ref: literal-too}}`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v.Object == nil {
			t.Fatalf("expected Object form, got %+v", v)
		}
		got, ok := v.Object["concat"]
		if !ok || got.LiteralString == nil || *got.LiteralString != "literal-value" {
			t.Errorf("inner concat key should be literal string, got %+v", got)
		}
	})

	t.Run("value_multi_discriminator_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{ref: state.x, list: [1, 2]}`), &v)
		if err == nil {
			t.Fatalf("expected error for multi-key Value, got: %+v", v)
		}
		if !strings.Contains(err.Error(), "multiple discriminator") {
			t.Errorf("error should call out multi-key, got: %v", err)
		}
	})

	t.Run("predicate_multi_discriminator_rejected", func(t *testing.T) {
		var p schema.Predicate
		err := yaml.Unmarshal([]byte(`{eq: {path: state.x, equal: 1}, present: state.x}`), &p)
		if err == nil {
			t.Fatalf("expected error for multi-key Predicate, got: %+v", p)
		}
	})

	t.Run("predicate_deferred_in_hint", func(t *testing.T) {
		var p schema.Predicate
		err := yaml.Unmarshal([]byte(`{in: state.allowed}`), &p)
		if err == nil {
			t.Fatalf("expected deferred-verb error for {in: ...}, got: %+v", p)
		}
		if !strings.Contains(err.Error(), "\"in\" is deferred") {
			t.Errorf("error should call out deferred verb, got: %v", err)
		}
	})

	t.Run("predicate_deferred_matches_hint", func(t *testing.T) {
		var p schema.Predicate
		err := yaml.Unmarshal([]byte(`{matches: state.x}`), &p)
		if err == nil {
			t.Fatalf("expected deferred-verb error for {matches: ...}, got: %+v", p)
		}
		if !strings.Contains(err.Error(), "\"matches\" is deferred") {
			t.Errorf("error should call out deferred verb, got: %v", err)
		}
	})

	t.Run("predicate_empty_and_rejected", func(t *testing.T) {
		var p schema.Predicate
		err := yaml.Unmarshal([]byte(`{and: []}`), &p)
		if err == nil {
			t.Fatalf("expected error for {and: []}, got: %+v", p)
		}
	})

	t.Run("predicate_empty_or_rejected", func(t *testing.T) {
		var p schema.Predicate
		err := yaml.Unmarshal([]byte(`{or: []}`), &p)
		if err == nil {
			t.Fatalf("expected error for {or: []}, got: %+v", p)
		}
	})

	t.Run("value_empty_concat_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{concat: []}`), &v)
		if err == nil {
			t.Fatalf("expected error for {concat: []}, got: %+v", v)
		}
		if !strings.Contains(err.Error(), "at least 2") {
			t.Errorf("error should say at least 2 elements, got: %v", err)
		}
	})

	t.Run("value_singleton_concat_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{concat: ["only"]}`), &v)
		if err == nil {
			t.Fatalf("expected error for singleton concat, got: %+v", v)
		}
		if !strings.Contains(err.Error(), "at least 2") {
			t.Errorf("error should say at least 2 elements, got: %v", err)
		}
	})

	t.Run("value_singleton_concat_rejected_json", func(t *testing.T) {
		var v schema.Value
		err := json.Unmarshal([]byte(`{"concat": ["only"]}`), &v)
		if err == nil {
			t.Fatalf("expected JSON error for singleton concat, got: %+v", v)
		}
	})

	t.Run("value_empty_list_round_trip_yaml", func(t *testing.T) {
		// {list: []} must round-trip byte-identically and parse back to a
		// non-nil empty slice, not a nil slice or the empty-LiteralString
		// fallback.
		in := []byte(`{list: []}`)
		var v schema.Value
		if err := yaml.Unmarshal(in, &v); err != nil {
			t.Fatalf("unmarshal {list: []}: %v", err)
		}
		if v.List == nil {
			t.Fatalf("expected non-nil empty List, got nil")
		}
		if len(v.List) != 0 {
			t.Fatalf("expected empty List, got len=%d", len(v.List))
		}
		out1, err := yaml.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var v2 schema.Value
		if err := yaml.Unmarshal(out1, &v2); err != nil {
			t.Fatalf("re-unmarshal: %v", err)
		}
		out2, err := yaml.Marshal(v2)
		if err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
		if !bytes.Equal(out1, out2) {
			t.Errorf("empty-list round-trip not byte-identical:\n  1: %s  2: %s", out1, out2)
		}
	})

	t.Run("value_empty_list_round_trip_json", func(t *testing.T) {
		in := []byte(`{"list":[]}`)
		var v schema.Value
		if err := json.Unmarshal(in, &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v.List == nil || len(v.List) != 0 {
			t.Fatalf("expected non-nil empty List, got %v", v.List)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(out) != `{"list":[]}` {
			t.Errorf("empty-list JSON: got %s, want {\"list\":[]}", out)
		}
	})

	t.Run("value_ref_unknown_sibling_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{ref: state.x, defualt: "y"}`), &v)
		if err == nil {
			t.Fatalf("expected error for ref+typo sibling, got: %+v", v)
		}
		if !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("error should call out unknown key, got: %v", err)
		}
	})

	t.Run("value_now_unknown_sibling_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{now: true, offest: "-1h"}`), &v)
		if err == nil {
			t.Fatalf("expected error for now+typo sibling, got: %+v", v)
		}
	})

	t.Run("value_format_unknown_sibling_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{format: rfc3339, vlaue: {now: true}}`), &v)
		if err == nil {
			t.Fatalf("expected error for format+typo sibling, got: %+v", v)
		}
	})

	t.Run("predicate_eq_unknown_sibling_rejected", func(t *testing.T) {
		var p schema.Predicate
		err := yaml.Unmarshal([]byte(`{eq: {path: state.x, equal: 1}, prseent: state.x}`), &p)
		if err == nil {
			t.Fatalf("expected error for eq+typo sibling, got: %+v", p)
		}
	})

	t.Run("value_unknown_discriminator_lists_valid_keys", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{nope: 1, alsonope: 2}`), &v)
		if err == nil {
			t.Fatalf("expected error, got: %+v", v)
		}
		// The picker advertises the closed discriminator set so authors can
		// recover from a typo. With §6.5 resolved (Value.Raw removed) the
		// list MUST NOT mention "raw".
		if !strings.Contains(err.Error(), "literal_string") || !strings.Contains(err.Error(), "object") {
			t.Errorf("error should list valid discriminators, got: %v", err)
		}
		if strings.Contains(err.Error(), "raw") {
			t.Errorf("error must not mention the removed raw variant, got: %v", err)
		}
	})

	t.Run("value_raw_form_rejected_at_parse_time", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{raw: {cel: "1+1"}}`), &v)
		if err == nil {
			t.Fatalf("expected error parsing removed {raw: ...} form, got: %+v", v)
		}
	})

	t.Run("path_escapes_dotted_segments_yaml", func(t *testing.T) {
		p := schema.Path{Parts: []string{"steps", "my.weird.id", "body"}}
		out, err := yaml.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(out), "parts:") {
			t.Errorf("expected {parts: ...} form for path with dotted segment; got: %s", out)
		}
		var p2 schema.Path
		if err := yaml.Unmarshal(out, &p2); err != nil {
			t.Fatalf("re-unmarshal: %v", err)
		}
		if len(p2.Parts) != len(p.Parts) {
			t.Fatalf("parts length mismatch: %v vs %v", p.Parts, p2.Parts)
		}
		for i := range p.Parts {
			if p2.Parts[i] != p.Parts[i] {
				t.Errorf("part %d: %q vs %q", i, p2.Parts[i], p.Parts[i])
			}
		}
	})

	t.Run("path_escapes_dotted_segments_json", func(t *testing.T) {
		p := schema.Path{Parts: []string{"steps", "weird.id", "body", "field"}}
		out, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(out), `"parts"`) {
			t.Errorf("expected {parts: ...} JSON form; got: %s", out)
		}
		var p2 schema.Path
		if err := json.Unmarshal(out, &p2); err != nil {
			t.Fatalf("re-unmarshal: %v", err)
		}
		if len(p2.Parts) != len(p.Parts) {
			t.Fatalf("parts length mismatch: %v vs %v", p.Parts, p2.Parts)
		}
		for i := range p.Parts {
			if p2.Parts[i] != p.Parts[i] {
				t.Errorf("part %d: %q vs %q", i, p2.Parts[i], p.Parts[i])
			}
		}
	})

	t.Run("token_cache_expiry_field_dotted_string", func(t *testing.T) {
		var c schema.TokenCache
		if err := yaml.Unmarshal([]byte("store_in: cached_token\nexpiry_field: expires_in\nexpiry_buffer: 60s\n"), &c); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := c.ExpiryField.String(); got != "expires_in" {
			t.Errorf("ExpiryField.String() = %q, want %q", got, "expires_in")
		}
		if len(c.ExpiryField.Parts) != 1 || c.ExpiryField.Parts[0] != "expires_in" {
			t.Errorf("ExpiryField.Parts = %v, want [expires_in]", c.ExpiryField.Parts)
		}
	})

	t.Run("token_cache_expiry_field_parts_form", func(t *testing.T) {
		var c schema.TokenCache
		if err := yaml.Unmarshal([]byte("store_in: t\nexpiry_field:\n  parts: [data, expires.at]\nexpiry_buffer: 60s\n"), &c); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(c.ExpiryField.Parts) != 2 ||
			c.ExpiryField.Parts[0] != "data" ||
			c.ExpiryField.Parts[1] != "expires.at" {
			t.Errorf("ExpiryField.Parts = %v, want [data expires.at]", c.ExpiryField.Parts)
		}
	})

	t.Run("request_cache_expiry_field_dotted_string", func(t *testing.T) {
		var c schema.RequestCache
		if err := yaml.Unmarshal([]byte("store_in: session_token\nexpiry_field: expires_in\nexpiry_buffer: 60s\n"), &c); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := c.ExpiryField.String(); got != "expires_in" {
			t.Errorf("ExpiryField.String() = %q, want %q", got, "expires_in")
		}
	})

	t.Run("cursor_update_structured_roundtrip", func(t *testing.T) {
		yamlSrc := "kind: use_now\n"
		var d schema.CursorUpdateDirective
		if err := yaml.Unmarshal([]byte(yamlSrc), &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if d.Kind != "use_now" {
			t.Errorf("Kind = %q, want use_now", d.Kind)
		}
		if d.Lookback != nil {
			t.Errorf("Lookback = %+v, want nil", d.Lookback)
		}
	})

	t.Run("cursor_update_structured_with_lookback", func(t *testing.T) {
		yamlSrc := "kind: latest_event_timestamp\nlookback: \"-5m\"\n"
		var d schema.CursorUpdateDirective
		if err := yaml.Unmarshal([]byte(yamlSrc), &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if d.Kind != "latest_event_timestamp" {
			t.Errorf("Kind = %q, want latest_event_timestamp", d.Kind)
		}
		if d.Lookback == nil || d.Lookback.LiteralString == nil || *d.Lookback.LiteralString != "-5m" {
			t.Errorf("Lookback = %+v, want literal string \"-5m\"", d.Lookback)
		}
	})

	t.Run("cursor_update_structured_with_event_time", func(t *testing.T) {
		yamlSrc := "kind: latest_event_timestamp\nevent_time:\n  path: created_at\n"
		var d schema.CursorUpdateDirective
		if err := yaml.Unmarshal([]byte(yamlSrc), &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if d.Kind != "latest_event_timestamp" {
			t.Errorf("Kind = %q, want latest_event_timestamp", d.Kind)
		}
		if d.EventTime == nil {
			t.Fatalf("EventTime nil, want path=created_at")
		}
		if got := d.EventTime.Path.String(); got != "created_at" {
			t.Errorf("EventTime.Path = %q, want created_at", got)
		}
	})

	t.Run("cursor_update_scalar_shorthand_rejected_yaml", func(t *testing.T) {
		var d schema.CursorUpdateDirective
		err := yaml.Unmarshal([]byte("use_now\n"), &d)
		if err == nil {
			t.Fatalf("expected error for scalar cursor_update, got: %+v", d)
		}
		if !strings.Contains(err.Error(), "scalar shorthand") {
			t.Errorf("error should mention scalar shorthand, got: %v", err)
		}
	})

	t.Run("cursor_update_scalar_shorthand_rejected_json", func(t *testing.T) {
		var d schema.CursorUpdateDirective
		err := json.Unmarshal([]byte(`"use_now"`), &d)
		if err == nil {
			t.Fatalf("expected error for scalar cursor_update, got: %+v", d)
		}
		if !strings.Contains(err.Error(), "scalar shorthand") {
			t.Errorf("error should mention scalar shorthand, got: %v", err)
		}
	})

	// review-03: an all-zero Value (no form set, IsZero false) must error
	// at marshal time instead of silently round-tripping to LiteralString("").
	t.Run("value_empty_marshal_yaml_errors", func(t *testing.T) {
		_, err := yaml.Marshal(schema.Value{})
		if err == nil {
			t.Fatalf("expected MarshalYAML error for zero Value, got nil")
		}
		if !strings.Contains(err.Error(), "zero value cannot be marshalled") {
			t.Errorf("error should mention zero value, got: %v", err)
		}
	})

	t.Run("value_empty_marshal_json_errors", func(t *testing.T) {
		_, err := json.Marshal(schema.Value{})
		if err == nil {
			t.Fatalf("expected MarshalJSON error for zero Value, got nil")
		}
		if !strings.Contains(err.Error(), "zero value cannot be marshalled") {
			t.Errorf("error should mention zero value, got: %v", err)
		}
	})

	// review-03: predicate comparison verbs (gt/lt/gte/lte) share the
	// {path, equal} shape with eq and must round-trip byte-identically.
	for _, verb := range []string{"gt", "lt", "gte", "lte"} {
		verb := verb
		t.Run("predicate_"+verb+"_roundtrip_yaml", func(t *testing.T) {
			src := "{" + verb + ": {path: cursor.page, equal: 100}}"
			var p schema.Predicate
			if err := yaml.Unmarshal([]byte(src), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out1, err := yaml.Marshal(p)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var p2 schema.Predicate
			if err := yaml.Unmarshal(out1, &p2); err != nil {
				t.Fatalf("re-unmarshal: %v", err)
			}
			out2, err := yaml.Marshal(p2)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if !bytes.Equal(out1, out2) {
				t.Errorf("%s round-trip:\n  1: %s  2: %s", verb, out1, out2)
			}
			// Confirm exactly one of the new fields is set.
			if names := p.VariantNames(); len(names) != 1 || names[0] != verb {
				t.Errorf("VariantNames = %v, want [%s]", names, verb)
			}
		})

		t.Run("predicate_"+verb+"_roundtrip_json", func(t *testing.T) {
			src := `{"` + verb + `":{"path":"cursor.page","equal":100}}`
			var p schema.Predicate
			if err := json.Unmarshal([]byte(src), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(out) != src {
				t.Errorf("%s JSON: got %s, want %s", verb, out, src)
			}
		})
	}
}

// TestValidateWarnings verifies that specific documents produce the
// expected warning-severity diagnostic without producing an error.
func TestValidateWarnings(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		wantMsg  string // substring of the expected warning message
		wantPath string // substring of the diagnostic path
	}{
		{
			name: "extract_path_namespace_shadow_warning",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    extract:
      - name: frozen
        path: state.frozen
        target: cursor
        coerce: bool
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg:  "body-relative",
			wantPath: "extract[0].path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := schema.Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			diags := schema.Validate(doc)
			var errs, warns []schema.Diagnostic
			for _, d := range diags {
				switch d.Severity {
				case "error":
					errs = append(errs, d)
				case "warning":
					warns = append(warns, d)
				}
			}
			if len(errs) > 0 {
				t.Fatalf("expected no errors, got: %+v", errs)
			}
			found := false
			for _, d := range warns {
				if strings.Contains(d.Message, tc.wantMsg) && strings.Contains(d.Path, tc.wantPath) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected warning containing %q at path containing %q; got %+v",
					tc.wantMsg, tc.wantPath, warns)
			}
		})
	}
}

// TestIsSecret exercises the secret-propagation predicate.
func TestIsSecret(t *testing.T) {
	src := `ir_version: "1"
state:
  fields:
    api_key:
      type: secret
    public:
      type: string
auth:
  bearer:
    token: {ref: state.api_key}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`
	doc, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	secretRef := schema.Value{Ref: &schema.RefValue{Path: schema.Path{Parts: []string{"state", "api_key"}}}}
	publicRef := schema.Value{Ref: &schema.RefValue{Path: schema.Path{Parts: []string{"state", "public"}}}}
	literal := func(s string) schema.Value { return schema.Value{LiteralString: &s} }

	cases := []struct {
		name string
		v    schema.Value
		want bool
	}{
		{"direct_secret_ref", secretRef, true},
		{"direct_public_ref", publicRef, false},
		{"bare_literal", literal("hello"), false},
		{"concat_with_secret", schema.Value{Concat: []schema.Value{literal("Bearer "), secretRef}}, true},
		{"concat_without_secret", schema.Value{Concat: []schema.Value{literal("Bearer "), publicRef}}, false},
		{"format_wrapping_secret", schema.Value{Format: &schema.FormatValue{Verb: "rfc3339", Value: secretRef}}, true},
		{"format_wrapping_literal", schema.Value{Format: &schema.FormatValue{Verb: "rfc3339", Value: literal("x")}}, false},
		{"base64_wrapping_secret", schema.Value{Base64: &secretRef}, true},
		{"list_with_secret", schema.Value{List: []schema.Value{literal("a"), secretRef}}, true},
		{"list_without_secret", schema.Value{List: []schema.Value{literal("a"), publicRef}}, false},
		{"object_with_secret", schema.Value{Object: map[string]schema.Value{"k": secretRef}}, true},
		{"ref_with_secret_default", schema.Value{Ref: &schema.RefValue{Path: schema.Path{Parts: []string{"state", "public"}}, Default: &secretRef}}, true},
		{"now_with_literal_offset", schema.Value{Now: &schema.NowValue{Offset: ptrValue(literal("-1h"))}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := schema.IsSecret(doc, tc.v)
			if got != tc.want {
				t.Errorf("IsSecret(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func ptrValue(v schema.Value) *schema.Value { return &v }

// TestAutoRegisteredFieldDecl verifies that the OAuth2 / request cache.store_in
// slot materialises in d.State.Fields after Validate, so targets walking the
// state map see it as a runtime-typed FieldDecl.
func TestAutoRegisteredFieldDecl(t *testing.T) {
	t.Run("request_cache_store_in", func(t *testing.T) {
		path := filepath.Join("testdata", "session_login_cached.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		doc, err := schema.Parse(data)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		diags := schema.Validate(doc)
		for _, d := range diags {
			if d.Severity == "error" {
				t.Fatalf("unexpected diagnostic: %s: %s", d.Path, d.Message)
			}
		}
		fd, ok := doc.State.Fields["session_token"]
		if !ok {
			t.Fatalf("expected auto-registered state.fields.session_token, got fields: %v", doc.State.Fields)
		}
		if fd.Type != "string" {
			t.Errorf("FieldDecl.Type = %q, want string", fd.Type)
		}
		if fd.Mutability != "runtime" {
			t.Errorf("FieldDecl.Mutability = %q, want runtime", fd.Mutability)
		}
	})

	t.Run("oauth2_cache_store_in", func(t *testing.T) {
		path := filepath.Join("testdata", "token_cache.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		doc, err := schema.Parse(data)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		diags := schema.Validate(doc)
		for _, d := range diags {
			if d.Severity == "error" {
				t.Fatalf("unexpected diagnostic: %s: %s", d.Path, d.Message)
			}
		}
		if doc.Auth.OAuth2 == nil || doc.Auth.OAuth2.ClientCredentials == nil ||
			doc.Auth.OAuth2.ClientCredentials.Cache == nil {
			t.Fatalf("template does not exercise oauth2 cache")
		}
		store := doc.Auth.OAuth2.ClientCredentials.Cache.StoreIn
		fd, ok := doc.State.Fields[store]
		if !ok {
			t.Fatalf("expected auto-registered state.fields.%s, got fields: %v", store, doc.State.Fields)
		}
		if fd.Type != "string" || fd.Mutability != "runtime" {
			t.Errorf("FieldDecl = %+v, want {Type:string, Mutability:runtime}", fd)
		}
	})
}

// isVersionError reports whether err is an ir_version mismatch (indicating a
// v1 template that has not yet been migrated to v2).
func isVersionError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "unsupported ir_version")
}

// TestValidateErrors verifies that specific invalid documents produce the
// expected diagnostic messages.
func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantMsg string // substring of the expected diagnostic message
	}{
		{
			name: "auth_no_variant",
			doc: `ir_version: "1"
auth: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "exactly one variant",
		},
		{
			name: "unknown_state_ref",
			doc: `ir_version: "1"
auth:
  bearer:
    token: {ref: state.unknown_field}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "not declared in state.fields",
		},
		{
			name: "pagination_multi_variant",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
  cursor_token:
    token_at: next
    send_as: query.cursor
progress:
  stateless: {}
`,
			wantMsg: "exactly one variant",
		},
		{
			name: "defaults_unknown_state_ref",
			doc: `ir_version: "1"
defaults:
  base_url: {ref: state.missing}
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "not declared in state.fields",
		},
		{
			name: "state_default_type_mismatch_int",
			doc: `ir_version: "1"
state:
  fields:
    page_size:
      type: int
      default: not-a-number
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "must be an integer",
		},
		{
			name: "state_default_invalid_duration",
			doc: `ir_version: "1"
state:
  fields:
    interval:
      type: duration
      default: "not-a-duration"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "valid Go duration",
		},
		{
			name: "state_default_enum_outside_values",
			doc: `ir_version: "1"
state:
  fields:
    mode:
      type: enum
      values: [a, b, c]
      default: zzz
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "not one of the declared enum values",
		},
		{
			name: "send_as_bad_format",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  cursor_token:
    token_at: next
    send_as: cookie.token
progress:
  stateless: {}
`,
			wantMsg: "send_as must be",
		},
		{
			name: "from_pagination_role_strategy_mismatch",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    query:
      cursor: {from_pagination: page}
response:
  decode: json
  events_at: events
pagination:
  cursor_token:
    token_at: next
    send_as: query.cursor
progress:
  stateless: {}
`,
			wantMsg: "does not match the active pagination strategy",
		},
		{
			name: "extract_path_required_for_body_source",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    extract:
      - name: tag
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "path is required",
		},
		{
			name: "extract_coerce_unknown_verb",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    extract:
      - name: tag
        path: foo
        coerce: not_a_verb
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "unknown coerce verb",
		},
		{
			name: "async_job_step_ref_unknown",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: real
    method: POST
    path: /api/v1/exports
    expect_status: [202]
  - method: GET
    url: https://example.com/result
response:
  decode: json
  events_at: items
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: ghost
      extract:
        export_id: {path: id}
    poll:
      step: real
      complete_when: {literal_bool: true}
`,
			wantMsg: "is not declared by any requests[].id",
		},
		{
			name: "async_poll_missing_complete_when",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/exports
    expect_status: [202]
  - id: poll
    method: GET
    path: /api/v1/status
response:
  decode: json
  events_at: items
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    poll:
      step: poll
`,
			wantMsg: "complete_when is required",
		},
		{
			name: "duplicate_cache_storein",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: login
    method: POST
    path: /api/v1/login
    cache:
      store_in: session
      expiry_field: expires_in
      expiry_buffer: 60s
  - id: refresh
    method: POST
    path: /api/v1/refresh
    cache:
      store_in: session
      expiry_field: expires_in
      expiry_buffer: 60s
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "duplicates requests[0].cache.store_in",
		},
		{
			name: "oauth2_expiry_buffer_invalid_duration",
			doc: `ir_version: "1"
state:
  fields:
    token_url:
      type: url
      default: "http://x/token"
    client_id:
      type: string
      default: "id"
    client_secret:
      type: secret
      default: "s"
auth:
  oauth2:
    client_credentials:
      token_url: {ref: state.token_url}
      client_id: {ref: state.client_id}
      client_secret: {ref: state.client_secret}
      cache:
        store_in: token
        expiry_field: expires_in
        expiry_buffer: "not-a-dur"
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "valid Go duration",
		},
		{
			name: "fanout_as_shadows_state",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: list
    method: GET
    path: /api/v1/incidents
  - method: GET
    path:
      concat:
        - /api/v1/incidents/
        - {ref: state.x}
        - /details
    fan_out:
      over: {ref: steps.list.body.incidents}
      as: state
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "shadows the reserved namespace",
		},
		{
			name: "fanout_as_shadows_item",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: list
    method: GET
    path: /api/v1/incidents
  - method: GET
    path:
      concat:
        - /api/v1/incidents/
        - {ref: item.x}
        - /details
    fan_out:
      over: {ref: steps.list.body.incidents}
      as: item
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "shadows the reserved namespace",
		},
		{
			name: "from_pagination_role_rejected_on_link_header",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    query:
      tok: {from_pagination: token}
response:
  decode: json
  events_at: events
pagination:
  link_header:
    rel: next
progress:
  stateless: {}
`,
			wantMsg: "does not match the active pagination strategy \"link_header\"",
		},
		{
			name: "from_pagination_role_rejected_on_next_url_in_body",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    query:
      tok: {from_pagination: token}
response:
  decode: json
  events_at: events
pagination:
  next_url_in_body:
    next_url_at: meta.next
progress:
  stateless: {}
`,
			wantMsg: "does not match the active pagination strategy \"next_url_in_body\"",
		},
		{
			name: "oauth2_deferred_grant_hints",
			doc: `ir_version: "1"
auth:
  oauth2:
    jwt_bearer:
      issuer: foo
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "deferred",
		},
		{
			name: "fanout_as_shadows_earlier_extract",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: list
    method: GET
    path: /api/v1/incidents
    extract:
      - name: page_token
        path: next
        target: extract
  - method: GET
    path:
      concat:
        - /api/v1/incidents/
        - {ref: page_token.id}
        - /details
    fan_out:
      over: {ref: steps.list.body.incidents}
      as: page_token
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "collides with an extract name in scope",
		},
		{
			name: "state_values_on_non_enum",
			doc: `ir_version: "1"
state:
  fields:
    mode:
      type: string
      values: [a, b]
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "values is only valid when type: enum",
		},
		{
			name: "expect_status_out_of_range",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    expect_status: [20]
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "out of HTTP range",
		},
		{
			name: "expect_status_duplicate",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    expect_status: [200, 200]
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "is duplicated",
		},
		{
			name: "produces_events_on_head",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: HEAD
    path: /api/v1/events
    produces_events: true
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "HEAD has no response body",
		},
		{
			name: "produces_events_implicit_last_head",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: HEAD
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "HEAD has no response body",
		},
		{
			name: "cursor_update_scalar_rejected",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/submit
  - id: poll
    method: GET
    path: /api/v1/status
  - id: fetch
    method: GET
    path: /api/v1/result
    produces_events: true
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    poll:
      step: poll
      complete_when: {eq: {path: cursor.status, equal: "done"}}
    fetch:
      step: fetch
    on_complete:
      cursor_update: use_now
`,
			wantMsg: "scalar shorthand is not accepted",
		},
		{
			name: "cursor_update_stateless_lookback_rejected",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/submit
  - id: poll
    method: GET
    path: /api/v1/status
  - id: fetch
    method: GET
    path: /api/v1/result
    produces_events: true
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    poll:
      step: poll
      complete_when: {eq: {path: cursor.status, equal: "done"}}
    fetch:
      step: fetch
    on_complete:
      cursor_update:
        kind: stateless
        lookback: "-5m"
`,
			wantMsg: "lookback is meaningless when kind is 'stateless'",
		},
		{
			name: "fan_out_over_literal_string_rejected",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: list
    method: GET
    path: /api/v1/things
  - method: GET
    path: /api/v1/details
    fan_out:
      over: items
      as: it
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "fan_out.over must resolve to a list",
		},
		{
			name: "fan_out_over_object_rejected",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: list
    method: GET
    path: /api/v1/things
  - method: GET
    path: /api/v1/details
    fan_out:
      over: {object: {k: v}}
      as: it
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "fan_out.over must resolve to a list",
		},
		{
			name: "on_status_code_out_of_range",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    on_status:
      99: skip
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "out of HTTP range",
		},
		{
			name: "on_status_unknown_action",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    on_status:
      429: backoff
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "unknown on_status action",
		},
		{
			name: "async_job_implicit_producer_head_rejected",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/submit
  - id: fetch
    method: HEAD
    path: /api/v1/result
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    fetch:
      step: fetch
`,
			wantMsg: "implicit producer for async_job",
		},
		{
			name: "from_progress_strategy_mismatch_window_on_latest",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    query:
      since: {from_progress: window_start}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  latest_event_timestamp:
    event_time:
      path: ts
`,
			wantMsg: "does not match the active progress strategy",
		},
		{
			name: "from_progress_strategy_mismatch_latest_on_window",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: POST
    path: /api/v1/search
    body:
      json:
        since: {from_progress: latest_timestamp}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  time_window:
    initial_offset: "24h"
`,
			wantMsg: "does not match the active progress strategy",
		},
		{
			name: "from_progress_role_rejected_on_stateless",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    query:
      since: {from_progress: latest_timestamp}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "does not match the active progress strategy",
		},
		{
			name: "from_progress_unknown_role",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    query:
      since: {from_progress: bogus}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  latest_event_timestamp:
    event_time:
      path: ts
`,
			wantMsg: "unknown progress role",
		},
		{
			name: "body_ref_outside_complete_when",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    query:
      since: {ref: body.timestamp}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "body namespace is only valid inside complete_when predicates",
		},
		{
			name: "steps_ref_missing_body_segment",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: login
    method: POST
    path: /api/v1/login
  - method: GET
    path: /api/v1/events
    query:
      token: {ref: steps.login}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "steps ref must include the body segment",
		},
		{
			name: "steps_ref_wrong_second_segment",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: login
    method: POST
    path: /api/v1/login
  - method: GET
    path: /api/v1/events
    query:
      token: {ref: steps.login.notbody.token}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`,
			wantMsg: "steps ref second segment must be \"body\"",
		},
		{
			name: "time_window_initial_offset_omitted",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: POST
    path: /api/v1/search
    body:
      json:
        since: {from_progress: window_start}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  time_window:
    format: rfc3339
`,
			wantMsg: "initial_offset is required",
		},
		{
			name: "time_window_format_unknown_verb",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - method: POST
    path: /api/v1/search
    body:
      json:
        since: {from_progress: window_start}
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  time_window:
    initial_offset: "24h"
    format: rcf3339
`,
			wantMsg: "unknown format verb",
		},
		{
			name: "cursor_update_unknown_kind",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/submit
  - id: poll
    method: GET
    path: /api/v1/status
  - id: fetch
    method: GET
    path: /api/v1/result
    produces_events: true
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    poll:
      step: poll
      complete_when: {eq: {path: cursor.status, equal: "done"}}
    fetch:
      step: fetch
    on_complete:
      cursor_update:
        kind: bogus
`,
			wantMsg: "unknown kind",
		},
		{
			name: "cursor_update_latest_event_timestamp_requires_event_time",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/submit
  - id: poll
    method: GET
    path: /api/v1/status
  - id: fetch
    method: GET
    path: /api/v1/result
    produces_events: true
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    poll:
      step: poll
      complete_when: {eq: {path: cursor.status, equal: "done"}}
    fetch:
      step: fetch
    on_complete:
      cursor_update:
        kind: latest_event_timestamp
`,
			wantMsg: "event_time is required",
		},
		{
			name: "cursor_update_use_now_rejects_event_time",
			doc: `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/submit
  - id: poll
    method: GET
    path: /api/v1/status
  - id: fetch
    method: GET
    path: /api/v1/result
    produces_events: true
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    poll:
      step: poll
      complete_when: {eq: {path: cursor.status, equal: "done"}}
    fetch:
      step: fetch
    on_complete:
      cursor_update:
        kind: use_now
        event_time:
          path: created_at
`,
			wantMsg: "event_time is meaningful only when kind is 'latest_event_timestamp'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := schema.Parse([]byte(tc.doc))
			if err != nil {
				if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("parse error %q does not contain %q", err.Error(), tc.wantMsg)
				}
				return
			}
			diags := schema.Validate(doc)
			found := false
			var allMsgs []string
			for _, d := range diags {
				if d.Severity != "error" {
					continue
				}
				allMsgs = append(allMsgs, d.Message)
				if tc.wantMsg == "" || strings.Contains(d.Message, tc.wantMsg) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a validation error containing %q, got: %v", tc.wantMsg, allMsgs)
			}
		})
	}
}

// TestSchemaShapes_Review03 covers structural acceptance and validation for
// every schema-shape addition landed by review-03: per-iteration progress
// lookback, predicate comparison verbs, OAuth2 password_grant, and the
// extended on_status action set.
func TestSchemaShapes_Review03(t *testing.T) {
	t.Run("use_now_lookback_accepted", func(t *testing.T) {
		src := `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  use_now:
    lookback: "-1m"
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if diags := schema.Validate(doc); len(diags) > 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		if doc.Progress.UseNow == nil || doc.Progress.UseNow.Lookback == nil {
			t.Fatalf("expected UseNow.Lookback to be set, got %+v", doc.Progress.UseNow)
		}
	})

	t.Run("latest_event_timestamp_lookback_accepted", func(t *testing.T) {
		src := `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  latest_event_timestamp:
    event_time:
      path: ts
    initial:
      lookback: "24h"
    lookback: "-30s"
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if diags := schema.Validate(doc); len(diags) > 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		if doc.Progress.LatestEventTimestamp.Lookback == nil {
			t.Fatalf("expected per-iteration Lookback to be set")
		}
		if doc.Progress.LatestEventTimestamp.Initial == nil {
			t.Fatalf("expected Initial to be set alongside Lookback")
		}
	})

	t.Run("predicate_gt_in_request_if", func(t *testing.T) {
		src := `ir_version: "1"
state:
  fields:
    threshold:
      type: int
      default: 5
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    if:
      gt:
        path: state.threshold
        equal: 0
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if diags := schema.Validate(doc); len(diags) > 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		if doc.Requests[0].If == nil || doc.Requests[0].If.Gt == nil {
			t.Fatalf("expected If.Gt to be set, got %+v", doc.Requests[0].If)
		}
	})

	t.Run("oauth2_password_grant_accepted", func(t *testing.T) {
		src := `ir_version: "1"
state:
  fields:
    token_url:
      type: url
      default: "http://x/token"
    username:
      type: string
      default: "u"
    password:
      type: secret
      default: "p"
auth:
  oauth2:
    password_grant:
      token_url: {ref: state.token_url}
      username: {ref: state.username}
      password: {ref: state.password}
      cache:
        store_in: token
        expiry_field: expires_in
        expiry_buffer: 60s
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if diags := schema.Validate(doc); len(diags) > 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		if doc.Auth.OAuth2 == nil || doc.Auth.OAuth2.PasswordGrant == nil {
			t.Fatalf("expected PasswordGrant to be set, got %+v", doc.Auth.OAuth2)
		}
		// The cache store_in auto-registers as a runtime state field.
		if _, ok := doc.State.Fields["token"]; !ok {
			t.Errorf("expected password_grant cache store_in %q to auto-register in state.fields", "token")
		}
	})

	t.Run("oauth2_two_grants_rejected", func(t *testing.T) {
		src := `ir_version: "1"
state:
  fields:
    token_url:
      type: url
      default: "http://x/token"
    s:
      type: secret
      default: "x"
auth:
  oauth2:
    client_credentials:
      token_url: {ref: state.token_url}
      client_id: id
      client_secret: {ref: state.s}
    password_grant:
      token_url: {ref: state.token_url}
      username: u
      password: {ref: state.s}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		diags := schema.Validate(doc)
		found := false
		for _, d := range diags {
			if d.Severity == "error" && strings.Contains(d.Message, "exactly one grant") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected 'exactly one grant' error; got %+v", diags)
		}
	})

	t.Run("on_status_actions_all_accepted", func(t *testing.T) {
		src := `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
    on_status:
      304: skip
      416: fail
      429: empty_events
      401: invalidate_cache
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  stateless: {}
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if diags := schema.Validate(doc); len(diags) > 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
	})

	t.Run("from_progress_async_job_no_constraint", func(t *testing.T) {
		// async_job is the graceful-fallback case for the from_progress
		// role/strategy cross-check: cursor.last_timestamp may be provided
		// by on_complete.cursor_update.kind, which the IR cannot resolve
		// statically. {from_progress: latest_timestamp} must validate
		// cleanly under async_job.
		src := `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/exports
    body:
      json:
        since: {from_progress: latest_timestamp}
    expect_status: [202]
  - id: poll
    method: GET
    path: /api/v1/status
    produces_events: true
response:
  decode: json
  events_at: items
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
    poll:
      step: poll
      complete_when: {literal_bool: true}
    on_complete:
      cursor_update:
        kind: use_now
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		for _, d := range schema.Validate(doc) {
			if d.Severity == "error" {
				t.Errorf("unexpected error diagnostic: %s: %s", d.Path, d.Message)
			}
		}
	})

	t.Run("async_job_implicit_producer_submit_only", func(t *testing.T) {
		// async_job with only submit declared: submit step is the implicit
		// events producer. Non-HEAD method is acceptable; validate cleanly.
		src := `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    path: /api/v1/exports
response:
  decode: json
  events_at: events
pagination:
  none: {}
progress:
  async_job:
    submit:
      step: submit
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		// We only care that the producer check accepts submit-as-producer.
		// Other rules may fail (e.g. complete_when), so we accept any
		// non-empty diagnostic list as long as it does NOT mention the
		// HEAD/producer rejection.
		diags := schema.Validate(doc)
		for _, d := range diags {
			if strings.Contains(d.Message, "implicit producer") {
				t.Errorf("submit-only async_job should not trigger producer rejection; got %q", d.Message)
			}
		}
	})
}
