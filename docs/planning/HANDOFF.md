# Handoff — Phase 2 Slice 5 (`schema/value.go` Value language)

Slice 4 closed (struct definitions rewritten — see
[`IMPL-04-schema-structs.md`](IMPL-04-schema-structs.md)). The schema
package is **build-red** at the start of Slice 5: `schema/value.go` has
one stale `d.State.Fields` reference, and `schema/validate.go` is the
larger fold that Slice 7 owns. Both colour-changes are expected per
[`PHASE-2-PLAN.md` §4](PHASE-2-PLAN.md#4-verification-posture).

The next slice is **Slice 5 — `schema/value.go` Value language**.

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them. Rule #1
   (no backwards compat) and Rule #4 (no design-doc references in
   comments) are particularly load-bearing in `value.go`.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2
   slice list, the cross-cutting rules specific to Phase 2, and the
   verification posture. §3.3 is the load-bearing rule on throwaway
   smoke tests.
3. [`PHASE-2-PLAN.md` §"Slice 5"](PHASE-2-PLAN.md#slice-5--schemavaluego).
   Your slice's detailed scope.
4. [`docs/schema.md` §Values](../schema.md#values). The Phase 1 prose
   doc — your single source of truth for the new Value forms,
   especially:
   - [§Values §Discriminated forms](../schema.md#discriminated-forms)
     for the closed set of map keys (the table that now includes `add`,
     `subtract`, `max`, `min`, `first`, `last`, `count`, `regex`).
   - [§Values §String interpolation](../schema.md#string-interpolation)
     for the `${...}` segment scanner, the `|<default>` sigil, and the
     `\$` / `\{` escape rules.
   - [§Values §Reducers](../schema.md#reducers) for the
     list-literal-or-list-shaped-Value input contract.
   - [§Values §Arithmetic](../schema.md#arithmetic) for the type-pair
     table on `add` / `subtract`.
   - [§Values §Format verbs](../schema.md#format-verbs) for the closed
     verb set plus the Go layout-string fallback rule.
   - [§Values §Regex](../schema.md#regex) for the `{regex: {...}}`
     Value-form shape.
5. [`IMPL-04-schema-structs.md`](IMPL-04-schema-structs.md). Your
   immediate predecessor's artefact — read the "Notes for downstream
   slices §Slice 5" section in particular.
6. [`docs/DESIGN_SUGGESTIONS.md` §4.1](../DESIGN_SUGGESTIONS.md). The
   rationale behind the Value-language redesign. Consult only when the
   prose in `schema.md` is ambiguous.

The current `schema/value.go` (864 lines) is the pre-redesign shape; you
will rewrite the relevant parts (Value union, codec, IsSecret).
`schema/schema.go` is the post-redesign shape — Slice 4's output is what
your Value fields plug into.

---

## Your slice

**Branch.** Develop on `claude/slice-05-schema-value-<token>`.

**Files you may touch.**

- `schema/value.go`

**Files you must NOT touch.**

- `schema/schema.go`, `schema/read.go`, `schema/doc.go` — Slice 4 owns
  these. The post-redesign struct shape is locked.
- `schema/path.go`, `schema/predicate.go`, `schema/validate.go` —
  Slices 6 and 7 own them.
- `schema/fixtures_test.go`, `schema/example_test.go` — Slice 17
  rewrites tests.
- Any file outside `schema/`.

**Deliverables.**

1. `schema/value.go` rewritten against `docs/schema.md` §Values.
   Specifically:
   - **Add the missing variants** to the `Value` struct: `Add`,
     `Subtract`, `Max`, `Min`, `First`, `Last`, `Count`, `Regex`.
     Each carries the shape called out in `schema.md` (Add/Subtract
     take a two-operand list; reducers take a single Value that
     resolves to a list or projection; Regex takes
     `{pattern, from, capture?, default?}`).
   - **String interpolation parser.** Rewrite the scalar branch of
     `Value.UnmarshalYAML` (and the `'"'` branch of
     `Value.UnmarshalJSON`) to scan for `${<path>[|<default>]}`
     segments and desugar to `{concat: [<literal>, {ref}, ...]}`. A
     scalar with no `$` continues to decode as `LiteralString`. Escape
     rules per `schema.md` §String interpolation.
   - **Reject `{now: true, offset: ...}`.** Plain `{now: true}` stays
     valid. The sibling `offset:` key is removed; the `NowValue` struct
     loses its `Offset *Value` field. Authors who want an offset now
     write `{add: [{now: true}, "1h"]}` or
     `{subtract: [{now: true}, "30m"]}`.
   - **Fix the `IsSecret` regression.** The Slice 4 struct change
     (`Doc.State map[string]FieldDecl`, no wrapper) leaves
     `isSecretStatePath` at `schema/value.go:828` calling
     `d.State.Fields[...]`. Change to `d.State[...]`. This is the one
     non-Value-language fix the slice must absorb.
   - **Extend `IsSecret`** to walk the new Value forms (Add, Subtract,
     Max, Min, First, Last, Count, Regex) so secret-tainted refs still
     propagate through them.
   - **Update `valueDiscriminatorKeys` and `valueVariantAllowedKeys`**
     to include the new keys (`add`, `subtract`, `max`, `min`, `first`,
     `last`, `count`, `regex`).
   - **Format verb set.** The closed-set verbs the validator
     recognises grow to include `parse_duration` and `url_encode`
     (already in the existing list — confirm). Slice 7 owns the
     closed-set/Go-layout dispatch in the validator; for Slice 5, the
     Format Value shape is unchanged.

2. A new artefact at `docs/planning/IMPL-05-schema-value.md`. It carries:
   - Scope (the files you owned, in one sentence).
   - Old → new Value-form map (what changed in the discriminator key
     set, the IsSecret walker, the codec).
   - Removed-content list (`NowValue.Offset`, the migration-hint
     `removedValueDiscriminatorKeys` table — both `from_pagination` and
     `from_progress` are stale references to the deleted `cursor.*`
     namespace, so remove the entire table per global rule #1).
   - Notes for downstream slices: Slice 6 (path closed set including
     the `events.first`/`events.last`/`events.<int>`/`events.count`
     shortcuts), Slice 7 (validator type-checking of reducers and
     `add`/`subtract`), Slice 9 (`evalValue` runtime).

3. Slice-table row 5 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-05-schema-value.md` link.

4. `HANDOFF.md` rewritten to point at Slice 6
   (`schema/path.go` + `schema/predicate.go`).

**Smoke tests.** Strongly encouraged inside this slice — the Value
language is the most subtle part of the schema. Suggested smoke tests
(all `// SMOKE - DELETE BEFORE SLICE CLOSE` and gone in the final
commit):

- `${state.url}/path` round-trips through `Parse` to the equivalent
  `{concat: [...]}` form.
- `"${state.next_token|}"` desugars to
  `{ref: state.next_token, default: ""}`.
- `{now: true, offset: "1h"}` is rejected at parse time.
- `{add: [{now: true}, "1h"]}` and
  `{subtract: [{now: true}, "720h"]}` parse cleanly.
- `{max: {ref: events.*.timestamp}}` and `{max: [1, 2, 3]}` both parse.
- `IsSecret` on a `{concat: [...,{ref: state.api_key}]}` returns true
  when `state.api_key` is `type: secret`.

Slice 17 owns the authoritative test rewrite — do NOT spend time
keeping `schema/fixtures_test.go` (3128 lines) green.

**Out of scope.** No struct-shape changes (Slice 4 owns the IR shape).
No path-closed-set work (Slice 6). No validator changes (Slice 7). No
client / runtime changes. No template changes.

---

## Watch out for

- **The `removedValueDiscriminatorKeys` table is stale.** Both entries
  (`from_pagination`, `from_progress`) point at the deleted `cursor.*`
  namespace. Remove the entire map per global rule #1 (no migration
  code) and global rule #4 (no references to removed concepts in
  comments). The parser's no-discriminator-key error is the only
  diagnostic authors of stale templates see — that's correct under the
  no-backward-compat posture.

- **`Value.UnmarshalYAML`'s scalar branch is the interpolation entry
  point.** A string with no `$` continues to be a plain `LiteralString`
  (zero-allocation, fast path); a string with `$` is scanned and
  desugared. Both YAML and JSON scalar paths need the same desugaring;
  the JSON path is the `case '"':` arm of `Value.UnmarshalJSON`.

- **Escape rules are subtle.** Literal `$` outside a `${...}` is plain
  text (no escape needed). Literal `$` inside a string that ALSO
  contains a `${...}` segment must be written `\$`. Literal `{` after a
  `$` outside a `${...}` segment is plain text. Inside a `${...}` a
  literal `{` is `\{`. Test the corner cases.

- **`${...|<default>}` defaults parse as YAML scalars.** That means
  `"${state.page|1}"` produces a Ref with an integer `1` default, not a
  string `"1"`. The default-parse pass should call into the existing
  YAML scalar-typing rule, not just construct a string Value.

- **Reducers accept TWO input shapes.** A list literal
  (`{max: [v1, v2]}` desugars to `{max: {list: [v1, v2]}}`) and a
  list-shaped Value (`{max: {ref: events.*.timestamp}}`). The parser
  accepts both; the runtime (Slice 9) and the validator (Slice 7) both
  honour the dual shape.

- **`Add` / `Subtract` operands are positional.** The IR carries them
  as `[2]Value` (or `[]Value` with a validate-time length check). Order
  matters: `{subtract: [{now: true}, "720h"]}` is
  `now() - 720h`, not the reverse.

- **`Regex` is BOTH a Value form AND a per-write-site optional
  string.** The Value form is full-strength
  `{regex: {pattern, from, capture?, default?}}`. The struct fields on
  ExtractVar, ProgressWrite, AdvanceWrite, NextURLPagination just carry
  a plain `Regex string` (and `Capture int` on NextURLPagination).
  Don't conflate the two — Slice 5 owns only the Value form; the
  struct-level `Regex` strings are Slice 4 shapes.

- **The build is RED at the start of this slice.** Specifically:
  - `schema/value.go:828` — the `IsSecret` fix described above.
  - `schema/validate.go` — Slice 7 owns the rewrite.
  Fix the value.go line as part of this slice; don't touch validate.go.
  Build will still be red after this slice (validate.go still broken).

---

## When you finish

`git add` the modified `schema/value.go`, the new
`IMPL-05-schema-value.md`, the updated `RESEARCH_PLAN.md`, and the
updated `HANDOFF.md`. Commit with a message like:

```
feat(schema): slice 5 — Value language additions and string interpolation

Adds Add/Subtract/Max/Min/First/Last/Count/Regex Value forms. Implements
string-interpolation parsing (${state.x|default} desugars to a {concat:
[...]} Value). Rejects {now: true, offset: ...}; the offset sibling is
gone (use {add: [{now: true}, <dur>]} or {subtract: ...} instead).
Removes the migration-hint table for the deleted cursor.* namespace.
Updates IsSecret to walk every new Value form. Fixes the one-line
d.State.Fields → d.State regression Slice 4 left behind.

schema/validate.go is left broken — Slice 7 owns the validator rewrite.
```

Push to `claude/slice-05-schema-value-<token>` and open a PR.

If you discover the slice is wider than the plan (a fold that wasn't
visible from the doc level — e.g. the validator-side closed-verb table
turns out to live in `value.go` and you need to move it), **stop and
flag it via `AskUserQuestion`** rather than widening scope silently. A
small cross-file fix is fine; rewriting another slice's primary file is
not.
