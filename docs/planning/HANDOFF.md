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
3. **[`../schema.md`](../schema.md)** — the rewritten per-field
   reference. The file you are rewriting links to it, uses its
   vocabulary verbatim, and must NOT contradict it. Spot-check that the
   new `api-methods.md` describes each IR shape using the names defined
   in `schema.md` (e.g. `pagination.cursor_token` / `pagination.next_url`
   / `pagination.counter` / `pagination.custom` / `pagination.none`).
4. **[`IMPL-01-docs-schema.md`](IMPL-01-docs-schema.md)** — the slice-1
   summary. The "Notes for downstream slices" section in there records
   word choices (pagination variant names, per-drain vs persistent
   vocabulary, loop terminology, cache vocabulary, namespace roots,
   progress evaluation timing, async-job pattern wording, `events.*`
   projections) that this slice MUST reuse rather than re-derive.
5. **The file you are rewriting** — see "Task" below for the path.

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

**Slice 2 (Phase 1):** Rewrite `docs/api-methods.md` to fully describe
the API shapes the runner expresses, against the new schema as defined
in `DESIGN_SUGGESTIONS.md` and now codified in `docs/schema.md`.

The current `docs/api-methods.md` is 1305 lines and contains roughly 80
references to concepts that are being removed (the `cursor.*` namespace
and its inferred fields, `mutability` annotations, `defaults.base_url`
URL composition, `placeholder_event`, `initial.lookback`, all the
`*_at` pagination path fields, `event_time.path` sub-objects, the old
pagination variant names, the named `progress:` variants, the
`async_job` phase machine, the `flow:` block). The rewrite is
substantial — treat this as a ground-up replacement, not a patch.

### What the new `docs/api-methods.md` must cover

It is the per-API-shape recipe book for template authors. It describes
each API-communication pattern (auth scheme, pagination shape, body
encoding, response parsing, state checkpointing, cross-cutting
concerns) by naming it, summarising it in a paragraph, and showing the
IR knobs that express it.

It must contain, at minimum:

- **Authentication patterns.** No-auth, bearer, basic, api-key (in
  header or query), custom header, OAuth2 client_credentials, OAuth2
  password_grant, multi-mode dispatch, refresh-token-as-state-field,
  and the unified `Cache` block for caching tokens (both auth-grant
  and step-level forms — they are one struct now).
- **Pagination patterns.** Group by the named variants in the new
  schema: `none`, `cursor_token` (covers opaque cursors, scroll IDs,
  GraphQL Relay end cursors, "next page number from body"),
  `next_url` (body and Link header), `counter` (covers page-number
  and offset by choosing `start:` and `step:`), and `custom` (the
  escape hatch). Each entry explains which API shapes map to which
  variant.
- **Request body shapes.** `json:`, `form:`, `raw:`. Discriminator
  rules and when to use which.
- **Response parsing.** `decode: json` vs `decode: ndjson`.
  `events_at` namespace-rooted paths, including the
  `steps.<id>.body.*` form. The `events.*` projection namespace and
  its `events.first`, `events.last`, `events.<int>`, `events.count`
  shortcuts.
- **State checkpointing patterns.** The flat `progress:` list and the
  concrete recipes it expresses — max-of-events high-water mark
  (with the explicit `{max: [{ref: state.last_timestamp}, {max:
  {ref: events.*.<field>}}]}` form for restart-safety), clock-driven
  (`{now: true}`), sliding window (two entries, `state.window_end` →
  `state.window_start` via the pre-write snapshot rule), pinned-id
  (`{ref: events.first.id}`), per-page checkpointing on empty
  pages, first-run seeding via `default:` on the destination state
  field.
- **Async-job pattern.** Plain requests with `terminate_when:` on the
  poll step and ordinary extracts to `state.*`. No phase machine.
- **Fan-out.** `fan_out.over` / `as` / `merge` with the per-item
  binding namespace; mutual exclusivity with `cache`.
- **Per-status handling.** `requests[].on_status` with the closed
  verb set (`skip`, `fail`, `empty_events`, `invalidate_cache`) and
  `error.mode` defaults.
- **Loop primitives.** `requests[].terminate_when:` for the request
  loop; `pagination.*.terminate_when:` for the pagination loop. The
  variant-specific defaults documented in `schema.md`.
- **URL composition.** Every request declares an absolute `url:`
  Value. The two idioms are string interpolation
  (`"${state.url}/path"`) and `{concat: [...]}` for cases where
  interpolation is awkward (e.g. when an entire URL is read from
  per-drain state with a bootstrap fallback:
  `{ref: state.next_url, default: "${state.url}/<bootstrap-path>"}`).
- **Buckets.** Keep the existing three-bucket convention (Default —
  supported / Deferred / Out of scope) and tag entries inline. Do not
  invent new buckets.

### What must NOT be in the new `docs/api-methods.md`

- Any reference to `cursor.*` as a namespace, or to fields like
  `cursor.token`, `cursor.page`, `cursor.offset`, `cursor.next_url`,
  `cursor.next_link`, `cursor.scroll_id`, `cursor.last_timestamp`,
  `cursor.window_start`, `cursor.window_end`, `cursor.phase`.
- Any reference to `mutability` as a state-field annotation.
- The `defaults:` block or `defaults.base_url`.
- `placeholder_event`.
- `initial.lookback`, `initial_offset`, or any first-run-seeding
  apparatus that is not just `default:` on a state field.
- `*_at` path fields on pagination variants (`token_at`,
  `scroll_id_at`, `end_cursor_at`, `has_more_at`, `has_next_page_at`,
  `next_url_at`).
- `event_time.path` sub-objects on any progress entry.
- The old pagination variant names (`page_number`, `offset`,
  `link_header`, `next_url_in_body`, `scroll_id`, `graphql_relay`).
  Recipes that previously used them are mapped to the new
  `cursor_token` / `next_url` / `counter` / `custom` variants — pick
  the right one and write the recipe under the new name.
- The old progress variant names (`latest_event_timestamp`,
  `max_event_field`, `use_now`, `time_window`, `async_job`,
  `stateless`). Replaced by the flat list-of-writes form and (for
  `stateless`) by an omitted or empty `progress:` block.
- The `flow:` block.
- `TokenCache` / `RequestCache` as two separate structs. They are one
  `Cache` block now, used by both `auth.oauth2.<grant>.cache` and
  `requests[].cache`.
- Any "migration", "backwards-compat", "deprecated", or "legacy"
  language. The new schema is the schema. There is no old schema.
- Any "see `DESIGN_SUGGESTIONS.md`" links. The design doc is the
  upstream source; the rewritten docs are the downstream reference.
  They are separate audiences.
- Any references to `docs/schema-reference.md`. That file is being
  regenerated post-Phase 2; do not link to it.
- Any references to slice numbers, this planning directory, or any
  in-flight work-tracking artefacts.

### Cross-references

The new `api-methods.md` may link to:
- `schema.md` (the per-field reference — link freely; reuse its
  section anchors)
- `runtime.md` (will be rewritten in Slice 3; current content is
  stale but the file path is stable)
- `stores.md` (will be rewritten in Slice 3; same caveat)
- `usage.md` (will be rewritten in Slice 3; same caveat)

Do NOT link to `schema-reference.md` or `DESIGN_SUGGESTIONS.md`.

### Cleanup expectations (global rule #5)

While rewriting `api-methods.md`, you do NOT need to touch other docs
or code files for this slice. Slice 3 covers the other prose docs;
code-touch slices handle code comments. Just rewrite `api-methods.md`.

---

## Expected output

1. **`docs/api-methods.md`** — fully rewritten. Roughly the same size
   or somewhat smaller than today's 1305 lines (the redesign collapses
   the pagination and progress variant catalogue, removes the
   `cursor.*` apparatus, and removes the async-job phase machine — all
   net reductions). Bigger if you choose to expand the recipe set;
   smaller if you compress aggressively. The handoff does not pin a
   line target.
2. **`docs/planning/IMPL-02-docs-api-methods.md`** — a short artefact
   recording what was rewritten, broadly which sections changed
   structurally, and any decisions made during the rewrite that
   future agents need to know. This is not a worklog of every edit;
   it is a 50-200 line summary so other agents working downstream
   slices can quickly orient on what the new `api-methods.md` covers.
   Sections recommended:
   - "Scope" — one paragraph on what was rewritten.
   - "Section map" — old sections → new sections.
   - "Removed content" — bullet list of recipes deleted (or
     consolidated, with replacement noted).
   - "Notes for downstream slices" — anything Slice 3 needs to know
     (vocabulary, recipe groupings, anything you settled on).

---

## Validation before you finish

Before checking off the slice:

- [ ] Search the new `docs/api-methods.md` for these strings and
      confirm zero hits (use ripgrep): `cursor.`, `mutability`,
      `placeholder_event`, `initial.lookback`, `initial_offset`,
      `next_url_at`, `token_at`, `scroll_id_at`, `end_cursor_at`,
      `has_more_at`, `has_next_page_at`, `event_time.`,
      `defaults.base_url`, `latest_event_timestamp`, `max_event_field`,
      `async_job`, `flow:`, `progress.stateless`, `page_number`,
      `link_header`, `next_url_in_body`, `scroll_id` (as a pagination
      variant name; `state.scroll_id` is fine if a recipe uses it as
      the per-drain destination), `graphql_relay`, `TokenCache`,
      `RequestCache`.
- [ ] Search for `migrat`, `backward`, `deprecat`, `legacy`,
      `slice [0-9]`, `phase [12]` (case-insensitive) and confirm zero
      hits.
- [ ] Confirm the doc agrees with `DESIGN_SUGGESTIONS.md` on every
      field name and with `schema.md` on every section anchor it
      links to.
- [ ] Spot-check that the bearer_simple and next_url_in_body recipes
      (the two side-by-sides in `DESIGN_SUGGESTIONS.md` §5) are
      described — a reader of `api-methods.md` should be able to find
      the recipe for "simple GET with timestamp progress" and for
      "URL-from-body pagination with timestamp progress".
- [ ] Confirm the doc has no links to `DESIGN_SUGGESTIONS.md` or
      `schema-reference.md`.
- [ ] Confirm `IMPL-02-docs-api-methods.md` exists and matches the
      template described above.

---

## Updating this file when you finish

Once both deliverables are in place:

1. Edit `RESEARCH_PLAN.md` and tick the Slice 2 checkbox.
2. Overwrite this `HANDOFF.md` with the next slice's content. The
   next slice is **Slice 3 (Phase 1): rewrite `docs/runtime.md`,
   `docs/stores.md`, and `docs/usage.md` as one bundled slice**. Use
   this same file structure (required reading → task → expected
   output → validation → updating instructions). The "expected
   output" file for Slice 3 is `IMPL-03-docs-runtime-stores-usage.md`.
3. Commit nothing on your own — leave staging to the human operator.

Do not start Slice 3 yourself unless explicitly asked. The operator
controls phase transitions.
