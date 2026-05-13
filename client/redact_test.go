// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestSafeURL asserts the URL sanitizer keeps scheme/host/path and drops
// userinfo + query — the two places either Go's net/http or the runner's
// IR can plant a secret.
func TestSafeURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "https://api.example.com/v1/events", "https://api.example.com/v1/events"},
		{"query-secret", "https://api.example.com/v1/events?api_key=super-secret&since=2026", "https://api.example.com/v1/events"},
		{"userinfo", "https://user:hunter2@api.example.com/v1", "https://api.example.com/v1"},
		{"query+userinfo", "https://user:hunter2@api.example.com/v1?token=t", "https://api.example.com/v1"},
		{"path-only", "/api/v1/events?cursor=abc", "/api/v1/events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.in)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.in, err)
			}
			got := safeURL(u)
			if got != tc.want {
				t.Errorf("safeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.Contains(got, "super-secret") || strings.Contains(got, "hunter2") || strings.Contains(got, "token=") || strings.Contains(got, "api_key=") {
				t.Errorf("safeURL(%q) leaked credential: %q", tc.in, got)
			}
		})
	}
}

func TestSafeURL_Nil(t *testing.T) {
	got := safeURL(nil)
	if got != "<nil-url>" {
		t.Errorf("safeURL(nil) = %q, want \"<nil-url>\"", got)
	}
}

// TestRedactURLError feeds redactURLError representative *url.Error
// instances and asserts the rewritten message no longer contains the
// query-embedded secret.
func TestRedactURLError(t *testing.T) {
	secret := "super-secret-token-abc"
	raw := "https://api.example.com/v1/events?api_key=" + secret + "&since=2026"
	ue := &url.Error{Op: "Get", URL: raw, Err: errors.New("dial tcp: lookup failed")}

	got := redactURLError(ue)
	if got == nil {
		t.Fatal("redactURLError(*url.Error) = nil")
	}
	if strings.Contains(got.Error(), secret) {
		t.Errorf("redacted error still contains secret: %q", got.Error())
	}
	if !strings.Contains(got.Error(), "api.example.com") {
		t.Errorf("redacted error dropped host: %q", got.Error())
	}
}

// TestRedactURLError_Wrapped exercises the case where *url.Error is
// nested inside a fmt.Errorf %w chain. redactURLError must still locate
// it and rewrite the message.
func TestRedactURLError_Wrapped(t *testing.T) {
	secret := "tok-XYZ-1234"
	ue := &url.Error{Op: "Get", URL: "https://api.example.com/v1/x?k=" + secret, Err: errors.New("EOF")}
	wrapped := fmt.Errorf("http.Do: %w", ue)
	wrappedAgain := fmt.Errorf("step1: %w", wrapped)

	got := redactURLError(wrappedAgain)
	if got == nil {
		t.Fatal("redactURLError(nested) = nil")
	}
	if strings.Contains(got.Error(), secret) {
		t.Errorf("nested redacted error still contains secret: %q", got.Error())
	}
}

// TestRedactURLError_NonURLError must pass through unchanged: there is
// no URL to rewrite.
func TestRedactURLError_NonURLError(t *testing.T) {
	err := errors.New("plain error: no URL here")
	got := redactURLError(err)
	if got.Error() != err.Error() {
		t.Errorf("redactURLError mutated non-URL error: %q", got.Error())
	}
}

func TestRedactURLError_Nil(t *testing.T) {
	if got := redactURLError(nil); got != nil {
		t.Errorf("redactURLError(nil) = %v, want nil", got)
	}
}

// TestRedactValue exercises redactValue's secret-aware rendering. A Value
// that resolves to a secret-typed state field renders as "<redacted>";
// non-secret Values render as a structural summary (variant + path
// hints), never the resolved runtime string.
func TestRedactValue(t *testing.T) {
	doc := &schema.Doc{
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"api_key": {Type: "secret", Default: "leak-me"},
			"region":  {Type: "string", Default: "us-east-1"},
		}},
	}

	secretRef := vRef("state.api_key")
	if got := redactValue(doc, secretRef); got != "<redacted>" {
		t.Errorf("redactValue(secret ref) = %q, want \"<redacted>\"", got)
	}

	regionRef := vRef("state.region")
	if got := redactValue(doc, regionRef); got != "<ref state.region>" {
		t.Errorf("redactValue(region ref) = %q, want \"<ref state.region>\"", got)
	}

	// Compositions that transitively reach a secret are also redacted.
	concat := vConcat(vStr("Bearer "), secretRef)
	if got := redactValue(doc, concat); got != "<redacted>" {
		t.Errorf("redactValue(concat with secret) = %q, want \"<redacted>\"", got)
	}

	// A non-secret literal renders its shape but never resolves at this
	// layer — redactValue must never call evalValue (state is per-scope).
	if got := redactValue(doc, vStr("/api/v1/events")); got != `"/api/v1/events"` {
		t.Errorf("redactValue(literal) = %q, want quoted literal", got)
	}
}

// TestRedactValue_ShapeOnly asserts valueShape never surfaces the
// runtime value of a Ref — only the declared path.
func TestRedactValue_ShapeOnly(t *testing.T) {
	tests := []struct {
		name string
		v    schema.Value
		want string
	}{
		{"zero", schema.Value{IsZero: true}, "<zero>"},
		{"now", schema.Value{Now: &schema.NowValue{}}, "<now>"},
		{"from_pagination", vFromPag("token"), "<from_pagination:token>"},
		{"from_progress", vFromProg("latest_timestamp"), "<from_progress:latest_timestamp>"},
		{"format", vFormat("rfc3339", vRef("cursor.last_timestamp")), "<format:rfc3339>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := valueShape(tc.v)
			if got != tc.want {
				t.Errorf("valueShape = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDrain_APIKeyInQuery_DoesNotLeak drives a doc whose secret-typed
// state field is injected into the URL as auth.api_key.in_query=true,
// then makes the server refuse the request. The runner's error chain
// MUST NOT contain the secret value.
func TestDrain_APIKeyInQuery_DoesNotLeak(t *testing.T) {
	const secret = "ultra-secret-api-key-9000"

	// Server that 500s every request so the runner hits error.mode=fail.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: server.URL},
			"api_key": {Type: "secret", Default: secret},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth: schema.Auth{APIKey: &schema.APIKeyAuth{
			Header:  "api_key",
			InQuery: true,
			Value:   vRef("state.api_key"),
		}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
		Error:      &schema.ErrorBlock{Mode: "fail"},
	}

	r := &Runner{
		Doc:    doc,
		Sink:   &captureSink{},
		Now:    fixedNow(),
		Client: server.Client(),
	}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain returned nil; expected error.mode=fail to surface 500")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Drain error leaked api_key value: %q", err.Error())
	}
}

// TestDrain_TransportError_RedactsQuerySecret simulates a transport-level
// failure (server closed, refused) so client.Do returns a *url.Error
// whose embedded URL carries the secret. The returned error must not
// contain the secret.
func TestDrain_TransportError_RedactsQuerySecret(t *testing.T) {
	const secret = "transport-leak-token-42"

	// Start then immediately close so Do() fails at connect.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := server.URL
	server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: addr},
			"api_key": {Type: "secret", Default: secret},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth: schema.Auth{APIKey: &schema.APIKeyAuth{
			Header:  "api_key",
			InQuery: true,
			Value:   vRef("state.api_key"),
		}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
		Error:      &schema.ErrorBlock{Mode: "fail"},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain returned nil; expected transport error against closed server")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Drain error leaked api_key value: %q", err.Error())
	}
	// The redacted host should still surface — operators need to know
	// WHICH endpoint failed; the secret is the only thing that's lost.
	if !strings.Contains(err.Error(), "127.0.0.1") && !strings.Contains(err.Error(), "localhost") {
		t.Logf("note: redacted error = %q (host not asserted; httptest may use different loopback)", err.Error())
	}
}

// TestRFC3339ParseErrorDoesNotLeakValue asserts the rfc3339-parse error
// no longer embeds the input string verbatim — a regression test against
// the previous "cannot parse %q as RFC 3339" formulation, which would
// surface a secret-typed state field's runtime value when piped through
// format:rfc3339.
func TestRFC3339ParseErrorDoesNotLeakValue(t *testing.T) {
	const secret = "sensitive-not-a-timestamp"
	_, err := toTime(secret)
	if err == nil {
		t.Fatal("toTime(non-timestamp) = nil; expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("toTime error leaked input: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "RFC 3339") {
		t.Errorf("toTime error = %q, want RFC 3339 mention", err.Error())
	}
}
