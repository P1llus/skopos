# Handoff

This file describes the single next task. It has no history; previous
work is recorded as a checkbox in [`RESEARCH_PLAN.md`](RESEARCH_PLAN.md)
and as an `IMPL-*.md` artefact in this directory.

You are an LLM agent starting fresh. Read this file end-to-end, read
the references it points at, then execute. When done, update the
checkbox in `RESEARCH_PLAN.md` and overwrite this file with the next
task (the "Updating this file when you finish" section at the bottom
tells you how).

---

## Required reading (in order)

1. **[`RESEARCH_PLAN.md`](RESEARCH_PLAN.md)** — read the "Global rules"
   section in full. Every rule there applies to this task. Do not skip
   any of them; they encode lessons from previous agent runs.
2. **[`../DESIGN_SUGGESTIONS.md`](../DESIGN_SUGGESTIONS.md)** — the
   complete schema design. This is the source of truth for every shape
   decision. The file you are rewriting must agree with this document.
3. **The file you are rewriting** — see "Task" below for the path.

You may also consult any source file in `schema/`, `client/`, `cmd/`,
or `templates/` to ground specific claims. Treat those files'
**code** as the current implementation (which is being rewritten in a
later phase). Treat their **comments and `doc.go`** as suspect — many
contain stale references to the old shape and must NOT be used as
source of truth.

Do NOT consult `docs/schema-reference.md` — it is a generated artefact
that will be regenerated after the schema package is rewritten. Until
then, ignore it entirely.

---

## Task

**Slice 1 (Phase 1):** Rewrite `docs/schema.md` to fully describe the
new schema as defined in `DESIGN_SUGGESTIONS.md`.

The current `docs/schema.md` is 1010 lines and contains roughly 85
references to concepts that are being removed (`cursor.*` namespace,
`mutability` annotations, `defaults.base_url`, `placeholder_event`,
`initial.lookback`, `next_url_at`, `token_at`, `scroll_id_at`,
`end_cursor_at`, `has_more_at`, `has_next_page_at`, `event_time`, and
several others). The rewrite is substantial — treat this as a
ground-up replacement, not a patch.

### What the new `docs/schema.md` must cover

It is the per-field reference for template authors. It must contain,
at minimum:

- The top-level keys table (`ir_version`, `state`, `auth`, `requests`,
  `response`, `pagination`, `progress`, `error`) — note that
  `defaults` is removed.
- A full section per top-level key with the field table, examples,
  and validation rules. Match the shape in `DESIGN_SUGGESTIONS.md`
  §3 exactly.
- The cross-cutting primitives — the Value language (every form
  including the new `add`, `subtract`, `max`, `min`, `first`,
  `last`, `count`, `regex`, and string interpolation forms), the
  Predicate language, the Namespace table (`state.*`, `cache.*`,
  `events.*`, `extract.*`, `steps.*`, `response.*`, `<fan_out.as>.*`).
  Match `DESIGN_SUGGESTIONS.md` §4 exactly.
- The Type table (`string`, `int`, `bool`, `secret`, `duration`,
  `timestamp`, `url`, `enum`). `rfc3339` as a type is NOT in the new
  schema.
- The Path syntax (dotted form, escape form for segments containing
  `.`).
- Design-rule invariants at the bottom (single-discriminator
  unions, structural validation only, no hidden namespaces, etc.).

### What must NOT be in the new `docs/schema.md`

- Any reference to `cursor.*` as a namespace.
- Any reference to `mutability` as a state-field annotation.
- The `defaults:` block.
- `placeholder_event`.
- `initial.lookback`, `initial_offset`, or any first-run-seeding
  apparatus that is not just `default:` on a state field.
- `*_at` path fields on pagination variants.
- `event_time.path` sub-objects.
- The old pagination variant set (`page_number`, `offset`,
  `link_header`, `next_url_in_body`, `scroll_id`, `graphql_relay` —
  all collapsed into the new `cursor_token`, `next_url`, `counter`,
  `none`, `custom` set).
- The old progress variants (`latest_event_timestamp`,
  `max_event_field`, `use_now`, `time_window`, `async_job`,
  `stateless` — collapsed to the flat list-of-writes form).
- The `flow:` block.
- Any "migration", "backwards-compat", "deprecated", or "legacy"
  language. The new schema is the schema. There is no old schema.
- Any "see DESIGN_SUGGESTIONS.md" links. The design doc is the
  upstream source; the rewritten `schema.md` is the downstream
  reference. They are separate audiences.
- Any references to slice numbers, this planning directory, or any
  in-flight work-tracking artefacts.

### Cross-references

The new `schema.md` may link to:
- `runtime.md` (will be rewritten in Slice 3; current content is
  stale but the file path is stable)
- `api-methods.md` (will be rewritten in Slice 2; same caveat)
- `stores.md` (will be rewritten in Slice 3; same caveat)
- `usage.md` (will be rewritten in Slice 3; same caveat)

Do NOT link to `schema-reference.md` (that file is being deprecated
in favour of regeneration; do not embed links to it).

### Cleanup expectations (global rule #5)

While rewriting `schema.md`, you do NOT need to touch other docs or
code files for this slice. Slice 3 covers the other prose docs;
code-touch slices handle code comments. Just rewrite `schema.md`.

---

## Expected output

1. **`docs/schema.md`** — fully rewritten. Roughly the same size or
   slightly smaller than today's 1010 lines (the redesign collapses
   variants and removes the inferred `cursor.*` namespace, so the
   new file is more compact at the same coverage level).
2. **`docs/planning/IMPL-01-docs-schema.md`** — a short artefact
   recording what was rewritten, broadly which sections changed
   structurally, and any decisions made during the rewrite that
   future agents need to know. This is not a worklog of every
   edit; it is a 50-150 line summary so other agents working
   downstream slices can quickly orient on what the new `schema.md`
   covers. Sections recommended:
   - "Scope" — one paragraph on what was rewritten.
   - "Section map" — old sections → new sections (so a future
     reader can locate concepts that moved).
   - "Removed content" — bullet list of concepts deleted (with
     replacement noted where relevant).
   - "Notes for downstream slices" — anything Slices 2 and 3 need
     to know (e.g. "I picked verb X for concept Y; reuse it").

---

## Validation before you finish

Before checking off the slice:

- [ ] Search the new `docs/schema.md` for these strings and confirm
      zero hits (use ripgrep with `-w` for word matches where it
      matters): `cursor.`, `mutability`, `placeholder_event`,
      `initial.lookback`, `initial_offset`, `next_url_at`,
      `token_at`, `scroll_id_at`, `end_cursor_at`, `has_more_at`,
      `has_next_page_at`, `event_time.`, `defaults.base_url`,
      `latest_event_timestamp`, `async_job`, `flow:`,
      `progress.stateless`.
- [ ] Confirm the doc agrees with `DESIGN_SUGGESTIONS.md` on every
      field name. Spot-check the bearer_simple migration in
      `DESIGN_SUGGESTIONS.md` §5.1 — a reader of the new `schema.md`
      should be able to understand every field in that proposed
      template.
- [ ] Confirm the doc has no links to `DESIGN_SUGGESTIONS.md` or
      `schema-reference.md`.
- [ ] Confirm `IMPL-01-docs-schema.md` exists and matches the
      template described above.

---

## Updating this file when you finish

Once both deliverables are in place:

1. Edit `RESEARCH_PLAN.md` and tick the Slice 1 checkbox.
2. Overwrite this `HANDOFF.md` with the next slice's content. The
   next slice is **Slice 2 (Phase 1): rewrite `docs/api-methods.md`**.
   Use this same file structure (required reading → task → expected
   output → validation → updating instructions). The "expected
   output" file for Slice 2 is `IMPL-02-docs-api-methods.md`.
3. Commit nothing on your own — leave staging to the human operator.

Do not start Slice 2 yourself unless explicitly asked. The operator
controls phase transitions.
