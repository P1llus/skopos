# Handoff — Phase 2 Slice 11 (`client/progress.go`)

Slice 10 closed (`client/pagination.go` rewritten against the new IR;
`paginationPlan` shrunk to a single `advance(s *scope) (terminate bool,
err error)` method; the seven legacy strategies collapsed into four
named variants — `none`, `cursor_token`, `next_url`, `counter` — plus
the author-controlled `custom` primitive; every advance writes
directly to `state.<name>`, no `cursor` map remains; each variant's
`terminate_when:` is evaluated before any advance writes fire; default
predicates match `docs/runtime.md` §3).
See [`IMPL-10-client-pagination.md`](IMPL-10-client-pagination.md).
`schema/` plus `client/{state,value,predicate,extract,bodypath,pagination}.go`
build green; Slice 10 introduced **zero** new errors in the file it
owns. The two new errors in `runner.go` (`pagination.seed undefined`,
`too many arguments in call to pagination.advance`) are deliberate —
Slice 12 rewires the call site against the new interface.

The next slice is **Slice 11 — `client/progress.go`** (collapse the
five legacy progress variants — plus the embedded `async_job` phase
machine — into a single flat-list `applyProgress(s, doc.Progress)`
pass that fires once per accepted page-response).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 11"](PHASE-2-PLAN.md#slice-11--clientprogressgo).
   Your slice's detailed scope.
4. [`docs/schema.md` §progress](../schema.md). The new flat
   `[]ProgressWrite` shape and the per-entry `{to, from, coerce?, regex?}`
   contract.
5. [`docs/runtime.md` §5 (progress evaluation timing)](../runtime.md).
   "Once per accepted page-response, including empty pages." The
   staging contract — every `from:` resolves against the pre-write
   snapshot of `state.*`, then every `to:` writes.
6. [`IMPL-08-client-state.md`](IMPL-08-client-state.md). The
   `(*scope).resetPerDrainScratch` per-drain wipe and the per-iteration
   wipes the runner is already expected to perform.
7. [`IMPL-09-client-value.md`](IMPL-09-client-value.md). `evalValue` is
   already wired for the reducers and arithmetic forms progress uses
   pervasively (`{max: [{ref: state.last_ts}, {max: {ref: events.*.ts}}]}`,
   `{add: [{now: true}, "1h"]}`).
8. [`IMPL-10-client-pagination.md`](IMPL-10-client-pagination.md).
   Especially the §"Notes for downstream slices" / Slice 11 sub-section —
   `customPagination.advance` already implements the snapshot-then-write
   pattern; mirror it.

---

## Your slice

**Branch.** Develop on `claude/slice-11-client-progress-<token>`.

**Files you may touch.**

- `client/progress.go`

**Files you must NOT touch.**

- `client/state.go`, `client/value.go`, `client/predicate.go`,
  `client/extract.go`, `client/bodypath.go`, `client/pagination.go`
  (frozen at Slices 8-10's close).
- Any other file under `client/` (Slices 12-14 own those).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).

**Deliverables.**

1. **Delete the `progressPlan` interface and every variant struct.**
   `statelessProgress`, `latestTimestampProgress`,
   `maxEventFieldProgress`, `useNowProgress`, `timeWindowProgress`,
   `asyncJobProgress` are all gone. So are the `seed` / `advance` /
   `shouldSkipForPhase` / `currentPhase` / `phaseTransition` method
   sets. The async-job phase machine is gone — `requests[].terminate_when:`
   is the replacement, owned by Slice 12.

2. **Provide one entry point.** `func applyProgress(s *scope, writes
   schema.Progress) error`. It is called once per accepted
   page-response (the runner places the call site in Slice 12). The
   function:
   - Stages every `writes[i].from:` evaluation first, against the
     same pre-write snapshot of `state.*`. Reads cross-write
     references as the OLD value.
   - Applies `regex:` (capture group 0, no default, only when the
     resolved value is non-nil) and `coerce:` (`applyFormat`) per
     entry.
   - Writes every staged result to its `writes[i].to:` `state.<name>`
     destination.
   - Returns the first error encountered with a clear prefix
     (`progress[<i>].from:`, `progress[<i>].regex:`, etc.).
   - On an empty / nil `writes` list, returns `nil` immediately.

3. **Per-page firing semantics.** Progress fires once per accepted
   page-response, INCLUDING empty pages. The runner's call site
   (Slice 12) gates on the "accepted" qualifier — your function does
   not need to inspect status codes or events.

4. **No per-drain seed.** The `seed` half of the old `progressPlan`
   is gone. State fields with declared defaults are seeded by the
   per-drain wipe in `(*scope).resetPerDrainScratch` (operator-config
   + per-drain scratch). Persistent fields are read from the
   snapshot in `newScope` (Slice 8 wiring). Authors who want a
   first-run seed write their bootstrap as a `state.<name>: {default:
   ...}` declaration.

5. **No cumulative merging.** Each `progress[i]` entry writes its
   `from:` evaluation as-is. Cumulative high-water marks are
   author-written: `from: {max: [{ref: state.last_ts}, {max: {ref:
   events.*.ts}}]}`. The runtime does NOT inspect what the field's
   prior value was and does NOT merge.

6. **Reuse, don't duplicate.** `evalValue` resolves every `from:`.
   `(*scope).applyRegex` handles `regex:` (Slice 9). `applyFormat`
   handles `coerce:`. The snapshot-then-write pattern in
   `customPagination.advance` (Slice 10) is the structural sibling —
   the staging loop here should read the same way.

7. **Hygiene pass** per global rule #5. Top-of-file comments,
   doc-strings on every helper, error-message strings: no
   `cursor.<name>` as a namespace root, no `body.<path>` as a
   top-level root, no slice numbers, no design-doc references, no
   "for backwards compatibility", no references to the five legacy
   progress variants or the async-job phase machine. Error messages
   should read as if the post-redesign shape had always existed.

8. New artefact `docs/planning/IMPL-11-client-progress.md` carrying:
   - Scope (one sentence).
   - Old → new map (per legacy variant → which post-redesign feature
     covers it).
   - Removed-content list (every deleted struct + method + field +
     helper).
   - Build-state enumeration.
   - Notes for downstream slices (especially Slice 12 — the runner
     places the per-page call site and gates on "accepted").

9. Slice-table row 11 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-11-client-progress.md` link.

10. `HANDOFF.md` rewritten to point at Slice 12 (`client/runner.go`).

**Smoke tests.** Throwaway and optional. The trickier areas are:

- Two writes that depend on each other (write[0] reads `{ref:
  state.x}`; write[1] writes `state.x`) — verify the cross-write
  reference reads the OLD value.
- A cumulative high-water mark via
  `from: {max: [{ref: state.last_ts}, {max: {ref:
  events.*.timestamp}}]}` — verify the inner `max` reduces the
  projection and the outer `max` picks the greater of state vs.
  page max. The reducers themselves are already covered by Slice 9.
- An empty-page firing — verify `applyProgress` evaluates `{now:
  true}` and writes it to `state.last_run_at` even when
  `scope.events` is `[]any{}`.
- A `coerce: int` write where the from resolves to a numeric string —
  verify `applyFormat("int", "42")` returns `int64(42)`.
- A `regex:` write where the regex doesn't match — verify the staged
  value becomes `nil` (no default) and `state.<name> = nil` after the
  write.

The client package will still NOT compile end-to-end at slice close
(Slices 12-14 own the files that still reference removed shapes), so a
real smoke `_test.go` won't build inside `package client`. A
walkthrough verification is acceptable; document it in
`IMPL-11-client-progress.md`.

**Out of scope.** No schema changes (Slice 7 closed). No state /
scope / value-runtime / pagination changes (Slices 8-10 closed). No
runner / on_status / error-mode changes (Slice 12). No cache / auth /
http changes (Slice 13). No sink / trace changes (Slice 14).

---

## Watch out for

- **The package will not compile end-to-end.** `runner.go`,
  `oauth2.go`, `requestcache.go`, `http.go`, `auth.go` still
  reference removed schema types. Your slice owns one file only; the
  rest stays red until the slices that own them land.

- **No `seed` step.** The five-variant plan had a per-drain seed for
  `latestTimestampProgress`, `useNowProgress`, `timeWindowProgress`,
  and a phase-machine boot for `asyncJobProgress`. The new flat-list
  plan does not. Per-drain bootstrapping lives in
  `(*scope).resetPerDrainScratch` and field `default:` declarations.

- **No phase machine.** The async-job phases (`submit` / `poll` /
  `fetch`) and their gating (`shouldSkipForPhase`,
  `phaseTransition`) are gone. The new shape is a three-request
  chain in `requests:` where the poll step carries
  `terminate_when:`. That whole mechanism is Slice 12's responsibility;
  this slice just deletes the residue.

- **Snapshot-then-write semantics are mandatory.** A naive loop that
  reads, writes, then reads the next entry would let entry 1 see
  entry 0's write. Stage every `from:` into a slice first; only then
  apply every `to:`.

- **Progress writes go to `state.<name>` directly.** There is no
  `cursor` namespace. A `progress[i].to` Path is `state.<name>` and
  the validator (Slice 7) guarantees the field is declared under
  `state:` (and that the field's lifetime is persistent — not
  per-drain-scratch and not operator-config).

- **`events.*` projections route through `scope`.** Cumulative
  high-water marks read `{ref: events.*.timestamp}`; the projection
  resolves via `resolveNamespaceRef`'s `events.*` arm. Don't
  open-code `len(scope.events.([]any))` or reach into the events
  list directly.

- **Tests stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `client/progress_test.go`. Expect `go test ./client/...` to stay
  red.

---

## When you finish

`git add` the rewritten file, the new `IMPL-11-client-progress.md`,
the updated `RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit
with a message like:

```
feat(client): slice 11 — progress collapsed to a flat write list

Five legacy progress variants and the async-job phase machine
collapse into a single applyProgress(s, doc.Progress) pass that
fires once per accepted page-response (Slice 12 owns the call site).
Each entry's from: resolves against the pre-write snapshot of
state.*; regex + coerce run per entry; every to: writes
state.<name> after every from: has been staged. No seed, no phases,
no cumulative merging — cumulative high-water marks are written
explicitly with {max: [{ref: state.last_ts}, {max: {ref:
events.*.ts}}]}.

schema/ + client/{state,value,predicate,extract,bodypath,pagination,
progress}.go build at slice close (modulo the rest of client/ which
other slices still own); tests stay red until Slice 17.
```

Push to `claude/slice-11-client-progress-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
