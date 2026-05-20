// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p1llus/skopos/schema"
)

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
	doc.State["tenant"] = schema.FieldDecl{Type: "string", Default: new(vStr("acme"))}
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
	doc.State["mode"] = schema.FieldDecl{Type: "string", Default: new(vStr("b"))}
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
			"url": {Type: "url", Default: new(vStr(server.URL))},
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
