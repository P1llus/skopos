// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Load parses a schema.Doc from r, which may contain YAML or JSON.
//
// JSON is detected by the presence of a leading '{' byte (after trimming
// whitespace). All other input is treated as YAML.
//
// Load returns an error if:
//   - r cannot be read
//   - the input is not valid YAML or JSON
//   - the decoded ir_version does not equal IRVersion ("1")
func Load(r io.Reader) (*Doc, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("schema.Load: reading input: %w", err)
	}
	return Parse(data)
}

// Parse parses a schema.Doc from a byte slice (YAML or JSON).
//
// See Load for format-detection and version-check semantics.
func Parse(data []byte) (*Doc, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("schema.Parse: empty input")
	}

	var doc Doc
	var err error

	if isJSON(data) {
		err = json.Unmarshal(data, &doc)
	} else {
		err = yaml.Unmarshal(data, &doc)
	}
	if err != nil {
		return nil, fmt.Errorf("schema.Parse: %w", err)
	}

	if doc.IRVersion != IRVersion {
		return nil, fmt.Errorf("schema.Parse: unsupported ir_version %q; this package requires %q", doc.IRVersion, IRVersion)
	}

	return &doc, nil
}

// isJSON reports whether data looks like a JSON object (starts with '{').
func isJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{'
}
