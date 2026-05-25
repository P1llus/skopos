// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DecodeChain is the response decode pipeline: zero or more byte-transform
// stages (gzip, zip) followed by exactly one terminal decoder (csv, json,
// ndjson) as the last element. The YAML scalar form "json" / "ndjson" is the
// degenerate single-terminal chain.
type DecodeChain []DecodeStage

// DecodeStage is one stage of the response decode chain. Exactly one variant
// key must be present.
type DecodeStage struct {
	// Gzip decompresses the upstream stream (RFC 1952). Byte-transform.
	Gzip *struct{} `yaml:"gzip,omitempty" json:"gzip,omitempty"`
	// Zip expands a ZIP archive into its members. Byte-transform.
	Zip *ZipDecode `yaml:"zip,omitempty" json:"zip,omitempty"`
	// CSV decodes delimited rows into events. Terminal.
	CSV *CSVDecode `yaml:"csv,omitempty" json:"csv,omitempty"`
	// JSON decodes the stream as one JSON value. Terminal.
	JSON *struct{} `yaml:"json,omitempty" json:"json,omitempty"`
	// NDJSON decodes one JSON value per line. Terminal.
	NDJSON *struct{} `yaml:"ndjson,omitempty" json:"ndjson,omitempty"`
}

// ZipDecode configures the zip byte-transform stage.
type ZipDecode struct {
	// Glob optionally selects which archive members to decode by their base
	// name (e.g. "*.csv"). Empty selects every member. Members are decoded
	// in name-sorted order and their events concatenated.
	Glob string `yaml:"glob,omitempty" json:"glob,omitempty"`
}

// CSVDecode configures the csv terminal decoder.
type CSVDecode struct {
	// Header selects "present" (the first row is field names; each later row
	// decodes to a map) or "absent" (each row decodes to a list).
	Header string `yaml:"header" json:"header"`
}

// decodeStageVariants returns the (name, payload) pairs for every DecodeStage
// variant that is currently set, in declaration order.
func decodeStageVariants(s DecodeStage) (names []string, payloads []any) {
	add := func(name string, payload any, set bool) {
		if set {
			names = append(names, name)
			payloads = append(payloads, payload)
		}
	}
	add("gzip", s.Gzip, s.Gzip != nil)
	add("zip", s.Zip, s.Zip != nil)
	add("csv", s.CSV, s.CSV != nil)
	add("json", s.JSON, s.JSON != nil)
	add("ndjson", s.NDJSON, s.NDJSON != nil)
	return names, payloads
}

// Variant returns the active DecodeStage variant name and payload. Returns
// ("", nil) when zero or more than one variant is set.
func (s DecodeStage) Variant() (string, any) {
	names, payloads := decodeStageVariants(s)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set DecodeStage variant.
func (s DecodeStage) VariantNames() []string {
	names, _ := decodeStageVariants(s)
	return names
}

// isTerminalDecode reports whether name is a terminal decoder.
func isTerminalDecode(name string) bool {
	switch name {
	case "csv", "json", "ndjson":
		return true
	}
	return false
}

// isByteTransformDecode reports whether name is a byte-transform stage.
func isByteTransformDecode(name string) bool {
	return name == "gzip" || name == "zip"
}

// Terminal returns the chain's terminal decoder name (csv|json|ndjson), or ""
// when the chain is empty or its last stage is not a terminal decoder.
func (c DecodeChain) Terminal() string {
	if len(c) == 0 {
		return ""
	}
	name, _ := c[len(c)-1].Variant()
	if isTerminalDecode(name) {
		return name
	}
	return ""
}

// IsRowOriented reports whether the chain's terminal decoder yields one event
// per row (csv or ndjson), as opposed to one JSON tree the runner walks with
// events_at.
func (c DecodeChain) IsRowOriented() bool {
	switch c.Terminal() {
	case "csv", "ndjson":
		return true
	}
	return false
}

// decodeStageFromName builds the single terminal stage named by the scalar
// decode form. Only the arg-less terminals (json, ndjson) are valid as a
// scalar; gzip/zip/csv must use the list form.
func decodeStageFromName(name string) (DecodeStage, error) {
	switch name {
	case "json":
		return DecodeStage{JSON: &struct{}{}}, nil
	case "ndjson":
		return DecodeStage{NDJSON: &struct{}{}}, nil
	default:
		return DecodeStage{}, fmt.Errorf("decode scalar %q must be json or ndjson; use the list form for gzip/zip/csv", name)
	}
}

// UnmarshalYAML accepts either a scalar string (the degenerate
// single-terminal chain) or a sequence of DecodeStage maps.
func (c *DecodeChain) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			*c = nil
			return nil
		}
		stage, err := decodeStageFromName(node.Value)
		if err != nil {
			return fmt.Errorf("schema.DecodeChain at line %d: %w", node.Line, err)
		}
		*c = DecodeChain{stage}
		return nil

	case yaml.SequenceNode:
		var stages []DecodeStage
		if err := node.Decode(&stages); err != nil {
			return fmt.Errorf("schema.DecodeChain at line %d: %w", node.Line, err)
		}
		*c = DecodeChain(stages)
		return nil

	default:
		return fmt.Errorf("schema.DecodeChain: decode must be a string or a list of stages (line %d)", node.Line)
	}
}

// UnmarshalJSON accepts either a JSON string (the degenerate single-terminal
// chain) or an array of DecodeStage objects, mirroring the YAML rules.
func (c *DecodeChain) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		stage, err := decodeStageFromName(s)
		if err != nil {
			return fmt.Errorf("schema.DecodeChain: %w", err)
		}
		*c = DecodeChain{stage}
		return nil
	}
	var stages []DecodeStage
	if err := json.Unmarshal(data, &stages); err != nil {
		return fmt.Errorf("schema.DecodeChain: decode must be a string or a list of stages: %w", err)
	}
	*c = DecodeChain(stages)
	return nil
}
