# Handoff — Phase 2 Slice 12 (`client/runner.go`)

Slice 11 closed (`client/progress.go` rewritten against the new IR;
the five-variant `progressPlan` interface and the async-job phase
machine collapse into one `applyProgress(s, doc.Progress)` pass that
fires once per accepted page-response; every entry's `from:` resolves
against the pre-write snapshot of `state.*`, then every `to:` writes;
`regex:` captures group 0 with no default and is skipped when the
staged value is nil; `coerce:` dispatches through `applyFormat`).
See [`IMPL-11-client-progress.md`](IMPL-11-client-progress.md).
`schema/` plus `client/{state,value,predicate,extract,bodypath,
pagination,progress}.go` build green; Slice 11 introduced **zero**
new errors in the file it owns. The new errors in `runner.go`
(`makeProgressPlan undefined`, `progressPlan undefined` at three
sites) are deliberate — Slice 12 rewires the call site against the
new function-shaped interface.

The next slice is **Slice 12 — `client/runner.go`** (rewrite the
drain loop, the request-level loop primitive, the `on_status` verb
table, and the `error.mode` fallback against the post-redesign
surface).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 12"](PHASE-2-PLAN.md#slice-12--clientrunnergo).
   Your slice's detailed scope.
4. [`docs/runtime.md`](../runtime.md). The whole document is the
   operational reference for Slice 12. The high-priority sections:
   §2 (drain lifecycle), §3 (pagination loop), §4 (request loop), §5
   (progress evaluation timing), §6 (error semantics — `on_status` +
   `error.mode`).
5. [`docs/schema.md` §requests](../schema.md) for the new
   `requests[]` shape — `id:`, `terminate_when:`, `on_status:`, the
   `cache:` block, `fan_out:` exclusivity. §error.mode for the closed
   verb set.
6. [`IMPL-08-client-state.md`](IMPL-08-client-state.md). The
   `(*scope).resetPerDrainScratch` per-drain wipe is the replacement
   for every old `pagination.seed` / `progress.seed` call. Read the
   classification helpers (`perDrainScratchFields`,
   `persistentStateFields`) and the deferred-`Save` contract for
   `error.mode: warn` / `fail`.
7. [`IMPL-10-client-pagination.md` §"Notes for downstream slices" /
   Slice 12 sub-section](IMPL-10-client-pagination.md). The new
   `pagination.advance(s) (terminate bool, err error)` signature; the
   polarity flip from `want_more=true` (loop again) to
   `terminate=true` (stop); the MaxPages safety cap.
8. [`IMPL-11-client-progress.md` §"Notes for downstream slices" /
   Slice 12 sub-section](IMPL-11-client-progress.md). The
   `applyProgress(s, r.Doc.Progress)` call site placement and the
   "accepted page-response" gate.

---

## Your slice

**Branch.** Develop on `claude/slice-12-client-runner-<token>`.

**Files you may touch.**

- `client/runner.go`

**Files you must NOT touch.**

- `client/state.go`, `client/value.go`, `client/predicate.go`,
  `client/extract.go`, `client/bodypath.go`, `client/pagination.go`,
  `client/progress.go` (frozen at Slices 8-11's close).
- `client/auth.go`, `client/oauth2.go`, `client/requestcache.go`,
  `client/http.go`, `client/fanout.go` (Slice 13 owns the auth /
  cache / http / fan-out rewrites; this slice should NOT pre-rewire
  any of their surfaces).
- `client/sink.go`, `client/filestore.go`, `client/redact.go`,
  `client/trace.go`, `client/doc.go` (Slice 14 owns those).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).

**Deliverables.**

1. **Drain sequence (per `docs/runtime.md` §2).**
   1. `store.Load` → seed scope.
   2. `(*scope).resetPerDrainScratch()` — wipes every per-drain
      scratch state field back to its declared `default:` (or unset
      when no default).
   3. Pagination loop. Each iteration:
      a. Run the requests chain end-to-end, honouring `if:` and the
         per-request `terminate_when:` loop on each step.
      b. Decode the producer step's body, resolve `events_at`, bind
         to `scope.events` / `scope.body` / `scope.responseHeaders`.
      c. Emit each event to `Sink` (one call per event).
      d. `applyProgress(s, r.Doc.Progress)` — once per accepted
         page-response, including empty pages.
      e. `pagination.advance(s)`: terminate? exit. Else loop.
   4. `defer store.Save(s.snapshot())` runs on normal exit,
      `error.mode: warn`, AND `error.mode: fail`.

2. **Request loop (per `docs/runtime.md` §4).** A request with
   `terminate_when:` re-fires until the predicate evaluates true; a
   request without it runs exactly once. The predicate sees the
   just-finished step's response body and headers via
   `response.body.<path>` / `response.header.<name>`.

3. **`on_status` verb table (per `docs/runtime.md` §6).** Closed
   set:
   - `skip` — drop response, emit no events, advance progress as if
     successful (`applyProgress` still fires).
   - `fail` — emit no events, surface the iteration as a non-success
     so `error.mode` takes over.
   - `empty_events` — emit no events but DO call `applyProgress`.
   - `invalidate_cache` — drop every reachable `cache.*` slot (the
     active auth's cache + every `requests[].cache` slot), then
     treat the response as a non-event "retry next iteration"
     signal. Degrade to `empty_events` with a log line when no
     reachable cache slot exists.

4. **`error.mode` fallback (per `docs/runtime.md` §6).**
   - `standard` (default) — pagination loop ends, per-drain wipe
     runs at the next drain start, deferred Save runs.
   - `warn` — log, continue, iteration advances as if the page came
     back empty, `applyProgress` does NOT fire for this iteration.
   - `fail` — `Drain` returns non-nil; deferred Save still runs.

5. **MaxPages safety cap.** Increment a page counter after each
   `pagination.advance(s)` returns `(false, nil)`. When the counter
   exceeds the document-level cap (or the runner's compiled-in
   ceiling when the document declines to set one), exit the drain
   with an "iteration cap exceeded" diagnostic.

6. **Removals.**
   - `pagination.seed(s)` call sites — gone. The per-drain wipe
     replaces them.
   - `progress.seed(s)` / `progress.advance(s, events)` /
     `progress.shouldSkipForPhase(req, phase)` /
     `progress.currentPhase(s)` /
     `progress.phaseTransition(s, stepID, res)` call sites — all
     gone. Replace with the single `applyProgress` call.
   - `progressPlan` type references — gone.
   - `makeProgressPlan(doc)` constructor call — gone. The runner
     reads `r.Doc.Progress` directly.
   - `placeholder_event` two-pass logic — gone. Empty pages just
     trigger the next page; no synthetic event.
   - Async-job phase gating (`shouldSkipForPhase` /
     `currentPhase` / `phaseTransition`) — gone. The new shape is a
     three-request chain in `requests:` where the poll step carries
     `terminate_when:`. Authors write a progress entry to persist
     the completion timestamp.
   - `placeholder_event` references in trace records — gone.
   - Any `cursor.*` namespace plumbing — gone. The validator and the
     scope resolver no longer accept that root.

7. **Reuse, don't duplicate.** `(*scope).resetPerDrainScratch`
   handles bootstrapping. `(*scope).evalPredicate` handles every
   predicate (`if:`, `terminate_when:` on requests, `terminate_when:`
   on pagination, `on_status` decode). `(*scope).evalValue` handles
   every Value (URL, headers, body, cache TTL, etc.). The pagination
   plan, the progress applier, and the request executor are already
   shaped to be called from inside this loop.

8. **Hygiene pass** per global rule #5. Top-of-file comments,
   doc-strings on every helper, error-message strings: no
   `cursor.<name>` as a namespace root, no `body.<path>` as a
   top-level root, no slice numbers, no design-doc references, no
   "for backwards compatibility", no references to the legacy
   progress variants, the async-job phase machine, the
   `placeholder_event` mechanism, the old `Defaults.BaseURL` /
   `requests[].path` shape, or the old `state.fields` /
   `mutability:` vocabulary. Error messages should read as if the
   post-redesign shape had always existed.

9. New artefact `docs/planning/IMPL-12-client-runner.md` carrying:
   - Scope (one sentence).
   - Old → new map (per legacy call → new mechanism — `pagination.seed`,
     `progress.seed`, `progress.advance`, the five `progressPlan`
     methods, `placeholder_event`, the async-job phase machine).
   - Removed-content list (every deleted helper, field, call site).
   - Build-state enumeration (Slice 12 should bring the runner.go
     red-error count down to zero; the remaining client/* errors
     live in `auth.go` / `oauth2.go` / `requestcache.go` /
     `http.go` until Slice 13).
   - Notes for downstream slices (Slice 13 on the auth / cache /
     http rewires, Slice 14 on the sink / redact / trace surface
     and the new trace record layout, Slice 15 on the CLI's
     continuous-mode error policy).

10. Slice-table row 12 in `RESEARCH_PLAN.md` flipped to `[x]` with
    the `IMPL-12-client-runner.md` link.

11. `HANDOFF.md` rewritten to point at Slice 13
    (`client/{http,auth,cache,fanout}.go`).

**Smoke tests.** Throwaway and optional. The trickier areas are:

- A request with `terminate_when:` that needs three iterations
  before the predicate becomes true — verify the same request is
  re-issued, the scope's `response.body.<path>` and
  `response.header.<name>` resolve against the latest response, and
  `applyProgress` does NOT fire mid-request-loop (it fires once per
  page-response, AFTER the request chain settles).
- An `on_status: 429: empty_events` page — verify no events emit,
  `applyProgress` fires, pagination advances, the loop continues.
- An `on_status: 304: skip` page — verify no events emit,
  `applyProgress` fires, pagination advances, the loop continues.
- An `on_status: 401: invalidate_cache` page where the active auth
  has a `cache:` slot — verify the slot is removed from
  `scope.cache` and the loop continues (next iteration re-fetches
  the token via the cache helper's miss path).
- An `error.mode: warn` failure — verify the iteration advances
  without firing `applyProgress` and without emitting events.
- A drain that hits MaxPages — verify the "iteration cap exceeded"
  diagnostic surfaces and the deferred Save runs.

The client package will still NOT compile end-to-end at slice close
(Slices 13-14 own files that still reference removed shapes), so a
real smoke `_test.go` won't build inside `package client`. A
walkthrough verification is acceptable; document it in
`IMPL-12-client-runner.md`.

**Out of scope.** No schema changes (Slice 7 closed). No state /
scope / value-runtime / pagination / progress changes (Slices 8-11
closed). No http / auth / cache / fan-out changes (Slice 13). No
sink / trace changes (Slice 14). No CLI changes (Slice 15). No
templates (Slice 16). No tests (Slice 17).

---

## Watch out for

- **The package will not compile end-to-end.** `auth.go`,
  `oauth2.go`, `requestcache.go`, `http.go` still reference removed
  schema types. Your slice owns one file only; the rest stays red
  until Slice 13.

- **The new request loop is the replacement for the async-job phase
  machine.** Authors who used to write `async_job: {submit, poll,
  fetch}` now write three entries in `requests:` — the poll step
  carries `terminate_when:` and the runner re-fires it until the
  predicate is satisfied. Do NOT add any phase-gating helper; the
  re-fire is the whole mechanism.

- **`applyProgress` fires once per accepted page-response.** The
  call site is inside the pagination loop, AFTER the request chain
  settles and AFTER events emit to the sink. Authors who used to
  rely on `progress.seed` firing per drain now declare a
  `default:` on the destination state field — the per-drain wipe
  evaluates that default at drain start.

- **Per-drain wipe semantics are about scratch vs. persistent.**
  `(*scope).resetPerDrainScratch` wipes only the fields classified
  as scratch (target of any `pagination.*.to` write that is not
  also a `progress[].to` target). Persistent fields (target of any
  `progress[].to` or `requests[].extract` with `to: state.*`) keep
  their values across drains and are read off the snapshot in
  `newScope`.

- **`pagination.advance(s)` has the new polarity.** `terminate=true`
  means stop. The old `want_more=true` meant loop. Don't invert.

- **`error.mode: warn` skips `applyProgress`.** This is per
  `docs/runtime.md` §6: warn-on-failure must not leak partial
  progress writes that survive the deferred Save. Skip the
  `applyProgress` call AND continue the loop.

- **Deferred Save runs in every termination path.** Normal exit,
  `error.mode: warn` (success), `error.mode: fail` (error return),
  MaxPages cap exceeded, panic recovery (if any). Write the deferred
  Save before the first early-return inside Drain so every exit path
  hits it.

- **No `cursor.*` references survive in trace records, log lines, or
  error strings.** A grep for `cursor` in `runner.go` after the
  slice closes should find nothing.

- **Tests stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `client/runner_test.go`. Expect `go test ./client/...` to stay
  red.

---

## When you finish

`git add` the rewritten file, the new `IMPL-12-client-runner.md`,
the updated `RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit
with a message like:

```
feat(client): slice 12 — runner against the new IR

Drain loop rewrite per docs/runtime.md §2: load → per-drain wipe →
pagination loop (requests chain with terminate_when on each step,
events to sink, applyProgress per accepted page, pagination advance)
→ deferred Save. The on_status verb table (skip / fail /
empty_events / invalidate_cache) replaces every retry / placeholder
mechanism; the request-level terminate_when loop replaces the
async-job phase machine; applyProgress fires once per accepted
page-response, including empty pages. MaxPages guards the
pagination loop against runaway servers.

schema/ + client/{state,value,predicate,extract,bodypath,pagination,
progress,runner}.go build at slice close (modulo auth/oauth2/
requestcache/http which Slice 13 owns); tests stay red until Slice 17.
```

Push to `claude/slice-12-client-runner-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
