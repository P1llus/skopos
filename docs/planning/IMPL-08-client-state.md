# IMPL-08 — client/state.go

## Scope

Rewrite `client/state.go` against the post-redesign IR: `Snapshot` drops
its `Cursor` field, `scope` drops the `cursor` map and gains `events` +
`cache`, `newScope` evaluates declared defaults at seed time, `snapshot()`
filters by inferred lifetime, and `resolveNamespaceRef` mirrors the
schema package's closed root set (`state | cache | events | extract |
steps | response`) plus author-chosen `fan_out.as` aliases. The per-drain
wipe is exposed as `(*scope).resetPerDrainScratch` so Slice 12's runner
owns the call site.

## Old → new map

### `Snapshot`

| Before                                                   | After                                                   |
|----------------------------------------------------------|---------------------------------------------------------|
| `struct{ State map[string]any; Cursor map[string]any }`  | `struct{ State map[string]any }`                        |

`Cursor` is gone. The cursor namespace was deleted in Slices 4 + 6; the
runtime values that used to live there (next-cursor token, page number,
offset, scroll id, etc.) are now declared as ordinary `state.<name>`
fields and reach the persisted snapshot only when their lifetime
classifies as operator-config or persistent.

### `scope`

| Field                          | Before                                                                                              | After                                                                                                                |
|--------------------------------|-----------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------|
| `doc *schema.Doc`              | retained                                                                                            | retained                                                                                                             |
| `state map[string]any`         | retained                                                                                            | retained                                                                                                             |
| `cursor map[string]any`        | seeded from `Snapshot.Cursor`; written by `pagination.seed`, `progress.seed`, `extract.target=cursor` | **deleted**.                                                                                                         |
| `cache map[string]any`         | n/a                                                                                                 | **added**. Process-memory cache slots written by `auth.oauth2.<grant>.cache` and `requests[].cache`; never persisted. |
| `events any`                   | n/a                                                                                                 | **added**. Decoded events list bound per page-response; resolves `events.first / last / <int> / count / *.<field>`. |
| `extract map[string]any`       | retained                                                                                            | retained                                                                                                             |
| `steps map[string]any`         | retained                                                                                            | retained                                                                                                             |
| `stepHeaders map[string]http.Header` | retained                                                                                       | retained                                                                                                             |
| `body any`                     | retained                                                                                            | retained                                                                                                             |
| `responseHeaders http.Header`  | retained                                                                                            | retained                                                                                                             |
| `item any`, `itemBinding string` | retained                                                                                          | retained                                                                                                             |
| `nowFn func() time.Time`       | retained                                                                                            | retained                                                                                                             |
| `logger *log.Logger`           | retained                                                                                            | retained                                                                                                             |

### `newScope`

| Before                                                                                  | After                                                                                                                                  |
|-----------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------|
| Seeded `state` by copying each `FieldDecl.Default` raw `*Value` into the map.            | Seeds `state` by **evaluating** each declared default through `evalValue`, then overlays `snap.State`. Defaults without a value stay unset. |
| Seeded `cursor` from `snap.Cursor`.                                                      | **Deleted**.                                                                                                                            |
| Always built all maps.                                                                   | Same, plus `cache`.                                                                                                                     |
| Performed the per-drain wipe inline.                                                     | **No wipe**. The runner owns the wipe call site via `resetPerDrainScratch` so continuous-mode operation can reuse one scope across drains. |

### `resolveNamespaceRef`

| Root before    | Root after                                                                                                  |
|----------------|-------------------------------------------------------------------------------------------------------------|
| `state`        | unchanged.                                                                                                  |
| `cursor`       | **deleted** (parser rejects the root upstream; runtime arm gone).                                            |
| `cache`        | **added**. `cache.<name>` reads `scope.cache[name]`.                                                         |
| `events`       | **added**. `events`, `events.count`, `events.first[.field]`, `events.last[.field]`, `events.<int>[.field]`, `events.*[.field]`. |
| `extract`      | unchanged.                                                                                                  |
| `steps`        | unchanged (`steps.<id>.body[.<path>]` / `steps.<id>.header.<name>`).                                          |
| `response`     | unchanged (`response.body[.<path>]` / `response.header.<name>`).                                              |
| fan-out alias  | unchanged (still matched before the closed-root switch).                                                     |

### `snapshot()`

| Before                                                                                                   | After                                                                                                                  |
|----------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------|
| Walked `FieldDecl.Mutability == "runtime"` to decide which state fields persisted; copied cursor whole.   | Walks every declared state field and includes the slot unless it classifies as per-drain scratch (a pagination write target). `cache.*` and every non-state namespace are unconditionally excluded. |

## Removed content

Top-level / scope-level:

- `Snapshot.Cursor` field and the documentation that named it.
- `scope.cursor` field and every comment block that named it.
- The `pagination.seed` / `progress.seed` per-drain priming language in
  the type-level doc-string (no seed step exists in the new lifecycle —
  the per-drain wipe replaces it).
- The Mutability-driven snapshot filter (`Mutability` no longer exists
  on `FieldDecl`).
- The detailed enumeration of pre-redesign cursor keys
  (`cursor.token`, `cursor.page`, `cursor.offset`, `cursor.next_link`,
  `cursor.next_url`, `cursor.scroll_id`, `cursor.<cursor_var>`,
  `cursor.last_timestamp`, `cursor.window_start`/`window_end`,
  `cursor.phase`, etc.) that documented each strategy's reserved names.

Imports:

- No package-level imports changed at the top of the file (still
  `fmt`, `log`, `maps`, `net/http`, `time`, plus `schema`); `strconv`
  is new (events index parsing).

## Added content

- `cache map[string]any` and `events any` on `scope`.
- `(*scope).seedDefaults()` — evaluates declared `Default` Values into
  `state`.
- `(*scope).resetPerDrainScratch()` — Runner.Drain calls this to wipe
  every per-drain-scratch state field at the start of a drain.
- `fieldLifetime` enum with three constants.
- `classifyStateField(doc, name) (fieldLifetime, bool)` — read-off-doc
  helper.
- `perDrainScratchFields(doc) map[string]struct{}` — set of pagination
  targets.
- `persistentStateFields(doc) map[string]struct{}` — set of progress and
  extract-to-state targets.
- `stateFieldName(path) (name, ok)` — extracts the `<name>` suffix from
  a `state.<name>` Path.
- `(*scope).resolveEvents(rest)` — the events shortcut vocabulary.
- `projectEvents(list, rest)` — list-projection helper that backs
  `events.*`.

## Build state

Schema package: green (`go build ./schema/...`).

Client package: still red. Slice 8's rewrite did not introduce new
breakages; every remaining `go build ./client/...` error lives in files
owned by later slices:

- `client/extract.go` — referenced `s.cursor` and `ev.Name` /
  `ev.Target` fields removed in Slice 4. Slice 9 owns the rewrite.
- `client/pagination.go` — references the legacy seven pagination
  variant types and `s.cursor` slots. Slice 10 owns the rewrite.
- `client/progress.go` — references the legacy progress variant types
  and `s.cursor` slots. Slice 11 owns the rewrite.
- `client/runner.go` — references `Response.PlaceholderEvent`,
  `Request.Path`, `Cache.StoreIn`. Slice 12 owns the rewrite.
- `client/oauth2.go`, `client/requestcache.go` — reference the deleted
  `schema.TokenCache` / `schema.RequestCache` types. Slice 13 merges
  both into `client/cache.go`.
- `client/http.go` — references the deleted `Defaults.BaseURL` /
  `Request.Path` and the change from `*Value` to `Value` on
  `Request.URL`. Slice 12 / 13 own the rewrite.
- `client/auth.go` — references the wrapped
  `MultiModeAuth.Default.Auth`; the new shape is a bare `Auth`.
  Slice 13 owns the rewrite.
- `client/value.go` — references the removed `NowValue.Offset` field
  (no `{now: true, offset: ...}` sibling). Slice 9 owns the rewrite.

The `cursor` arm removed from `(*scope).resolveNamespaceRef` is a
deliberate scope-surface break: every `s.cursor[...]` access in the
files above is in scope for the slice that owns it. Slice 8 documents
the breakage rather than reintroducing a transitional shim per global
rule #1 (no backwards compatibility).

## Notes for downstream slices

### Slice 9 — `client/value.go` + `predicate.go` + `extract.go` + `bodypath.go`

- `evalValue` is called from `(*scope).seedDefaults` and
  `(*scope).resetPerDrainScratch`; the value runtime owns the new
  Value forms (`Add`, `Subtract`, `Max`, `Min`, `First`, `Last`,
  `Count`, `Regex`) and the resolution semantics for absent state
  fields.
- Reducer arguments resolve through the events-projection arm of
  `resolveNamespaceRef` — `{ref: events.*.<field>}` returns `[]any`
  via `projectEvents`. `Max`/`Min`/`First`/`Last`/`Count` consume that
  shape directly.
- The per-event sub-path walk for reducer projection treats absent
  fields as `nil`; the reducer is responsible for skipping nil entries
  (matches the absent-tolerant predicate policy).
- `extract.go` writes to `scope.state` (when `to: state.<name>`) or
  `scope.extract` (when `to: extract.<name>`). The `cursor` arm in
  the current file disappears.

### Slice 11 — `client/progress.go`

- The runner binds `scope.events` to the decoded events list before
  calling `applyProgress`. Progress writes resolve `{ref: events.*}`
  through the same arm as predicates and reducers.
- There is no `progress.seed` step. The destination state field's
  `default:` carries the first-run seed; if a progress entry needs a
  cumulative high-water mark, the author writes the explicit
  `{max: [{ref: state.last_ts}, {max: {ref: events.*.timestamp}}]}`.

### Slice 12 — `client/runner.go`

- Call `(*scope).resetPerDrainScratch()` at the top of every drain,
  AFTER `Store.Load` + `newScope` but BEFORE the pagination loop.
  Continuous-mode operation reuses one `*scope` across drains; the
  runner re-wipes scratch fields at the start of each drain so a
  partial drain re-bootstraps pagination on the next start.
- Bind `scope.events` once per accepted page-response before sink
  delivery and before evaluating `progress:`. Resetting it to nil at
  the top of every iteration is optional (predicates that reference
  events outside the iteration window resolve to absent).
- Reset `scope.extract`, `scope.steps`, and `scope.stepHeaders` to
  fresh empty maps at the top of every pagination iteration.
- The deferred `Store.Save` writes `s.snapshot()` — the helper already
  filters per-drain scratch out, so the runner does not need to clear
  state fields before calling.

### Slice 13 — `client/cache.go`

- `scope.cache` is the destination map for every `Cache` block runtime.
  The slot key is `Cache.To.Parts[1]` (the validator guarantees
  `Cache.To` is `cache.<name>`).
- Slots store the captured value alongside their resolved
  `expires_at` (and the operator's `buffer`); the cache runtime
  decides whether to re-run the underlying step on read. Slice 8
  does NOT pre-define the slot value shape; Slice 13 picks the
  representation.

### Slice 14 — `client/filestore.go`

- The on-disk JSON shape is `{"state": {...}}`. Pre-redesign snapshots
  with a `"cursor"` key are not migrated — global rule #1. A loader
  faced with a stale file simply ignores the unknown key (Go's
  `json.Unmarshal` skips unknown fields on `Snapshot`); the next
  `Save` writes the new shape and the stale slot is gone forever.

### Slice 17 — tests

- `client/state_test.go` is stale. The new contract is:
  - `Snapshot{}` round-trips through `json.Marshal/Unmarshal` with no
    `cursor` key.
  - `newScope` evaluates declared defaults via `evalValue` (so a
    `{subtract: [{now: true}, "720h"]}` default ends up in
    `state[name]` as a `time.Time` instead of a `*schema.Value`).
  - `resetPerDrainScratch` deletes / re-defaults pagination targets
    only; persistent + operator-config fields survive.
  - `(*scope).snapshot()` omits pagination targets, includes everything
    else.
  - `resolveNamespaceRef` resolves `events.count`, `events.first.x`,
    `events.last.x`, `events.<int>.x`, `events.*.x`, plus the closed
    root set and the fan-out alias arm.

## Verification

`go build ./schema/...` is **green** at slice close. `go build
./client/...` stays **red** with the same 129 errors that existed at
the start of the slice; every error is in a file owned by Slice 9
or later (the §"Build state" list above enumerates them). Zero new
errors live in `client/state.go`.

End-to-end verification (a real `go test ./client/...` pass against
the new namespace shape) lands with Slice 17.

### Smoke run

A throwaway smoke runner outside `package client` mirrored the
pure-function helpers (`walk`, `headerLookup`, `projectEvents`,
`stateFieldName`, `perDrainScratchFields`, `persistentStateFields`,
plus an inline mirror of `(*scope).resolveEvents`) and exercised
them against hand-rolled `*schema.Doc` and events lists. 28 asserts
covering:

- `events.count` → cardinality on a non-empty list; `0` on absent
  events; `events.count.<extra>` errors.
- `events.first.<field>`, `events.last.<field>`, `events.<int>.<field>`
  resolve; `events.<7>.<field>` and `events.<-1>.<field>` are
  unresolved (predicate absent-tolerant); `events.<non-int>.<field>`
  is unresolved.
- `events.*.<field>` projection preserves positional alignment;
  absent fields surface as `nil` entries; empty events produces an
  empty `[]any`.
- `events` (no sub-path) returns the raw list.
- `stateFieldName` accepts `state.<name>`; rejects every other
  root + arity.
- `perDrainScratchFields` covers each pagination variant
  (`counter`, `custom.advance[]`); `persistentStateFields` covers
  `progress[].to` and `requests[].extract.to`.
- `headerLookup` is case-insensitive via `http.Header.Get`; absent
  headers surface as unresolved.

The smoke runner was deleted before commit per PHASE-2-PLAN §3.
`(*scope).snapshot()` filter, `(*scope).resetPerDrainScratch`, and
the `evalValue`-dependent code paths are walked-through (see the
trace in the planning history) rather than executed — those land
under real test coverage in Slice 17, once the rest of the client
package compiles.
