# IMPL-01 — docs/schema.md rewrite

## Scope

`docs/schema.md` was rewritten end-to-end against
`DESIGN_SUGGESTIONS.md` §3 and §4. Treated as a ground-up replacement,
not a patch: the old file was structurally tied to the prior schema's
top-level shape (a `defaults:` block, a hidden inferred cursor
namespace, named progress / pagination variants for every wire shape)
and every section had to be rebuilt to match the new shape. The new
file is 1164 lines (vs. 1010 in the prior version); the growth comes
from new content (string-interpolation, `add` / `subtract` / `max` /
`min` / `first` / `last` / `count` / `regex` Value forms, the
`events.*` namespace table, the per-variant `pagination.terminate_when`
defaults, the `Cache` unification block) rather than from preserved
legacy material — the legacy material is gone.

The doc carries no link to `DESIGN_SUGGESTIONS.md` and no link to the
generated `schema-reference.md`. It cross-references `runtime.md`,
`api-methods.md`, `stores.md`, and `usage.md` (which are themselves
rewritten in downstream slices); these links are intentional and the
filenames are stable.

## Section map

Old section → new section.

| Old section                                              | New section                                                                                |
|----------------------------------------------------------|--------------------------------------------------------------------------------------------|
| `ir_version`                                             | `ir_version` (unchanged)                                                                   |
| `state` (with `.fields` wrapper, `mutability:` field)    | `state` (flat map, lifetime inferred from write sites)                                     |
| `defaults`                                               | **removed** — every request uses an absolute `url:`; URL composition is in the `requests` intro |
| `auth.none` / `bearer` / `basic` / `api_key` / `custom`  | same names; bodies unchanged                                                               |
| `auth.oauth2.<grant>` + `TokenCache`                     | `auth.oauth2.<grant>` + a unified `Cache` block under §auth                                |
| `auth.multi_mode`                                        | same; `default:` is now a bare `Auth` value (no `{auth: ...}` wrapper)                     |
| `requests` per-entry table                               | same shape; gains `terminate_when:`; loses `path:`; `url:` always required and absolute    |
| `ExtractVar` (had `target:`)                             | `ExtractVar` keyed by `to: state.* | extract.*`; no separate `target:` field               |
| `FanOut` / `RequestCache`                                | `FanOut` (unchanged); `RequestCache` merged into the shared `Cache` block                  |
| `response.placeholder_event`                             | **removed** — covered in §response's "An empty page is still a valid accepted page" rule   |
| `pagination.{cursor_token, page_number, offset, link_header, next_url_in_body, scroll_id, graphql_relay}` | collapsed to **`cursor_token`**, **`next_url`**, **`counter`**, plus **`custom`** and **`none`** |
| `progress.{stateless, latest_event_timestamp, max_event_field, use_now, time_window, async_job}` | collapsed to a flat list of `{to, from, ...}` writes; no named variants                    |
| `progress.async_job`                                     | **removed** — the submit / poll / fetch pattern is plain requests with `terminate_when:`   |
| `error`                                                  | unchanged                                                                                  |
| `Values` discriminated forms table                       | same forms + **string interpolation**, **`add`**, **`subtract`**, **`max`**, **`min`**, **`first`**, **`last`**, **`count`**, **`regex`**; `{now: ..., offset: ...}` dropped |
| `Paths` + namespace table                                | `Paths` + `Namespaces` table; **`cursor.*` root removed**; **`cache.*`** and **`events.*`** added |
| Cursor namespace (inferred) sub-table                    | **removed** — there is no cursor namespace                                                 |
| Type table                                               | new dedicated `Types` section (split out of the old `state` subsection); adds `timestamp`; drops `rfc3339` as a type |
| `Predicates`                                             | same; absent-tolerance is now a top-level rule; `pagination.*.terminate_when` and `requests[].terminate_when` are the only loop sites |
| `Design rules`                                           | same shape; "Cursor is fully inferred" replaced with "No hidden namespaces" and "Single source of lifetime" |

## Removed content

- `defaults:` block — every request declares an absolute `url:` Value.
  Template authors compose with `${state.url}/...` interpolation or
  `{concat: [...]}`.
- `state.<name>.mutability` annotation — lifetime is inferred from
  where the field appears as a write destination.
- The `cursor.*` namespace and its inferred-namespace table — every
  per-drain destination is `state.<name>` (with lifetime inferred from
  the write site). `cache.<name>` and `events.<name>` cover the other
  hidden-namespace cases.
- The named progress variants (`stateless`, `latest_event_timestamp`,
  `max_event_field`, `use_now`, `time_window`, `async_job`). Replaced
  by a flat list of `{to: state.<name>, from: <Value>}` entries. First-
  run seeding moves to the destination state field's `default:`.
- The named pagination variants `page_number`, `offset`, `link_header`,
  `next_url_in_body`, `scroll_id`, `graphql_relay`. Collapsed into
  `cursor_token` (server-issued token), `next_url` (server-issued URL),
  `counter` (client-incremented), plus `custom` and `none`.
- `*_at` field names on every pagination variant (`token_at`,
  `scroll_id_at`, `end_cursor_at`, `has_more_at`, `has_next_page_at`,
  `next_url_at`). Replaced with the standard `from:` + `to:` shape.
- `placeholder_event` — the new runtime keeps the page loop alive
  without a fabricated event.
- `progress.async_job` and the `cursor.phase` / `submit.extract` /
  `poll.extract` / `fetch.extract` apparatus. Replaced by plain
  requests with `terminate_when:` predicates and ordinary
  `extract: [{to: state.*}, ...]` writes.
- `event_time.path` sub-objects on progress variants. Replaced with
  reducer Values over `events.*.<field>` projections.
- `initial.lookback` / `initial_offset` first-run apparatus. Replaced
  by `default:` on the destination state field (which accepts the full
  Value language, including `{subtract: [{now: true}, "720h"]}`).
- `type: rfc3339` field type (from an earlier draft). Replaced with
  `type: timestamp` + `format: rfc3339` (or any other format verb / Go
  layout string).
- `{now: true, offset: ...}` Value form. Replaced with
  `{subtract: [{now: true}, <duration>]}` and
  `{add: [{now: true}, <duration>]}`.
- `flow:` block (never shipped; ruled out in the design doc).

## Notes for downstream slices

These are decisions or word choices in `schema.md` that Slices 2 and 3
should reuse rather than re-derive.

- **Pagination variant names.** The three named variants are
  **`cursor_token`**, **`next_url`**, **`counter`** plus **`custom`**
  and **`none`**. Other docs (`api-methods.md` template recipes,
  `runtime.md` execution-order sections, `usage.md` walkthroughs) must
  use these exact names. The old names (`page_number`, `offset`,
  `link_header`, `next_url_in_body`, `scroll_id`, `graphql_relay`) do
  not appear anywhere in the new schema; templates that previously
  used them are described by selecting the right named variant
  (`counter` for both `page_number` and `offset` — `start:` / `step:`
  pick which; `cursor_token` for all opaque-token forms including
  scroll IDs and GraphQL Relay; `next_url` for both body- and Link-
  header URLs).
- **Per-drain vs persistent vocabulary.** The schema doc calls them
  "per-drain scratch" and "persistent" state fields, with "operator
  config" as the third sub-flavour. Downstream docs should keep these
  three terms.
- **Loop terminology.** The two loops are the **pagination loop** (one
  request, many pages, controlled by `pagination.*.terminate_when:`)
  and the **request loop** (one request fired repeatedly via
  `requests[].terminate_when:`, used for async-poll patterns).
  Downstream docs should not invent new names for either.
- **Cache slot vocabulary.** Both auth-grant caches and step-level
  request caches use the same `Cache` struct and write into the
  `cache.<name>` namespace. There is one struct, not two; downstream
  docs should reference `Cache` (capital C) by that one name.
- **Namespace roots.** `state.*`, `cache.*`, `events.*`, `extract.*`,
  `steps.<id>.body.*`, `steps.<id>.header.*`, `response.body.*`,
  `response.header.*`, `<fan_out.as>.*`. No other roots exist. In
  particular `cursor.*` is gone — any downstream prose that says
  "the runner writes the cursor to ..." should be rewritten to "the
  runner writes the per-drain state field `state.<name>`".
- **Progress evaluation timing.** Progress fires **once per accepted
  page-response**, including empty pages. Downstream docs explaining
  the runtime should state this explicitly; it is not "at end of
  drain" anymore.
- **Async-job pattern wording.** When a downstream doc has to describe
  the async-job pattern, it describes it as three plain requests with
  `terminate_when:` on the poll step and ordinary extracts to
  `state.*`. No "phase machine"; no "submit / poll / fetch roles".
- **`events.*` projections.** `events.*.field` for the projected list;
  `events.first.field` / `events.last.field` for the two declared-order
  shortcuts; `events.<int>.field` for positional; `events.count` for
  cardinality. Downstream prose should use these forms verbatim.
- **`format:` accepts Go layout strings.** This is in addition to the
  closed-set verbs (`string`, `int`, `bool`, `rfc3339`, `rfc3339nano`,
  `unix_seconds`, `unix_millis`, `duration`, `url_encode`,
  `parse_duration`). State-field-declared `format:` is the preferred
  spot for non-default timestamp shapes; ad-hoc Value-time `format:`
  is the escape hatch.
- **String interpolation has explicit defaults via `|`.** The form is
  `${path|default-as-yaml-scalar}`. Downstream examples should prefer
  interpolation over `{concat: [...]}` when the result is composed of
  literals and refs only — the schema doc consistently uses
  interpolation in its examples.

## Validation

- Zero hits on the forbidden-strings ripgrep (`cursor.`, `mutability`,
  `placeholder_event`, `initial.lookback`, `initial_offset`,
  `next_url_at`, `token_at`, `scroll_id_at`, `end_cursor_at`,
  `has_more_at`, `has_next_page_at`, `event_time.`,
  `defaults.base_url`, `latest_event_timestamp`, `async_job`, `flow:`,
  `progress.stateless`, `DESIGN_SUGGESTIONS`, `schema-reference`).
- Zero hits on `migrat`, `backward`, `deprecat`, `legacy`,
  `slice [0-9]`, `phase [12]`.
- Spot-checked against `DESIGN_SUGGESTIONS.md` §5.1
  (`bearer_simple.yml` proposed template): every field used in that
  template (`state.<name>.default` with a `{subtract: [{now: true},
  "720h"]}` Value, `auth.bearer.token`, request `url:` interpolation,
  `query:` interpolation, `response.decode` + `events_at`,
  `pagination.none`, `progress` flat-list with `{max: [{ref:
  state.last_timestamp}, {max: {ref: events.*.timestamp}}]}`,
  `error.mode`) is described in the rewritten `schema.md`.
- No links to `DESIGN_SUGGESTIONS.md` or `schema-reference.md`.
