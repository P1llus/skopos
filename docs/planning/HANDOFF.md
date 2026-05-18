# Handoff — Phase 2 Slice 9 (`client/value.go` + `predicate.go` + `extract.go` + `bodypath.go`)

Slice 8 closed (`client/state.go` rewritten against the new IR:
`Snapshot` carries only `State`, `scope` drops `cursor` and gains
`events` + `cache`, `newScope` evaluates declared defaults at seed
time via `evalValue`, `snapshot()` filters per-drain scratch off the
inferred lifetime, `resolveNamespaceRef` mirrors the closed root set
plus the `events.*` projection vocabulary, the per-drain wipe is
exposed as `(*scope).resetPerDrainScratch` for the runner). See
[`IMPL-08-client-state.md`](IMPL-08-client-state.md). The schema
package still builds **green**; the client package stays red until the
remaining slices land.

The next slice is **Slice 9 — `client/value.go` + `predicate.go` +
`extract.go` + `bodypath.go`** (the value-resolution runtime).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 9"](PHASE-2-PLAN.md#slice-9--clientvaluego--predicatego--extractgo--bodypathgo).
   Your slice's detailed scope.
4. [`docs/schema.md` §Value language](../schema.md). The Value form
   catalogue, including the new `Add`, `Subtract`, `Max`, `Min`,
   `First`, `Last`, `Count`, `Regex` operators and the desugared
   string-interpolation shape.
5. [`docs/runtime.md` §5 (progress evaluation timing)](../runtime.md)
   plus §6 (error semantics) and §7 (secret redaction). Slice 9's
   value runtime is what those sections call into.
6. [`IMPL-05-schema-value.md`](IMPL-05-schema-value.md). The parser /
   struct shape your runtime evaluates.
7. [`IMPL-08-client-state.md`](IMPL-08-client-state.md). Especially
   the §"Notes for downstream slices" / Slice 9 sub-section — it
   describes the `events.*` projection arm your reducers consume and
   the new extract destination contract.

---

## Your slice

**Branch.** Develop on `claude/slice-09-client-value-<token>`.

**Files you may touch.**

- `client/value.go`
- `client/predicate.go`
- `client/extract.go`
- `client/bodypath.go`

**Files you must NOT touch.**

- `client/state.go` (frozen at Slice 8's close).
- Any other file under `client/` (Slices 10–14 own those).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).

**Deliverables.**

1. **`client/value.go` — evalValue extensions.** Per
   [`PHASE-2-PLAN.md` §Slice 9](PHASE-2-PLAN.md#slice-9--clientvaluego--predicatego--extractgo--bodypathgo):
   - Add branches for `v.Add`, `v.Subtract`, `v.Max`, `v.Min`,
     `v.First`, `v.Last`, `v.Count`, `v.Regex`.
   - Add / Subtract operate on duration or numeric pairs per
     `docs/schema.md`.
   - The five reducers consume either a `{list: [...]}` literal or a
     list-shaped Value — typically `{ref: events.*.<field>}`, which
     resolves through `(*scope).resolveEvents` from Slice 8 and returns
     `[]any`.
   - `Count` returns `int64`; the others return the input's element
     type (skip absent / nil entries for the comparator reducers).
   - `Regex` runs the compiled pattern over the resolved `From`
     string, captures group `Capture` (default 0 = full match), and
     falls back to `Default` when there's no match.
   - Delete the `v.Now.Offset` branch (the `{now: true, offset: ...}`
     sibling form was removed in Slice 5; the offset desugars into
     `{add: [{now: true}, "<dur>"]}` now).
   - Update the file-level doc-string: drop the cursor-namespace
     paragraph; describe the new runtime representation set.

2. **`client/predicate.go` — closed verb set.** Confirm absent-tolerance
   on every predicate form. No structural change is required; rewrite
   any stale comment that mentions `cursor.*` or the legacy body root.

3. **`client/extract.go` — new destination contract.** The
   `ExtractVar.Target` field was deleted in Slice 4; the destination
   namespace is encoded in `to:` directly. Rewrite `runExtracts`:
   - The destination path's root is either `state` (writes to
     `scope.state`) or `extract` (writes to `scope.extract`).
   - There is no `cursor` arm.
   - Coerce / Regex still apply pre-write.
   - The `From` Path is unchanged; the same four arms apply.

4. **`client/bodypath.go` — hygiene + alignment.** Remove every
   reference to the deleted `body.<path>` root. Keep `stripBodyRoot`
   accepting `response.body.<path>` and `steps.<id>.body.<path>`;
   reject anything else with a clear diagnostic. Drop the
   "progress.{latest_event_timestamp, max_event_field}.event_time.path"
   reference from `pathParts`'s doc-string.

5. **Hygiene pass** per global rule #5. Top-of-file comments,
   doc-strings on every helper, error-message strings: no `cursor.*`,
   no `body.<path>` as a top-level root, no `target:` on extract,
   no `{now: true, offset: ...}` reference, no slice numbers, no
   design-doc references. Error messages should read as if the
   post-redesign shape had always existed.

6. **`isZeroRuntime` review.** With reducer outputs producing
   `[]any` for projections and `nil` entries for absent fields,
   confirm the zero-Value detection still does the right thing for
   `Ref` fallback. Empty list (`[]any{}`) MUST count as zero — that's
   already the current behaviour, but re-read with the new flow.

7. New artefact `docs/planning/IMPL-09-client-value.md` carrying:
   - Scope (one sentence).
   - Old → new map per file.
   - Removed-content list.
   - Notes for downstream slices (Slice 10–14).

8. Slice-table row 9 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-09-client-value.md` link.

9. `HANDOFF.md` rewritten to point at Slice 10 (`client/pagination.go`).

**Smoke tests.** Throwaway and optional. The trickier areas are:
- the five reducers against `[]any` returned by `events.*.<field>`
  (especially with nil entries for absent fields);
- `Regex` with named-vs-numbered capture groups;
- `Add`/`Subtract` with duration + timestamp pairs vs. pure-numeric pairs.

The client package will still NOT compile end-to-end at slice close
(Slices 10–14 own the files that still reference removed shapes),
so a real smoke `_test.go` won't build inside `package client`. A
walkthrough verification is acceptable; document it in
`IMPL-09-client-value.md`.

**Out of scope.** No schema changes (Slice 7 closed). No state /
scope changes (Slice 8 closed). No pagination / progress / runner
changes (Slices 10–12). No cache / auth / http changes (Slice 13).
No sink / trace changes (Slice 14).

---

## Watch out for

- **The package will not compile end-to-end.** `pagination.go`,
  `progress.go`, `runner.go`, `oauth2.go`, `requestcache.go`,
  `http.go`, `auth.go` all still reference removed schema types or
  the deleted `scope.cursor` slot. Your slice owns four files only;
  the rest stays red until the slices that own them land.

- **`evalValue` is called from `(*scope).seedDefaults` and
  `resetPerDrainScratch`.** Slice 8 wired both call sites. Don't break
  the contract: `evalValue(*fd.Default)` must produce the resolved
  runtime representation (string / int64 / bool / time.Time / []any /
  map[string]any / nil) for every Value form a Default might use.

- **Reducer operand resolution funnels through
  `(*scope).resolveEvents`.** Slice 8's `events.*` arm returns `[]any`
  with positional alignment — absent fields surface as `nil` entries.
  `Max`/`Min`/`First`/`Last` skip nil entries; `Count` counts every
  position (including nil), matching `events.count`'s definition. If
  you want stricter semantics for one of them, write the rationale
  into `IMPL-09-client-value.md`.

- **Absent-tolerance is the predicate policy.** `present` returns
  false for nil values; `eq`/`gt`/`lt`/`gte`/`lte` return false rather
  than erroring; `not` / `and` / `or` short-circuit on absence. The
  current `predicate.go` already implements this — verify rather than
  rewrite.

- **`ExtractVar.Name` is gone in Slice 4.** The destination IS the
  `to:` Path, and the name is `Parts[1]`. The current `extract.go`
  uses `ev.Name`, `ev.Target` — those fields don't exist any more.
  The rewrite is full, not a patch.

- **Tests stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `client/*_test.go`. Expect `go test ./client/...` to stay red.

---

## When you finish

`git add` the rewritten files, the new `IMPL-09-client-value.md`,
the updated `RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit
with a message like:

```
feat(client): slice 9 — value runtime against the new IR

evalValue gains Add/Subtract/Max/Min/First/Last/Count/Regex; the
{now: true, offset: ...} sibling form is gone (offsets desugar to
{add: [{now: true}, "<dur>"]}). Reducer arguments resolve through the
events.*.<field> projection in scope. extract.go writes to state.<name>
or extract.<name> directly off ExtractVar.To; the cursor arm is gone.
bodypath.go is aligned to the response.body / steps.<id>.body
namespace roots only.

schema/ + client/{value,predicate,extract,bodypath}.go build at slice
close (modulo the rest of client/ which other slices still own); tests
stay red until Slice 17.
```

Push to `claude/slice-09-client-value-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
