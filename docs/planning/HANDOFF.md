# Handoff — Phase 2 Slice 7 (`schema/validate.go`)

Slice 6 closed (closed-set namespace-root check at parse time; `events.*`
shortcut vocabulary documented; removed-root migration hints for
`cursor`/`body`/`item`; `equal:` → `value:` migration codec deleted from
predicate.go; package docstrings cleaned of stale references). See
[`IMPL-06-schema-path-predicate.md`](IMPL-06-schema-path-predicate.md).
The schema package is still **build-red** — the entire remaining error
set lives in `schema/validate.go`, which is yours to rewrite.

The next slice is **Slice 7 — `schema/validate.go`**.

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them. Rule #1
   (no backwards compat), Rule #2 (`DESIGN_SUGGESTIONS.md` is the
   source of truth), Rule #4 (no design-doc / slice-number / stale-
   concept references in comments), and Rule #5 (clean up stale
   comments in any file you touch) are particularly load-bearing here.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture. §3.3 is
   the load-bearing rule on throwaway smoke tests; §4 is the
   verification posture table.
3. [`PHASE-2-PLAN.md` §"Slice 7"](PHASE-2-PLAN.md#slice-7--schemavalidatego).
   Your slice's detailed scope — eleven structural-change bullets.
4. [`docs/schema.md`](../schema.md) end-to-end. The validator IS the
   prose semantics of that file; every constraint in §state, §auth,
   §requests, §response, §pagination, §progress, §error, §values, and
   §predicates becomes a check in `validate.go`. §3.6 (pagination
   variants) and §state (lifetime inference) are the two heaviest
   sections — read those twice.
5. [`docs/runtime.md` §error.mode + §6 (cache-invalidate-on-error)](../runtime.md).
   The validator enforces the `on_status:` verb closure and the
   "invalidate_cache without a reachable cache block degrades to
   empty_events" rule (per Slice 4's `error.mode`).
6. [`IMPL-04-schema-structs.md`](IMPL-04-schema-structs.md),
   [`IMPL-05-schema-value.md`](IMPL-05-schema-value.md), and
   [`IMPL-06-schema-path-predicate.md`](IMPL-06-schema-path-predicate.md).
   The three predecessor artefacts. IMPL-04 has the new struct shape;
   IMPL-05 has the post-desugar Value surface area; IMPL-06 has the
   closed-root set (`pathClosedRoots`) plus the live predicate
   call-site list.

The current `schema/validate.go` is 1487 lines of pre-redesign checks
woven throughout — `cursor.*` namespace, `mutability:` field,
`state.fields`, the seven pagination variants, the five progress
variants, the `async_job` phase machine, `Defaults`, `path:`,
`placeholder_event:`. Almost none of it survives. Plan to rewrite the
file end-to-end rather than incrementally patch.

---

## Your slice

**Branch.** Develop on `claude/slice-07-schema-validator-<token>`.

**Files you may touch.**

- `schema/validate.go`

**Files you must NOT touch.**

- `schema/schema.go`, `schema/read.go`, `schema/doc.go`,
  `schema/value.go`, `schema/path.go`, `schema/predicate.go` —
  Slices 4-6 own these. The post-redesign struct, Value-language,
  and path/predicate shapes are locked.
- `schema/fixtures_test.go`, `schema/example_test.go` — Slice 17.
- Any file outside `schema/`.

**Deliverables.**

1. `schema/validate.go` rewritten against the eleven bullets in
   [`PHASE-2-PLAN.md` §Slice 7](PHASE-2-PLAN.md#slice-7--schemavalidatego).
   The bullets are reproduced here for convenience; consult the plan
   for the source-of-truth list.
   1. Replace `cursorSchema(d *Doc)` with the lifetime-inference rule
      (`docs/schema.md` §state): operator-config (has `default:`,
      never written), per-drain scratch (target of any
      `pagination.*.to` write), persistent (target of any
      `progress[].to` write or `requests[].extract` with
      `to: state.*`).
   2. Reject conflicts: same field written by both `pagination.*.to`
      and `progress[].to`, or by a `to:` write AND `default:`-only
      classification.
   3. Validate the new five-variant Pagination union
      (`none|cursor_token|next_url|counter|custom`). Per-variant
      required fields per `docs/schema.md` §3.6.
   4. Validate `Progress` as a flat list of writes; each entry's
      `to:` must be `state.<name>` AND that field must be declared.
   5. Validate `requests[].terminate_when:` as a Predicate; reject
      when a request is missing `id:` but referenced from
      `steps.<id>.body.<path>` or `fan_out.over` ref.
   6. Validate the closed `on_status:` verb set
      (`skip|fail|empty_events|invalidate_cache`). Reject
      `invalidate_cache` when no reachable cache block exists
      (warn-degrade per `docs/runtime.md` §6).
   7. Validate the unified `Cache` block (auth + request). Reject
      `requests[].fan_out` + `requests[].cache` co-occurrence.
   8. Validate reducer arguments (`Max`/`Min`/`First`/`Last`/`Count`):
      either a `{list: [...]}` literal or a list-shaped Value
      (typically `{ref: events.*.<field>}`). Slice 5 already
      normalised the list-literal sugar; you read a single `*Value`
      operand per reducer.
   9. Validate string-interpolation refs: every `Concat` arm that
      reaches the validator may be the post-desugar form of a
      `${...}` string — the validator type-checks the inner Refs
      and Defaults as usual.
   10. Validate the fan-out reserved-root set: `fan_out.as` must not
       shadow `state | cache | events | extract | steps | response`.
       Use `pathClosedRoots` directly (same package).
   11. Reject `Defaults`, `state.fields`, `mutability:`, `cursor.*`
       (already caught at parse — defensive only), `placeholder_event:`,
       `flow:`, `async_job:`, `complete_when:`, `target:` on extract,
       `path:` on request, `{now: true, offset: ...}` (already caught
       at parse — defensive only).

2. **Hygiene pass** on `validate.go` per global rule #5. Top-of-file
   comments, doc-strings on every helper, and error-message strings:
   no `cursor.*`, no `body.<path>`, no `async_job.*`, no
   `pagination.scroll_id.*`, no `pagination.link_header.*`, no
   `state.fields`, no slice numbers. Error messages should read as if
   the post-redesign IR had always existed.

3. **`events.*` projection binding.** Slice 6 documented the shortcuts
   (`events.first` / `events.last` / `events.<int>` / `events.count` /
   `events.*`). The validator binds the semantics:
   - `events.count` resolves to an int (cardinality of the active page).
   - `events.<int>` requires a non-negative integer literal segment.
   - `events.*.<field>` is a projection; the `*` segment is illegal
     outside the events root.
   - Reducer operands typed `{ref: events.*.<field>}` type-check as
     list-shaped.

4. New artefact `docs/planning/IMPL-07-schema-validator.md` carrying:
   - Scope (one sentence).
   - Old → new check map: every check in the pre-redesign validator,
     classified as kept / rewritten / deleted, with a one-line note
     each.
   - Removed-content list (helper functions, structures, constants,
     error messages that are gone).
   - Notes for downstream slices: Slice 8 (client/state.go's lifetime
     classification mirrors yours), Slice 12 (client/runner.go reads
     the resolved `error.mode` verbs), Slice 16 (templates rely on
     the validator accepting the new shape).

5. Slice-table row 7 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-07-schema-validator.md` link.

6. `HANDOFF.md` rewritten to point at Slice 8 (`client/state.go`).

**Smoke tests.** Throwaway and optional, but recommended for the
trickier checks — lifetime-inference conflict diagnostics, reducer arg
shape rejection, `on_status` closure, `invalidate_cache`-without-cache
degradation. Mark them `// SMOKE - DELETE BEFORE SLICE CLOSE`. The
existing `schema/fixtures_test.go` and `schema/example_test.go` are
massively stale and OFF-limits per Slice 17.

**Out of scope.** No struct-shape changes (Slice 4). No Value-language
additions (Slice 5). No path / predicate changes (Slice 6 closed). No
client / runtime / template touches. No regeneration of
`schema-reference.md` (Slice 18).

---

## Watch out for

- **`pathClosedRoots` is internal to `schema/path.go`.** It is a
  lowercase package-level var, accessible from `validate.go` because
  same package. The fan-out reserved-root check should reuse it
  directly rather than copying the list.

- **`pathRemovedRoots` rejection already fires at parse time.** The
  three removed-reserved roots (`cursor`, `body`, `item`) never reach
  the validator inside a `*Path`. Don't add validator-side checks for
  them; add at most a single defensive top-level reject for an
  author who somehow constructs a `Path` programmatically.

- **`PredicateEq` no longer has custom UnmarshalYAML/JSON.** The
  `equal:` migration hint is gone. Author errors like an `equal:` key
  in an `eq:` body surface as yaml.v3's silent-unknown-key behaviour;
  if you want strict rejection of unknown sibling keys inside
  `{eq: {...}}`, add it at the validator layer, not back into
  predicate.go.

- **Lifetime inference is the hardest check.** Reading the new
  `docs/schema.md` §state twice before writing code will save you a
  rewrite. The classification table is: `default:`-only →
  operator-config; `pagination.*.to` target → per-drain scratch;
  `progress[].to` target or `requests[].extract.to: state.*` target
  → persistent. A field that's *both* a `default:` AND a write target
  takes its lifetime from the write target. A field with conflicting
  write targets (pagination AND progress) is an error.

- **`invalidate_cache` without a reachable cache block.** Per
  `docs/runtime.md` §6, the runner degrades to `empty_events` at
  request time when this happens. The validator emits a warning (not
  an error) for this case. The diagnostic structure already supports
  warning-level entries; reuse it.

- **The build is RED at the start of this slice. It must be GREEN at
  the end.** Slice 7 is the last schema-package slice. After your
  rewrite, `go build ./schema/...` passes. Slices 8-14 will start
  consuming `*schema.Doc` and will assume the validator is the gate
  on shape.

- **Test files stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `schema/fixtures_test.go` or `schema/example_test.go`. The existing
  tests' assertions are massively wrong against the new validator;
  expect a sea of red on `go test ./schema/...` and ignore it.

---

## When you finish

`git add` the rewritten `schema/validate.go`, the new
`IMPL-07-schema-validator.md`, the updated `RESEARCH_PLAN.md`, and the
updated `HANDOFF.md`. Commit with a message like:

```
feat(schema): slice 7 — full validator rewrite against the new IR

Replaces the pre-redesign validator end-to-end. Adds lifetime inference
(operator-config / per-drain scratch / persistent) off write sites,
five-variant pagination check, flat progress-write check,
terminate_when predicate gates, the closed on_status verb set with the
invalidate_cache degrade rule, the unified Cache block check, the
fan-out reserved-root check off pathClosedRoots, and the reducer
operand shape check. Removes every old-shape check (cursor namespace,
mutability, state.fields, the 7 pagination / 5 progress variants, the
async_job phase machine, Defaults, placeholder_event, …).

schema/ builds green at slice close; tests stay red (Slice 17 rewrite).
```

Push to `claude/slice-07-schema-validator-<token>` and open a PR.

If you discover the slice is wider than the plan (e.g. a check needs
data from the new `client/state.go` snapshot that doesn't exist yet),
**stop and flag it via `AskUserQuestion`** rather than widening scope
silently. The validator is intentionally the last schema-package slice
so that all the surrounding shape is fixed; if you find you can't
finish without crossing into client/, the plan needs revisiting.
