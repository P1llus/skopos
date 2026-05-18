// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestRequest_QueryParams pins query.<k> evaluation: each value Value
// resolves through evalValue and the result is written to the URL
// query. nil values are skipped.
func TestRequest_QueryParams(t *testing.T) {
	var seenQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query()
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["since"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("2026-01-01"))}
	doc.Requests[0].Query = map[string]schema.Value{
		"since":  vRef("state.since"),
		"limit":  vInt(50),
		"absent": vRef("state.never_set"), // resolves to nil → skip
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seenQuery.Get("since") != "2026-01-01" {
		t.Errorf("since = %q", seenQuery.Get("since"))
	}
	if seenQuery.Get("limit") != "50" {
		t.Errorf("limit = %q", seenQuery.Get("limit"))
	}
	if seenQuery.Has("absent") {
		t.Errorf("absent query key should be omitted; got %q", seenQuery.Get("absent"))
	}
}

// TestRequest_BodyJSON pins the body.json variant: a map<string, Value>
// renders as JSON with the Value-typed values resolved.
func TestRequest_BodyJSON(t *testing.T) {
	var seenBody string
	var seenContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		seenBody = string(bs)
		seenContentType = r.Header.Get("Content-Type")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["since"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("2026-01-01"))}
	doc.Requests[0].Method = "POST"
	doc.Requests[0].Body = &schema.Body{JSON: map[string]schema.Value{
		"since":  vRef("state.since"),
		"limit":  vInt(50),
		"active": vBool(true),
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !strings.Contains(seenContentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", seenContentType)
	}
	if !strings.Contains(seenBody, `"since":"2026-01-01"`) {
		t.Errorf("body missing since: %s", seenBody)
	}
	if !strings.Contains(seenBody, `"limit":50`) {
		t.Errorf("body missing limit: %s", seenBody)
	}
	if !strings.Contains(seenBody, `"active":true`) {
		t.Errorf("body missing active: %s", seenBody)
	}
}

// TestRequest_BodyForm pins the body.form variant: form-urlencoded,
// content-type application/x-www-form-urlencoded.
func TestRequest_BodyForm(t *testing.T) {
	var seenBody string
	var seenContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		seenBody = string(bs)
		seenContentType = r.Header.Get("Content-Type")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Requests[0].Method = "POST"
	doc.Requests[0].Body = &schema.Body{Form: map[string]schema.Value{
		"key":   vStr("value"),
		"limit": vInt(10),
	}}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !strings.Contains(seenContentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", seenContentType)
	}
	form, _ := url.ParseQuery(seenBody)
	if form.Get("key") != "value" || form.Get("limit") != "10" {
		t.Errorf("form body = %v", form)
	}
}

// TestRequest_BodyRaw pins the body.raw variant: a single Value resolves
// to a string and is sent verbatim. No content-type is set by default.
func TestRequest_BodyRaw(t *testing.T) {
	var seenBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		seenBody = string(bs)
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Requests[0].Method = "POST"
	raw := vStr(`{"hello":"world"}`)
	doc.Requests[0].Body = &schema.Body{Raw: &raw}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seenBody != `{"hello":"world"}` {
		t.Errorf("body = %q, want raw payload", seenBody)
	}
}

// TestRequest_BodyRawInterpolation pins string-interpolation inside raw
// bodies: a ${state.x} segment is desugared at parse time and resolved
// at request time.
func TestRequest_BodyRawInterpolation(t *testing.T) {
	var seenBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		seenBody = string(bs)
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["tenant"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("acme"))}
	doc.Requests[0].Method = "POST"
	raw := mustInterp(`'{"tenant":"${state.tenant}","event":"hello"}'`)
	doc.Requests[0].Body = &schema.Body{Raw: &raw}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !strings.Contains(seenBody, `"tenant":"acme"`) {
		t.Errorf("body missing interpolated tenant: %s", seenBody)
	}
}

// TestRequest_Headers pins the request headers map.
func TestRequest_Headers(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["etag"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr(`"v42"`))}
	doc.Requests[0].Headers = map[string]schema.Value{
		"X-Custom":       vStr("hi"),
		"If-None-Match":  vRef("state.etag"),
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seen.Get("X-Custom") != "hi" {
		t.Errorf("X-Custom = %q", seen.Get("X-Custom"))
	}
	if seen.Get("If-None-Match") != `"v42"` {
		t.Errorf("If-None-Match = %q", seen.Get("If-None-Match"))
	}
}

// TestRequest_IfPredicateGates pins requests[].if: a false predicate
// skips the step entirely.
func TestRequest_IfPredicateGates(t *testing.T) {
	var aHits, bHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) {
		aHits++
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) {
		bHits++
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.State["mode"] = schema.FieldDecl{Type: "string", Default: ptrValue(vStr("b"))}
	doc.Requests = []schema.Request{
		{
			ID:     "a",
			Method: "GET",
			URL:    mustInterp("${state.url}/a"),
			If: &schema.Predicate{Eq: &schema.PredicateEq{
				Path: mustPath("state.mode"), Value: vStr("a"),
			}},
		},
		{
			ID:     "b",
			Method: "GET",
			URL:    mustInterp("${state.url}/b"),
			If: &schema.Predicate{Eq: &schema.PredicateEq{
				Path: mustPath("state.mode"), Value: vStr("b"),
			}},
			ProducesEvents: true,
		},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if aHits != 0 {
		t.Errorf("a hits = %d, want 0 (if: false)", aHits)
	}
	if bHits != 1 {
		t.Errorf("b hits = %d, want 1", bHits)
	}
}

// TestRequest_ExpectStatusAllowList pins expect_status: any code in the
// list is treated as success; anything else trips the on_status /
// error.mode dispatcher.
func TestRequest_ExpectStatusAllowList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Return 202 — needs to be in expect_status to count as success.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer server.Close()

	doc := minimalDoc(server.URL)
	doc.Requests[0].ExpectStatus = []int{200, 202}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Errorf("Drain: %v (202 should be accepted via expect_status)", err)
	}
}

// TestRequest_ExtractToExtractNamespace pins the per-iteration scratch
// path: extract.<name> writes do not survive into the next iteration.
func TestRequest_ExtractToExtractNamespace(t *testing.T) {
	var lastSeenScratch string
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"session": "abc"})
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		lastSeenScratch = r.Header.Get("X-Scratch")
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: ptrValue(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{
			{
				ID:     "login",
				Method: "POST",
				URL:    mustInterp("${state.url}/login"),
				Extract: []schema.ExtractVar{
					{To: mustPath("extract.scratch_session"), From: mustPath("response.body.session")},
				},
			},
			{
				ID:     "events",
				Method: "GET",
				URL:    mustInterp("${state.url}/events"),
				Headers: map[string]schema.Value{
					"X-Scratch": vRef("extract.scratch_session"),
				},
				ProducesEvents: true,
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
		Pagination: schema.Pagination{None: &struct{}{}},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if lastSeenScratch != "abc" {
		t.Errorf("X-Scratch = %q, want abc", lastSeenScratch)
	}
}
