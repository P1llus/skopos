// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/p1llus/skopos/client"
	"github.com/p1llus/skopos/schema"
)

// A Runner executes one *schema.Doc against the live HTTP world, emitting
// each drained event into a Sink. The Drain call paginates until the
// document's strategy reports no more data and then returns.
func ExampleRunner_Drain() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"events":[{"id":"e1"},{"id":"e2"}]}`)
	}))
	defer server.Close()

	src := `ir_version: "1"
state:
  url: {type: url}
auth:
  none: {}
requests:
  - method: GET
    url: "${state.url}/events"
response:
  decode: json
  events_at: response.body.events
pagination:
  none: {}
`
	doc, err := schema.Load(strings.NewReader(src))
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	def := schema.Value{LiteralString: &server.URL}
	doc.State["url"] = schema.FieldDecl{Type: "url", Default: &def}

	var out bytes.Buffer
	r := &client.Runner{
		Doc:    doc,
		Sink:   client.NewJSONLSink(&out),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		fmt.Println("drain:", err)
		return
	}
	fmt.Print(out.String())
	// Output:
	// {"id":"e1"}
	// {"id":"e2"}
}

// JSONLSink writes one JSON object per line. Any io.Writer works; the
// bundled sink locks around its encoder so concurrent Runners can share it.
func ExampleNewJSONLSink() {
	var buf bytes.Buffer
	sink := client.NewJSONLSink(&buf)
	_ = sink.Emit(map[string]any{"id": "e1"})
	_ = sink.Emit(map[string]any{"id": "e2"})
	fmt.Print(buf.String())
	// Output:
	// {"id":"e1"}
	// {"id":"e2"}
}
