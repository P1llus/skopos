// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/p1llus/skopos/schema"
)

// Snapshot is the persistable runtime state.
//
// Config-mutability state fields (mutability: config, the default) are NOT
// persisted — they come from schema.State.Fields[].Default or from the
// operator's configuration. A Snapshot loaded from disk merges in defaults
// from the IR document for any config field the file does not carry.
type Snapshot struct {
	// State holds runtime-mutable state fields — the only ones that
	// change between iterations: OAuth2 cached tokens at
	// state.<cache.store_in>, custom-login session tokens, etc.
	State map[string]any `json:"state,omitempty"`
	// Cursor holds every inferred cursor field for the active
	// pagination + progress + async_job strategies, plus
	// framework-internal expiry-tracking slots paired with cached auth
	// tokens (the __oauth2_<store_in>_expires_at key catalogued in
	// scope's cursor catalogue).
	Cursor map[string]any `json:"cursor,omitempty"`
}

// Store is the persistence contract. Implementations: MemoryStore (default,
// in-memory only), FileStore (JSON file). External callers can plug in
// BoltDB, etcd, etc.
type Store interface {
	// Load returns the latest persisted snapshot, or the zero Snapshot
	// when no prior snapshot exists (first-run state). A missing
	// underlying medium (e.g. a not-yet-created file) is not an error.
	Load() (Snapshot, error)
	// Save persists s, overwriting any prior snapshot atomically. The
	// runner calls Save once per Drain (via a deferred call, even on
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

// scope holds the resolved namespaces for one iteration of the loop. It
// shadows the IR namespace table.
//
// # Field lifetimes
//
// Be precise about which fields survive iterations and which do not — bugs
// here are silent and load-bearing.
//
//   - state:          PER-DRAIN. Seeded once from Snapshot.State + IR defaults
//     in newScope; runtime-mutable writes (OAuth2 token cache, custom-login
//     session tokens) accumulate within a drain and persist across drains
//     via the deferred store.Save in Runner.Drain.
//
//   - cursor:         PER-DRAIN, PERSISTED. Seeded once from Snapshot.Cursor
//     in newScope. Mutated by pagination.advance + progress.advance + the
//     async_job phase machine + extract[].target=cursor. Persisted whole on
//     drain end (even on error) so the next drain resumes from the
//     high-water mark. The schema is inferred from the document's active
//     strategies — there's no central registry. The keys written by each
//     strategy in the runner today:
//
//     pagination.cursor_token         "token"            (opaque server-supplied cursor)
//     pagination.page_number          "page"             (1-based, increments by advance)
//     pagination.offset               "offset"           (0-based; advances by batch_size or observed event count)
//     pagination.link_header          "next_link"        (full next-page URL parsed from the Link header)
//     pagination.next_url_in_body     "next_url"         (full next-page URL read from the producer body at next_url_at)
//     pagination.scroll_id            "scroll_id"        (server-supplied scroll session id; cleared on complete_when termination)
//     pagination.graphql_relay        <cursor_var>       (Relay endCursor; author-named via cfg.CursorVar — typically "after"; cleared when has_next_page=false)
//     progress.latest_event_timestamp "last_timestamp"   (RFC 3339 string)
//     progress.max_event_field        "last_timestamp"   (RFC 3339 string; advance walks events and picks the max value at cfg.EventTime.Path)
//     progress.time_window            "window_start", "window_end" (formatted via cfg.Format — default rfc3339; advance slides window_start to the just-finished window_end)
//     progress.use_now                "last_timestamp"   (advance writes s.now() - lookback; no events walk)
//     progress.async_job              "phase"            ("submit" | "poll" | "fetch")
//     async_job.{submit,poll}.extract <author-named>     (auth tokens, job ids, etc.)
//     async_job.on_complete=use_now   "last_timestamp"   (set at producer completion)
//     async_job.on_complete=latest_event_timestamp "last_timestamp" (max value at cu.event_time.path inside the producer body's events list)
//     auth.oauth2.<grant>.cache       "__oauth2_<store_in>_expires_at" (RFC 3339 string; paired with state.<store_in> which holds the access token. Refreshed when now+expiry_buffer catches the cached value)
//     extract[].target=cursor         <extract.name>     (author-declared)
//
//     Unset keys read as nil at Value-eval time; the {default: ...} branch
//     on Ref covers first-drain absence.
//
//   - extract:        PER-ITERATION. Reset to a fresh empty map at the top
//     of every iteration in Runner.Drain. A later iteration MUST NOT see a
//     previous iteration's extract bindings — that would silently leak
//     stale values into multi-page drains.
//
//   - steps:          PER-ITERATION. Same reset rule as extract. Step bodies
//     from a previous iteration are not visible to later iterations;
//     authors who need cross-iteration access must extract a field into
//     state or cursor.
//
//   - item:           PER-FAN-OUT-ITERATION. Reserved for fan_out's
//     per-item binding.
//
//   - body:           SCOPED to complete_when predicate evaluation
//     (pagination.scroll_id.complete_when, progress.async_job.poll.complete_when).
//     Outside that narrow window the field is unset. The
//     response.body.<path> ref resolves against this field; the legacy
//     body.<path> root was deleted in slice 2.
//
//   - responseHeaders: SCOPED, mirrors body. Set alongside scope.body
//     whenever the response context is active (currently only
//     complete_when predicate evaluation). Resolves response.header.<name>
//     refs case-insensitively via http.Header.Get.
//
//   - stepHeaders:    PER-ITERATION, mirrors steps. Populated after every
//     completed request whose req.ID is set. Resolves
//     steps.<id>.header.<name> refs.
//
//   - fromPagination: PER-ITERATION. Reset by paginationPlan.seed at the
//     top of every iteration.
//
//   - fromProgress:   PER-DRAIN. Seeded once by progressPlan.seed before
//     the loop starts. NOT re-seeded per iteration — that would shift
//     since=<...> mid-drain and cause every page after the first to query
//     a moving window. The window-end advance happens once via
//     progress.advance after the drain completes.
type scope struct {
	doc     *schema.Doc
	state   map[string]any
	cursor  map[string]any
	extract map[string]any
	steps   map[string]any         // step id → decoded body
	item    any                    // per-item binding when inside fan_out
	body    any                    // active response context body (unused outside the predicate);
	                               // resolves response.body.<path> via lookupBodyPath
	responseHeaders http.Header    // active response context headers (mirrors body)
	stepHeaders     map[string]http.Header // step id → response headers

	// Active pagination/progress signals exposed to Value via
	// {from_pagination: ...} / {from_progress: ...}. Populated by the
	// pagination + progress drivers each iteration before requests run.
	fromPagination map[string]any
	fromProgress   map[string]any

	// nowFn is the clock; defaults to time.Now. Per-scope (not package
	// global) so concurrent runners can use independent clocks.
	nowFn func() time.Time

	// logger surfaces operational breadcrumbs from helpers that don't
	// otherwise have access to Runner.Logger. Optional; nil means "no
	// breadcrumb" (the helper falls back to its existing silent path).
	// Wired by Runner.Drain after newScope returns.
	logger *log.Logger
}

// newScope seeds a scope from a snapshot and the IR's state-field defaults.
// For each declared state field, the snapshot value wins over the default;
// fields absent from both stay unset (and any {ref: state.<name>} hits the
// {default: ...} branch or errors).
func newScope(doc *schema.Doc, snap Snapshot, now func() time.Time) (*scope, error) {
	if now == nil {
		now = time.Now
	}
	s := &scope{
		doc:            doc,
		state:          make(map[string]any),
		cursor:         make(map[string]any),
		extract:        make(map[string]any),
		steps:          make(map[string]any),
		stepHeaders:    make(map[string]http.Header),
		fromPagination: make(map[string]any),
		fromProgress:   make(map[string]any),
		nowFn:          now,
	}

	// Seed state from defaults, overriding with snapshot writes.
	if doc.State != nil {
		for name, fd := range doc.State.Fields {
			if fd.Default != nil {
				s.state[name] = fd.Default
			}
		}
	}
	for k, v := range snap.State {
		s.state[k] = v
	}

	// Seed cursor from snapshot. Cursor schema is inferred from strategies;
	// unset fields read as the zero value at evaluation time.
	for k, v := range snap.Cursor {
		s.cursor[k] = v
	}

	return s, nil
}

// snapshot returns a Snapshot containing only fields that should persist.
// Runtime-mutability state fields persist; config-mutability fields do not
// (they come from schema.State.Fields[].Default). Cursor is persisted whole.
func (s *scope) snapshot() Snapshot {
	out := Snapshot{
		State:  make(map[string]any),
		Cursor: make(map[string]any),
	}
	if s.doc.State != nil {
		for name, fd := range s.doc.State.Fields {
			if fd.Mutability != "runtime" {
				continue
			}
			if v, ok := s.state[name]; ok {
				out.State[name] = v
			}
		}
	}
	for k, v := range s.cursor {
		out.Cursor[k] = v
	}
	return out
}

// resolveNamespaceRef returns the value at path within s. The path's root
// segment must be one of: state, cursor, extract, steps, item, body. The
// remaining segments index into the value at that root.
//
// Returns (nil, false) for unresolved leaves; callers decide whether the
// absence is fatal (e.g. {ref: ...} with no default) or fine (e.g. predicate
// guarding).
func (s *scope) resolveNamespaceRef(p schema.Path) (any, bool, error) {
	if p.IsEmpty() {
		return nil, false, fmt.Errorf("empty path")
	}
	root, rest := p.Parts[0], p.Parts[1:]
	var top any
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
	case "cursor":
		if len(rest) < 1 {
			return nil, false, fmt.Errorf("cursor ref requires a field name")
		}
		v, ok := s.cursor[rest[0]]
		if !ok {
			return nil, false, nil
		}
		return walk(v, rest[1:])
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
		// steps.<id>.body.<path>  → decoded response body
		// steps.<id>.header.<name> → response header value (first match)
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
			top = body
			return walk(top, rest[2:])
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
	case "item":
		if s.item == nil {
			return nil, false, nil
		}
		return walk(s.item, rest)
	case "response":
		// response.body.<path>    → s.body (the active response context;
		//                          uses lookupBodyPath so list indexing
		//                          matches the body-walk used everywhere
		//                          else in the runtime)
		// response.header.<name>  → s.responseHeaders[name] (first value, case-insensitive)
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

// headerLookup returns the first value for header `name` in h. The lookup is
// case-insensitive via http.Header.Get (which canonicalises the key). Returns
// (nil, false, nil) when the header is absent — callers decide whether that
// counts as unresolved.
func headerLookup(h http.Header, name string) (any, bool, error) {
	if h == nil {
		return nil, false, nil
	}
	if v := h.Get(name); v != "" {
		return v, true, nil
	}
	// An explicitly-empty header value is still "present" for predicate
	// purposes. Distinguish "absent" (no key) from "present but empty"
	// via the canonical-key map lookup.
	canonical := http.CanonicalHeaderKey(name)
	if vs, ok := h[canonical]; ok && len(vs) > 0 {
		return vs[0], true, nil
	}
	return nil, false, nil
}

// walk descends rest steps into v, treating each step as a map key. NDJSON
// arrays are walked by integer index — but the IR's namespace refs are
// always map-rooted in practice (state.<name>, cursor.<name>), so the
// integer-index branch is only exercised by body-relative paths (handled
// separately in bodypath.go).
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
