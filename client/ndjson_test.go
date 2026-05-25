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

	doc := &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: new(vStr(server.URL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{{
			Method: "GET",
			URL:    mustInterp("${state.url}/events"),
		}},
		Response:   schema.Response{Decode: decodeNDJSON(), EventsAt: mustPath("")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Error:      &schema.ErrorBlock{Mode: "fail"},
	}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: nil error, want ndjson decode failure")
	}
	if !strings.Contains(err.Error(), "ndjson decode at line 2") {
		t.Errorf("err = %v, want substring 'ndjson decode at line 2'", err)
	}
}
