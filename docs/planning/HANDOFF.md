# Handoff — Phase 2 Slice 4 (`schema/schema.go` struct definitions)

Phase 1 is closed. Phase 2 is planned in
[`PHASE-2-PLAN.md`](PHASE-2-PLAN.md); the slice table lives in
[`RESEARCH_PLAN.md`](RESEARCH_PLAN.md). The next slice is **Slice 4 —
`schema/schema.go` + `read.go` + `doc.go` struct definitions**.

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2
   slice list, the cross-cutting rules specific to Phase 2, and the
   verification posture. §3.3 is the load-bearing rule on throwaway
   smoke tests.
3. [`PHASE-2-PLAN.md` §"Slice 4"](PHASE-2-PLAN.md#slice-4--schemaschemago--readgo--docgo).
   Your slice's detailed scope.
4. [`docs/schema.md`](../schema.md). The Phase 1 prose doc — your
   single source of truth for the post-redesign struct shape. Section
   numbering tracks `DESIGN_SUGGESTIONS.md`'s §3. Specifically:
   - §1-2 (top-level keys, top-level shape) for what `Doc` should hold.
   - §3 (state) for the flat `state:` map and the lifetime-inference
     table — though field-level `Mutability` is gone (lifetime is
     derived, not authored).
   - §4 (auth) for the new unified `Cache` struct and bare-`Auth`
     `MultiModeAuth.Default`.
   - §5 (requests) for the new `terminate_when:` field and the
     `Cache` (not `RequestCache`) block.
   - §6 (response) for the removal of `placeholder_event`.
   - §7 (pagination) for the 5-key union (`None`, `CursorToken`,
     `NextURL`, `Counter`, `Custom`).
   - §8 (progress) for the flat list of writes.
   - §9 (error) for the unchanged shape.
5. [`docs/DESIGN_SUGGESTIONS.md` §3](../DESIGN_SUGGESTIONS.md). The
   rationale behind each shape decision. Consult only when the prose
   in `schema.md` is ambiguous.

You do NOT need to read the existing `schema/*.go` files end-to-end.
The current `schema.go` (978 lines) is the old shape; you will replace
it. The current `validate.go`, `value.go`, `path.go`, `predicate.go`
will be broken at the end of this slice — that is expected and is
fixed by Slices 5, 6, 7. Do not modify them in this slice.

---

## Your slice

**Branch.** Develop on `claude/slice-04-schema-structs-<token>` (the
operator's repo workflow will assign the exact suffix; if unspecified,
use a short distinctive tag of your choosing).

**Files you may touch.**

- `schema/schema.go`
- `schema/read.go`
- `schema/doc.go`

**Files you must NOT touch.**

- `schema/value.go`, `schema/path.go`, `schema/predicate.go`,
  `schema/validate.go` — Slices 5, 6, 7 own them. Leave them broken.
- `schema/fixtures_test.go`, `schema/example_test.go` — Slice 17
  rewrites tests.
- Any file outside `schema/`.

**Deliverables.**

1. The three files above, rewritten against the post-redesign shape
   from `docs/schema.md`. `go build ./schema/...` succeeds.
2. A new artefact at `docs/planning/IMPL-04-schema-structs.md`. It
   carries:
   - Scope (the files you owned, in one sentence).
   - Old → new section/type map. For each removed type (`Defaults`,
     `TokenCache`, `RequestCache`, `CursorTokenPagination`,
     `PageNumberPagination`, `OffsetPagination`, `LinkHeaderPagination`,
     `NextURLInBodyPagination`, `ScrollIDPagination`,
     `GraphQLRelayPagination`, `TimestampProgress`, `UseNowProgress`,
     `EventTime`, `Initial`, `TimeWindowProgress`, `AsyncJobProgress`,
     `AsyncSubmitStep`, `AsyncPollStep`, `AsyncFetchStep`,
     `AsyncExtract`, `AsyncOnComplete`, `CursorUpdateDirective`, and
     every `*_raw` mirror), the new home (or "deleted, replaced by
     ...").
   - Removed-content list (the field names that disappear from the
     IR: `state.fields`, `FieldDecl.Mutability`, `Defaults.BaseURL`,
     `Request.Path`, `Request.RequestCache`, `Response.PlaceholderEvent`,
     `ExtractVar.Target`, etc.).
   - Notes for downstream slices: which types Slice 5 (Value
     language), Slice 7 (validator), and Slice 8 (client state) will
     read off of.
3. Slice-table row in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-04-schema-structs.md` link.
4. `HANDOFF.md` rewritten to point at Slice 5 (`schema/value.go`).

**Smoke tests.** You MAY add throwaway tests inline (e.g. a
`TestParseEveryNewShape` that round-trips a small YAML sample through
`Parse` to verify the new struct tags). Mark them
`// SMOKE - DELETE BEFORE SLICE CLOSE` and delete them in the final
commit of the slice. Slice 17 owns the authoritative test rewrite.

**Out of scope.** No validator changes (Slice 7). No value-language
additions (Slice 5). No path/predicate changes (Slice 6). No client
changes. No template changes. No `doc.go` references to design docs or
slice numbers — see global rule #4.

---

## Watch out for

- **`FieldDecl.Mutability` field is gone.** Lifetime is derived at
  validate time from where the field is written; it is not authored
  and not stored on the parsed struct. Slice 7's validator will
  compute the classification. Do not add a derived field on `FieldDecl`
  in this slice — let Slice 7 own that decision.
- **`Pagination` union has 5 keys total.** `None`, `CursorToken`,
  `NextURL`, `Counter`, `Custom`. The 7-variant old shape is gone.
  Variant content per `docs/schema.md` §7.
- **`Progress` is a slice (`[]ProgressWrite`), not a struct with
  variant pointers.** A progress block with zero writes (`progress: []`
  or omitted) means "no progress tracking" (what was `stateless`).
- **`MultiModeAuth.Default` is a bare `Auth`**, not the wrapped
  `{auth: ...}` form. Match `branches[].auth` symmetrically.
- **`Cache` is one struct used in two places** — `OAuth2Auth.*.Cache`
  AND `Request.Cache`. Define it once, embed twice. The runtime
  unification (Slice 13) merges `oauth2.go` + `requestcache.go` into
  one `cache.go`.
- **No `placeholder_event:` on `Response`.** Empty pages trigger the
  next page in the new runtime; no synthetic event is emitted.
- **`Request.URL` is required; `Request.Path` is removed.** Every
  request carries an absolute URL Value (typically a string
  interpolation against `state.url`). The interpolation parser is
  Slice 5's job — for Slice 4, `URL` is just a `Value` field.

---

## When you finish

`git add` the three modified `schema/` files, the new
`IMPL-04-schema-structs.md`, the updated `RESEARCH_PLAN.md`, and the
updated `HANDOFF.md`. Commit with a message like:

```
feat(schema): slice 4 — struct definitions for the post-redesign IR

Rewrites schema/schema.go, schema/read.go, schema/doc.go against the
shape described in docs/schema.md. Drops Defaults, the 7-variant
pagination union, the 5-variant progress union, the async_job phase
machine, FieldDecl.Mutability, TokenCache+RequestCache (replaced by
unified Cache), Request.Path, Response.PlaceholderEvent, ExtractVar.Target.
Validator (schema/validate.go) and value runtime (schema/value.go) are
left broken — Slices 7 and 5 respectively rewrite them.
```

Push to `claude/slice-04-schema-structs-<token>` and open a PR.

If you discover the slice is wider than the plan (a fold that wasn't
visible from the doc level — e.g. `value.go` needs a one-line change
to compile against the new `Value` shape), **stop and flag it via
`AskUserQuestion`** rather than widening scope silently. A small
cross-file fix is fine; rewriting another slice's primary file is not.
