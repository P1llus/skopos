# Research and implementation plan

Top-level plan for the schema redesign rollout. This file holds the
slice list, the order, the global constraints, and the location of
each slice's implementation plan. It carries no history beyond
checkboxes — once a slice is complete, only the checkbox flips.

The schema design itself is locked. See
[`../DESIGN_SUGGESTIONS.md`](../DESIGN_SUGGESTIONS.md) — that document
is the single source of truth for every shape decision.

The current agent always reads [`HANDOFF.md`](HANDOFF.md) for its
specific task. This file is the index.

---

## Global rules — apply to every slice

Every agent working any slice MUST follow these. They are not
suggestions and they are not slice-specific.

1. **No backwards compatibility, no migration code.** The project has
   no installed user base. There is nothing to migrate. Every change
   deletes the old shape in the same commit that introduces the new
   one. No shims, no detection-and-translation paths, no "load v0
   snapshot, emit v1 snapshot" logic, no fallback branches that read
   stale keys. Anything not described in `DESIGN_SUGGESTIONS.md` is
   removed, not preserved.

2. **`DESIGN_SUGGESTIONS.md` is the source of truth.** It overrides
   anything in code comments, docstrings, `doc.go`, old docs, or the
   generated `docs/schema-reference.md`. When a stale doc disagrees
   with the design, the design wins and the doc is rewritten.

3. **`docs/schema-reference.md` is generated and stale.** Do not
   update it by hand. Do not treat it as authoritative. It will be
   regenerated AFTER the schema package is rewritten in a later
   slice. Until then, ignore it.

4. **Comments and docstrings do not reference design docs, slice
   numbers, transition states, or stale concepts.** Code comments,
   `doc.go` files, and docstrings describe what the code IS today —
   not what it was, not what is planned, not the design rationale
   that lives in the design doc. Specifically forbidden:
   - "see `DESIGN_SUGGESTIONS.md`" or any link to the design doc
   - "slice N", "phase 1", "TBD", "see plan"
   - Mentions of the old shape ("formerly `cursor.*`", "was
     `mutability: runtime`", etc.)
   - "for backwards compatibility", "legacy", "deprecated"
   - "will be implemented in", "future work"
   If the code refers to something that does not exist, the
   reference is removed.

5. **Clean up stale comments out of scope.** When an agent encounters
   stale, misleading, or design-doc-referencing comments anywhere in
   a file they're already opening, they fix them — even if those
   lines are not the slice's primary target. This is a global hygiene
   pass that piggybacks on every slice's file touches.

6. **Tests and CI need not pass per slice.** Tests will be rewritten
   in a dedicated late-phase slice once the code is shaped correctly.
   Agents must NOT slow themselves down trying to keep stale tests
   green. They must NOT delete tests preemptively either — leave them
   broken; the test slice handles the rewrite.

7. **Each slice produces or updates one named implementation plan
   file in this directory.** File names are listed below. After the
   slice's research portion is complete, the implementation plan is
   the executable artefact; an implementation agent reads it
   end-to-end and executes.

8. **Each agent finishes by updating its own row in this file's slice
   table (check the box, note the output file) and then rewriting
   `HANDOFF.md` to point at the next slice.**

---

## Phase 1 — Documentation rewrite

The doc set must be a faithful description of the post-redesign
schema before any code is touched. Agents working code slices are
told to ignore stale `doc.go` / docstrings, but the prose docs in
`docs/` are the reference they DO consult — those have to be right.

Three files are in scope, each its own research slice. The three
small ones bundle into a fourth slice. `DESIGN_SUGGESTIONS.md` is
NOT touched (it stays as the design record). `schema-reference.md`
is NOT touched (it will be regenerated post-Phase 2).

| #   | Status | Research target                                              | Implementation plan file                   |
|-----|--------|--------------------------------------------------------------|--------------------------------------------|
| 1   | [x]    | `docs/schema.md` (1010 lines, ~85 stale refs)                | `IMPL-01-docs-schema.md`                   |
| 2   | [x]    | `docs/api-methods.md` (1305 lines, ~80 stale refs)           | `IMPL-02-docs-api-methods.md`              |
| 3   | [ ]    | `docs/runtime.md` + `docs/stores.md` + `docs/usage.md` (901 lines combined, ~32 stale refs) | `IMPL-03-docs-runtime-stores-usage.md` |

**Order:** strictly sequential, 1 → 2 → 3. Each slice is research +
implementation in one agent session (the rewrites are mechanical
once the design is understood). `schema.md` first because the other
two docs cross-reference it.

After all three slices are checked off, this Phase 1 is complete.
We then pause for review before Phase 2 research is planned.

---

## Phase 2 — Code and template rewrite

Planned AFTER Phase 1 is complete and reviewed. Domains expected:

- **`schema/` package** — struct definitions (`schema.go`), parsing
  (`value.go`, `path.go`, `predicate.go`), validator (`validate.go`).
  Likely 3-4 slices because `validate.go` alone is 1487 lines.
- **`client/` package** — runtime. Drain loop (`runner.go`),
  pagination (`pagination.go`), progress (`progress.go`), state
  (`state.go`), value resolution (`value.go`), caches
  (`oauth2.go` + `requestcache.go`). Likely 5-6 slices.
- **`cmd/` package** — CLI. Small, probably one slice.
- **`templates/` directory** — 26 templates to rewrite plus the
  schema fixtures in `schema/testdata/`. Likely one slice once the
  schema package is rewritten so the rewrites can be validated.
- **Tests** — final slice. All test files across `schema/` and
  `client/` are rewritten against the new shape. Run `go test ./...`
  to a green build at the end.
- **`docs/schema-reference.md` regeneration** — final step. Run
  whatever generator produces the file; commit the regenerated
  output.

These are deferred. Do not plan them in detail until Phase 1 closes.

---

## Output file naming convention

Implementation plan files in this directory follow
`IMPL-NN-<short-tag>.md` where `NN` is a zero-padded two-digit
sequence number and `<short-tag>` describes the scope. The sequence
continues across phases (so Phase 2 starts at `IMPL-04-*`).

Files like `docs/RESEARCH_PLAN.md` and `docs/HANDOFF.md` are bare-named at the
root of this directory.

The next agent always reads `HANDOFF.md` first.
