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
   decision. The files you are rewriting must agree with this document.
3. **[`../schema.md`](../schema.md)** — the rewritten per-field
   reference. Match its vocabulary verbatim (variant names, lifetime
   terms, namespace roots, loop names).
4. **[`../api-methods.md`](../api-methods.md)** — the slice-2 rewrite.
   The files you are rewriting cross-reference it (each section anchor
   is stable); they should also reuse the loop names, the on_status
   verb set, the cache vocabulary, the async-job phrasing, and the
   pagination-variant coverage groupings from this doc verbatim.
5. **[`IMPL-01-docs-schema.md`](IMPL-01-docs-schema.md)** and
   **[`IMPL-02-docs-api-methods.md`](IMPL-02-docs-api-methods.md)** —
   their "Notes for downstream slices" sections pin vocabulary that
   this slice MUST reuse rather than re-derive (loop names; async-job
   wording; cache slot names; per-drain wipe phrasing; progress timing
   phrasing; fan-out reserved-root list; URL-composition idioms;
   `events.*` reference forms; on_status closed-verb set; pagination
   variant coverage).
6. **The three files you are rewriting** — see "Task" below for the
   paths.

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

**Slice 3 (Phase 1):** Rewrite `docs/runtime.md`, `docs/stores.md`, and
`docs/usage.md` against the new schema in `DESIGN_SUGGESTIONS.md` (now
codified in `docs/schema.md` and `docs/api-methods.md`).

The current three files total 901 lines with ~32 stale references
between them (the `cursor.*` namespace, `mutability` annotations, the
old pagination variant names, the named progress variants, the
`async_job` dispatcher, the `flow:` block, the
`TokenCache`/`RequestCache` split, the `defaults.base_url` block, the
`state.fields` wrapper). Treat each file as a from-scratch rewrite
against the new shape, not a patch — but bundle the three under one
slice because (a) they are small individually, (b) they cross-reference
each other and the two earlier-rewritten docs, and (c) the same
vocabulary applies across all three.

### What each new file must cover

**`docs/runtime.md`** — the runtime / execution-order reference. It
describes what the runner does at run time, given a parsed spec. It
covers, at minimum:

- The drain lifecycle: per-drain state wipe at start, pagination loop,
  request loop, progress evaluation timing ("once per accepted
  page-response, including empty pages"), state commit, error mode
  semantics (`standard` / `warn` / `fail`), sink delivery before commit
  (at-least-once).
- The pagination-loop execution order — request → `terminate_when` →
  variant write → loop (matching `schema.md`'s "execution order per
  page" section).
- The request-loop execution order for `requests[].terminate_when:` —
  one request fires, predicate evaluates, loop or proceed.
- `error.mode` semantics in detail (what each mode does with non-2xx
  HTTP responses, network errors, decode failures); how `on_status:`
  verbs interact with each mode.
- The `cache.*` namespace lifetime (process memory only; cleared on
  runner restart; never persisted) and cache invalidation semantics
  (`on_status: {<code>: invalidate_cache}` drops every reachable
  `cache.*` slot).
- Secret redaction (when the runner redacts what; how secret-ness
  propagates through Value composition).
- The `Tracer` surface (what fields a `Tracer.Exchange` record carries;
  the `JSONLTracer` output shape).
- HTTP transport defaults (30-second timeout; caller-injected
  `*http.Client`).
- Anything else `client/` package code currently expresses that is
  worth pinning at the doc level.

**`docs/stores.md`** — the persistence contract. It describes what the
`Store` interface persists, what it does NOT persist, and the
guarantees on top.

- What is persisted: `state.<name>` fields whose lifetime infers to
  "persistent" (written by `progress:` or by `requests[].extract` with
  `to: state.*`); operator-config fields (declared with `default:`,
  no writes). These survive across drains and runner restarts.
- What is NOT persisted: per-drain scratch (`state.<name>` written by
  `pagination:`); `cache.*` slots; `events.*`; `extract.*`;
  `steps.*`; `response.*`. Per-drain state is wiped at the start of
  every drain.
- The `Store` interface shape (Load / Save semantics; concurrency;
  atomicity; error handling).
- The `state.json` file shape (or whatever the on-disk persisted
  surface is); how operator-supplied input is layered with persisted
  state at start.
- Concurrent-runner / locking guarantees (if any).
- What happens on first run (no persisted state present): operator
  defaults seed the state; first-run progress writes commit normally.

**`docs/usage.md`** — end-to-end walkthroughs and operator-facing
how-tos. It targets the operator who wants to point the runner at an
API and get events flowing. Covers, at minimum:

- "Pick a template" walkthrough — how to read a template, what state
  fields the operator supplies, how to override defaults.
- "Write a new template" walkthrough — the minimum YAML for a simple
  GET-with-bearer-token API; growing it into pagination; adding
  progress; adding a multi-step chain; adding caching. Reuse the
  `api-methods.md` recipe vocabulary; do not re-derive it.
- The CLI surface (`cmd/`) — what subcommands exist, what flags
  matter for operating a template, where state is persisted on disk.
- Operator-facing error messages and what they mean (broad strokes;
  the per-field details live in `schema.md`).
- Anything else `cmd/` currently does that is operator-facing.

### What must NOT be in any of the three new files

Same forbidden list as the prior two slices. Specifically:

- Any reference to `cursor.*` as a namespace or to fields like
  `cursor.token`, `cursor.page`, `cursor.offset`, `cursor.next_url`,
  `cursor.next_link`, `cursor.scroll_id`, `cursor.last_timestamp`,
  `cursor.window_start`, `cursor.window_end`, `cursor.phase`.
- `mutability` as a state-field annotation.
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
- The old progress variant names (`latest_event_timestamp`,
  `max_event_field`, `use_now`, `time_window`, `async_job`,
  `stateless`).
- The `flow:` block.
- `TokenCache` / `RequestCache` as two separate structs.
- Any "migration", "backwards-compat", "deprecated", or "legacy"
  language.
- Any "see `DESIGN_SUGGESTIONS.md`" links.
- Any references to `docs/schema-reference.md`.
- Any references to slice numbers, this planning directory, or any
  in-flight work-tracking artefacts.

### Cross-references

The three new files may freely link to each other and to
`schema.md` / `api-methods.md`. Reuse their section anchors.

Do NOT link to `schema-reference.md` or `DESIGN_SUGGESTIONS.md`.

### Cleanup expectations (global rule #5)

While rewriting these three docs, you do NOT need to touch other docs
or code files for this slice. Code-touch slices handle code comments
and `doc.go` files; the only `doc.go` / source-comment edits you make
in this slice are inside files you happen to open for grounding — and
even then, only when the comment is glaringly stale or design-doc
referencing. Default action: leave source-tree comments alone in this
slice unless they are right next to something you need to read.

---

## Expected output

1. **`docs/runtime.md`** — fully rewritten. Probably similar in length
   to the current 348 lines, possibly a bit shorter (the redesign
   removes the cursor-namespace explanation, the async-job dispatcher
   description, and the named-progress timing matrix — but adds the
   per-drain wipe rule, the unified Cache section, and the request-loop
   primitive).
2. **`docs/stores.md`** — fully rewritten. Probably similar in length
   to the current 286 lines, possibly shorter (the `mutability`
   classification is gone; lifetime inference is one paragraph).
3. **`docs/usage.md`** — fully rewritten. Probably similar in length
   to the current 267 lines. Walk-through-driven; reuse `api-methods.md`
   recipe vocabulary.
4. **`docs/planning/IMPL-03-docs-runtime-stores-usage.md`** — a single
   summary artefact for the three rewrites, sectioned by file. Same
   template as `IMPL-01` and `IMPL-02`: "Scope" (one paragraph per
   file), "Section map" (old → new for each file), "Removed content"
   (bullet list, marked per file), "Notes for downstream slices" (any
   word choices or recipe groupings the eventual Phase 2 code-touch
   slices need to keep consistent). 100-300 lines total is fine.

---

## Validation before you finish

Before checking off the slice:

- [ ] Search each of the three new files for the forbidden-strings
      ripgrep (the full list above) and confirm zero hits.
- [ ] Search each file for `migrat`, `backward`, `deprecat`, `legacy`,
      `slice [0-9]`, `phase [12]` (case-insensitive) and confirm zero
      hits.
- [ ] Confirm each file agrees with `DESIGN_SUGGESTIONS.md` on every
      field name, with `schema.md` on every section anchor it links to,
      and with `api-methods.md` on vocabulary (loop names, async-job
      phrasing, cache slot vocabulary, pagination variant groupings,
      `on_status:` verb set, fan-out reserved-root list).
- [ ] Confirm no links to `DESIGN_SUGGESTIONS.md` or
      `schema-reference.md`.
- [ ] Spot-check that the persistence contract in `stores.md` matches
      the lifetime inference rule from `schema.md` (operator config /
      per-drain / persistent — only persistent + operator-config
      survive; per-drain is wiped at drain start; `cache.*` is process
      memory only).
- [ ] Spot-check that `runtime.md`'s progress-evaluation section says
      "once per accepted page-response, including empty pages" (or an
      equivalent rephrasing — but the "accepted page-response" qualifier
      and the "including empty pages" clause must both be present and
      load-bearing).
- [ ] Confirm `IMPL-03-docs-runtime-stores-usage.md` exists and is
      sectioned by the three rewritten files.

---

## Updating this file when you finish

Once all four deliverables are in place:

1. Edit `RESEARCH_PLAN.md` and tick the Slice 3 checkbox. Phase 1 is
   complete after Slice 3 — there is no Slice 4. The next user
   interaction is a review before Phase 2 is planned.
2. Overwrite this `HANDOFF.md` with a short "Phase 1 complete; awaiting
   review before Phase 2" notice. Phase 2 slices are not planned yet;
   do not invent them. The notice should:
   - State that Slices 1, 2, and 3 are checked off in `RESEARCH_PLAN.md`.
   - Point at the four `IMPL-0N-*.md` artefacts as the per-slice
     records.
   - Note that Phase 2 (code and template rewrite, including
     `schema-reference.md` regeneration) is the next work to plan, but
     that no agent should start Phase 2 work until the operator
     explicitly opens Phase 2 planning.
3. Commit nothing on your own — leave staging to the human operator.

Do not start Phase 2 yourself. The operator controls phase transitions.
