# IMPL-07 — schema/validate.go

## Scope

Rewrote `schema/validate.go` end-to-end against the post-redesign IR: the
1487-line pre-redesign validator is replaced by a 1210-line file whose
checks key off the new `Doc` shape (flat `State`, unified `Cache`, five
pagination variants, flat `Progress`, `Request.TerminateWhen`, closed
`on_status` verb set, lifetime inference off write sites). The schema
package now builds green at slice close.

## Old → new check map

Every check in the pre-redesign validator, classified.

### Kept (with rewrite for new shape)

| Pre-redesign check                            | Post-redesign check                                              | Note                                                                                                                                  |
|-----------------------------------------------|------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------|
| `ir_version` equals `IRVersion`               | same                                                             | unchanged.                                                                                                                            |
| state field type is in closed set             | `validStateType` — added `timestamp`                             | `timestamp` was added in the new shape; the rest unchanged.                                                                           |
| enum requires `values`; `values` only on enum | same                                                             | unchanged shape; new code structure.                                                                                                  |
| default literal matches declared type         | `checkValue` is now called on `*Value` defaults                  | the new `FieldDecl.Default` is a `*Value`, not an `interface{}`; the type-vs-literal coercion check is gone. Value-form validity replaces it. |
| auth variant cardinality (zero / multi)       | same                                                             | now reads `Auth.VariantNames()`.                                                                                                      |
| auth per-variant required fields              | same                                                             | bearer/basic/api_key/custom Values type-checked through `checkValue`.                                                                  |
| oauth2 grant cardinality                      | same                                                             | reads `OAuth2Auth.VariantNames()`.                                                                                                    |
| multi_mode requires branches + default        | same                                                             | default is now a bare `Auth` (no `.Auth` wrapper).                                                                                    |
| multi_mode nesting rejected                   | same                                                             | applied to both branch arm and default arm.                                                                                           |
| body variant cardinality                      | same                                                             | unchanged.                                                                                                                            |
| request method in closed set                  | same                                                             | unchanged.                                                                                                                            |
| request id uniqueness                         | same                                                             | unchanged.                                                                                                                            |
| produces_events cardinality (≤1)              | same                                                             | dropped the async_job role-step interaction.                                                                                           |
| HEAD producer rejected                        | same                                                             | both explicit and implicit-producer paths.                                                                                            |
| expect_status range and duplicate check       | same                                                             | unchanged.                                                                                                                            |
| extract from-path body-rooted                 | `checkExtractFromPath`                                           | accepts `response.body|header.<...>` and `steps.<id>.body|header.<...>`.                                                              |
| response.decode in {json, ndjson}             | same                                                             | unchanged.                                                                                                                            |
| events_at body-rooted, empty-OK               | `checkEventsAtPath`                                              | accepts empty path (body root IS events list); body-only (no header).                                                                  |
| error.mode in closed set                      | same                                                             | unchanged.                                                                                                                            |
| predicate variant cardinality                 | same                                                             | unchanged.                                                                                                                            |
| Value form recursion                          | `checkValue`                                                     | extended to cover the new forms (`Add`, `Subtract`, `Max`, `Min`, `First`, `Last`, `Count`, `Regex`).                                  |
| Path ref namespace check                      | `checkPathRef` + per-root helpers                                | rebuilt around the new closed root set (state | cache | events | extract | steps | response) plus fan-out aliases.                     |

### Added (new in Slice 7)

| Check                                                                                               | Site                                                                  |
|-----------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------|
| Lifetime inference: every state-write site verified against declared state                          | `checkLifetimes`                                                      |
| Lifetime conflict: per-drain (pagination) ∩ persistent (progress/extract) is rejected                | `checkLifetimes`                                                      |
| Unified `Cache.To` is `cache.<name>`; `ExpiresAt` is required Value; `Buffer` is required Go-duration | `checkCache`                                                          |
| Request.TerminateWhen Predicate                                                                     | `checkRequest`                                                         |
| Pagination variant union (5 keys)                                                                   | `checkPagination`                                                      |
| Pagination `from:` path-slot restrictions per variant                                               | `checkPaginationFromPath`                                              |
| Pagination Counter: optional Start/Step Values; optional TerminateWhen                              | `checkPagination` (counter arm)                                        |
| Pagination Custom: requires non-empty Advance, requires explicit TerminateWhen                      | `checkPagination` (custom arm)                                         |
| Flat Progress list: each entry's `to: state.<name>` against a declared field; `from:` Value         | `checkProgress` + `checkLifetimes`                                     |
| Cache slot ref-resolution: `{ref: cache.<name>}` against declared cache slots                       | `checkCacheRef` + `collectCacheSlots`                                  |
| `on_status` closed verb set (skip/fail/empty_events/invalidate_cache)                               | `checkOnStatus`                                                        |
| `invalidate_cache` without reachable cache: warning (runtime degrades to empty_events)              | `checkOnStatus` (warn-degrade path)                                   |
| `requests[].cache` + `requests[].fan_out` mutually exclusive                                        | `checkRequests`                                                        |
| `fan_out.over` list-shape check                                                                     | `checkListShapedValue`                                                 |
| `fan_out.as` reserved-root check against `pathClosedRoots`                                          | `checkFanOut`                                                          |
| `fan_out.as` collision against state fields and request ids                                         | `checkFanOut`                                                          |
| Reducer operand list-shape check                                                                    | `checkReducerOperand` (Max/Min/First/Last/Count)                       |
| `events.*` projection vocabulary binding (count / first / last / `<int>` / `*`)                     | `checkEventsRef`                                                       |
| `*` projection segment legal only at events.\<...>                                                   | `checkNoStarOutsideEvents`                                             |
| `extract.<name>` ref scoping (per-request accumulation)                                             | `checkExtractRef` + per-step extract namespace growth                  |
| `Regex.Capture` non-negative                                                                        | `checkValue` (regex arm)                                               |
| `NextURLPagination.Capture` non-negative                                                            | `checkPagination` (next_url arm)                                       |

### Deleted

| Pre-redesign check                                                                                  | Reason                                                                                                |
|-----------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------|
| `cursorSchema(d *Doc)` and the cursor-name set                                                      | the cursor namespace is gone; lifetime inference off state-write sites replaces it.                   |
| `preregisterStateAndCursor` auto-registration of `<store_in>` and `<store_in>_expires_at` slots     | the unified `Cache` block writes to `cache.<name>` (process memory); no auto state slot is created.   |
| `Defaults.BaseURL` validation                                                                       | `Defaults` is gone; URLs interpolate via Values directly.                                             |
| `FieldDecl.Mutability` closed-set check                                                             | the mutability field is gone; lifetime is inferred.                                                   |
| Per-strategy pagination switch (7 strategies)                                                       | collapsed to 5 variants; the legacy 7 are gone from the struct.                                       |
| Per-variant progress switch (5 variants) + `TimestampProgress`, `AsyncJobProgress`, etc.            | progress is a flat list; the variant union is gone from the struct.                                   |
| `placeholder_event:` validation                                                                     | the field is gone from `Response`.                                                                    |
| `requests[].path` / `requests[].path` vs `url` mutual exclusion                                     | `Request.Path` is gone; every request has `url:`.                                                     |
| `extract.target` (extract / cursor) closed set                                                       | extract destination namespace is encoded in `to:` (state.\<name> vs extract.\<name>); no target field. |
| `RequestCache` / `TokenCache` per-shape checks (`store_in`, `expiry_field`, `expiry_buffer`)        | unified `Cache` struct replaces both.                                                                 |
| `cursor.<name>` ref binding                                                                         | cursor root is parser-rejected; ref-time check unreachable.                                           |
| `body.<path>` ref binding (legacy bare root)                                                        | body root is parser-rejected; ref-time check unreachable.                                             |
| `isNamespaceRoot` helper                                                                            | `pathClosedRoots` is the single source of truth.                                                       |
| `checkAsyncJob`, `checkAsyncStepRef`, `checkTimestampProgress`, `implicitAsyncJobProducerStep`      | async_job phase machine is gone; the same pattern composes from `Request.TerminateWhen` + extracts.   |
| `paginationStrategy` (7-arm switch)                                                                  | the pagination union has its own `Variant()` helper now.                                              |
| Pagination + async_job exclusion rule                                                               | async_job is gone.                                                                                    |
| OAuth2 grant exclusion message naming jwt_bearer / authorization_code / device_code                 | message rewritten; the design doc rationale stays in the design doc.                                  |
| `checkFieldDefault` literal-vs-type matcher                                                          | `FieldDecl.Default` is now a `*Value`; the type-coercion check moves to runtime.                      |

## Removed content list

Top-level functions:

- `cursorSchema(d *Doc) map[string]struct{}` (the per-strategy
  cursor-name set).
- `preregisterStateAndCursor(d *Doc, namespace *ns)` (auto-registration
  pre-pass).
- `paginationStrategy(p Pagination) string` (7-arm switch).
- `implicitAsyncJobProducerStep(d *Doc) string`.
- `checkFieldDefault(path string, fd FieldDecl)` (interface{} default
  literal type check).
- `checkDefaults(path string, d *Defaults, namespace *ns)`.
- `checkTokenCache(path string, c *TokenCache, namespace *ns, declaredStateFields map[string]struct{})`.
- `checkDuration(path, name, s string)` (specific to TokenCache /
  RequestCache; folded inline into `checkCache`).
- `checkAsyncJob(path string, aj *AsyncJobProgress, namespace *ns)`.
- `checkAsyncStepRef(path, id string, namespace *ns)`.
- `checkRequestCache(path string, c RequestCache, namespace *ns)`.
- `checkTimestampProgress(path string, tp TimestampProgress, namespace *ns)`.
- `checkBodyRootedPath(path string, p Path, namespace *ns, allowEmpty bool)`
  (the generic helper; three named per-slot helpers replace it:
  `checkEventsAtPath`, `checkPaginationFromPath`, `checkExtractFromPath`).
- `checkPerEventPath(path string, p Path)`.
- `checkFanOutOverForm(path string, val Value)` (replaced by
  `checkListShapedValue` with the same shape rejection set).
- `checkFanOutAsShadow(path, as string, namespace *ns)` (the new
  `checkFanOut` reads `pathClosedRoots` directly).
- `isNamespaceRoot(s string) bool` (the closed-root map is the source
  of truth now).

Structures:

- `ns` struct (cursor, itemNamespace, oauthStoreIn fields) replaced by
  `scope` (state, stepIDs, extract, cacheSlots, fanOutAlias, hasCache).

Error-message strings:

- Every message naming `cursor.<name>` as a legal root.
- Every message referencing `body.<path>` as a legal root.
- Every message naming `async_job`, `placeholder_event`, `defaults`,
  `state.fields`, `mutability`, `path:` on requests, or any of the seven
  legacy pagination strategies.
- The OAuth2 grant-exclusion message that listed `jwt_bearer`,
  `authorization_code`, `device_code` as "deferred".

## Notes for downstream slices

### Slice 8 — `client/state.go`

- The validator's lifetime classification is computed in
  `checkLifetimes` and discarded; only diagnostics are emitted. The
  client mirrors the same classification at scope-build time. The rule
  is: a field with `default:` only → operator config; a field that is
  the `to:` of any `pagination.*.to` write → per-drain scratch (wiped
  at drain start); a field that is the `to:` of any `progress[].to`
  write or `requests[].extract` with `to: state.*` → persistent
  (snapshot-included). A field that is BOTH per-drain and persistent
  is a validator error; the client never sees the conflicting case.
- `resolveNamespaceRef` mirrors the closed root set
  (state | cache | events | extract | steps | response). The fan-out
  alias is dispatched off the scope's currently-bound alias.
- The `events.*` projection vocabulary surfaces as ordinary
  `Path.Parts` to the scope: `events.first`, `events.last`,
  `events.<int>`, `events.count`, `events.*[.field]`. The validator
  enforces shape; the scope-resolver enforces semantics at evaluation
  time.

### Slice 12 — `client/runner.go`

- The runner reads `Request.OnStatus[<code>]` against the same closed
  verb set the validator enforces (skip / fail / empty_events /
  invalidate_cache). `invalidate_cache` against a doc with no reachable
  Cache block degrades to `empty_events` at request time per
  `docs/runtime.md` §6; the validator emits a warning at load time so
  the operator sees the degrade ahead of the first 4xx.
- `Request.TerminateWhen` is the request-level loop primitive. The
  validator binds the predicate's `path:` refs against the active
  scope's declared state / cache / steps / response paths; the runner
  re-fires the same step until the predicate is true.
- `error.mode` verb set (standard | warn | fail) is closed and
  validated; the runner dispatches off the same set.

### Slice 16 — `templates/*.yml`

- Every bundled template must round-trip through `schema.Validate`
  without errors (warnings are allowed for templates that opt into
  cache-less `invalidate_cache` degradation, though no template
  currently motivates that pattern).
- The reserved root set used by `fan_out.as` is
  state | cache | events | extract | steps | response. Author-chosen
  aliases like `incident`, `repo`, `user` clear the check; using any
  closed-root name is rejected.

## Build state

`go build ./schema/...` is **green** at slice close. `go test
./schema/...` is **red** — the pre-redesign assertions in
`schema/fixtures_test.go` and `schema/example_test.go` are still in
place; Slice 17 owns their rewrite.

## Verification

A throwaway smoke file (`schema/slice7_smoke_test.go`, build tag
`smoke`) ran 21 asserts covering: clean baseline, lifetime conflict
(pagination + progress), lifetime conflict (pagination +
extract-to-state), undeclared state field rejection, reducer scalar
operand rejection, reducer over `events.*` projection acceptance,
`invalidate_cache` warn-degrade when no cache block reachable,
`invalidate_cache` clean when cache reachable, unknown on_status verb
rejection, `pagination.custom` requires `terminate_when:`, fan_out.as
shadows reserved root, fan_out.as alias ref resolves,
`events.count` accepts no sub-path, `events.<int>` accepts integer
segment, `*` segment outside events rejected, Cache.To must be
`cache.<name>` (rejects `state.<name>`), request cache + fan_out
mutually exclusive, multi_mode nested rejected, request.terminate_when
predicate path validated against declared state, response.decode
closed-set rejection, progress.to must be `state.<name>`. The smoke
file was deleted before commit per the PHASE-2-PLAN convention.
