// SPDX-License-Identifier: Apache-2.0

// Package schema defines skopos's declarative spec: a description of
// HTTP-pull integrations as data.
//
// A spec document describes one drain — a pagination loop that fetches
// pages, decodes them, emits events to a Sink, applies progress writes,
// and commits state. The in-process runner (the client package) executes
// the loop directly; the schema is data-only and does not import the
// runtime, so any alternative consumer of *Doc is decoupled from changes
// here.
//
// The top-level keys are:
//
//   - ir_version  — wire-format identifier (must equal IRVersion).
//   - state       — flat map of typed field declarations.
//   - auth        — discriminated union (none | bearer | basic | api_key |
//     custom | oauth2 | multi_mode).
//   - requests    — ordered list of HTTP requests run per iteration.
//   - response    — how to decode the producer step and where the events
//     list lives.
//   - pagination  — discriminated union (none | cursor_token | next_url |
//     counter | custom).
//   - progress    — flat list of state writes evaluated per accepted
//     page-response.
//   - error       — how non-success HTTP responses are surfaced.
//
// Values, Paths, Predicates, and the closed Namespace and Type sets are
// the cross-cutting primitives every block reuses.
//
// The entry points are:
//
//   - [Load] — parse a *Doc from an io.Reader (YAML or JSON).
//   - [Parse] — parse a *Doc from a byte slice (YAML or JSON).
//   - [Validate] — run structural validation on a parsed *Doc.
package schema

// IRVersion is the only ir_version string this package accepts. It is a
// wire-format identifier, not a release version: bumps signal a breaking
// change to the spec shape, not a release of skopos itself.
const IRVersion = "1"
