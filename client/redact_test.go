// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestSafeURL pins the URL redaction shape: scheme + host + path only.
// Query strings and userinfo are stripped because either can carry
// secrets (api_key in_query=true, secret-typed refs).
func TestSafeURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://api.example.com/v1/events?api_key=SECRET", "https://api.example.com/v1/events"},
		{"https://user:hunter2@api.example.com/v1/events", "https://api.example.com/v1/events"},
		{"http://localhost:8080/path", "http://localhost:8080/path"},
	}
	for _, tc := range tests {
		u, err := url.Parse(tc.in)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", tc.in, err)
		}
		if got := safeURL(u); got != tc.want {
			t.Errorf("safeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if got := safeURL(nil); got != "" {
		t.Errorf("safeURL(nil) = %q, want empty string", got)
	}
}

// TestRedactURLError pins the *url.Error rewrite: the embedded URL is
// replaced with safeURL'd form.
func TestRedactURLError(t *testing.T) {
	t.Run("rewrites_url_error", func(t *testing.T) {
		raw := &url.Error{
			Op:  "Get",
			URL: "https://api.example.com/v1/events?api_key=SECRET",
			Err: errors.New("connection refused"),
		}
		got := RedactURLError(raw)
		if strings.Contains(got.Error(), "SECRET") {
			t.Errorf("redacted error still contains SECRET: %v", got)
		}
		if !strings.Contains(got.Error(), "api.example.com") {
			t.Errorf("redacted error should keep host: %v", got)
		}
	})

	t.Run("no_url_error_is_passthrough", func(t *testing.T) {
		raw := errors.New("plain")
		got := RedactURLError(raw)
		if got.Error() != "plain" {
			t.Errorf("non-url-error should pass through; got %v", got)
		}
	})

	t.Run("nil_error_returns_nil", func(t *testing.T) {
		if got := RedactURLError(nil); got != nil {
			t.Errorf("nil error should return nil; got %v", got)
		}
	})
}

// TestRedactValue pins the IR-shape rendering for non-secret Values and
// the <redacted> token for secret-tainted ones.
func TestRedactValue(t *testing.T) {
	src := `ir_version: "1"
state:
  api_key: {type: secret}
  public:  {type: string}
auth:
  none: {}
requests:
  - method: GET
    url: "http://x/y"
    events_at: response.body.events
pagination:
  none: {}
`
	doc, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	t.Run("public_ref_renders_shape", func(t *testing.T) {
		v := vRef("state.public")
		if got := redactValue(doc, v); !strings.Contains(got, "state.public") {
			t.Errorf("redactValue(public) = %q, want shape including state.public", got)
		}
	})

	t.Run("secret_ref_redacted", func(t *testing.T) {
		v := vRef("state.api_key")
		if got := redactValue(doc, v); got != "<redacted>" {
			t.Errorf("redactValue(secret) = %q, want <redacted>", got)
		}
	})

	t.Run("concat_with_secret_redacted", func(t *testing.T) {
		v := schema.Value{Concat: []schema.Value{vStr("prefix "), vRef("state.api_key")}}
		if got := redactValue(doc, v); got != "<redacted>" {
			t.Errorf("redactValue(concat with secret) = %q, want <redacted>", got)
		}
	})
}

// TestValueShape pins the structure-only renderer used outside the
// redact path.
func TestValueShape(t *testing.T) {
	cases := []struct {
		name string
		v    schema.Value
		want string
	}{
		{"literal_string", vStr("hello"), `"hello"`},
		{"literal_int", vInt(42), "42"},
		{"literal_bool", vBool(true), "true"},
		{"ref", vRef("state.x"), "<ref state.x>"},
		{"now", vNow(), "<now>"},
		{"concat", schema.Value{Concat: []schema.Value{vStr("a"), vStr("b")}}, "<concat>"},
		{"format", schema.Value{Format: &schema.FormatValue{Verb: "rfc3339"}}, "<format:rfc3339>"},
		{"base64", schema.Value{Base64: new(vStr("x"))}, "<base64>"},
		{"list", schema.Value{List: []schema.Value{vStr("a")}}, "<list>"},
		{"object", schema.Value{Object: map[string]schema.Value{"k": vStr("v")}}, "<object>"},
		{"add", schema.Value{Add: &schema.ArithExpr{Operands: []schema.Value{vInt(1), vInt(2)}}}, "<add>"},
		{"zero", schema.Value{IsZero: true}, "<zero>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := valueShape(tc.v); got != tc.want {
				t.Errorf("valueShape(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
