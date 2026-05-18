# Handoff — Phase 2 Slice 6 (`schema/path.go` + `schema/predicate.go`)

Slice 5 closed (Value language additions, string interpolation, IsSecret
walker for the new forms; one-line `d.State.Fields → d.State` fix
absorbed). See [`IMPL-05-schema-value.md`](IMPL-05-schema-value.md). The
schema package is still **build-red** because `schema/validate.go` is
Slice 7's rewrite. `path.go` and `predicate.go` build cleanly against
the new struct shape today; Slice 6 closes them out without re-RED'ing
anything you don't already see.

The next slice is **Slice 6 — `schema/path.go` + `schema/predicate.go`**.

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them. Rule #1
   (no backwards compat), Rule #4 (no design-doc / slice-number /
   stale-concept references in comments), and Rule #5 (clean up stale
   comments in any file you touch) are particularly load-bearing here.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture. §3.3 is
   the load-bearing rule on throwaway smoke tests.
3. [`PHASE-2-PLAN.md` §"Slice 6"](PHASE-2-PLAN.md#slice-6--schemapathgo--schemapredicatego).
   Your slice's detailed scope.
4. [`docs/schema.md` §Paths](../schema.md#paths) and
   [`docs/schema.md` §Namespaces](../schema.md#namespaces) and
   [`docs/schema.md` §Predicates](../schema.md#predicates) — your source
   of truth for the new namespace-root vocabulary, the `events.*`
   shortcut set, and the closed predicate verbs.
5. [`IMPL-05-schema-value.md` §"Notes for downstream slices §Slice 6"](IMPL-05-schema-value.md).
   Your immediate predecessor's artefact — read the Slice 6 notes
   in particular; the interpolation parser already calls `ParsePath`
   and assumes `*` is a legal segment.
6. [`IMPL-04-schema-structs.md`](IMPL-04-schema-structs.md). The struct
   shape underlying every path / predicate reference.

The current `schema/path.go` (~450 lines) and `schema/predicate.go`
(~500 lines) both build today against the new struct shape. The slice
is small in terms of new code; most of the work is closing the
namespace-root set, recognising the new `events.*` shortcuts, and a
hygiene pass over the docstrings (they reference `cursor.*`,
`async_job.poll.complete_when`, `pagination.scroll_id.complete_when`,
and `item.<path>` — all gone).

---

## Your slice

**Branch.** Develop on `claude/slice-06-schema-path-predicate-<token>`.

**Files you may touch.**

- `schema/path.go`
- `schema/predicate.go`

**Files you must NOT touch.**

- `schema/schema.go`, `schema/read.go`, `schema/doc.go`, `schema/value.go` —
  Slices 4 and 5 own these. The post-redesign struct + Value-language
  shape is locked.
- `schema/validate.go` — Slice 7 owns the full validator rewrite. Don't
  pull validator logic into path.go or predicate.go.
- `schema/fixtures_test.go`, `schema/example_test.go` — Slice 17.
- Any file outside `schema/`.

**Deliverables.**

1. `schema/path.go` rewritten against `docs/schema.md` §Paths +
   §Namespaces:
   - **Closed root set** (parse-time check, not validator):
     `state`, `cache`, `events`, `extract`, `steps`, `response`. Plus
     any `fan_out.as` name — but that name is author-chosen and
     contextual, so the parse-time check should NOT hard-code the
     fan-out alias; Slice 7's validator handles the fan-out reserved-root
     check. `path.go` rejects only roots that are NEVER legal anywhere
     (i.e. anything outside the six fixed roots AND outside the bare
     identifier shape that fan-out aliases use).
   - Drop `cursor`, `body`, `item` from the legal-root vocabulary
     entirely. Stale docstring mentions of `cursor.<name>` / `body.<path>`
     / `item.<path>` go away per global rule #5.
   - **`events.*` shortcut set**: `events.first`, `events.last`,
     `events.<int>` (where `<int>` is a non-negative integer literal),
     `events.count`, `events.*` (projection wildcard). The parser
     recognises these at path-parse time; the validator (Slice 7) is
     where they bind to a concrete events list.
   - **`steps.<id>.body[.<path>]` and `steps.<id>.header.<name>`** stay
     as-is.
   - **`response.body[.<path>]` and `response.header.<name>`** stay
     as-is.
   - The segment-escape form (`{parts: [...]}`) is unchanged.

2. `schema/predicate.go`:
   - The verb set stays exactly as today: `eq | gt | lt | gte | lte |
     present | not | and | or | literal_bool`. No additions.
   - Drop the docstring list of call-sites that references `async_job.poll.complete_when`
     and `pagination.scroll_id.complete_when` — both deleted in Slice 4.
     Replace with the live call-site list (per `docs/schema.md`
     §Predicates): `requests[].if`, `requests[].terminate_when`,
     `auth.multi_mode.branches[].when`, `Value.select.branches[].when`.
   - Drop the `cursor.*` examples from the doc comments; replace with
     `state.*` or `events.*` examples drawn from the post-redesign
     templates (`docs/schema.md` has good ones).
   - The `equal:` → `value:` migration-hint codec (legacy renamed key)
     is **stale** per global rule #1 (no migration code). Remove the
     `predicateEqRaw` shadow and the `equal:` rejection — the field is
     called `value:` everywhere now. Authors of stale templates get
     the standard "unknown key" diagnostic.
   - Absent-tolerance is the policy: `{present: state.x}` returns
     false when `state.x` is unset; `{eq: ...}`, `{gt: ...}` and
     friends return false when either side is absent. The codec
     doesn't enforce this (runtime + validator do); but the package-
     level docstring should state it once.

3. **Hygiene pass** on both files per global rule #5. Cleanup targets:
   - `path.go`'s top docstring lists `cursor`, `item`, `response` as
     legal roots — update to the post-redesign six-root set.
   - `path.go`'s "primary form" examples reference `cursor.last_timestamp` —
     rewrite to a post-redesign example (`events.last.timestamp`
     or `state.last_timestamp`).
   - `predicate.go`'s package docstring lists deleted call sites
     (`async_job.poll.complete_when`, `pagination.scroll_id.complete_when`)
     — replace with the live list above.
   - `predicate.go`'s example block (`{eq: {path: cursor.phase, ...}}`,
     etc.) — rewrite around `state.*` references.

4. New artefact `docs/planning/IMPL-06-schema-path-predicate.md` carrying:
   - Scope (the files you owned, one sentence).
   - Old → new root-set map for path.go.
   - Old → new call-site map for predicate.go (and the deleted
     `equal:` → `value:` migration codec).
   - Removed-content list (`cursor` root, `body` root, `item` root,
     `predicateEqRaw`, `predicateEqEqualRenamedHint`).
   - Notes for downstream slices: Slice 7 (validator reads the closed
     root set off path.go), Slice 8 (`client/state.go`'s
     `resolveNamespaceRef` mirrors the same closed set), Slice 9
     (`client/value.go` consumes the `events.*` shortcuts via the new
     scope's events resolver).

5. Slice-table row 6 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-06-schema-path-predicate.md` link.

6. `HANDOFF.md` rewritten to point at Slice 7 (`schema/validate.go`).

**Smoke tests.** Optional and throwaway. The path/predicate parsers are
mechanical; a handful of round-trip asserts (parse + marshal + reparse)
is enough. Mark them `// SMOKE - DELETE BEFORE SLICE CLOSE`. If you do
add them, validate.go's red state means you'll need the same
"temporarily move validate.go + the stale test files aside, run, move
back" trick Slice 5 used. The smoke file goes away at slice close.

**Out of scope.** No struct-shape changes (Slice 4 / Slice 5 own those).
No validator changes (Slice 7). No new Value forms (Slice 5 closed
that set). No client / runtime changes.

---

## Watch out for

- **The closed-root check is parse-time, not validator-time.** Authors
  who type `cursor.foo` see an error from `path.go`, not from a deep
  validator walk. Keep the error message clean: "namespace root
  'cursor' is not recognised (legal roots: state, cache, events,
  extract, steps, response, or a fan-out alias)". The "or a fan-out
  alias" clause is what lets parsing succeed for the contextual
  `fan_out.as` name; the validator decides whether the alias is in
  scope at the use site.

- **`events.*` is a path-parse concern, not a Value concern.** A path
  like `events.first.timestamp` parses as `{parts: ["events", "first",
  "timestamp"]}`. The `first` / `last` / `count` segments are not
  reserved at the path layer beyond being valid segments. The
  validator (Slice 7) is where the events-projection semantics are
  enforced. Don't bake the shortcut list into path.go's grammar.

- **`*` is a legal path segment.** `events.*.timestamp` parses as
  three parts `["events", "*", "timestamp"]`. Don't reject `*` at
  parse time — the existing `ParsePath` already accepts it (just rejects
  empty segments). Verify the existing behaviour still holds.

- **The `equal:` → `value:` migration codec is dead code.** Delete the
  `predicateEqRaw` shadow type and the custom `UnmarshalYAML` /
  `UnmarshalJSON` on `PredicateEq` outright. After the delete,
  `PredicateEq` uses default yaml.v3 / encoding/json decoding (its
  field tags already say `yaml:"value"` / `json:"value"`).

- **The build is RED at the start of this slice and stays RED at the
  end** — same posture as Slice 5. Specifically, `schema/validate.go`'s
  errors are unchanged. Don't widen scope into validate.go.

---

## When you finish

`git add` the modified `schema/path.go`, `schema/predicate.go`, the new
`IMPL-06-schema-path-predicate.md`, the updated `RESEARCH_PLAN.md`, and
the updated `HANDOFF.md`. Commit with a message like:

```
feat(schema): slice 6 — namespace roots, path & predicate rules

Closes path.go's namespace-root vocabulary on {state, cache, events,
extract, steps, response} plus author-chosen fan-out aliases. Drops
cursor / body / item from the legal-root set. Recognises events.first /
events.last / events.<int> / events.count shortcuts at path-parse time.
Removes the dead equal:→value: migration codec from predicate.go. Live
predicate call-site list rewritten around the post-redesign IR.

schema/validate.go is still red — Slice 7 owns the validator rewrite.
```

Push to `claude/slice-06-schema-path-predicate-<token>` and open a PR.

If you discover the slice is wider than the plan (e.g. the validator-side
closed-root table turns out to live in path.go and needs to migrate),
**stop and flag it via `AskUserQuestion`** rather than widening scope
silently. The validator rewrite is Slice 7's. A small cross-file fix
is fine; rewriting another slice's primary file is not.
