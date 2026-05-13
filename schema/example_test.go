// SPDX-License-Identifier: Apache-2.0

package schema_test

import (
	"fmt"
	"strings"

	"github.com/p1llus/skopos/schema"
)

// Load parses an IR document from a YAML (or JSON) reader. The returned
// *schema.Doc still needs to be passed through schema.Validate before it can be
// handed to a runner.
func ExampleLoad() {
	const yaml = `
ir_version: "1"
state:
  fields:
    url: {type: url, default: "https://api.example.com"}
defaults:
  base_url: {ref: state.url}
auth:
  none: {}
requests:
  - method: GET
    path: /events
response:
  decode: json
  events_at: ""
pagination:
  none: {}
progress:
  stateless: {}
`
	doc, err := schema.Load(strings.NewReader(yaml))
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	fmt.Println("ir_version:", doc.IRVersion)
	fmt.Println("requests:", len(doc.Requests))
	// Output:
	// ir_version: 1
	// requests: 1
}

// Validate reports structural errors. A clean document returns an empty
// slice; otherwise each Diagnostic names the path that failed.
func ExampleValidate() {
	const yaml = `
ir_version: "1"
auth:
  none: {}
requests: []
response:
  decode: json
  events_at: ""
pagination:
  none: {}
progress:
  stateless: {}
`
	doc, err := schema.Load(strings.NewReader(yaml))
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	diags := schema.Validate(doc)
	for _, d := range diags {
		if d.Severity == "error" {
			fmt.Printf("%s: %s\n", d.Path, d.Message)
			break
		}
	}
	// Output:
	// requests: at least one request is required
}
