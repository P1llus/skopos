# IMPL-11 — `client/progress.go`

## Scope

Replace the five-variant `progressPlan` interface (plus the embedded
`async_job` phase machine) with one `applyProgress(s *scope, writes
schema.Progress) error` pass that fires once per accepted page-response,
stages every `from:` against the pre-write snapshot of `state.*`, and
commits every `to:` after staging completes.

## Old → new map

The seven legacy strategies (five `progressPlan` variants plus the
`async_job` phase machine, plus the bare `stateless` no-op) all collapse
into a single flat write list — the runtime never branches on variant
identity. The shape conversions below are what authors write under the
new `progress:` key.

| Legacy variant            | New shape                                                                                                                       | Notes |
|---------------------------|--------------------------------------------------------------------------------------------------------------------------------|-------|
| `stateless`               | omit `progress:`, or `progress: []`                                                                                            | The empty list is the no-progress form; `applyProgress` returns nil immediately. |
| `latest_event_timestamp`  | `progress: [{to: state.last_ts, from: {max: [{ref: state.last_ts}, {max: {ref: events.*.<field>}}]}}]`                         | Cumulative high-water mark is author-written via reducers. `state.last_ts.default: {subtract: [{now: true}, "<lookback>"]}` covers the prior `initial.lookback` first-run seed. |
| `max_event_field`         | Same shape as `latest_event_timestamp`.                                                                                        | The legacy variant was a documentation-only distinction; the runtime behaviour is identical. |
| `use_now`                 | `progress: [{to: state.last_ts, from: {now: true}}]` or `progress: [{to: state.last_ts, from: {subtract: [{now: true}, "<lookback>"]}}]` | The per-iteration `lookback` becomes an explicit `{subtract: ...}` arithmetic Value. |
| `time_window`             | Two entries — `{to: state.window_start, from: {ref: state.window_end, default: {subtract: [{now: true}, "<initial_offset>"]}}}` plus `{to: state.window_end, from: {now: true}}` | The "slide the window" semantic is the snapshot-then-write ordering: the new `state.window_start` reads the OLD `state.window_end`. First-run seeding lives in the `default:` arm of the ref. |
| `async_job` (phase machine) | A three-request `requests:` chain (`submit` / `poll` / `fetch`) using `requests[].terminate_when:` on the poll step.          | Owned by Slice 12. `on_complete.cursor_update` is replaced by a `progress:` entry written by the author. |
| `async_job.on_complete.cursor_update.kind=use_now`              | `progress: [{to: state.last_ts, from: {now: true}}]` (with `{subtract: ...}` for lookback). |  |
| `async_job.on_complete.cursor_update.kind=latest_event_timestamp` | Same `{max: [...]}` form as `latest_event_timestamp`.                                       |  |

## Removed content

The whole old file is removed wholesale. Specifically gone:

- `progressPlan` interface (the entire five-method surface: `seed`,
  `advance`, `shouldSkipForPhase`, `currentPhase`, `phaseTransition`).
- `makeProgressPlan` dispatcher.
- `statelessProgress` struct + four method implementations.
- `latestTimestampProgress` struct + four method implementations.
- `maxEventFieldProgress` struct + four method implementations.
- `useNowProgress` struct + four method implementations.
- `timeWindowProgress` struct + `formatVerb` helper + four method
  implementations.
- `asyncJobProgress` struct + every helper on it:
  `firstPhase`, `nextPhase`, `stepID`, `phaseForStep`, `seed`,
  `advance`, `shouldSkipForPhase`, `currentPhase`, `phaseTransition`,
  `captureExtracts`, `clearExtractedCursors`, `applyOnComplete`.
- `seedTimestampCursor`, `advanceTimestampCursor`, `maxEventTime` —
  the shared timestamp-cursor helpers.
- Every reference to a `cursor.*` namespace root inside this file
  (was `s.cursor["last_timestamp"]`, `s.cursor["window_start"]`,
  `s.cursor["window_end"]`, `s.cursor["phase"]`, plus every
  extract-captured cursor name).
- Every reference to a phase ("submit" / "poll" / "fetch") and to the
  `requests[].id` matching that selects the active phase step.
- Every reference to `on_complete.cursor_update` (the `use_now` /
  `latest_event_timestamp` / `stateless` switch).

What remains in `client/progress.go` is one exported-internal entry
point plus its doc comment:

- `func applyProgress(s *scope, writes schema.Progress) error`

That's it.

## Type-pair dispatch

`applyProgress` has no per-variant dispatch — the whole point of the
collapse is that there are no variants left. The only branching inside
the function is per-entry transform application:

| Slot on `ProgressWrite` | When applied                                                | Helper |
|-------------------------|-------------------------------------------------------------|--------|
| `From`                  | Always.                                                     | `(*scope).evalValue` |
| `Regex`                 | When `Regex != ""` AND the staged value is non-nil.         | `(*scope).applyRegex` with `capture=0`, `def=nil` |
| `Coerce`                | When `Coerce != ""` AND the staged value is non-nil.        | `applyFormat` |
| `To`                    | Always, in the second pass after every `From` has staged.   | `stateFieldName` from `state.go` |

The regex / coerce gating on `got != nil` mirrors the same gates inside
`customPagination.advance` (Slice 10): the staged nil is preserved
through transforms and committed to `state.<name>` as nil so authors
can use `{ref: state.<name>, default: ...}` at read sites to detect
"never resolved" pages.

## Logic summary

```
if len(writes) == 0 { return nil }

staged := []any of len(writes)
for i, w := range writes:
    got, err := s.evalValue(w.From)        // wrap "progress[i].from:"
    if w.Regex != "" && got != nil:
        got, err = s.applyRegex(...)        // wrap "progress[i].regex:"
    if w.Coerce != "" && got != nil:
        got, err = applyFormat(...)         // wrap "progress[i].coerce <verb>:"
    staged[i] = got

for i, w := range writes:
    name, ok := stateFieldName(w.To)        // wrap "progress[i].to:"
    s.state[name] = staged[i]

return nil
```

The two-pass structure is what makes the snapshot-then-write contract
work. Reading inside the first pass through `s.evalValue(w.From)` calls
`(*scope).resolveNamespaceRef` for `{ref: state.<other>}` references —
that resolver reads `s.state[name]`, which is still the prior page's
value because no `to:` writes have fired yet.

## Walkthrough verification

The package will not compile end-to-end at slice close (Slices 12-14
own the files that still reference removed shapes), so a real smoke
`_test.go` won't build inside `package client`. Walkthroughs against
the trickiest cases follow.

### Case 1 — Two writes that depend on each other (sliding window)

```yaml
progress:
  - to: state.window_start
    from: {ref: state.window_end, default: {subtract: [{now: true}, "30d"]}}
  - to: state.window_end
    from: {now: true}
```

Suppose the previous page left `state.window_start = 2026-04-01T00:00:00Z`
and `state.window_end = 2026-05-01T00:00:00Z`, and the current `now() =
2026-06-01T00:00:00Z`.

- Pass 1, entry 0: `evalValue({ref: state.window_end, default: ...})`
  reads `s.state["window_end"]` → `2026-05-01T00:00:00Z`. Stores in
  `staged[0]`.
- Pass 1, entry 1: `evalValue({now: true})` returns
  `2026-06-01T00:00:00Z`. Stores in `staged[1]`.
- Pass 2, entry 0: `s.state["window_start"] = 2026-05-01T00:00:00Z`.
- Pass 2, entry 1: `s.state["window_end"] = 2026-06-01T00:00:00Z`.

The window slid: the new `window_start` carries the OLD `window_end`,
not the just-being-written one. Declaration order is irrelevant —
swapping the two entries produces the same result.

### Case 2 — Cumulative high-water mark

```yaml
progress:
  - to: state.last_ts
    from: {max: [{ref: state.last_ts}, {max: {ref: events.*.timestamp}}]}
```

Suppose `state.last_ts = "2026-05-01T00:00:00Z"` and the page's events
include timestamps `["2026-04-30T12:00:00Z", "2026-05-02T08:00:00Z",
"2026-05-01T18:00:00Z"]`.

- `evalValue` for the outer `{max: [a, b]}`:
  - `a = evalValue({ref: state.last_ts})` → `"2026-05-01T00:00:00Z"`.
  - `b = evalValue({max: {ref: events.*.timestamp}})`:
    - `{ref: events.*.timestamp}` resolves through
      `resolveNamespaceRef`'s `events` arm + `*` projection →
      `["2026-04-30T12:00:00Z", "2026-05-02T08:00:00Z", "2026-05-01T18:00:00Z"]`.
    - The reducer (`evalReducerInput` + `reduceExtreme`) picks the
      max → `"2026-05-02T08:00:00Z"`.
  - The outer `max` picks the greater of `a` and `b` →
    `"2026-05-02T08:00:00Z"`.
- Pass 2: `s.state["last_ts"] = "2026-05-02T08:00:00Z"`.

If the next page has only older events (or is empty), the inner `max`
yields nil / a smaller value and the outer `max` preserves the
existing `state.last_ts` — the high-water mark monotonically advances.

### Case 3 — Empty-page firing

```yaml
progress:
  - to: state.last_run_at
    from: {now: true}
```

With `scope.events = []any{}` (empty page accepted by `expect_status`
+ `on_status`):

- Pass 1, entry 0: `evalValue({now: true})` returns `s.now()`. No
  inspection of `scope.events`. Staged.
- Pass 2, entry 0: `s.state["last_run_at"] = <time>`.

The function never branches on event count; the runner's call site is
responsible for the "accepted page-response" gate. Empty pages are
just one shape of accepted page-response.

### Case 4 — Coerce numeric string

```yaml
progress:
  - to: state.events_seen
    from: {ref: response.body.total}
    coerce: int
```

Suppose the response body has `total: "42"` (a JSON string that
should be persisted as an integer).

- Pass 1, entry 0: `evalValue({ref: response.body.total})` returns
  `"42"`. `Regex == ""` skips that branch. `Coerce == "int"` and
  `got != nil` → `applyFormat("int", "42")` calls `toInt("42")` which
  returns `int64(42)`. Staged as `int64(42)`.
- Pass 2, entry 0: `s.state["events_seen"] = int64(42)`.

### Case 5 — Regex no-match staging nil

```yaml
progress:
  - to: state.next_etag
    from: {ref: response.header.ETag}
    regex: '^W/"([^"]+)"$'
```

Suppose `response.header.ETag` is the strong-etag form `"abc"` (not
weak), so the regex does not match.

- Pass 1, entry 0: `evalValue` returns `"\"abc\""`. `Regex != "" &&
  got != nil` → `s.applyRegex(...)` returns `(nil, nil)` because
  `def == nil` and the match failed. Staged as `nil`. The `Coerce`
  branch is skipped because `got == nil`.
- Pass 2, entry 0: `s.state["next_etag"] = nil`.

Authors who want a default at read time use
`{ref: state.next_etag, default: ...}` at the next request's slot of
choice — the snapshot-then-write loop preserves the staged nil
faithfully, which is the right call for "weak etag absent on this
page" telemetry.

## Build state

- `go build ./schema/...` — green.
- `go build -gcflags="-e" ./client/...` — red, 32 errors across 5
  files (`auth.go`, `http.go`, `oauth2.go`, `requestcache.go`,
  `runner.go`).
- Zero errors in `client/progress.go`.
- Zero errors in any file owned by an earlier slice (`state.go`,
  `value.go`, `predicate.go`, `extract.go`, `bodypath.go`,
  `pagination.go`).
- New errors introduced by this slice all live in `runner.go` and
  are deliberate:
  - `undefined: makeProgressPlan` at the constructor call site;
  - `undefined: progressPlan` at three remaining type references;
  these get rewired by Slice 12 against `applyProgress`.

## Notes for downstream slices

### Slice 12 — `client/runner.go`

- Drop the constructor: there is no `makeProgressPlan` — the runner
  reads `r.Doc.Progress` directly and hands it to `applyProgress` per
  iteration.
- Drop every `progressPlan` field, parameter, and type reference on
  `Runner`.
- The new call site is one line per page iteration, placed inside the
  pagination loop AFTER events have been emitted to the sink and
  BEFORE `pagination.advance(s)`:

  ```go
  if err := applyProgress(s, r.Doc.Progress); err != nil { ... }
  ```

- Gate the call on the "accepted page-response" qualifier — skip the
  call when the iteration is short-circuited by a transport-level
  error, an `on_status: fail`, or an `if:` skip on the producer step.
  An `on_status: skip` / `on_status: empty_events` / accepted-with-
  empty-events page DOES fire `applyProgress`.
- Under `error.mode: warn`, the runner logs the failure and continues
  WITHOUT calling `applyProgress` for that iteration (per
  `docs/runtime.md` §6).
- Drop every `phaseTransition` / `shouldSkipForPhase` /
  `currentPhase` call site. The new request-loop primitive in Slice 12
  (`requests[].terminate_when:`) replaces the phase machine. The
  three-request `submit` / `poll` / `fetch` chain becomes three normal
  entries in `requests:`; the poll step carries
  `terminate_when:`, and authors write a progress entry to persist
  the completion timestamp.
- The old `progressPlan.advance(s, events)` was invoked once per
  drain on the producer step's events; the new function fires once
  per page-response. A multi-page drain therefore now applies its
  cumulative high-water mark page by page, which is intentional —
  state survives a mid-drain `error.mode: standard` interrupt.

### Slice 13 — `client/http.go`, `auth.go`, `cache.go`

- Unaffected by the progress rewrite. The `cache.*` namespace is read
  by `evalValue` through the scope's existing resolver; this slice
  did not touch any of that.

### Slice 14 — `client/sink.go`, `redact.go`, `trace.go`

- Secret-taint propagation already covers every Value form an author
  can write inside `progress[].from` (the same `evalValue` surface
  the redact layer walks). No new plumbing.

### Slice 17 — Tests

- `client/progress_test.go` (~177 lines on disk today) is fully red.
  The rewrite anchors on golden inputs that exercise the five
  walkthrough cases above plus the empty-list / nil-list short-circuit
  and the `progress[i].to: must be state.<name>` defensive arm (the
  validator catches this at parse time today, but the runtime arm is
  cheap insurance against a future regression in the schema layer).
