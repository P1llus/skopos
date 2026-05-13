// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// ndjsonDoc returns a single-request Doc whose response is decoded as
// ndjson and whose events_at applies per-line.
func ndjsonDoc(baseURL string, eventsAt string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: baseURL},
		}},
		Defaults:   &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:       schema.Auth{None: &struct{}{}},
		Requests:   []schema.Request{{Method: "GET", Path: ptrValue(vStr("/api/v1/events"))}},
		Response:   schema.Response{Decode: "ndjson", EventsAt: mustPath(eventsAt)},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
}

// TestEndToEnd_NDJSON drives an ndjson response across two valid lines
// (each carrying its own `events` list) plus one whitespace-only blank
// line. Asserts the runner emits the concatenated per-line events list
// and skips blanks.
func TestEndToEnd_NDJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			`{"events":[{"id":"a"},{"id":"b"}]}` + "\n" +
				"\n" +
				`{"events":[{"id":"c"}]}` + "\n",
		))
	}))
	defer server.Close()

	sink := &captureSink{}
	r := &Runner{Doc: ndjsonDoc(server.URL, "events"), Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := len(sink.events); got != 3 {
		t.Fatalf("emitted %d events, want 3 (blank line skipped, per-line events_at concatenated)", got)
	}
}

// TestEndToEnd_NDJSON_DecodeErrorContext asserts the runner surfaces a
// "ndjson decode at line N" wrapper for a malformed line.
func TestEndToEnd_NDJSON_DecodeErrorContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		// Line 2 is malformed JSON.
		_, _ = w.Write([]byte(
			`{"events":[]}` + "\n" +
				`{not-json` + "\n" +
				`{"events":[]}` + "\n",
		))
	}))
	defer server.Close()

	doc := ndjsonDoc(server.URL, "events")
	doc.Error = &schema.ErrorBlock{Mode: "fail"}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: nil error, want ndjson decode failure")
	}
	if !strings.Contains(err.Error(), "ndjson decode at line 2") {
		t.Errorf("err = %v, want substring 'ndjson decode at line 2'", err)
	}
}
