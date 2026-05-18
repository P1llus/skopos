# IMPL-12 — `client/runner.go`

## Scope

Rewrite `client/runner.go` against the post-redesign IR: collapse the
async-job phase machine, the `progressPlan` constructor, and the
`placeholder_event` two-pass logic into one drain loop that matches
[`docs/runtime.md` §2](../runtime.md#2-drain-lifecycle). The drain
sequence becomes Load → `(*scope).resetPerDrainScratch()` → pagination
loop (request chain with `requests[].terminate_when:` per step → bind
producer body/headers/events → emit to sink → `applyProgress(s,
doc.Progress)` → `pagination.advance(s)`) → deferred `Save`. The
`on_status:` verb table (`skip` / `fail` / `empty_events` /
`invalidate_cache`) and the `error.mode` fallback (`standard` / `warn`
/ `fail`) replace every retry / placeholder / phase-gate mechanism the
old runner carried.

## Old → new map

| Legacy call site / mechanism                                  | New mechanism                                                                                          |
|---------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|
| `pagination.seed(s)` per iteration                            | `(*scope).resetPerDrainScratch()` once at drain start; named-variant `to:` fields reset to their `default:` (or unset). Pagination plans no longer seed. |
| `progress.seed(s)` once per drain                             | First-run bootstrapping is the destination `state.<name>.default:` Value; the per-drain wipe re-evaluates it. No seed step exists. |
| `progress.advance(s, allEvents)` at drain end                 | `applyProgress(s, r.Doc.Progress)` inside the pagination loop, once per accepted page-response (including empty pages). |
| `makeProgressPlan(r.Doc)` constructor                         | Removed. The runner reads `r.Doc.Progress` directly.                                                    |
| `progressPlan` interface (5 methods)                          | Removed. `applyProgress` is one function, no variants.                                                  |
| `progress.shouldSkipForPhase(req, phase)`                     | Removed. The request loop primitive (`requests[].terminate_when:`) replaces phase gating; authors write three plain entries in `requests:` for the async-job pattern. |
| `progress.currentPhase(s)` / `progress.phaseTransition(...)`  | Removed. Same as above.                                                                                 |
| `placeholder_event` two-pass emission                         | Removed. Empty pages just trigger the next page; the `pendingPlaceholderEmpty` flag is gone.            |
| `isAsyncJob(progress)` / `producerStepID(doc, progress, phase)` | `producerStepID(doc)` reads `produces_events: true` directly; the implicit-last fallback returns `""` and `runIteration` compares by slice index. |
| Five-arm `on_status` switch (`skip`/`fail`/`empty_events`/`invalidate_cache`) inside `runIteration` | Four-arm switch inside `runRequest`, returning a `stepStatus` enum; per-status verbs decide the request's terminal verdict, which then maps to an `iterKind`. |
| `error.mode` switch inside the per-request loop                | Same shape, but the per-request verdict is now an `iterKind` (`iterAccepted` / `iterSkip` / `iterEmpty` / `iterInvalidate` / `iterWarn` / `iterBreak` / `iterFatal`); the Drain switch consumes that. |
| `s.invalidateAuthCaches(auth)` + `s.invalidateStepCaches(doc)` | `dropReachableCaches(s, doc)` walks `doc.Auth` (recursing into `multi_mode` branches) and `doc.Requests` for every `Cache` block, deleting `scope.cache[<slot>]` for the slots it finds. The legacy `state.<store_in>` slot convention is gone. |
| Producer body / headers passed as positional args to `pagination.advance` | Producer body / headers / events bound onto `scope.body` / `scope.responseHeaders` / `scope.events` after the chain settles. `pagination.advance(s)` reads from scope. |
| `pagination.advance(s, body, headers, events) (want_more, err)` polarity | `pagination.advance(s) (terminate, err)`. The runner exits the loop on `terminate=true`. |
| `valuePathLabel(req.Path, req.URL)` for log labels             | `reqLabel(req)` falls back to `requests[<METHOD>]` when `req.ID` is unset. The URL is a Value (often a `${...}`-interpolated string) and is not rendered into log lines — secret-typed refs inside could leak. |
| Request-level loop: never modelled (the async-job phase machine was the closest analogue) | `requests[].terminate_when:` fires the same request until the predicate evaluates true; the runner binds `scope.body` / `scope.responseHeaders` transiently during the predicate eval so `response.body.<path>` / `response.header.<name>` refs resolve against the just-finished response. |

## Removed content

Type declarations:

- `iterationResult` (the old fields: `producerBody`, `producerHeaders`,
  `events`, `iterMore`, `producerIteration`, `advance`, `fatal`) →
  replaced with the new `iterationResult{kind, producerBody,
  producerHeaders, events}` and an `iterKind` enum.

Functions / methods:

- `isAsyncJob(p progressPlan) bool`.
- The old multi-argument `producerStepID(doc, progress, phase)` —
  replaced with the single-argument `producerStepID(doc)`.
- `valuePathLabel(p, u *schema.Value) string` — `reqLabel` no longer
  renders the URL.
- The `placeholder_event` evaluation block (`r.Doc.Response.PlaceholderEvent`,
  the two-pass `pendingPlaceholderEmpty` flag, the bookkeeping that
  emitted a synthetic event on confirmed-empty pages).

Call sites / blocks:

- The whole "async_job phase machine" sub-system: every
  `progress.currentPhase(s)`, `progress.shouldSkipForPhase(req, phase)`,
  `progress.phaseTransition(s, stepID, res)` call.
- The drain-end `progress.advance(s, allEvents)` call (and the
  accumulator `allEvents`).
- The drain-start `progress.seed(s)` and per-iteration
  `pagination.seed(s)` calls.
- The drain-end `drainCanAdvance` flag (the new design surfaces
  break-without-advance through `iterBreak` directly).
- The `req.Cache != nil && s.cachedStepValue(req.Cache)` pre-check
  block — request-level caching is Slice 13's territory under the
  unified `Cache` struct (the cached value lives in `scope.cache`, not
  `scope.state`, and the cache-hit decision is made inside the HTTP
  layer).
- The `s.storeStepValue(req.Cache, res.body)` post-success block —
  same reason.
- The `s.invalidateAuthCaches(auth)` / `s.invalidateStepCaches(doc)`
  call sites — replaced by `dropReachableCaches(s, doc)` which walks
  the new unified `Cache` shape and clears `scope.cache[<slot>]` slots
  directly. The legacy `state.<store_in>` slot convention is gone.

Imports:

- No `progressPlan` references survive.

Doc comments / log strings:

- Every reference to `cursor.*` as a namespace root.
- Every reference to `async_job`, `phase`, `submit`, `poll`, `fetch`,
  `placeholder_event`, `Defaults.BaseURL`, `path:` on a request,
  `state.fields`, `mutability:`.

## Drain sequence

The Drain method runs the following in strict order:

1. **Validate inputs.** `Doc` and `Sink` required; `Store`, `Client`,
   `Logger`, `MaxPages` get defaults.
2. **Load snapshot.** `store.Load()` returns the persisted snapshot.
3. **Seed scope.** `newScope(doc, snap, now)` evaluates declared
   defaults and overlays the snapshot.
4. **Register deferred Save+Flush.** Registered BEFORE per-drain
   wipe and the pagination loop. Runs on every termination path:
   normal exit, `error.mode: warn` continuation, `error.mode: fail`
   abort, `iterBreak` from standard-mode, MaxPages cap, ctx
   cancellation, fatal iteration.
5. **Per-drain wipe.** `(*scope).resetPerDrainScratch()` resets every
   pagination `to:` field to its declared default (or unset).
6. **Construct pagination plan.** `makePaginationPlan(doc)` returns
   the active variant's driver.
7. **Pagination loop.** Each iteration:
   1. Check `ctx.Err()` and the MaxPages cap. Increment `pages`.
   2. Reset per-iteration scratch: `s.extract`, `s.steps`,
      `s.stepHeaders`, `s.body`, `s.responseHeaders`, `s.events`.
   3. Run the request chain via `runIteration`. Return on any error.
   4. Switch on `out.kind`:
      - `iterFatal`: defensive sentinel (paired with a non-nil err).
      - `iterBreak`: return nil — pagination loop ends here, deferred
        Save fires.
      - `iterInvalidate`: `continue` — no emit, no progress, no
        pagination advance; the next iteration retries the same
        page.
      - all other kinds fall through.
   5. Bind producer body / headers / events to scope (for
      applyProgress + pagination.advance reads).
   6. Emit events to sink (only for `iterAccepted`).
   7. Apply progress writes (`iterAccepted` / `iterSkip` /
      `iterEmpty`; skipped for `iterWarn`).
   8. Call `plan.advance(s)`; on terminate=true, return nil.

## Request loop

`runRequest` is the per-request loop primitive. It:

1. Fires `executeRequest`. On `unexpectedStatusError`, dispatches the
   per-step `on_status[<code>]` verb; falls through to `error.mode` on
   no override.
2. On a successful exchange, binds `scope.body` / `scope.responseHeaders`
   to the response and evaluates `req.TerminateWhen` (if set).
3. If `TerminateWhen` evaluates false, re-fires the same request after
   honouring `ctx.Err()`. If it evaluates true (or is absent), returns
   `stepOK` with the final `stepResult`.

The returned `stepStatus` (`stepOK` / `stepSkip` / `stepEmpty` /
`stepInvalidate` / `stepWarn` / `stepBreak` / `stepFatal`) maps to the
iteration's `iterKind`:

| `stepStatus` on the producer       | Iteration verdict |
|------------------------------------|-------------------|
| `stepOK` (or any non-producer step succeeding all the way to chain end) | `iterAccepted` |
| `stepSkip` on producer             | `iterSkip`       |
| `stepEmpty` on producer            | `iterEmpty`      |
| `stepInvalidate` on any step       | `iterInvalidate` |
| `stepWarn` on any step             | `iterWarn`       |
| `stepBreak` on any step            | `iterBreak`      |
| `stepFatal` on any step            | `iterFatal` (paired with non-nil error) |

Non-producer steps with `stepSkip` / `stepEmpty` let the chain continue
— the producer step might still run successfully later and the
iteration ends `iterAccepted`. `stepInvalidate` / `stepWarn` /
`stepBreak` / `stepFatal` stop the chain regardless of which step
produced them.

When the producer step's `if:` evaluates false (or the producer was
never reached because every preceding step gated out), the iteration
ends with `producerRan=false`; the final verdict downgrades from
`iterAccepted` to `iterWarn` so progress does NOT fire and pagination
advances as if the page came back empty. This matches
[`docs/runtime.md` §5](../runtime.md#5-progress-evaluation-timing)'s
"Progress does NOT fire when a step is skipped by `if:`".

## `on_status` verb table

Per [`docs/runtime.md` §6](../runtime.md#6-error-semantics):

| Verb              | `stepStatus` | Iteration effect                                                              |
|-------------------|--------------|-------------------------------------------------------------------------------|
| `skip`            | `stepSkip`   | No events. Progress + pagination advance.                                     |
| `fail`            | `stepFatal`  | Drain aborts with error.                                                      |
| `empty_events`    | `stepEmpty`  | No events. Progress + pagination advance.                                     |
| `invalidate_cache`| `stepInvalidate` | Caches dropped via `dropReachableCaches(s, doc)`. No events, no progress, no pagination advance; the next iteration retries the same page. Degrades to `stepEmpty` (`empty_events` semantics) when no reachable cache slot exists, with a log line. |

`invalidate_cache` walks `doc.Auth` (recursing into `multi_mode`
branches) and `doc.Requests` for every `Cache` block, then deletes
`scope.cache[<slot>]` for each slot whose name resolves through
`cacheSlotName(p schema.Path)` (the closed-form `cache.<name>`).

## `error.mode` fallback

| Mode       | `stepStatus` | Iteration effect                                                                    |
|------------|--------------|-------------------------------------------------------------------------------------|
| `standard` | `stepBreak`  | Pagination loop ends here without error. Deferred Save fires. Next drain re-tries the same window. |
| `warn`     | `stepWarn`   | Log, no events, no progress writes. Pagination advances as if the page came back empty. |
| `fail`     | `stepFatal`  | Drain returns non-nil. Deferred Save still fires.                                  |

Non-status errors (DNS, connection refused, decode failure) skip the
`on_status:` lookup entirely and dispatch directly through
`error.mode`. The pagination loop never advances on a transport-level
error in any mode (the `stepWarn` arm returns nil body/headers, the
producer body bind is nil, and the pagination plan's default
`terminate_when:` fires on absent source paths).

## MaxPages cap

The runner counts pagination iterations and exits with
`errMaxPagesExceeded` (`"client: drain exceeded MaxPages cap (suspected
pagination loop)"`) when the counter reaches `Runner.MaxPages` (or the
compiled-in default `10_000` when MaxPages is zero). A negative
MaxPages disables the cap.

The deferred Save persists whatever state was reached before the cap
fired, so a runaway loop that wrote 5000 pages' worth of progress
still saves that 5000-page mark.

## Removed and kept identifiers

Kept (still referenced as call targets by the new runner — bodies
owned by Slice 13's territory):

- `(*scope).executeRequest`, `(*scope).runExtracts`,
  `(*Runner).runFanOut`, `(*scope).evalPredicate`,
  `(*scope).evalValue`, `applyProgress`, `makePaginationPlan`,
  `(paginationPlan).advance`, `stripBodyRoot`, `locateEvents`,
  `redactURLError`, `asUnexpectedStatus`, `buildExchange`.

Added in this slice:

- `iterKind` enum (`iterAccepted` / `iterSkip` / `iterEmpty` /
  `iterInvalidate` / `iterWarn` / `iterBreak` / `iterFatal`).
- `stepStatus` enum (`stepOK` / `stepSkip` / `stepEmpty` /
  `stepInvalidate` / `stepWarn` / `stepBreak` / `stepFatal`).
- `(*Runner).runIteration` (rewritten signature: no `phase`, no
  `pagination` parameter, no `progress` parameter).
- `(*Runner).runRequest` — the per-request terminate_when: loop +
  on_status / error.mode dispatcher.
- `(*Runner).locateProducerEvents` — splits the events_at walk out of
  runIteration so the iteration body stays focussed on chain
  orchestration.
- `anyEvents` — lifts a `[]any` into the `scope.events` runtime shape
  (nil → nil; non-nil → as-is).
- `dropReachableCaches`, `dropAuthCaches`, `cacheSlotName` — the new
  cache invalidation walk against the unified `Cache` shape.

Removed in this slice (no longer present):

- `iterMore`, `producerIteration`, `drainCanAdvance`,
  `pendingPlaceholderEmpty`, `allEvents` slice — accumulator-style
  flags from the old drain.
- `isAsyncJob`, the old `producerStepID(doc, progress, phase)`,
  `valuePathLabel`.

## Build state

- `go build ./schema/...` — green.
- `go build -gcflags="-e" ./client/...` — red, with **zero** errors in
  `client/runner.go`. The remaining 21 errors live in:
  - `client/auth.go` (1) — Slice 13.
  - `client/http.go` (6) — Slice 13.
  - `client/oauth2.go` (7) — Slice 13.
  - `client/requestcache.go` (7) — Slice 13.
- Zero errors in any earlier-slice file (`state.go`, `value.go`,
  `predicate.go`, `extract.go`, `bodypath.go`, `pagination.go`,
  `progress.go`, `fanout.go`, `sink.go`, `trace.go`, `redact.go`,
  `filestore.go`, `doc.go`).
- Tests stay red until Slice 17.

## Walkthrough verification

The package will not compile end-to-end at slice close (Slices 13–14
own files that still reference removed shapes), so a real smoke
`_test.go` will not build inside `package client`. The trickier cases
were walked against the code as follows.

### Case 1 — request-level terminate_when (poll-until-complete)

```yaml
requests:
  - id: submit
    method: POST
    url: ${state.base_url}/jobs
    extract:
      - to: state.job_id
        from: response.body.id
  - id: poll
    method: GET
    url: ${state.base_url}/jobs/${state.job_id}
    terminate_when: {eq: {path: response.body.status, value: complete}}
  - id: fetch
    method: GET
    url: ${state.base_url}/jobs/${state.job_id}/results
    produces_events: true
```

Drain iteration:

1. `submit` runs once. `extract` writes `state.job_id`.
2. `poll` runs in the request loop. First response: `{status:
   "running"}` — `scope.body` binds to that; `terminate_when` evaluates
   false → re-fire. Second response: `{status: "running"}` — false →
   re-fire. Third response: `{status: "complete"}` — `terminate_when`
   evaluates true → return `stepOK`. The poll step's body lands in
   `s.steps["poll"]`; its headers in `s.stepHeaders["poll"]`.
3. `fetch` runs once. `produces_events: true` makes it the producer.
4. End of chain: scope.body / responseHeaders / events bind to
   `fetch`'s body and the decoded events list. Sink emits. applyProgress
   fires. pagination.advance fires.

Between steps, `scope.body` is reset to nil at the top of each
request's iteration so `poll`'s body cannot leak into `fetch`'s
hypothetical `response.body.*` ref (which the validator typically
rejects upstream, but the defensive reset keeps the runtime arm
explicit).

### Case 2 — `on_status: 429: empty_events`

```yaml
requests:
  - id: list
    method: GET
    url: ${state.base_url}/events
    on_status: {429: empty_events}
    produces_events: true
```

Drain iteration:

1. `list` runs. Response: status 429. `executeRequest` returns
   `(res, *unexpectedStatusError{status: 429})`.
2. `runRequest` matches `on_status[429] = "empty_events"`, logs, returns
   `(res, stepEmpty, nil)`.
3. Since `list` is the producer, runIteration sets
   `out.kind = iterEmpty`, `out.producerBody = nil`,
   `out.producerHeaders = nil`. `producerRan = true`.
4. Drain switch: not iterAccepted, so no sink emit. iterEmpty fires
   applyProgress. `pagination.advance(s)` runs against
   `scope.events = nil`, `scope.body = nil`, `scope.responseHeaders =
   nil`. For most variants (cursor_token / next_url) the from: path
   resolves to absent → terminate. The loop ends.

### Case 3 — `on_status: 304: skip` on producer (canonical ETag)

```yaml
requests:
  - id: list
    method: GET
    url: ${state.base_url}/events
    headers: {if-none-match: {ref: state.etag}}
    on_status: {304: skip}
    produces_events: true
```

Drain iteration:

1. `list` runs. Response: status 304. Dispatcher matches
   `on_status[304] = "skip"`, logs, returns `stepSkip`.
2. runIteration sets `out.kind = iterSkip`, `producerRan = true`.
3. Drain switch: iterSkip — no sink emit, but applyProgress runs (the
   author's progress entry could persist a `{now: true}` heartbeat
   even when the page hasn't changed). pagination.advance runs against
   nil scope.body — default terminate fires → loop ends.

### Case 4 — `on_status: 401: invalidate_cache` with an OAuth2 cache

```yaml
auth:
  oauth2:
    client_credentials:
      token_url: https://example.com/oauth/token
      client_id: ...
      client_secret: ...
      cache: {to: cache.access_token, expires_at: {ref: response.body.expires_in, default: "1h"}, buffer: "5m"}

requests:
  - id: list
    method: GET
    url: ${state.base_url}/events
    on_status: {401: invalidate_cache}
    produces_events: true
```

Drain iteration `pages=N`:

1. `list` runs with the cached `cache.access_token` (Slice 13's HTTP
   layer reads `scope.cache["access_token"]` and binds it into the
   `Authorization: Bearer` header). Response: status 401.
2. Dispatcher matches `on_status[401] = "invalidate_cache"`. Walks
   `doc.Auth.OAuth2.ClientCredentials.Cache.To` → `cache.access_token`
   → `cacheSlotName` returns `("access_token", true)`. Deletes
   `scope.cache["access_token"]`. `cleared = ["access_token"]`.
   Returns `stepInvalidate`.
3. runIteration returns `iterationResult{kind: iterInvalidate}`.
4. Drain switch: `iterInvalidate` → `continue`. No emit, no progress,
   no pagination advance. The next iteration's HTTP layer misses the
   cache, re-fetches a fresh token through the oauth2 grant's
   token_url, and the same page is requested again with the fresh
   `Authorization: Bearer` header.

Edge case: no cache block reachable. `dropReachableCaches` returns an
empty `cleared` slice. The dispatcher logs `"invalidate_cache (no
cache to invalidate; treating as empty_events)"` and returns
`stepEmpty` — the iteration becomes `iterEmpty` (no events, but
progress + advance run). The drain doesn't stall on an unhandleable
401.

### Case 5 — `error.mode: warn` on a producer 500

```yaml
error:
  mode: warn

requests:
  - id: list
    method: GET
    url: ${state.base_url}/events
    produces_events: true
```

Drain iteration:

1. `list` runs. Response: status 500. No `on_status` entry for 500.
   Dispatcher falls through to `error.mode: warn`, logs, returns
   `stepWarn`.
2. runIteration returns `iterationResult{kind: iterWarn, producerRan:
   true}`. `out.producerBody` and `out.producerHeaders` are nil.
3. Drain switch: not iterAccepted (no emit), iterWarn (skip
   applyProgress per `docs/runtime.md` §6). pagination.advance runs
   against nil body / nil headers / nil events. cursor_token's default
   terminate fires (`from:` resolves to absent) → loop ends. Deferred
   Save runs.

### Case 6 — MaxPages cap

A counter pagination with a buggy author-supplied `terminate_when` that
never evaluates true:

```yaml
pagination:
  counter:
    to: state.page
    start: 1
    step: 1
    terminate_when: {literal_bool: false}
```

Drain iteration:

1. Iteration 1 runs `list`. Sink emits 50 events. applyProgress fires.
   pagination.advance writes `state.page = 2`. `pages = 1`.
2. Iteration 2: same shape, `state.page = 3`. `pages = 2`.
3. ... iteration 10_000: `pages = 10_000`. The next loop iteration's
   guard fires: `pages >= maxPages` → return
   `errMaxPagesExceeded`. Deferred Save persists `state.page = 10_001`
   (the last advance fired). The next drain re-bootstraps state.page
   to 1 via the per-drain wipe.

### Case 7 — request `if:` gating the producer

```yaml
requests:
  - id: list
    method: GET
    url: ${state.base_url}/events
    if: {present: state.access_token}
    produces_events: true
```

Drain iteration with `state.access_token` unset (first run after a
fresh init):

1. `list` evaluates `if`: `{present: state.access_token}` → false.
   runIteration `continue`s the inner loop. `producerRan = false`.
2. After the chain, runIteration checks `!producerRan && out.kind ==
   iterAccepted` → downgrades `out.kind` to `iterWarn`.
3. Drain switch: iterWarn — no emit, no progress. pagination.advance
   runs against nil scope.body — default terminate fires → loop ends.

### Case 8 — interleaved non-producer skip with successful producer

```yaml
requests:
  - id: optional_ping
    method: GET
    url: ${state.base_url}/ping
    on_status: {404: skip}
  - id: list
    method: GET
    url: ${state.base_url}/events
    produces_events: true
```

Drain iteration:

1. `optional_ping` runs. Response: 404 → `stepSkip`. The chain
   continues — the per-step verdict doesn't override the iteration's
   kind because `optional_ping` is not the producer.
2. `list` runs. Response: 200 → `stepOK`. Producer captured.
3. runIteration ends `iterAccepted`. Drain emits events, applyProgress
   fires, pagination.advance fires.

## Notes for downstream slices

### Slice 13 — `client/http.go`, `auth.go`, `cache.go` (`oauth2.go` + `requestcache.go` merge), `fanout.go`

- The new runner calls `s.executeRequest(ctx, client, req, trace)`,
  `s.runExtracts(res, req.Extract)`, and
  `(*Runner).runFanOut(ctx, client, logger, s, req, errMode, iter, "")`.
  The fan-out call passes `""` for the `phase` parameter — Slice 13
  is free to drop the parameter from the signature in the same
  rewrite, but the runner side is already phase-free.
- `dropReachableCaches(s, doc)` lives in `runner.go` and walks the
  new unified `schema.Cache` shape (To Path / ExpiresAt Value /
  Buffer string). Slice 13 owns the cache MISS / WRITE path; the
  runner only owns the INVALIDATE path. The two stay independent —
  the runner deletes `scope.cache[<slot>]` entries; the HTTP layer
  reads/writes those same entries via the unified `Cache` block.
- `req.Cache != nil` no longer triggers a pre-check inside
  `runIteration`. The HTTP layer (Slice 13) owns the cached-fresh /
  cached-stale decision and the underlying-fetch fan-out; the runner
  treats every request as "fire it" and lets the HTTP layer return a
  cached `stepResult` when applicable. This keeps the runner's
  iteration loop free of cache plumbing.
- `req.URL` is now a `Value` (was a `*Value`); the runner's
  `reqLabel` does NOT render the URL into log strings (a secret-typed
  ref inside the Value could leak through to operator-facing logs).
  Slice 13's HTTP layer renders the safe URL into trace records via
  `safeURL` already; nothing changes there.
- The runner reads `req.OnStatus[<status>]` as `map[int]string` and
  dispatches against the closed verb set (`skip` / `fail` /
  `empty_events` / `invalidate_cache`). The validator already enforces
  the closed verb set at validate time; the runtime defensive arm
  falls through to `error.mode` for any unknown verb (which the
  validator would have rejected).
- `runFanOut`'s body still references the legacy
  `s.invalidateAuthCaches` / `s.invalidateStepCaches` helpers for its
  internal `on_status: invalidate_cache` handling. Slice 13 should
  rewire fan-out's invalidate path to use the new
  `dropReachableCaches` helper as well.

### Slice 14 — `client/sink.go`, `redact.go`, `trace.go`, `doc.go`

- `Exchange.Phase` is no longer populated by the runner (every
  `buildExchange` call now passes `""`). Slice 14 can drop the field
  from `Exchange` (and `httpTrace`) entirely, or leave it as
  `omitempty` with no contributors — both shapes produce the same
  wire output.
- The runner's per-iteration call site for the tracer is
  `r.Tracer.OnExchange(buildExchange(r.Doc, req, trace, runErr, iter,
  ""))`. Slice 14 may want to rename the parameter or change the
  signature to drop the phase argument now that it is dead.
- The runner does NOT touch `Sink.Flush()` directly inside the
  pagination loop; only the deferred unwind calls it. This matches
  the contract Slice 14's `JSONLSink` already documents.
- Secret redaction inherits from Slice 14's existing layer — the
  runner does not introduce any new value-rendering surface.

### Slice 15 — `cmd/skopos/*`

- The continuous-mode (`--interval`) CLI calls `Drain` in a loop with
  sleeps. Every `Drain` return must be checked: `nil` from a clean
  drain or a `warn`-mode drain; non-nil from a `fail`-mode drain or
  the MaxPages cap. The CLI's continuous loop should log the error
  and continue when the drain returns `errMaxPagesExceeded` (the
  deferred Save persisted whatever state was reached); other errors
  follow whatever continuous-mode error policy Slice 15 picks.
- `errMaxPagesExceeded` is exported indirectly via `errors.Is` so the
  CLI can distinguish the cap from a server-side failure. If Slice 15
  needs the sentinel as a typed value, it can be exported in the same
  slice rewrite — the runner-side rename to `ErrMaxPagesExceeded` is
  a one-token change.

### Slice 17 — Tests

- The current `client/runner_test.go` (~526 lines) is fully red.
  Every async_job test fixture, every `placeholder_event` test
  fixture, every `progressPlan` test fixture, and every `cursor.*`
  reference is now stale. The rewrite should anchor on golden inputs
  driven by `internal/testserver/` fakes that exercise:
  - Each named pagination variant against a multi-page server.
  - `requests[].terminate_when:` against a 3-step submit/poll/fetch
    chain.
  - Each `on_status` verb against a fake server that returns the
    matching status on cue.
  - Each `error.mode` mode against transport-level failures and
    unexpected status codes.
  - The MaxPages cap against a fake server returning the same cursor
    token forever.
  - The `dropReachableCaches` walk against fixtures with one auth
    cache, one step cache, both, and multi_mode auth.
