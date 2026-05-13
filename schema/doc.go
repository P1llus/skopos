// SPDX-License-Identifier: Apache-2.0

// Package schema defines skopos's declarative spec: a description of
// HTTP-pull integrations as data.
//
// A spec document describes an abstract pull loop —
//
//	Iteration { fetch → extract_events → advance_cursor → emit }
//
// — that the in-process runner (the client package) executes directly.
// The schema is data-only; a future code-generating backend could lower
// the same *schema.Doc to a different runtime without changes to schema/.
//
// The canonical schema version for this package is "1". Documents with any
// other ir_version are rejected by [Load] and [Parse].
//
// The entry points are:
//
//   - [Load] — parse a schema.Doc from an io.Reader (YAML or JSON).
//   - [Parse] — parse a schema.Doc from a byte slice (YAML or JSON).
//   - [Validate] — run structural validation on a parsed *Doc.
package schema

// IRVersion is the only ir_version string this package accepts. It is a
// wire-format identifier, not a release version: bumps signal a breaking
// change to the spec shape, not a release of skopos itself.
const IRVersion = "1"
