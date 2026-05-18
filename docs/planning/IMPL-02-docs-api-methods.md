# IMPL-02 — docs/api-methods.md rewrite

## Scope

`docs/api-methods.md` was rewritten end-to-end against the new schema
codified in `docs/schema.md`. Treated as a ground-up replacement, not a
patch: the old file's top-level organisation was structured around the
old pagination variant catalogue, the `cursor.*` namespace, the named
progress variants, and the async-job dispatcher — none of which exist
in the new schema. Every section was rebuilt against the new shape.

The new file is 1687 lines (vs. 1305 in the prior version). The growth
comes from new content rather than preserved legacy material — the
legacy material is gone. Specifically:

- Two end-to-end side-by-side recipes (§5.10) that assemble the pieces
  for "simple GET with timestamp progress" and "URL-from-body
  pagination with timestamp progress" — the two templates that §5 of
  the design discussion exercised. The new docs site doesn't link back
  to the design doc, so these recipes need to live here.
- A dedicated §6 for the async-job pattern (request-level loop) and a
  dedicated §7 for the two-loop primitives table — both promoted out
  of footnotes into top-level concerns.
- A new §3.1 on URL composition (string interpolation vs `{ref: ...}`
  with `default:`), introducing the two idioms before the request body
  shape sections.

Carries no link to `DESIGN_SUGGESTIONS.md` and no link to the generated
`schema-reference.md`. Links to `schema.md` (heavily), `runtime.md`,
`stores.md`, and `usage.md` (the latter three are still pre-rewrite
in their downstream slice, but the file paths are stable).

## Section map

Old sections → new sections.

| Old section                                              | New section                                                                                |
|----------------------------------------------------------|--------------------------------------------------------------------------------------------|
| §1.1–§1.7 auth variants                                  | §1.1–§1.7 (unchanged in topic; recipes use the new state-only ref vocabulary)              |
| §1.8 OAuth2 token caching (`store_in`, `expiry_field`, …)| §1.8 unified `Cache` block (one struct for both OAuth2 and step-level caches)              |
| §1.9 multi-mode auth                                     | §1.9 (unchanged; `default:` is now a bare `Auth` value)                                    |
| §1.10 session cookie via POST login                      | §1.10 (recipe rewritten against `to: extract.<name>` and absolute `url:`)                  |
| §1.11 HMAC/SigV4/OAuth1, §1.12 interactive grants        | §1.12 / §1.13 (kept as out-of-scope; new §1.11 records refresh-token-as-state-field)       |
| §2.1 none                                                | §2.1 (unchanged)                                                                           |
| §2.2 `page_number`, §2.3 `offset`                        | §2.4 `pagination.counter` (one variant covers both; `start:` and `step:` pick which)       |
| §2.4 `cursor_token`, §2.7 `scroll_id`, §2.8 `graphql_relay` | §2.2 `pagination.cursor_token` (one variant covers opaque tokens, scroll IDs, Relay end cursors, body-borne page numbers) |
| §2.5 `link_header`, §2.6 `next_url_in_body`              | §2.3 `pagination.next_url` (one variant covers both; `regex:`/`capture:` peel out the Link-header form) |
| (new)                                                    | §2.5 `pagination.custom` — escape hatch for APIs that don't fit the named variants         |
| §2.9 async-job polling                                   | §2.6 (cross-references §6); §5.5 async-job dispatcher replaced by §6 plain-requests recipe |
| §2.10 time-window pagination                             | §2.7 (cross-references §5.4)                                                               |
| §2.11 export+blob, §2.12 worklist/multi-phase            | §2.8, §2.9 (unchanged in disposition; cross-references `fan_out:` and out-of-scope buckets)|
| §2.13 wiring pagination signals                          | folded into the §2 intro + variant entries (every entry shows the explicit `{ref: state.<name>}` placement) |
| §3.1–§3.6 request body shapes                            | §3.2–§3.6 (recipes use `url:` interpolation; no more `path:`)                              |
| §3.7 if gating, §3.8 on_status                           | §3.11, §3.12 (unchanged in shape; on_status table is now a closed-verb table)              |
| §3.9 sequential chain                                    | §3.9 (rewritten; `extract` now uses `to: state.*` / `to: extract.*`)                       |
| §3.10 step-level cache                                   | §3.7 (rewritten against the unified `Cache` block; cross-references §1.8)                  |
| §3.11 fan-out                                            | §3.10 (unchanged in shape; reserved-root list updated to drop `cursor` and add `cache`/`events`) |
| (new)                                                    | §3.1 URL composition idioms; §3.8 dynamic headers; §3.13 `extract:` captures table         |
| §4.1–§4.4 response decode/parse                          | §4.1–§4.7 (split decoder selection + events_at from the array/object/ndjson recipes)       |
| §4.5 placeholder events                                  | §4.9 "empty pages are valid" — no synthetic event needed                                   |
| §4.6 ZIP/gzip/CSV                                        | §4.1 (kept as out-of-scope on the decoder section)                                         |
| (new)                                                    | §4.8 `events.*` namespace table                                                            |
| §5.1 stateless                                           | §5.1 (omit `progress:` or `progress: []`)                                                  |
| §5.2 time_window, §5.3 latest_event_timestamp, §5.4 max_event_field, §5.6 use_now | §5.2–§5.5 (flat-list recipes; one entry per recipe rather than one named variant per recipe) |
| §5.5 async_job dispatcher                                | §6 (full request-level-loop recipe; three plain requests with `terminate_when:` and ordinary extracts) |
| §5.7 multi-field cursor                                  | §5.9 (cross-iteration state in `state.*`; multi-field is just two `progress:` entries or two `extract.to: state.*` writes) |
| §5.8 ETag conditional fetch                              | §5.8 (rewritten against `state.etag` + `extract: [{to: state.etag, …}]`)                   |
| §5.9 state machine across iterations                     | folded into §6 + out-of-scope note in §7 (no escape hatch beyond two-loop shape)           |
| (new)                                                    | §5.6 first-write-wins via `ref` default; §5.7 per-page checkpointing on empty pages; §5.10 worked recipes |
| §6.1 token caching                                       | §8.1 (cross-references §1.8 and §3.7)                                                      |
| §6.2 cross-iteration state                               | §5.9 (folded into the progress section; `mutability` annotations gone)                     |
| §6.3 dual-mode behaviour                                 | §8.5                                                                                       |
| §6.4 secret redaction                                    | §8.2                                                                                       |
| §6.5 HTTP transport                                      | §8.3                                                                                       |
| §6.6 observability                                       | §8.4                                                                                       |
| (new)                                                    | §7 loop primitives — single table for both pagination and request loops                    |

## Removed content

Hard removals (with replacement noted where applicable):

- The `cursor.*` namespace and every reference to `cursor.<name>` —
  pagination writes go to `state.<name>` slots (per-drain by inference).
- `defaults.base_url` and the old `path:` field on requests — every
  request declares an absolute `url:` Value composed via string
  interpolation or `{concat: [...]}`.
- `placeholder_event` — empty pages are valid accepted pages; the loop
  controls itself.
- The named pagination variants `page_number`, `offset`, `link_header`,
  `next_url_in_body`, `scroll_id`, `graphql_relay`. Folded into
  `cursor_token` (opaque tokens, scroll IDs, Relay end cursors,
  body-borne page numbers), `next_url` (body- and Link-header URLs),
  and `counter` (page-number and offset, distinguished by
  `start:`/`step:`).
- The `*_at` field naming on every pagination variant (`token_at`,
  `scroll_id_at`, `end_cursor_at`, `has_more_at`, `has_next_page_at`,
  `next_url_at`). Replaced with the standard `from:` + `to:` shape.
- The named progress variants (`stateless`, `latest_event_timestamp`,
  `max_event_field`, `use_now`, `time_window`, `async_job`). Replaced
  by a flat list of `{to: state.<name>, from: <Value>}` entries.
- The async-job dispatcher (`progress.async_job` with submit / poll /
  fetch roles, `complete_when`, `on_complete.cursor_update`, the
  auto-injected `cursor.phase` state machine). Replaced by §6: three
  plain requests with `terminate_when:` on the poll step and ordinary
  `extract:` writes to author-declared `state.*` fields.
- The `flow:` block — never shipped; ruled out in the design.
- `TokenCache` / `RequestCache` as separate structs. They are one
  `Cache` block now, used by both `auth.oauth2.<grant>.cache` and
  `requests[].cache` (§1.8). Slot vocabulary: `cache.<name>` (process
  memory only).
- `mutability` annotations on state fields. Lifetime is inferred from
  write sites; no annotation is authored.
- `initial.lookback` / `initial_offset` / `state.fields` wrapper.
  First-run seeding is the `default:` on the destination state field;
  the wrapper is gone.
- The `extract` field's `name:` + `target:` shape. Replaced by `to:`
  with a namespace-rooted destination (`state.<name>` for persistent,
  `extract.<name>` for per-iteration); the destination's namespace
  decides persistence.
- The "wiring pagination signals into the request" cross-reference
  table. Wiring is shown inline in each pagination variant's recipe
  using explicit `{ref: state.<name>}` Values.
- The `progress.async_job.on_complete.cursor_update.kind` scalar.
  Replaced by ordinary `progress:` entries (`{now: true}`,
  `{max: ...}`, etc.) that fire after the fetch step.

## Notes for downstream slices

For Slice 3 (`runtime.md`, `stores.md`, `usage.md`) — vocabulary and
groupings that this slice settled on and that those docs should reuse
verbatim:

- **Loop names.** Two loops, both named: the **pagination loop**
  (driven by `pagination.<variant>.terminate_when:`) and the
  **request loop** (driven by `requests[].terminate_when:`). The §7
  table is the canonical reference; `runtime.md` should refer to these
  exact names when describing execution order. The phrase "request-level
  loop" is the natural long form for §6 (async-job) prose.
- **Async-job phrasing.** "Three plain requests with `terminate_when:`
  on the poll step and ordinary extracts to `state.*`." No "submit /
  poll / fetch roles" framing; no "dispatcher"; no "state machine".
  The submit and fetch steps fire once per drain iteration; only the
  poll step loops via `terminate_when:`.
- **Pagination variant coverage.** Each named variant covers more than
  one old shape — be explicit when prose mentions API patterns:
  `cursor_token` covers opaque tokens, scroll IDs, GraphQL Relay end
  cursors, body-borne page numbers; `next_url` covers body URLs and
  Link headers; `counter` covers page-number and offset (start/step).
  Do not introduce shorthand names for these sub-shapes; the four
  variants (plus `none` and `custom`) are the full taxonomy.
- **Cache vocabulary.** One `Cache` struct, two sites
  (`auth.oauth2.<grant>.cache` and `requests[].cache`). Both write
  into a `cache.<name>` slot; reads use `{ref: cache.<name>}`. The
  cache namespace is process-memory only and cleared on runner
  restart — `stores.md` should say so when describing the persistence
  contract.
- **Per-drain wipe.** Per-drain state fields (the `to:` destinations
  of any pagination variant) are wiped at the start of every drain.
  `runtime.md` should describe this in the drain-lifecycle section;
  the wording in this slice is "per-drain state is wiped at the start
  of every drain — a drain that fails mid-page re-bootstraps
  pagination on the next start."
- **Progress timing.** "Progress fires once per accepted page-response,
  including empty pages." The "accepted" qualifier is load-bearing:
  it means the status passed `expect_status:` and `on_status:` did not
  abort. `runtime.md` should use this exact phrasing.
- **URL composition.** Two idioms: string interpolation
  (`"${state.url}/path"`) for composed URLs; `{ref: ..., default: ...}`
  for "entire URL is read from per-drain state with a bootstrap
  fallback" (the canonical pagination-`next_url` shape). `usage.md`
  walkthroughs should prefer interpolation in examples that compose
  literals and refs.
- **`events.*` reference forms.** Use the §4.8 table verbatim
  (`events.*.field`, `events.first.field`, `events.last.field`,
  `events.<int>.field`, `events.count`). Do not invent shorthand.
- **`on_status:` closed verb set.** Four verbs only: `skip`, `fail`,
  `empty_events`, `invalidate_cache`. `retry` is intentionally absent.
  The §3.12 table is the canonical reference.
- **Fan-out reserved-root list.** `fan_out.as` must not collide with
  `state`, `cache`, `events`, `extract`, `steps`, or `response`. The
  old reserved list (`state`, `cursor`, `extract`, `steps`, `item`,
  `body`, `response`) is gone. `runtime.md` should match this list
  when documenting the fan-out binding.

## Validation

- Zero hits on the forbidden-strings ripgrep (`cursor.`, `mutability`,
  `placeholder_event`, `initial.lookback`, `initial_offset`,
  `next_url_at`, `token_at`, `scroll_id_at`, `end_cursor_at`,
  `has_more_at`, `has_next_page_at`, `event_time.`,
  `defaults.base_url`, `latest_event_timestamp`, `max_event_field`,
  `async_job`, `flow:`, `progress.stateless`, `page_number`,
  `link_header`, `next_url_in_body`, `graphql_relay`, `TokenCache`,
  `RequestCache`).
- Zero hits on `migrat`, `backward`, `deprecat`, `legacy`,
  `slice [0-9]`, `phase [12]` (case-insensitive).
- Zero links to `DESIGN_SUGGESTIONS.md` or `schema-reference.md`.
- All `schema.md#<anchor>` links resolve against the current
  `schema.md` (verified by listing emitted anchors).
- The two side-by-side recipes from the design discussion (the
  `bearer_simple` and `next_url_in_body` templates) are present and
  discoverable in §5.10.
- Per-pagination-variant recipes cover every shape the design's variant
  mapping table assigned (opaque cursor, scroll ID, GraphQL Relay end
  cursor; Link-header URL, body-borne URL; page-number counter, offset
  counter); a `custom:` escape-hatch recipe is also shown.
