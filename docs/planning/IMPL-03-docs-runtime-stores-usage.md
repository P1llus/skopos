# IMPL-03 — docs/runtime.md + docs/stores.md + docs/usage.md rewrite

## Scope

A single slice bundling three small docs because (a) they cross-reference
each other and the two earlier-rewritten docs, (b) they share the same
vocabulary (loop names, async-job phrasing, cache slot vocabulary,
pagination variant groupings, `on_status:` verb set, fan-out reserved-root
list), and (c) the same forbidden-list applies uniformly across them.

Each of the three files was treated as a ground-up rewrite, not a patch.
The old files were structurally tied to the prior schema's shape — the
`cursor.*` namespace, `mutability` annotations on state fields, the
`async_job` dispatcher, named pagination variants (`page_number`,
`offset`, `link_header`, `next_url_in_body`, `scroll_id`,
`graphql_relay`), named progress variants (`stateless`,
`latest_event_timestamp`, `max_event_field`, `use_now`, `time_window`,
`async_job`), the `flow:` block, the `TokenCache` / `RequestCache`
split, the `defaults.base_url` block, the `state.fields` wrapper.
Every section was rebuilt against the new shape codified in
`docs/schema.md` and `docs/api-methods.md`.

None of the three files links to `DESIGN_SUGGESTIONS.md` or
`schema-reference.md`. They cross-reference each other and the two
earlier-rewritten docs (`schema.md`, `api-methods.md`).

### `docs/runtime.md`

Length: 459 lines (vs. the prior 348). The growth comes from new
content rather than preserved legacy material — the legacy material is
gone. Specifically:

- A dedicated §3 for the **pagination loop** (with the per-variant
  `terminate_when:` default table) and a dedicated §4 for the
  **request loop**, separating the two primitives that the prior doc
  collapsed into one "page loop" description.
- A dedicated §5 for **progress evaluation timing**, pinning the
  "once per accepted page-response, including empty pages" rule
  including the "accepted" qualifier's load-bearing meaning
  (`expect_status:` passed + `on_status:` did not abort).
- A unified §6 for **error semantics** combining `on_status:` (the
  closed verb set table) and `error.mode` semantics. The
  `invalidate_cache → degrades-to-empty_events` fallback is
  documented.
- §8 for the **`cache.*` namespace** — process memory only, cleared on
  runner restart, dropped by `invalidate_cache`, re-fetch policy.
- §9 for the **`Tracer` surface** — the `Exchange` field shape, the
  redaction policy, the `JSONLTracer` adapter.
- §10 for **HTTP transport defaults** — 30-second timeout, caller-injected
  `*http.Client`, the explicit non-goals (retry / signing / interactive
  OAuth2 grants).
- §12 for **fan-out** with the explicit reserved-root list (`state`,
  `cache`, `events`, `extract`, `steps`, `response`).

What was removed: the cursor-namespace-and-scope-lifetimes table (the
old §3); the `async_job` phase-machine description (folded into §4 as
a plain request loop); the supported-variants matrix that tracked
every named pagination / progress variant; `placeholder_event`; the
two-pass-detector description.

### `docs/stores.md`

Length: 352 lines (vs. the prior 286). The growth comes from a
dedicated §1 lifetime-inference explanation, the §1.1 "what is NOT
persisted" table (covering every other namespace explicitly), and a
crisper §2 lifecycle description. Specifically:

- §1 enumerates what is persisted (operator-config + persistent state
  fields) and what is NOT (per-drain scratch state fields, `cache.*`,
  `events.*`, `extract.*`, `steps.*`, `response.*`, `<fan_out.as>.*`).
- §2 documents the `Snapshot` shape — `State map[string]any` only.
  The prior `Snapshot.Cursor` field is gone because the cursor
  namespace is gone; per-drain scratch lives under `state.*` with
  lifetime inferred from write sites and is excluded from `Save`
  output.
- §2 Lifecycle pins the runner-side behaviour: `Load` at top, layer
  declared defaults under the loaded snapshot, wipe per-drain scratch,
  pagination loop runs, deferred `Save` persists operator-config +
  persistent fields only.
- The SQLite worked example was rewritten to drop the `cursor` column.
  Schema is now one row per spec with one `state` BLOB column.
- The conformance test snippet was updated to assert `got.State`
  equality only (no `Cursor` field).

### `docs/usage.md`

Length: 707 lines (vs. the prior 267). The growth is intentional and
comes from new content that the slice handoff explicitly required —
two walkthroughs ("Pick a template" and "Write a new template") and a
detailed CLI surface section that the prior doc lacked. Specifically:

- §1 is a full **CLI surface** reference (`skopos validate`,
  `skopos run`, `skopos init`, `skopos template list` /
  `skopos template show`) with flag tables. The prior doc had no CLI
  section.
- §2 is a **"pick a template" walkthrough** — how to list bundled
  templates, print one to a working file, override the `state:` block,
  validate, run.
- §3 is a **"write a new template" walkthrough** that grows a spec
  from the minimum (bearer-authed GET, single page) through
  pagination → progress → multi-step chain → caching → per-step error
  handling. Each step ends in a runnable spec. Recipe vocabulary
  matches `api-methods.md` verbatim.
- §4-§10 (embedding, sinks, stores, HTTP client, tracing, scheduling,
  multi-spec) are the carry-over operator-facing how-tos from the
  prior doc, all rewritten against the new schema vocabulary.
- §11 is the **errors** reference with a dedicated "operator-facing
  diagnostics" sub-section showing the diagnostic format the validator
  emits.

What was removed: the `state.fields` wrapper in every example; the
`mutability` annotations; the `defaults.base_url` block; every recipe
that relied on `cursor.last_timestamp` / `cursor.window_start` / etc.
The `Cursor` field on `Snapshot` is gone from the embedded `Store`
example.

## Section map

### `docs/runtime.md`

| Old section                            | New section                                                                  |
|----------------------------------------|------------------------------------------------------------------------------|
| §1 Architectural position              | §1 (unchanged)                                                               |
| §2 Drain loop                          | §2 (rewritten — no `progress.seed`, no `pagination.seed`, no `cursor.*`; explicit per-drain wipe step) |
| §3 Scope lifetimes (table)             | folded into §2 (per-drain vs persistent narrative) and into `schema.md §state` (inference rule) |
| §4 Persistence contract                | moved to `stores.md` (this slice owns it now)                                |
| §5 Error policy                        | §6 (rewritten — `on_status:` verbs table + `error.mode` semantics)           |
| §6 MaxPages safety cap                 | §3 "MaxPages safety cap" sub-section                                         |
| §7 Concurrency model                   | §11 (unchanged in shape)                                                     |
| §8 Supported variants (matrix)         | **removed** — capability-by-variant matrix is gone; `api-methods.md` is the catalogue |
| §8.1 Authentication variants           | folded into `api-methods.md` §1                                              |
| §8.2 Requests / 8.3 Response           | folded into `api-methods.md` §3 / §4                                         |
| §8.4 Pagination                        | folded into `api-methods.md` §2; runtime side is §3 pagination loop          |
| §8.5 Progress                          | folded into `api-methods.md` §5; runtime side is §5 progress evaluation timing |
| §8.6 Value forms / 8.7 Predicate forms | folded into `schema.md` §Values / §Predicates                                |
| §8.8 Path                              | folded into `schema.md` §Paths                                               |
| §8.9 State                             | folded into `schema.md` §state + `stores.md` §1                              |
| §8.10 Sink, Store, CLI                 | split — Sink → `usage.md` §5; Store → `stores.md`; CLI → `usage.md` §1       |
| §8.11 HTTP transport                   | §10 (unchanged in shape)                                                     |
| §8.12 Observability                    | §9 (Tracer surface) + §13 (Observability prose)                              |
| §9 Non-goals                           | §14 (unchanged in shape; OAuth2 interactive grants explicit)                 |
| (new)                                  | §3 Pagination loop (with per-variant `terminate_when:` defaults table)       |
| (new)                                  | §4 Request loop                                                              |
| (new)                                  | §5 Progress evaluation timing                                                |
| (new)                                  | §8 Cache namespace (process-memory-only lifetime, invalidation, re-fetch)    |
| (new)                                  | §12 Fan-out (reserved-root list)                                             |

### `docs/stores.md`

| Old section                            | New section                                                                  |
|----------------------------------------|------------------------------------------------------------------------------|
| "The contract" + `Snapshot` definition | §2 The `Store` interface (Snapshot has only `State` now; no `Cursor`)        |
| "What lives where" (`State` / `Cursor`)| §1 What the runner persists (operator-config + persistent state)             |
| Lifecycle                              | §2 Lifecycle (rewritten — Load, layer defaults, wipe per-drain, deferred Save) |
| Concurrency contract                   | §3 (unchanged in shape)                                                      |
| Worked example: SQLite                 | §5 (rewritten — no `cursor` column; one `state` BLOB)                        |
| What "correct" means                   | §6 (unchanged in shape)                                                      |
| Testing your Store                     | §7 (rewritten — conformance test asserts `State` equality only)              |
| Why so minimal?                        | §8 (rewritten — lifetime inference + per-drain wipe replace runtime/config mutability framing) |
| (new)                                  | §1.1 What is NOT persisted (table covering every other namespace)            |
| (new)                                  | §4 Bundled implementations (split out for visibility)                        |

### `docs/usage.md`

| Old section                            | New section                                                                  |
|----------------------------------------|------------------------------------------------------------------------------|
| Two-step lifecycle                     | §4 Embedding the runner from Go (the embedded use case)                      |
| The Runner struct                      | §4 "The `Runner` struct" sub-section                                         |
| Sinks: receiving events                | §5 (unchanged in shape)                                                      |
| Stores: persisting state               | §6 (rewritten — only `State` map; cross-references `stores.md`)              |
| Custom HTTP client / transport         | §7 (unchanged)                                                               |
| Tracing                                | §8 (unchanged in shape; references `runtime.md` §7 / §9)                     |
| Scheduling: one-shot vs interval       | §9 (unchanged)                                                               |
| Concurrency model                      | folded into §6 / §10 / `runtime.md` §11                                      |
| Multiple specs in one process          | §10 (unchanged in shape)                                                     |
| Errors                                 | §11 (rewritten — `on_status:` table; `error.mode` modes; operator-facing diagnostic format) |
| (new)                                  | §1 The CLI surface (`validate` / `run` / `init` / `template`)                |
| (new)                                  | §2 Picking a bundled template (walkthrough)                                  |
| (new)                                  | §3 Writing a new template from scratch (6-step walkthrough)                  |
| (new)                                  | §11 "Operator-facing diagnostics" sub-section                                |

## Removed content

Hard removals (with replacement noted where applicable), tagged per
file. R = `runtime.md`; S = `stores.md`; U = `usage.md`.

- **The `cursor.*` namespace** (R, S, U). Replaced with `state.<name>`
  (with lifetime inferred from write sites).
- **`Snapshot.Cursor` map** (S, U). Removed: the snapshot carries only
  `State`. The SQLite example schema drops the `cursor` BLOB column.
- **`mutability` annotations on state fields** (R, S, U). Replaced
  with lifetime inferred from write sites (operator-config / per-drain
  scratch / persistent).
- **`state.fields` wrapper** (U). Replaced with the flat `state:` map.
- **`defaults.base_url` and `path:`** (U). Every request declares an
  absolute `url:` Value composed via string interpolation or `concat`.
- **`placeholder_event` and the two-pass-detector** (R). Removed:
  empty pages are valid accepted page-responses.
- **Named pagination variants** `page_number`, `offset`, `link_header`,
  `next_url_in_body`, `scroll_id`, `graphql_relay` (R). Replaced with
  the four-variant taxonomy from `api-methods.md` (`cursor_token`,
  `next_url`, `counter`, `custom`, plus `none`).
- **Named progress variants** `stateless`, `latest_event_timestamp`,
  `max_event_field`, `use_now`, `time_window`, `async_job` (R, U).
  Replaced with the flat list of `{to, from, ...}` writes.
- **`async_job` phase machine** (R). Replaced by the request-level
  loop (`requests[].terminate_when:`) and ordinary `extract: [{to:
  state.*}]` writes. Cross-reference to `api-methods.md` §6.
- **`flow:` block** (R, U). Never shipped; ruled out in the design.
- **`TokenCache` / `RequestCache` as separate structs** (R, S, U).
  Replaced with one `Cache` struct, two sites
  (`auth.oauth2.<grant>.cache` and `requests[].cache`).
- **The supported-variants matrix** (R §8). Removed: `api-methods.md`
  is the variant catalogue; `runtime.md` describes execution
  primitives, not a capability ledger.
- **Scope lifetimes table** (R §3). Folded into `schema.md`'s lifetime
  inference rule and into the per-drain-wipe step in §2.

## Notes for downstream slices

Phase 2 (code and template rewrite) will touch every file these three
docs describe. To keep wording stable across the docs and the eventual
code-touch slices' `doc.go` / comment updates, the following choices
are pinned here:

- **Drain lifecycle steps.** Five named steps in §2: Load, Per-drain
  wipe, Pagination loop, Sink delivery, Progress, Commit. The deferred
  `Save` is part of "Commit". Phase 2 code comments describing the
  drain should use these step names verbatim.
- **Per-drain wipe phrasing.** "Per-drain state is wiped at the start
  of every drain — a drain that fails mid-page re-bootstraps
  pagination on the next start." This is the phrasing in both
  `runtime.md` §2 and `schema.md` §state, and `api-methods.md` §2's
  intro reuses it as well.
- **Progress timing phrasing.** "Progress fires once per accepted
  page-response, including empty pages." The "accepted" qualifier and
  the "including empty pages" clause are both load-bearing. The
  "accepted" definition is "status passed `expect_status:` and any
  `on_status:` action did not abort the drain." Phase 2 code comments
  on the progress evaluator must use this exact wording.
- **Loop names.** Two loops, both named: the **pagination loop**
  (driven by `pagination.<variant>.terminate_when:`) and the
  **request loop** (driven by `requests[].terminate_when:`). The
  per-variant `terminate_when:` default table in `runtime.md` §3 is
  the canonical reference for the pagination loop. Phase 2 code
  should not invent new loop names.
- **`on_status:` closed verb set.** Four verbs: `skip`, `fail`,
  `empty_events`, `invalidate_cache`. `retry` is intentionally
  absent. `runtime.md` §6's table is the canonical reference; the
  `invalidate_cache → degrades-to-empty_events` fallback (when no
  cache slot is reachable) is documented.
- **Cache namespace.** Process memory only; cleared on runner
  restart; never persisted; dropped wholesale by `invalidate_cache`.
  `runtime.md` §8 is the canonical reference. Phase 2 code comments
  on the cache must not call it "persistent" or "session-scoped".
- **`Snapshot` shape.** One field: `State map[string]any`. No
  `Cursor` field. Phase 2 `client/state.go` rewrite must align the
  struct to this. `stores.md` §2 is the canonical reference; the
  SQLite worked example shows the on-disk shape.
- **Fan-out reserved-root list.** Six entries: `state`, `cache`,
  `events`, `extract`, `steps`, `response`. `runtime.md` §12 and
  `api-methods.md` §3.10 both list these; Phase 2 validator code
  enforcing the rule should match.
- **CLI flag set.** The flag table in `usage.md` §1 names the canonical
  set (`-i`, `-c`, `--state`, `--once`, `--interval`, `--out`,
  `--trace`, `--http-timeout`, `--max-pages` for `run`; `-o`,
  `--force` for `init`; `-o` for `template show`; `-i` for
  `validate`). Phase 2 CLI-touch slices should not rename any of
  these.
- **Operator-facing diagnostic format.** "path: severity: message
  (file:line:col)" as shown in `usage.md` §11's diagnostics example.
  Phase 2 validator-touch slices should preserve this format.

## Validation

- Zero hits on the forbidden-strings ripgrep across all three new
  files: `cursor.`, `mutability`, `placeholder_event`, `initial.lookback`,
  `initial_offset`, `next_url_at`, `token_at`, `scroll_id_at`,
  `end_cursor_at`, `has_more_at`, `has_next_page_at`, `event_time.`,
  `defaults.base_url`, `latest_event_timestamp`, `max_event_field`,
  `async_job`, `flow:` (as a standalone block, word-boundary checked
  to allow "workflow" / "flowed" English usage), `progress.stateless`,
  `page_number`, `link_header`, `next_url_in_body`, `graphql_relay`,
  `TokenCache`, `RequestCache`.
- Zero hits on `migrat`, `backward`, `deprecat`, `legacy`,
  `slice [0-9]`, `phase [12]` (case-insensitive).
- Zero links to `DESIGN_SUGGESTIONS.md` or `schema-reference.md`.
- All `schema.md#<anchor>` and `api-methods.md#<anchor>` links resolve
  against the current schema and api-methods docs (verified by listing
  the emitted anchors and cross-checking each link).
- `runtime.md` §5's progress-evaluation section contains both the
  "accepted page-response" qualifier and the "including empty pages"
  clause, both load-bearing.
- `stores.md` §1's lifetime-inference table matches the rule in
  `schema.md §state` (operator-config / per-drain / persistent — only
  persistent + operator-config survive; per-drain is wiped at drain
  start; `cache.*` is process memory only).
- `usage.md` §3's walkthrough uses the named pagination variants from
  `api-methods.md` (`cursor_token`, `next_url`, `counter`, `custom`,
  `none`) and the recipe vocabulary from `api-methods.md` §5
  (`progress:` flat-list writes with `{max: [{ref: state.*}, {max:
  {ref: events.*.field}}]}`).
- `usage.md` §1's CLI flag tables match the actual flag set in
  `cmd/skopos/main.go` / `cmd_run.go` / `cmd_init.go` /
  `cmd_template.go` / `cmd_validate.go`.
