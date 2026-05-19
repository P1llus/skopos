// SPDX-License-Identifier: Apache-2.0

package schema_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/p1llus/skopos/schema"
)

// TestSpecFixtures is the package-level golden suite: every YAML under
// schema/testdata/ (validator-surface fixtures) and templates/
// (user-facing templates) must Parse cleanly, Validate without error-
// severity diagnostics, and round-trip byte-stably through both YAML
// and JSON.
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
			if e.IsDir() || filepath.Ext(e.Name()) != ".yml" {
				continue
			}
			name := e.Name()
			t.Run(r.label+"/"+name, func(t *testing.T) {
				path := filepath.Join(r.dir, name)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("reading %s: %v", path, err)
				}

				doc, err := schema.Parse(data)
				if err != nil {
					t.Fatalf("Parse(%s): %v", path, err)
				}

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

				roundTripYAML(t, doc, path)
				roundTripJSON(t, doc, path)
			})
		}
	}
}

// roundTripYAML asserts: doc → marshal → parse → marshal → parse produces
// a structurally identical Doc and byte-identical output across both
// re-marshals.
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

// TestParseRejectsEmpty pins the empty-input rejection.
func TestParseRejectsEmpty(t *testing.T) {
	_, err := schema.Parse([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// TestParseRejectsBadVersion pins the ir_version mismatch rejection.
func TestParseRejectsBadVersion(t *testing.T) {
	bad := []byte(`ir_version: "0"
auth: {none: {}}
requests:
  - method: GET
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`)
	_, err := schema.Parse(bad)
	if err == nil {
		t.Fatal("expected error for bad ir_version, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported ir_version") {
		t.Errorf("error should mention ir_version, got: %v", err)
	}
}

// TestParseJSON pins the JSON-format detection: a leading '{' selects the
// JSON decoder.
func TestParseJSON(t *testing.T) {
	data := []byte(`{
  "ir_version": "1",
  "auth": {"none": {}},
  "requests": [{"method": "GET", "url": "http://x/y"}],
  "response": {"decode": "json", "events_at": "response.body.events"},
  "pagination": {"none": {}}
}`)
	doc, err := schema.Parse(data)
	if err != nil {
		t.Fatalf("Parse JSON: %v", err)
	}
	if doc.IRVersion != "1" {
		t.Errorf("expected ir_version 1, got %q", doc.IRVersion)
	}
	for _, d := range schema.Validate(doc) {
		if d.Severity == "error" {
			t.Errorf("unexpected validate error: %s", d.Message)
		}
	}
}

// TestPathParsing covers the dotted-string codec and the rejected legacy
// roots (cursor, body, item — removed in the redesign).
func TestPathParsing(t *testing.T) {
	tests := []struct {
		input   string
		wantStr string
		wantErr bool
	}{
		{"state.api_key", "state.api_key", false},
		{"response.body.data.issues.nodes", "response.body.data.issues.nodes", false},
		{"steps.login.body.session_id", "steps.login.body.session_id", false},
		{"events.last.timestamp", "events.last.timestamp", false},
		{"", "", false},
		{".bad", "", true},
		{"a..b", "", true},
		{"cursor.last_timestamp", "", true},
		{"body.something", "", true},
		{"item.id", "", true},
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

// TestPathEscapesDottedSegments pins the {parts: [...]} escape form for
// segments containing '.' (step ids built from external identifiers).
func TestPathEscapesDottedSegments(t *testing.T) {
	p := schema.Path{Parts: []string{"steps", "my.weird.id", "body"}}
	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), "parts:") {
		t.Errorf("expected {parts: ...} form for dotted segment; got: %s", out)
	}
	var p2 schema.Path
	if err := yaml.Unmarshal(out, &p2); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if !reflect.DeepEqual(p2.Parts, p.Parts) {
		t.Errorf("parts mismatch: %v vs %v", p2.Parts, p.Parts)
	}
}

// TestValueCodecs round-trips one representative of every Value form.
func TestValueCodecs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"literal_string", `"hello"`},
		{"literal_int", `42`},
		{"literal_bool", `true`},
		{"ref_simple", `{ref: state.api_key}`},
		{"ref_default", `{ref: state.api_key, default: ""}`},
		{"now", `{now: true}`},
		{"concat", `{concat: ["/api/", {ref: state.url}, "/v1"]}`},
		{"object", `{object: {key: value, n: 5, b: true}}`},
		{"format", `{format: rfc3339, value: {now: true}}`},
		{"base64", `{base64: hello}`},
		{"list", `{list: [1, 2, 3]}`},
		{"select", `{select: {branches: [{when: {literal_bool: true}, value: /gov}], default: /commercial}}`},
		{"add", `{add: [{now: true}, 1h]}`},
		{"subtract", `{subtract: [{now: true}, 1h]}`},
		{"max", `{max: [{ref: state.a}, {ref: state.b}]}`},
		{"min", `{min: [{ref: state.a}, {ref: state.b}]}`},
		{"first", `{first: [{ref: state.a}, {ref: state.b}]}`},
		{"last", `{last: [{ref: state.a}, {ref: state.b}]}`},
		{"count", `{count: [{ref: state.a}, {ref: state.b}]}`},
		{"regex", `{regex: {pattern: "v(\\d+)", from: {ref: state.tag}, capture: 1}}`},
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

// TestPredicateCodecs round-trips one representative of every Predicate
// form.
func TestPredicateCodecs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"eq", `{eq: {path: state.auth_mode, value: bearer}}`},
		{"gt", `{gt: {path: state.retries, value: 3}}`},
		{"lt", `{lt: {path: state.retries, value: 3}}`},
		{"gte", `{gte: {path: state.retries, value: 3}}`},
		{"lte", `{lte: {path: state.retries, value: 3}}`},
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

// TestCodecRejections covers the codec's structural rejections — typos,
// removed forms, multi-discriminator mappings.
func TestCodecRejections(t *testing.T) {
	t.Run("bare_unrecognised_mapping_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{some: thing, other: stuff}`), &v)
		if err == nil {
			t.Fatalf("expected error for bare unrecognised mapping, got value: %+v", v)
		}
		if !strings.Contains(err.Error(), "object") {
			t.Errorf("error should mention {object: ...} wrapper hint, got: %v", err)
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
		err := yaml.Unmarshal([]byte(`{eq: {path: state.x, value: 1}, present: state.x}`), &p)
		if err == nil {
			t.Fatalf("expected error for multi-key Predicate, got: %+v", p)
		}
	})

	t.Run("empty_concat_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{concat: []}`), &v)
		if err == nil {
			t.Fatalf("expected error for {concat: []}, got: %+v", v)
		}
	})

	t.Run("singleton_concat_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{concat: ["only"]}`), &v)
		if err == nil {
			t.Fatalf("expected error for singleton concat, got: %+v", v)
		}
	})

	t.Run("empty_and_rejected", func(t *testing.T) {
		var p schema.Predicate
		err := yaml.Unmarshal([]byte(`{and: []}`), &p)
		if err == nil {
			t.Fatalf("expected error for {and: []}, got: %+v", p)
		}
	})

	t.Run("ref_typo_sibling_rejected", func(t *testing.T) {
		var v schema.Value
		err := yaml.Unmarshal([]byte(`{ref: state.x, defualt: "y"}`), &v)
		if err == nil {
			t.Fatalf("expected error for ref+typo sibling, got: %+v", v)
		}
		if !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("error should call out unknown key, got: %v", err)
		}
	})

	t.Run("zero_value_marshal_errors", func(t *testing.T) {
		_, err := yaml.Marshal(schema.Value{})
		if err == nil {
			t.Fatalf("expected MarshalYAML error for zero Value, got nil")
		}
		if !strings.Contains(err.Error(), "zero value") {
			t.Errorf("error should mention zero value, got: %v", err)
		}
	})

	t.Run("zero_value_marshal_json_errors", func(t *testing.T) {
		_, err := json.Marshal(schema.Value{})
		if err == nil {
			t.Fatalf("expected MarshalJSON error for zero Value, got nil")
		}
	})

	t.Run("legacy_cursor_root_rejected", func(t *testing.T) {
		_, err := schema.ParsePath("cursor.last_timestamp")
		if err == nil {
			t.Fatal("expected ParsePath rejection for cursor.* root")
		}
		if !strings.Contains(err.Error(), "cursor") {
			t.Errorf("error should mention cursor root, got: %v", err)
		}
	})
}

// TestArithOperandCount pins the {add/subtract: [...]} exact-2 operand
// rule.
func TestArithOperandCount(t *testing.T) {
	bad := []string{
		`{add: []}`,
		`{add: [1]}`,
		`{add: [1, 2, 3]}`,
		`{subtract: [1]}`,
	}
	for _, src := range bad {
		t.Run(src, func(t *testing.T) {
			var v schema.Value
			if err := yaml.Unmarshal([]byte(src), &v); err == nil {
				t.Errorf("expected error parsing %q, got value %+v", src, v)
			}
		})
	}
}

// TestStringInterpolationDesugar pins the source-level interpolation
// behaviour: any string scalar in a Value position is scanned for
// ${path[|default]} segments and desugared into a Concat (or a single
// Ref when the segment is the whole string).
func TestStringInterpolationDesugar(t *testing.T) {
	t.Run("scalar_with_no_dollar_is_literal", func(t *testing.T) {
		var v schema.Value
		if err := yaml.Unmarshal([]byte(`"/api/v1/events"`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v.LiteralString == nil || *v.LiteralString != "/api/v1/events" {
			t.Errorf("expected literal string, got %+v", v)
		}
	})

	t.Run("single_segment_becomes_ref", func(t *testing.T) {
		var v schema.Value
		if err := yaml.Unmarshal([]byte(`"${state.url}"`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v.Ref == nil {
			t.Fatalf("expected Ref form, got %+v", v)
		}
		if v.Ref.Path.String() != "state.url" {
			t.Errorf("Ref.Path = %q, want state.url", v.Ref.Path.String())
		}
	})

	t.Run("multi_segment_becomes_concat", func(t *testing.T) {
		var v schema.Value
		if err := yaml.Unmarshal([]byte(`"${state.url}/path"`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(v.Concat) != 2 {
			t.Fatalf("expected 2-element Concat, got %+v", v)
		}
	})

	t.Run("default_after_pipe", func(t *testing.T) {
		var v schema.Value
		if err := yaml.Unmarshal([]byte(`"${state.cursor|}"`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v.Ref == nil || v.Ref.Default == nil {
			t.Fatalf("expected Ref with default, got %+v", v)
		}
	})
}

// TestIsSecret exercises the secret-propagation predicate. It walks every
// container the Value language can express and reports whether any
// reachable Ref resolves to a state.<name> path whose declared type is
// "secret".
func TestIsSecret(t *testing.T) {
	src := `ir_version: "1"
state:
  api_key: {type: secret}
  public:  {type: string}
auth:
  bearer:
    token: {ref: state.api_key}
requests:
  - method: GET
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
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
		{"base64_wrapping_secret", schema.Value{Base64: &secretRef}, true},
		{"list_with_secret", schema.Value{List: []schema.Value{literal("a"), secretRef}}, true},
		{"object_with_secret", schema.Value{Object: map[string]schema.Value{"k": secretRef}}, true},
		{"ref_with_secret_default", schema.Value{Ref: &schema.RefValue{Path: schema.Path{Parts: []string{"state", "public"}}, Default: &secretRef}}, true},
		{"add_with_secret_operand", schema.Value{Add: &schema.ArithExpr{Operands: []schema.Value{literal("a"), secretRef}}}, true},
		{"max_with_secret_operand", schema.Value{Max: &secretRef}, true},
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

// TestValidate_RejectsRemovedForms confirms that legacy shapes (cursor.*
// roots, state.fields indirection, defaults.base_url, requests[].path)
// no longer parse or validate cleanly. The validator is the operator's
// safety net against stale templates that pre-date the redesign.
func TestValidate_RejectsRemovedForms(t *testing.T) {
	t.Run("state_fields_indirection_rejected", func(t *testing.T) {
		src := `ir_version: "1"
state:
  fields:
    url: {type: url}
auth:
  none: {}
requests:
  - method: GET
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			// Parsing may succeed but validator rejects "fields" as not
			// a typed field declaration.
			return
		}
		diags := schema.Validate(doc)
		hasErr := false
		for _, d := range diags {
			if d.Severity == "error" {
				hasErr = true
				break
			}
		}
		if !hasErr {
			t.Errorf("expected error-severity diagnostic for state.fields indirection; got %v", diags)
		}
	})

	t.Run("requests_missing_url_rejected", func(t *testing.T) {
		// path: is no longer a recognised field — it falls into yaml's
		// unknown-keys-dropped path. The validator then rejects the
		// resulting request because url: is required.
		src := `ir_version: "1"
auth:
  none: {}
requests:
  - method: GET
    path: /api/v1/events
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`
		doc, err := schema.Parse([]byte(src))
		if err != nil {
			return
		}
		diags := schema.Validate(doc)
		urlMissing := false
		for _, d := range diags {
			if d.Severity == "error" && strings.Contains(d.Message, "url") {
				urlMissing = true
				break
			}
		}
		if !urlMissing {
			t.Errorf("expected url-is-required error for legacy path-field shape; got %v", diags)
		}
	})

	t.Run("legacy_v1_version_key_rejected", func(t *testing.T) {
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
	})
}

// TestRequestTerminateWhen covers the request-level loop primitive: the
// shape that backs the three-request submit/poll/fetch async pattern.
func TestRequestTerminateWhen(t *testing.T) {
	src := `ir_version: "1"
auth:
  none: {}
requests:
  - id: submit
    method: POST
    url: "http://x/submit"
    extract:
      - {to: state.export_id, from: response.body.export_id}
  - id: poll
    method: GET
    url: "http://x/status"
    terminate_when:
      eq:
        path: response.body.status
        value: complete
    extract:
      - {to: state.result_url, from: response.body.result_url}
  - id: fetch
    method: GET
    url: {ref: state.result_url}
    produces_events: true
state:
  export_id:  {type: string}
  result_url: {type: url}
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`
	doc, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := schema.Validate(doc)
	for _, d := range diags {
		if d.Severity == "error" {
			t.Errorf("unexpected error diagnostic: %s: %s", d.Path, d.Message)
		}
	}
	poll := doc.Requests[1]
	if poll.TerminateWhen == nil || poll.TerminateWhen.Eq == nil {
		t.Errorf("expected poll.TerminateWhen.Eq to be set; got %+v", poll.TerminateWhen)
	}
}

// TestPaginationVariants pins each named variant's basic shape: cursor_
// token / next_url / counter / custom each parse and validate cleanly
// against a minimal spec.
func TestPaginationVariants(t *testing.T) {
	specs := map[string]string{
		"cursor_token": `pagination:
  cursor_token:
    from: response.body.next_cursor
    to: state.next_token`,
		"next_url": `pagination:
  next_url:
    from: response.header.Link
    to: state.next_url
    regex: '<(.*?)>; rel="next"'
    capture: 1`,
		"counter": `pagination:
  counter:
    to: state.page
    start: 1
    step: 1`,
		"custom": `pagination:
  custom:
    advance:
      - {to: state.cursor, from: {ref: response.body.next}}
    terminate_when:
      not:
        present: response.body.next`,
	}

	header := `ir_version: "1"
state:
  next_token: {type: string}
  next_url:   {type: url}
  page:       {type: int}
  cursor:     {type: string}
auth: {none: {}}
requests:
  - method: GET
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
`

	for name, page := range specs {
		t.Run(name, func(t *testing.T) {
			src := header + page + "\n"
			doc, err := schema.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			diags := schema.Validate(doc)
			for _, d := range diags {
				if d.Severity == "error" {
					t.Errorf("[%s] unexpected error: %s: %s", name, d.Path, d.Message)
				}
			}
			if got, _ := doc.Pagination.Variant(); got != name {
				t.Errorf("Pagination.Variant() = %q, want %q", got, name)
			}
		})
	}
}

// TestProgressIsFlatWriteList confirms the Progress shape is a flat
// []ProgressWrite — no discriminated-union variants, no nested phase
// machinery.
func TestProgressIsFlatWriteList(t *testing.T) {
	src := `ir_version: "1"
state:
  last_timestamp: {type: timestamp}
auth: {none: {}}
requests:
  - method: GET
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
progress:
  - to: state.last_timestamp
    from:
      max:
        - {ref: state.last_timestamp}
        - {max: {ref: events.*.timestamp}}
`
	doc, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := schema.Validate(doc)
	for _, d := range diags {
		if d.Severity == "error" {
			t.Errorf("unexpected diagnostic: %s: %s", d.Path, d.Message)
		}
	}
	if len(doc.Progress) != 1 {
		t.Fatalf("Progress length = %d, want 1", len(doc.Progress))
	}
	w := doc.Progress[0]
	if w.To.String() != "state.last_timestamp" {
		t.Errorf("To = %q", w.To.String())
	}
	if w.From.Max == nil {
		t.Errorf("From should carry a Max reducer; got %+v", w.From)
	}
}

// TestOAuth2_ExactlyOneGrantRequired pins the discriminated-union rule
// on auth.oauth2: exactly one of client_credentials / password_grant.
func TestOAuth2_ExactlyOneGrantRequired(t *testing.T) {
	src := `ir_version: "1"
state:
  token_url: {type: url, default: "http://x/token"}
  s:         {type: secret, default: "x"}
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
    url: "http://x/y"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`
	doc, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := schema.Validate(doc)
	found := false
	for _, d := range diags {
		if d.Severity == "error" && strings.Contains(d.Message, "exactly one") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'exactly one' error; got %+v", diags)
	}
}

// TestUnifiedCacheBlock confirms the post-redesign Cache shape (used by
// both auth.oauth2.*.cache and requests[].cache): {to, expires_at,
// buffer}.
func TestUnifiedCacheBlock(t *testing.T) {
	src := `to: cache.access_token
expires_at: {ref: response.body.expires_in, default: "1h"}
buffer: 60s
`
	var c schema.Cache
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.To.String() != "cache.access_token" {
		t.Errorf("To = %q, want cache.access_token", c.To.String())
	}
	if c.Buffer != "60s" {
		t.Errorf("Buffer = %q, want 60s", c.Buffer)
	}
	if c.ExpiresAt.Ref == nil {
		t.Errorf("ExpiresAt should carry a Ref; got %+v", c.ExpiresAt)
	}
}

// TestMultiModeDefaultBareAuth pins DESIGN §3.3: multi_mode.default is a
// bare Auth value, NOT the wrapped {auth: ...} form. The bare form
// (default: {bearer: ...}) must parse cleanly; the wrapped form
// (default: {auth: {bearer: ...}}) must be rejected because "auth" is
// not a recognised Auth discriminator key.
func TestMultiModeDefaultBareAuth(t *testing.T) {
	const tmpl = `ir_version: "1"
state:
  url: {type: url, default: "http://x"}
  tok: {type: secret, default: "k"}
auth:
  multi_mode:
    branches:
      - when: {literal_bool: true}
        auth: {bearer: {token: {ref: state.tok}}}
    default: %s
requests:
  - method: GET
    url: "${state.url}"
response: {decode: json, events_at: response.body.events}
pagination: {none: {}}
`

	t.Run("bare_form_parses_and_validates", func(t *testing.T) {
		doc, err := schema.Parse([]byte(fmt.Sprintf(tmpl, `{bearer: {token: {ref: state.tok}}}`)))
		if err != nil {
			t.Fatalf("bare-form Parse: %v", err)
		}
		if doc.Auth.MultiMode == nil || doc.Auth.MultiMode.Default.Bearer == nil {
			t.Errorf("bare-form default did not parse as bearer Auth; got %+v", doc.Auth.MultiMode)
		}
		for _, d := range schema.Validate(doc) {
			if d.Severity == "error" {
				t.Errorf("bare-form validate error: %s — %s", d.Path, d.Message)
			}
		}
	})

	t.Run("wrapped_form_rejected_by_validate", func(t *testing.T) {
		// The wrapped form parses silently (the "auth" key is not a
		// recognised Auth discriminator, so the decoder drops it and the
		// Default ends up zero-valued). Validate catches the empty
		// variant set and surfaces the canonical "bare auth value"
		// diagnostic.
		doc, err := schema.Parse([]byte(fmt.Sprintf(tmpl, `{auth: {bearer: {token: {ref: state.tok}}}}`)))
		if err != nil {
			t.Fatalf("wrapped-form Parse (unexpected error): %v", err)
		}
		diags := schema.Validate(doc)
		want := "auth.multi_mode.default"
		found := false
		for _, d := range diags {
			if d.Path == want && strings.Contains(d.Message, "bare auth value") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("wrapped-form Validate did not surface the bare-auth diagnostic at %s; got %d diags: %v", want, len(diags), diags)
		}
	})
}
