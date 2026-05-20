// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/p1llus/skopos/schema"
)

// Snapshot is the persistable runtime state. Only the state namespace is
// persisted; every other runtime namespace (cache, events, extract, steps,
// response, fan-out aliases) lives in process memory only.
//
// State keys map exactly to the field names declared under state: in the
// spec, with no namespace prefix. Values round-trip through JSON.
type Snapshot struct {
	// State holds every persisted state field: operator-config fields
	// (declared with default: and never written to) and persistent
	// fields (the target of any progress write, or an extract with
	// to: state.<name>). Per-drain-scratch fields are omitted.
	State map[string]any `json:"state,omitempty"`
}

// Store is the persistence contract. Implementations: MemoryStore
// (in-memory, default), FileStore (atomic JSON file). External callers
// can plug in any backend that round-trips a Snapshot through
// Save/Load.
type Store interface {
	// Load returns the latest persisted snapshot, or the zero Snapshot
	// when no prior snapshot exists. A missing underlying medium
	// (e.g. a not-yet-created file) is not an error.
	Load() (Snapshot, error)
	// Save persists s, overwriting any prior snapshot atomically. The
	// runner calls Save once per Drain via a deferred call (even on
	// error) so partial progress survives a crash.
	Save(Snapshot) error
}

// MemoryStore is the default Store: state lives only for the lifetime of
// the process. Useful for tests and one-shot runs.
//
// Concurrency: single-threaded; not safe for concurrent Drain. Wrap with
// sync.Mutex (or use FileStore) when sharing across goroutines.
type MemoryStore struct {
	snap Snapshot
}

// Load returns the in-memory snapshot. Always nil error.
func (m *MemoryStore) Load() (Snapshot, error) { return m.snap, nil }

// Save overwrites the in-memory snapshot. Always nil error.
func (m *MemoryStore) Save(s Snapshot) error { m.snap = s; return nil }

// scope is the namespace-resolution context for one drain. It mirrors
// the IR's namespace table and is reused across pagination iterations
// within a single drain. Runner.Drain constructs a fresh scope on every
// invocation via newScope, so continuous-mode (multi-drain) operation
// does NOT share scope state across drains — only the persisted
// Snapshot survives, and the cache.* slots are repopulated lazily.
//
// Each namespace has a defined lifetime:
//
//   - state:           Per-drain, with operator-config and persistent
//     sub-fields persisted across drains via the deferred store.Save in
//     Runner.Drain. Per-drain scratch sub-fields (every pagination
//     write destination) are reset to their declared default at the
//     start of every drain.
//
//   - cache:           Process memory only. Slots are written by Cache
//     blocks (auth.oauth2.<grant>.cache, requests[].cache) and never
//     persisted. Repopulated lazily after a runner restart.
//
//   - events:          Per-page-response. The decoded events list bound
//     by the runner before sink delivery and progress evaluation;
//     rebound on the next page-response. Exposed as events.count,
//     events.first[.field], events.last[.field], events.<int>[.field],
//     and events.*[.field].
//
//   - extract:         Per-iteration. Reset to a fresh empty map at the
//     top of every pagination iteration in Runner.Drain; a later
//     iteration MUST NOT see a previous iteration's extract bindings.
//
//   - steps / stepHeaders: Per-iteration, same reset rule as extract.
//     Decoded bodies and post-auth response headers indexed by
//     request id.
//
//   - body / responseHeaders: Per-evaluation. Bound only while a
//     predicate or extract is being resolved against a specific
//     response context; resolves response.body.<path> via
//     lookupBodyPath and response.header.<name> via headerLookup.
//
//   - item / itemBinding: Per-fan-out-iteration. Set while a step with
//     fan_out is executing the active per-item iteration; refs rooted
//     at itemBinding resolve against item. Outside a fan_out step both
//     are zeroed and the binding name falls through to the
//     unknown-namespace branch.
type scope struct {
	doc *schema.Doc

	state   map[string]any
	cache   map[string]any
	extract map[string]any
	steps   map[string]any
	events  any

	body            any
	responseHeaders http.Header
	stepHeaders     map[string]http.Header

	item        any
	itemBinding string

	// nowFn is the clock; defaults to time.Now. Per-scope (not package
	// global) so concurrent runners can use independent clocks.
	nowFn func() time.Time

	// AWS SigV4 helpers, lazily built on first auth.sigv4 use and reused
	// for the scope's lifetime. The signer is stateless. The ambient
	// provider (AWS default credential chain) does I/O on construction and
	// self-caches/refreshes its credentials, so it is built at most once;
	// sigv4Once guards that one-time build. Static-credential auth builds
	// its provider inline and never touches awsAmbient.
	sigv4Signer   *v4.Signer
	awsAmbient    aws.CredentialsProvider
	awsAmbientErr error
	sigv4Once     sync.Once
}

// newScope seeds a scope from a snapshot and the IR's state-field
// defaults. Each declared field's default Value is evaluated and laid
// down first; the snapshot values are then overlaid so persisted fields
// win over defaults.
//
// Per-drain scratch fields are NOT wiped here — Runner.Drain owns the
// per-drain wipe call site so continuous-mode operation can reuse one
// scope across drains without re-loading the snapshot.
func newScope(doc *schema.Doc, snap Snapshot, now func() time.Time) (*scope, error) {
	if now == nil {
		now = time.Now
	}
	s := &scope{
		doc:         doc,
		state:       make(map[string]any),
		cache:       make(map[string]any),
		extract:     make(map[string]any),
		steps:       make(map[string]any),
		stepHeaders: make(map[string]http.Header),
		nowFn:       now,
	}
	if err := s.seedDefaults(); err != nil {
		return nil, err
	}
	maps.Copy(s.state, snap.State)
	return s, nil
}

// seedDefaults evaluates every declared state field's Default Value into
// state. Fields without a default stay unset; readers fall through to the
// {ref: ..., default: ...} branch or surface absence.
func (s *scope) seedDefaults() error {
	for name, fd := range s.doc.State {
		if fd.Default == nil {
			continue
		}
		v, err := s.evalValue(*fd.Default)
		if err != nil {
			return fmt.Errorf("state.%s default: %w", name, err)
		}
		s.state[name] = v
	}
	return nil
}

// snapshot returns the Snapshot to persist. Only operator-config and
// persistent state fields are written; per-drain scratch fields are
// omitted. The cache namespace and every other non-state runtime
// namespace are never persisted.
func (s *scope) snapshot() Snapshot {
	out := Snapshot{State: make(map[string]any)}
	scratch := perDrainScratchFields(s.doc)
	for name := range s.doc.State {
		if _, isScratch := scratch[name]; isScratch {
			continue
		}
		if v, ok := s.state[name]; ok {
			out.State[name] = v
		}
	}
	return out
}

// resetPerDrainScratch resets every per-drain-scratch state field on s to
// its declared default Value, or deletes the slot when no default is
// declared. The runner calls this at the start of every Drain so a drain
// that fails mid-page re-bootstraps pagination on the next start.
func (s *scope) resetPerDrainScratch() error {
	scratch := perDrainScratchFields(s.doc)
	for name := range scratch {
		fd, declared := s.doc.State[name]
		if !declared || fd.Default == nil {
			delete(s.state, name)
			continue
		}
		v, err := s.evalValue(*fd.Default)
		if err != nil {
			return fmt.Errorf("state.%s default: %w", name, err)
		}
		s.state[name] = v
	}
	return nil
}

// fieldLifetime classifies a declared state field by where it is written.
// The classification drives both the per-drain wipe (per-drain scratch
// fields are reset at drain start) and the snapshot filter (per-drain
// scratch fields are excluded from the persisted state).
type fieldLifetime int

const (
	// lifetimeOperatorConfig is a state field declared with a default
	// but never written as the to: of any pagination, progress, or
	// extract site. Persisted whole so the snapshot file stays
	// self-contained and a re-load reproduces the same starting state
	// without needing the operator to re-supply defaults.
	lifetimeOperatorConfig fieldLifetime = iota
	// lifetimePerDrainScratch is a state field that appears as the to:
	// destination of any pagination write (cursor_token.to /
	// next_url.to / counter.to / custom.advance[].to). Wiped at the
	// start of every drain; not persisted.
	lifetimePerDrainScratch
	// lifetimePersistent is a state field that appears as the to:
	// destination of any progress write or as an extract destination
	// with to: state.<name>. Persisted on every drain.
	lifetimePersistent
)

// classifyStateField returns the lifetime classification for state.<name>.
// ok=false when name is not declared under state:.
//
// The classification is read off the parsed *schema.Doc — never cached on
// the scope. The validator guarantees no field falls in more than one
// classification, so per-drain-scratch and persistent membership are
// disjoint sets in any document that passed schema.Validate.
func classifyStateField(doc *schema.Doc, name string) (fieldLifetime, bool) {
	if _, declared := doc.State[name]; !declared {
		return 0, false
	}
	if _, ok := perDrainScratchFields(doc)[name]; ok {
		return lifetimePerDrainScratch, true
	}
	if _, ok := persistentStateFields(doc)[name]; ok {
		return lifetimePersistent, true
	}
	return lifetimeOperatorConfig, true
}

// perDrainScratchFields returns the set of state field names whose
// lifetime is per-drain scratch. A field qualifies when it appears as the
// to: of any pagination write (cursor_token, next_url, counter, or every
// entry of custom.advance).
func perDrainScratchFields(doc *schema.Doc) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(p schema.Path) {
		if name, ok := stateFieldName(p); ok {
			out[name] = struct{}{}
		}
	}
	switch {
	case doc.Pagination.CursorToken != nil:
		add(doc.Pagination.CursorToken.To)
	case doc.Pagination.NextURL != nil:
		add(doc.Pagination.NextURL.To)
	case doc.Pagination.Counter != nil:
		add(doc.Pagination.Counter.To)
	case doc.Pagination.Custom != nil:
		for _, w := range doc.Pagination.Custom.Advance {
			add(w.To)
		}
	}
	return out
}

// persistentStateFields returns the set of state field names whose
// lifetime is persistent. A field qualifies when it appears as the to: of
// any progress write or an extract destination with to: state.<name>.
func persistentStateFields(doc *schema.Doc) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(p schema.Path) {
		if name, ok := stateFieldName(p); ok {
			out[name] = struct{}{}
		}
	}
	for _, w := range doc.Progress {
		add(w.To)
	}
	for _, req := range doc.Requests {
		for _, ex := range req.Extract {
			add(ex.To)
		}
	}
	return out
}

// stateFieldName extracts the field-name suffix from a state.<name> Path.
// Returns ok=false for any other shape.
func stateFieldName(p schema.Path) (string, bool) {
	if len(p.Parts) != 2 || p.Parts[0] != "state" {
		return "", false
	}
	return p.Parts[1], true
}

// resolveNamespaceRef returns the value at path within s. The path's root
// segment must be one of the closed namespace roots
// (state | cache | events | extract | steps | response) or the
// author-chosen fan_out.as binding name active in s.itemBinding (e.g.
// "incident" while a step with `fan_out: {as: incident}` is iterating).
//
// Returns (nil, false, nil) for unresolved leaves; callers decide whether
// the absence is fatal (e.g. {ref: ...} with no default) or acceptable
// (e.g. a predicate guarding optional data).
//
// The closed root set mirrors schema.pathClosedRoots; any addition there
// requires a matching arm here.
func (s *scope) resolveNamespaceRef(p schema.Path) (any, bool, error) {
	if p.IsEmpty() {
		return nil, false, fmt.Errorf("empty path")
	}
	root, rest := p.Parts[0], p.Parts[1:]

	// Author-chosen fan_out.as binding is matched before the static
	// switch so its name takes precedence over the namespace-root
	// vocabulary. The validator guarantees the binding does not collide
	// with any closed root or with declared state / extract names or
	// step ids.
	if s.itemBinding != "" && root == s.itemBinding {
		if s.item == nil {
			return nil, false, nil
		}
		return walk(s.item, rest)
	}

	switch root {
	case "state":
		if len(rest) < 1 {
			return nil, false, fmt.Errorf("state ref requires a field name")
		}
		v, ok := s.state[rest[0]]
		if !ok {
			return nil, false, nil
		}
		return walk(v, rest[1:])
	case "cache":
		if len(rest) < 1 {
			return nil, false, fmt.Errorf("cache ref requires a slot name")
		}
		v, ok := s.cache[rest[0]]
		if !ok {
			return nil, false, nil
		}
		return walk(v, rest[1:])
	case "events":
		return s.resolveEvents(rest)
	case "extract":
		if len(rest) < 1 {
			return nil, false, fmt.Errorf("extract ref requires a name")
		}
		v, ok := s.extract[rest[0]]
		if !ok {
			return nil, false, nil
		}
		return walk(v, rest[1:])
	case "steps":
		if len(rest) < 2 {
			return nil, false, fmt.Errorf("steps ref must be steps.<id>.body[.<path>] or steps.<id>.header.<name>")
		}
		stepID := rest[0]
		switch rest[1] {
		case "body":
			body, ok := s.steps[stepID]
			if !ok {
				return nil, false, nil
			}
			return walk(body, rest[2:])
		case "header":
			if len(rest) < 3 {
				return nil, false, fmt.Errorf("steps.<id>.header ref requires a header name")
			}
			h, ok := s.stepHeaders[stepID]
			if !ok || h == nil {
				return nil, false, nil
			}
			return headerLookup(h, rest[2])
		default:
			return nil, false, fmt.Errorf("steps ref must be steps.<id>.body[.<path>] or steps.<id>.header.<name>")
		}
	case "response":
		if len(rest) < 1 {
			return nil, false, fmt.Errorf("response ref requires a kind segment: response.body[.<path>] or response.header.<name>")
		}
		switch rest[0] {
		case "body":
			if s.body == nil {
				return nil, false, nil
			}
			return lookupBodyPath(s.body, rest[1:])
		case "header":
			if len(rest) < 2 {
				return nil, false, fmt.Errorf("response.header ref requires a header name")
			}
			if s.responseHeaders == nil {
				return nil, false, nil
			}
			return headerLookup(s.responseHeaders, rest[1])
		default:
			return nil, false, fmt.Errorf("response ref must be response.body[.<path>] or response.header.<name>")
		}
	default:
		return nil, false, fmt.Errorf("unknown namespace root %q", root)
	}
}

// resolveEvents walks the events sub-path. The accepted shapes are:
//
//	events                 the active page's event list (absent when no
//	                       page-response is bound).
//	events.count           cardinality of the active page (int64).
//	events.first[.field]   the first event (or its field).
//	events.last[.field]    the last event (or its field).
//	events.<int>[.field]   the event at the given zero-based index.
//	events.*[.field]       projection across every event; the `*`
//	                       segment is legal only at position 1 of an
//	                       events ref.
//
// Unresolved leaves return (nil, false, nil): empty pages, out-of-bounds
// indices, non-integer index segments, and missing fields all surface as
// absent so predicate evaluation stays absent-tolerant. The single
// exception is events.count, which resolves to 0 when no page is bound —
// the counter pagination variant's default terminate_when predicate
// reads it before the first response is decoded.
func (s *scope) resolveEvents(rest []string) (any, bool, error) {
	if len(rest) == 0 {
		if s.events == nil {
			return nil, false, nil
		}
		return s.events, true, nil
	}
	list, listOK := s.events.([]any)
	if s.events == nil || !listOK {
		if rest[0] == "count" && len(rest) == 1 {
			return int64(0), true, nil
		}
		return nil, false, nil
	}
	switch rest[0] {
	case "count":
		if len(rest) != 1 {
			return nil, false, fmt.Errorf("events.count takes no sub-path")
		}
		return int64(len(list)), true, nil
	case "first":
		if len(list) == 0 {
			return nil, false, nil
		}
		return walk(list[0], rest[1:])
	case "last":
		if len(list) == 0 {
			return nil, false, nil
		}
		return walk(list[len(list)-1], rest[1:])
	case "*":
		return projectEvents(list, rest[1:])
	default:
		idx, err := strconv.Atoi(rest[0])
		if err != nil {
			return nil, false, nil
		}
		if idx < 0 || idx >= len(list) {
			return nil, false, nil
		}
		return walk(list[idx], rest[1:])
	}
}

// projectEvents applies rest to every element of list and returns the
// collected results. The shape mirrors what reducer arguments expect:
// {ref: events.*.timestamp} feeds {max: ...} a list of timestamps.
// Elements where the sub-path does not resolve contribute a nil entry,
// preserving positional alignment with the source list.
func projectEvents(list []any, rest []string) (any, bool, error) {
	if len(rest) == 0 {
		return list, true, nil
	}
	out := make([]any, 0, len(list))
	for _, el := range list {
		v, ok, err := walk(el, rest)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			out = append(out, nil)
			continue
		}
		out = append(out, v)
	}
	return out, true, nil
}

// headerLookup returns the first value for header `name` in h. The lookup
// is case-insensitive via http.Header.Get (which canonicalises the key).
// Returns (nil, false, nil) when the header is absent. An explicitly-empty
// header value surfaces as ("", true, nil) so predicate evaluation can
// distinguish "absent" from "present but empty".
func headerLookup(h http.Header, name string) (any, bool, error) {
	if h == nil {
		return nil, false, nil
	}
	if v := h.Get(name); v != "" {
		return v, true, nil
	}
	canonical := http.CanonicalHeaderKey(name)
	if vs, ok := h[canonical]; ok && len(vs) > 0 {
		return vs[0], true, nil
	}
	return nil, false, nil
}

// walk descends rest steps into v, treating each step as a map key. The
// IR's namespace refs are map-rooted in practice; integer-indexed body
// paths use lookupBodyPath instead (see bodypath.go).
func walk(v any, rest []string) (any, bool, error) {
	for _, seg := range rest {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		v, ok = m[seg]
		if !ok {
			return nil, false, nil
		}
	}
	return v, true, nil
}
