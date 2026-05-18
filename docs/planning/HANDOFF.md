# Handoff — Phase 1 complete; awaiting review before Phase 2

Phase 1 (documentation rewrite) is finished. Do **not** start Phase 2
work in this session. Phase 2 (code and template rewrite, including
`docs/schema-reference.md` regeneration) is the next work to plan, but
the operator controls phase transitions and Phase 2 slices are not
planned yet. Wait for the operator to open Phase 2 planning before
inventing slice work.

---

## Phase 1 status

All three slices are checked off in [`RESEARCH_PLAN.md`](RESEARCH_PLAN.md):

- [x] Slice 1 — `docs/schema.md` rewrite. Record:
  [`IMPL-01-docs-schema.md`](IMPL-01-docs-schema.md).
- [x] Slice 2 — `docs/api-methods.md` rewrite. Record:
  [`IMPL-02-docs-api-methods.md`](IMPL-02-docs-api-methods.md).
- [x] Slice 3 — `docs/runtime.md` + `docs/stores.md` + `docs/usage.md`
  rewrite. Record:
  [`IMPL-03-docs-runtime-stores-usage.md`](IMPL-03-docs-runtime-stores-usage.md).

The four `IMPL-0N-*.md` artefacts in this directory are the per-slice
records. Each one contains the slice's scope, the old → new section
map, the removed-content list, and the "Notes for downstream slices"
section that pins vocabulary Phase 2 must reuse.

The prose docs in `docs/` (`schema.md`, `api-methods.md`, `runtime.md`,
`stores.md`, `usage.md`) are now a faithful description of the
post-redesign schema. They are the reference Phase 2 code-touch agents
will consult; `doc.go` / docstrings / code comments remain stale (and
are explicitly out of scope until their owning slice).

`docs/DESIGN_SUGGESTIONS.md` is unchanged (it is the design record and
stays as-is). `docs/schema-reference.md` is unchanged (it will be
regenerated AFTER the schema package is rewritten in a later Phase 2
slice — see global rule #3 in `RESEARCH_PLAN.md`).

---

## What Phase 2 will plan

(Indicative only — the operator's planning session will set the actual
slice list.)

`RESEARCH_PLAN.md` §"Phase 2" lists the expected domains:

- `schema/` package — struct definitions, parsing, validator. Likely
  3-4 slices because `validate.go` alone is 1487 lines.
- `client/` package — runtime. Drain loop, pagination, progress, state,
  value resolution, caches. Likely 5-6 slices.
- `cmd/` package — CLI. Small, probably one slice.
- `templates/` — 26 templates to rewrite plus the schema fixtures in
  `schema/testdata/`. Likely one slice once the schema package is
  rewritten so the rewrites can be validated.
- Tests — final slice. All test files across `schema/` and `client/`
  rewritten against the new shape. `go test ./...` to a green build at
  the end.
- `docs/schema-reference.md` regeneration — final step.

Each Phase 2 slice will produce its own `IMPL-NN-*.md` artefact in
this directory; the numbering continues from `IMPL-04-*`.

---

## For the next agent

If you are an agent reading this file: **do not start Phase 2 yet.**
Wait for the operator to overwrite this file with a specific Phase 2
slice task. If you have been asked to work on something other than
Phase 2, the operator will tell you what to do explicitly — there is
no implicit "next task" derivable from this notice.
