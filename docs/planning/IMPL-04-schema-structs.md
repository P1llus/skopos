# IMPL-04 — schema/schema.go + schema/read.go + schema/doc.go

## Scope

Rewrote the IR struct definitions (`schema/schema.go`), the parser entry
points (`schema/read.go`), and the package preamble (`schema/doc.go`)
against the post-redesign shape described in `docs/schema.md`. No other
schema/ files were touched; `schema/value.go`, `schema/path.go`,
`schema/predicate.go`, and `schema/validate.go` continue to compile only
to the extent that the new struct shape happens to still satisfy their
old call sites (path.go and predicate.go are unaffected; value.go and
validate.go are red — see [Build state](#build-state)).

## Doc top-level layout

| Field        | Old shape                      | New shape                                |
|--------------|--------------------------------|------------------------------------------|
| `IRVersion`  | `string`                       | unchanged                                |
| `State`      | `*State` (wrapper with Fields) | `map[string]FieldDecl` (flat, top-level) |
| `Defaults`   | `*Defaults` (BaseURL)          | **removed**                              |
| `Auth`       | `Auth`                         | unchanged shape (variant content updated below) |
| `Requests`   | `[]Request`                    | unchanged (per-entry shape updated)      |
| `Response`   | `Response`                     | unchanged shape (`PlaceholderEvent` field removed) |
| `Pagination` | `Pagination` (7-variant union) | `Pagination` (5-variant union)           |
| `Progress`   | `Progress` (6-variant union)   | `Progress` (`[]ProgressWrite` slice)     |
| `Error`      | `*ErrorBlock`                  | unchanged                                |

## Type-by-type old → new map

### Removed entirely

| Old type                       | Replacement                                                                                  |
|--------------------------------|----------------------------------------------------------------------------------------------|
| `State` (Fields wrapper)       | Deleted — `Doc.State` is now `map[string]FieldDecl` directly.                                |
| `Defaults`                     | Deleted — every request carries an absolute `URL Value`; no spec-level prefix.               |
| `TokenCache`                   | Deleted — replaced by the unified `Cache` struct, used at both OAuth2 grants and requests.   |
| `RequestCache`                 | Deleted — replaced by the unified `Cache` struct.                                            |
| `AuthDefault` (wrapper)        | Deleted — `MultiModeAuth.Default` is now a bare `Auth`.                                      |
| `CursorTokenPagination`        | Replaced — see new `CursorTokenPagination` (rebuilt with `From`/`To`/`TerminateWhen` fields).|
| `PageNumberPagination`         | Deleted — folded into the new `CounterPagination`.                                           |
| `OffsetPagination`             | Deleted — folded into the new `CounterPagination`.                                           |
| `LinkHeaderPagination`         | Deleted — folded into the new `NextURLPagination`.                                           |
| `NextURLInBodyPagination`      | Deleted — folded into the new `NextURLPagination`.                                           |
| `ScrollIDPagination`           | Deleted — folded into the new `CursorTokenPagination`.                                       |
| `GraphQLRelayPagination`       | Deleted — folded into the new `CursorTokenPagination`.                                       |
| `TimestampProgress`            | Deleted — progress is now a flat `[]ProgressWrite` slice.                                    |
| `UseNowProgress`               | Deleted — replaced by a `{from: {now: true}}` ProgressWrite.                                 |
| `EventTime`                    | Deleted — author writes `events.*.<field>` paths directly via Value language (Slice 5).      |
| `Initial`                      | Deleted — first-run seeding is the destination field's `default:` in `state:`.               |
| `TimeWindowProgress`           | Deleted — author composes two ProgressWrites (`window_start`, `window_end`).                 |
| `AsyncJobProgress`             | Deleted — async patterns now compose from `requests[].terminate_when` and plain extracts.    |
| `AsyncSubmitStep`              | Deleted — see above.                                                                         |
| `AsyncPollStep`                | Deleted — see above.                                                                         |
| `AsyncFetchStep`               | Deleted — see above.                                                                         |
| `AsyncExtract`                 | Deleted — see above.                                                                         |
| `AsyncOnComplete`              | Deleted — see above.                                                                         |
| `CursorUpdateDirective`        | Deleted — see above.                                                                         |
| `cursorUpdateRaw` (mirror)     | Deleted — the custom UnmarshalYAML/JSON pair went with the directive.                        |
| `extractVarRaw` (mirror)       | Deleted — the new ExtractVar uses default decoding (no migration hints per global rule #1).  |
| `cursorTokenPaginationRaw`     | Deleted — same reasoning.                                                                    |
| `scrollIDPaginationRaw`        | Deleted — same reasoning.                                                                    |

### Added

| New type                  | Purpose                                                                                |
|---------------------------|----------------------------------------------------------------------------------------|
| `Cache`                   | Unified `{to, expires_at, buffer}` struct used at both OAuth2 grants and requests.     |
| `NextURLPagination`       | Replaces `LinkHeaderPagination` + `NextURLInBodyPagination`. Carries `Regex`/`Capture` for the Link-header parse case. |
| `CounterPagination`       | Replaces `PageNumberPagination` + `OffsetPagination`. `Start`/`Step` are `*Value` so they accept either a literal int or `{ref: state.page_size}`. |
| `CustomPagination`        | Author-controlled primitive: `Advance []AdvanceWrite` + required `TerminateWhen Predicate`. |
| `AdvanceWrite`            | `{to, from, regex?, coerce?}` entry inside `CustomPagination.Advance`.                 |
| `ProgressWrite`           | `{to, from, coerce?, regex?}` entry — the only progress shape.                         |

### Changed in place

| Type                        | What changed                                                                              |
|-----------------------------|-------------------------------------------------------------------------------------------|
| `FieldDecl`                 | Dropped `Mutability` (lifetime is inferred from write sites by the validator in Slice 7). Added `Format` for `timestamp`/`duration` wire-format hint. `Default` typed as `*Value` (was `interface{}` literal). |
| `MultiModeAuth.Default`     | Now bare `Auth` (was wrapped `AuthDefault{Auth}`).                                        |
| `ClientCredentialsGrant.Cache` / `PasswordGrant.Cache` | `*Cache` (was `*TokenCache`).                                  |
| `Request`                   | Dropped `Path *Value` (URL is the only request locator). Made `URL Value` (was `*Value`) and required. Added `TerminateWhen *Predicate`. Changed `Cache *RequestCache` to `Cache *Cache`. |
| `ExtractVar`                | Dropped `Name string` and `Target string`. Destination is now `To Path` (carries the namespace prefix — `state.<name>` or `extract.<name>`). Added optional `Regex string` for inline regex transform. |
| `Response`                  | Dropped `PlaceholderEvent *Value`.                                                        |
| `Pagination`                | Variant set collapsed from 7 (+ `none`) to 4 (+ `none`): `none`/`cursor_token`/`next_url`/`counter`/`custom`. |
| `Progress`                  | Was a 6-key discriminated-union struct; now `[]ProgressWrite`. Omitted/empty means "no progress tracking" (what was `stateless`). |

## Removed-content list (field names that disappear from the IR)

- `State.Fields` (wrapper layer)
- `FieldDecl.Mutability`
- `Defaults.BaseURL` (and the entire `defaults:` block)
- `Request.Path`
- `Request.RequestCache` (replaced by `Request.Cache *Cache`)
- `Response.PlaceholderEvent`
- `ExtractVar.Name`
- `ExtractVar.Target`
- `CursorTokenPagination.TokenAt` (renamed to `From`; `To` is new)
- `PageNumberPagination.{PageParam, HasMoreAt, BatchSize}`
- `OffsetPagination.{OffsetParam, BatchSize}`
- `LinkHeaderPagination.Pattern`
- `NextURLInBodyPagination.NextURLAt`
- `ScrollIDPagination.{ScrollIDAt, CompleteWhen}`
- `GraphQLRelayPagination.{HasNextPageAt, EndCursorAt, CursorVar}`
- `Progress.{Stateless, LatestEventTimestamp, MaxEventField, UseNow, TimeWindow, AsyncJob}`
- `TimestampProgress.{EventTime, Initial, Lookback}`
- `UseNowProgress.Lookback`
- `EventTime.Path`
- `Initial.Lookback`
- `TimeWindowProgress.{InitialOffset, Format}`
- `AsyncJobProgress.{Submit, Poll, Fetch, OnComplete}`
- `AsyncSubmitStep.{Step, Extract}`
- `AsyncPollStep.{Step, CompleteWhen, Extract}`
- `AsyncFetchStep.Step`
- `AsyncExtract.From`
- `AsyncOnComplete.CursorUpdate`
- `CursorUpdateDirective.{Kind, Lookback, EventTime}`
- Custom UnmarshalYAML/JSON pairs on `CursorUpdateDirective`, `ExtractVar`, `CursorTokenPagination`, `ScrollIDPagination` (migration-hint codecs, all deleted — no migration code per global rule #1)

## Codec notes

The rewrite uses default `yaml.v3` and `encoding/json` decoding for every
new struct. The Phase 1 docs (`docs/schema.md` §Codec invariants) call out
slice-order preservation and alphabetical map-key emission — these are
codec-level invariants that the test slice (Slice 17) will exercise; this
slice did not add custom marshalers. The existing Value/Predicate/Path
custom codecs in `value.go` / `predicate.go` / `path.go` are unaffected
by the struct-shape change (Slice 5 owns the Value-language additions
that will extend those codecs).

The `Variant()` / `VariantNames()` helpers stay in their existing pattern
for `Auth`, `Body`, `Pagination`. A new `OAuth2Auth.Variant()` /
`OAuth2Auth.VariantNames()` pair was added since OAuth2 is also a
discriminated union and the validator (Slice 7) needs to dispatch on the
active grant. `Progress` is no longer a union, so its helpers are gone —
length-of-slice is the only check downstream code needs.

## Build state

`go build ./schema/...` is **red** at slice close — the operator
explicitly chose to follow the handoff's "leave them broken" directive
over the deliverable's "build succeeds" line (the two conflict).
Specifically:

- `schema/value.go:828` — `d.State.Fields` is undefined. The `isSecretStatePath`
  helper needs to become `d.State[p.Parts[1]]` (one-line fix). Slice 5
  owns `value.go`'s rewrite and will absorb this fix.
- `schema/validate.go` — 10+ unresolved symbols (`Defaults`, `State`
  type, `TokenCache`, `RequestCache`, `TimestampProgress`,
  `AsyncJobProgress`, `d.Progress.AsyncJob`, `d.State.Fields`, …). Slice
  7 owns the full validator rewrite.

`schema/path.go` and `schema/predicate.go` build cleanly against the new
struct shape (they don't reference any of the removed types).

The downstream client/cmd/templates packages will also fail to build
until the schema package is whole again at Slice 7's close. The plan
posture (PHASE-2-PLAN.md §4) acknowledges this: tests and CI need not
pass between Slices 4 and 16.

## Notes for downstream slices

### Slice 5 (`schema/value.go`)

- **The one-line `IsSecret` fix above** lives in
  `isSecretStatePath` at `schema/value.go:828`. Change
  `d.State.Fields[p.Parts[1]]` to `d.State[p.Parts[1]]`.
- Slice 5's main task is the Value language additions (Add, Subtract,
  Max, Min, First, Last, Count, Regex, string interpolation). The new
  Value forms feed back into:
  - `FieldDecl.Default` — accepts the full Value language (e.g.
    `default: {subtract: [{now: true}, "720h"]}`).
  - `Cache.ExpiresAt` — Value resolving to `time.Time`.
  - Every request slot (`URL`, `Query`, `Headers`, `Body.json`/`form`/`raw`).
  - `ProgressWrite.From` and `AdvanceWrite.From`.
- String interpolation (`"${state.url}/path"`) desugars to
  `{concat: [...]}`; the desugaring lives in `Value.UnmarshalYAML`'s
  scalar branch. The struct-side `URL Value` field on Request inherits
  this for free once Slice 5 lands.
- Slice 5 should reject `{now: true, offset: ...}` at parse time — the
  current `NowValue` struct still carries the `Offset` field. Slice 5
  removes it.

### Slice 6 (`schema/path.go` + `schema/predicate.go`)

- Path's namespace-root vocabulary needs `state`, `cache`, `events`,
  `extract`, `steps`, `response` in its closed set. `cursor` is gone.
  Slice 6 owns enforcing this in the parser/validator path layer.
- `events.*` shortcuts (`events.first`, `events.last`, `events.<int>`,
  `events.count`, `events.*`) are recognised at path-parse time by
  Slice 6.

### Slice 7 (`schema/validate.go`)

- The validator is a full rewrite. The struct shapes it now sees match
  `docs/schema.md` directly; the cursor-schema inference function
  (`cursorSchema(d *Doc)`) is gone — replace it with the lifetime
  inference rule that derives operator-config / per-drain / persistent
  from where each `state.<name>` is written.
- The `Pagination.Variant()` / `Auth.Variant()` / `OAuth2Auth.Variant()` /
  `Body.Variant()` helpers are the canonical exactly-one-variant check.
- The `Progress` slice is the canonical empty-or-write-list check;
  zero-length is the "no progress tracking" signal.
- `Request.TerminateWhen` is the new request-level loop primitive; it
  validates as any other Predicate.
- The `on_status:` closed verb set
  (`skip|fail|empty_events|invalidate_cache`) lives in
  `Request.OnStatus` as a `map[int]string`; the verb check is
  string-equality against the closed set.
- `Request.Cache` and `Request.FanOut` are mutually exclusive (per
  `docs/schema.md` §requests rules).
- `MultiModeAuth.Default` is a bare `Auth` — the validator's
  `auth.multi_mode.default` walker should recurse into it directly, not
  unwrap an `Auth` wrapper.

### Slice 8 (`client/state.go`)

- `Snapshot` becomes `struct{ State map[string]any }`; no `Cursor` field.
- `resolveNamespaceRef` enumerates the closed root set Slice 6 owns
  (`state`, `cache`, `events`, `extract`, `steps`, `response`).
- Lifetime classification (operator-config / per-drain / persistent) is
  read off the parsed `*Doc` via the validator's classifier, not stored
  on `FieldDecl`. The per-drain wipe and the `snapshot()` filter both
  consume that classification.

### Slice 10 (`client/pagination.go`)

- The five pagination plans the runtime needs map 1:1 onto the new
  variant structs (`None`, `CursorTokenPagination`, `NextURLPagination`,
  `CounterPagination`, `CustomPagination`).
- `CustomPagination.TerminateWhen` is required at parse time (its
  Predicate is a non-pointer); the runtime can rely on its presence
  without a nil check.

### Slice 11 (`client/progress.go`)

- `Progress` is `[]ProgressWrite`; the runtime iterates directly. No
  per-variant dispatch.
- `applyProgress(scope, doc.Progress)` stages every `From` resolution
  against the pre-write `state.*` snapshot, then writes every `To`
  destination. The batch semantics live in the runtime; the struct
  shape supports them.

### Slice 13 (`client/cache.go`)

- The unified `Cache` struct is shared by `OAuth2Auth.*.Cache` and
  `Request.Cache`. One runtime, two call sites.
- `Cache.To` is a `Path` rooted at `cache.<name>`; the runtime allocates
  the slot under `scope.cache`.
- `Cache.ExpiresAt` is a Value resolving to `time.Time`; re-fetch when
  `now() + buffer >= expires_at`.
