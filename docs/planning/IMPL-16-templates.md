# IMPL-16 — `templates/*.yml` + `templates/templates.go` + `schema/testdata/*.yml`

**Scope.** Rewrite every bundled spec template (27 files) and every schema
test fixture (20 files) against the post-redesign IR. No code, no test, no
docs touched outside of the YAML payloads and this artefact. The
`templates/templates.go` embed wrapper carries no shape-coupling and
needs no change.

---

## Build state

- `go build ./...` — green.
- `skopos validate -i <each>` — every template and every test fixture
  emits zero error-severity diagnostics.
- YAML codec round-trip — every rewritten YAML parses, marshals,
  re-parses, and re-marshals byte-identically; the parsed `*schema.Doc`
  is `reflect.DeepEqual` across both decode passes.
- `*_test.go` files remain red against the old shape (Slice 17 owns the
  test rewrite per global rule #6).

The throwaway smoke check that drove the above (a tiny `main.go`
iterating `templates.Names()` + `schema/testdata/*.yml` and running
`schema.Parse` + `schema.Validate` + YAML round-trip) was deleted before
slice close.

---

## Mechanical changes applied uniformly

Every rewritten YAML follows the same recipe (per the handoff checklist
and `docs/schema.md`):

1. **`state:` flattens.** `state.fields.<name>:` → `state.<name>:`. The
   single intermediate key disappears.
2. **`state.initial_interval` removed.** First-run seeding moves to a
   `default: {subtract: [{now: true}, "720h"]}` on the destination state
   field (typically `state.last_timestamp`). The 24h-window fixtures
   (`format_verbs`, `max_event_field`, `on_status_dispatch`,
   `lookback_progress`) use `"24h"`.
3. **`defaults:` block removed.** Every `requests[].url:` is now an
   absolute URL Value composed via string interpolation
   (`"${state.url}/path"`) or `{ref: state.<slot>, default: "..."}` for
   pagination's `next_url` variant.
4. **`cursor.<x>` renamed to `state.<x>` everywhere.** Every former
   cursor namespace member is now declared under the top-level `state:`
   block (lifetime inferred from write sites — pagination targets
   per-drain, progress targets persistent).
5. **Pagination variants collapsed** per `docs/schema.md` §3.6:
   - `cursor_token`, `scroll_id`, `graphql_relay`,
     page-number-from-body → **`cursor_token`** with `from:` / `to:` /
     optional `terminate_when:`.
   - `link_header`, `next_url_in_body` → **`next_url`** with `from:` /
     `to:` / optional `regex:` + `capture:`.
   - `page_number`, `offset` → **`counter`** with `start:` / `step:` and
     (when the server gives a has-more flag) an explicit
     `terminate_when:`.
6. **`progress:` rewritten as a flat list of `{to, from}` writes.**
   `latest_event_timestamp` becomes a single
   `{max: [{ref: state.last_timestamp}, {max: {ref: events.*.<field>}}]}`
   write; `use_now` becomes `{subtract: [{now: true}, "5m"]}`;
   `time_window` becomes two writes that slide
   `state.window_start` / `state.window_end`; `max_event_field` becomes
   the same max-merge shape as `latest_event_timestamp` since the new IR
   does not need a separate "no since filter" variant; `stateless`
   becomes `progress: []`.
7. **`async_job:` progress replaced by a three-request chain.** The
   `terminate_when:` predicate on the poll step is the request-level
   loop primitive. Phase information lives in plain `state.export_id`
   and `state.result_url`, written by ordinary `extract:` blocks on
   submit and poll. The fetch step carries `produces_events: true` and
   is the implicit producer.
8. **Token caching writes to `cache.<name>` via the unified `Cache`
   block.** `store_in: token` / `expiry_field` / `expiry_buffer` →
   `to: cache.access_token` / `expires_at: {ref: response.body.expires_in, default: "1h"}` / `buffer: 60s`.
9. **ETag conditional templates persist the etag in `state.etag` via
   `extract:`.** `etag_conditional` now sends `If-None-Match:
   {ref: state.etag, default: ""}`, captures `response.header.ETag`
   into `state.etag`, and treats 304 as `on_status: skip`. Progress
   writes `state.last_seen_at` to `{now: true}` so an unchanged feed
   still records a heartbeat.
10. **`placeholder_event:` dropped.** The IR has no such field; empty
    pages are valid accepted page-responses with no synthetic event.
11. **`state.fields`-keyed `mutability:` markers removed.** Lifetime is
    inferred from the write site at validate time.
12. **`requests[].path:` → `url:`.** Every step carries an absolute URL
    Value.
13. **Closed `on_status:` verb set.** `skip | fail | empty_events |
    invalidate_cache`. No `retry` verb, no other open keys.
14. **`extract[].target:` removed.** The destination's namespace prefix
    (`state.<name>` vs `extract.<name>`) decides persistence; there is
    no separate `target:` field.

---

## Per-template rewrite log

| Template                          | Old shape                                                                   | New shape                                                                                                                                  | Notes |
|-----------------------------------|-----------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------|-------|
| api_key_auth                      | api_key header, none, latest_event_timestamp                                | api_key header, none, max-merge progress on `state.last_timestamp`                                                                          |       |
| async_poll                        | bearer, none, async_job (use_now)                                           | bearer, none, three-request submit/poll/fetch chain; progress: `from: {now: true}`                                                          | async |
| async_poll_latest_ts              | bearer, none, async_job (latest_event_timestamp)                            | bearer, none, three-request chain; progress: max-merge over `events.*.timestamp`                                                            | async |
| async_poll_stateless              | bearer, none, async_job (stateless)                                         | bearer, none, three-request chain; `progress: []`                                                                                            | async |
| basic_auth                        | basic, none, stateless                                                      | basic, none, `progress: []`                                                                                                                  |       |
| bearer_simple                     | bearer, none, latest_event_timestamp                                        | bearer, none, max-merge progress on `state.last_timestamp`                                                                                   |       |
| cursor_token                      | bearer, cursor_token, latest_event_timestamp                                | bearer, cursor_token (`from: response.body.next_cursor`, `to: state.next_token`); max-merge progress over `events.*.created_at`              |       |
| custom_auth                       | custom, none, latest_event_timestamp                                        | custom, none, max-merge progress                                                                                                              |       |
| etag_conditional                  | none, page_number, stateless; on_status 304:skip                            | none, counter (start:1, step:1, has_next terminate_when); extracts `response.header.ETag` → `state.etag`; rides `If-None-Match`; progress writes `state.last_seen_at: {now: true}` | etag |
| etag_conditional_middle           | 3-step chain w/ middle if + 304:skip; select branches build the events path | Same shape; the events `url:` Value `select.branches` builds an absolute URL via `concat: [{ref: state.url}, ...]` for the present arm, with an absolute interpolated string for the default | etag |
| fanout                            | list + fan_out detail; latest_event_timestamp                               | Same chain; max-merge progress; URLs absolute via interpolation                                                                              | fanout |
| link_header                       | api_key, link_header (regex implicit), latest_event_timestamp               | api_key, next_url with explicit `regex: '<(.*?)>;\s*rel="next"'` + `capture: 1`; max-merge progress                                          |       |
| multi_mode_auth                   | multi_mode (bearer / api_key, default custom); latest_event_timestamp       | multi_mode default is now a bare Auth (was a wrapped `{auth: ...}`); max-merge progress                                                       |       |
| ndjson_response                   | bearer, none, stateless                                                     | bearer, none, `progress: []`; `decode: ndjson`, `events_at: ""`                                                                              |       |
| next_url_in_body                  | bearer, next_url_in_body, latest_event_timestamp                            | bearer, next_url (`from: response.body.meta.next_page`, `to: state.next_url`); request `url:` reads back with bootstrap default              |       |
| oauth2_client_credentials         | oauth2 client_credentials + cache.store_in:token; cursor_token; lat_ts      | OAuth2 grant writes `cache.access_token` via the unified Cache block (`expires_at: {ref: response.body.expires_in, default: "1h"}`, `buffer: 60s`); cursor_token pagination; max-merge progress | oauth2 |
| oauth2_password_grant             | oauth2 password_grant + cache.store_in:token; none; lat_ts                  | Password grant writes `cache.access_token`; none pagination; max-merge progress                                                              | oauth2 |
| oauth2_relay                      | oauth2 cc + cache; graphql_relay; latest_event_timestamp                    | OAuth2 cache via unified Cache; cursor_token with `from: response.body.data.issues.pageInfo.endCursor`, `to: state.after`, `terminate_when: hasNextPage == false`; max-merge progress | oauth2 |
| offset_pagination                 | bearer, offset (offset_param + batch_size); stateless                       | bearer, counter (`start: 0`, `step: {ref: state.page_size}`); `progress: []`; the `end:` query is computed as `{add: [{ref: state.offset, default: 0}, {ref: state.page_size}]}` |       |
| page_number                       | bearer, page_number (has_more_at); latest_event_timestamp                   | bearer, counter (`start: 1`, `step: 1`, explicit `terminate_when: {not: {present: response.body.meta.has_next}}`); max-merge progress         |       |
| post_form_body                    | bearer, none, latest_event_timestamp                                        | bearer, none, max-merge progress; `body.form` carries since / limit / format                                                                  |       |
| post_json_body                    | bearer, offset, time_window                                                 | bearer, counter (`start: 0`, `step: {ref: state.page_size}`); progress is a two-entry sliding window writing `state.window_start` and `state.window_end`; body computes `search_to` via `{add: [{ref: state.search_from, default: 0}, {ref: state.page_size}]}` |       |
| post_raw_body                     | bearer, none, stateless                                                     | bearer, none, `progress: []`; `body.raw` is a string interpolation `'{"tenant":"${state.tenant_id}","event":"hello"}'` (the old `{concat: [...]}` form is no longer needed once interpolation handles it) |       |
| scroll_id                         | bearer, scroll_id (complete_when); latest_event_timestamp                   | bearer, cursor_token (`from: response.body.request_metadata.scroll`, `to: state.scroll_id`, `terminate_when: complete == "true"`); max-merge progress |       |
| session_cookie                    | none, 2-step login + data with extract.session_cookie; stateless            | Same chain shape; extract now writes `extract.session_cookie`; `progress: []`                                                                  |       |
| session_login_cached              | bearer-from-state.session_token; requests[].cache (store_in)                | bearer reads `{ref: cache.session_token.token, default: "pending"}`; login step's `cache:` writes the decoded body to `cache.session_token`    | cache |
| simple_get_object                 | none, none, stateless                                                       | none, none, `progress: []`                                                                                                                    |       |

### Test fixtures (`schema/testdata/`)

| Fixture                           | Old shape                                                                   | New shape                                                                                                                                  | Notes |
|-----------------------------------|-----------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------|-------|
| api_key_query                     | api_key in_query; latest_event_timestamp                                    | api_key in_query; max-merge progress                                                                                                          |       |
| async_poll_latest_ts              | async_job latest_event_timestamp                                            | submit/poll/fetch chain; max-merge progress                                                                                                  | async |
| async_poll_stateless              | async_job stateless                                                         | submit/poll/fetch chain; `progress: []`                                                                                                       | async |
| cursor_token_placeholder          | cursor_token + `placeholder_event:` (now removed)                           | Cursor_token fixture pinning empty-page progress writes — `state.last_server_cursor` advances from `response.body.next_cursor` on every accepted page. Replaces the placeholder-event role. | repurposed |
| dual_mode_url_commercial          | bearer, none, latest_event_timestamp; select branches over `state.url`      | Same shape; select `value:` branches now use absolute interpolated URLs (`"${state.url}/api/v1/events-gov"`)                                  |       |
| dual_mode_url_gov                 | sibling of above (state.gov_cloud=true)                                     | sibling of above                                                                                                                              |       |
| etag_conditional_middle           | 3-step chain w/ middle if + 304:skip; select.branches builds events path    | Same shape; events URL select uses `concat: [{ref: state.url}, {ref: steps.middle.body.next_path}]` for the present arm                       | etag |
| fanout                            | list + fan_out detail; latest_event_timestamp                               | Same chain; URLs absolute via interpolation; max-merge progress                                                                              | fanout |
| format_verbs                      | every closed format verb; latest_event_timestamp w/ initial.lookback        | Same verbs; max-merge progress; default lookback 24h on `state.last_timestamp`                                                               |       |
| lookback_progress                 | latest_event_timestamp + per-iteration lookback "-30s"                      | progress is `{subtract: [{max: [..., {max: {ref: events.*.timestamp}}]}, "30s"]}` — explicit subtract over the max-merge                       |       |
| max_event_field                   | max_event_field; initial.lookback 24h                                       | max-merge progress (the new IR collapses max_event_field into the generic high-water shape)                                                  |       |
| multi_field_cursor                | extract.target: cursor (multiple writes); latest_event_timestamp            | Two persistent extracts target `state.next_partition_id` (with `default: 0` declared on the field) and `state.freeze_flag`; max-merge progress |       |
| oauth2_password_grant             | oauth2 password_grant + cache.store_in:token                                | Password grant + unified Cache to `cache.access_token`                                                                                       | oauth2 |
| oauth2_relay                      | oauth2 cc + cache; graphql_relay                                            | OAuth2 cache via unified Cache; cursor_token with hasNextPage terminate_when                                                                  | oauth2 |
| on_status_dispatch                | bearer, none, latest_event_timestamp + initial.lookback 24h; on_status 429/502 | Same on_status; max-merge progress with 24h-default `state.last_timestamp`                                                                |       |
| predicate_composition             | scroll_id pagination; and/or/not/lt/lte/gt/gte/literal_bool exercised        | cursor_token pagination with the same predicate tree under `terminate_when:`; if-predicate on the request unchanged in shape; `progress: []` |       |
| token_cache                       | oauth2 cc + cache + cursor_token                                            | OAuth2 cache via unified Cache; cursor_token pagination; max-merge progress                                                                  | oauth2 |
| token_cache_multistep             | oauth2 cc + cache + page_number + fan_out                                   | OAuth2 cache via unified Cache; counter (start:1, step: {ref: state.batch_size}) pagination; list + fan_out detail; `progress: []`           | oauth2 |
| use_now                           | progress.use_now lookback "5m"                                              | progress writes `{subtract: [{now: true}, "5m"]}` on every accepted page                                                                      |       |
| value_forms                       | base64, list, `{now: true, offset: "-24h"}`, literal_int                    | Same Value forms minus the rejected `{now: true, offset:}` sibling — replaced by `{subtract: [{now: true}, "24h"]}` (the documented form)    |       |

---

## Removed-content list

Every dropped feature, with the file(s) it appeared in. None of these
survive the rewrite; the validator rejects them all.

- `state.fields:` indirection — every file.
- `state.initial_interval` declarations — every template carrying a
  timestamp cursor.
- `defaults: {base_url: ...}` blocks — every file.
- `cursor.<x>` namespace references and `extract[].target: cursor`
  directives — every file that tracked drain-spanning state.
- `progress.latest_event_timestamp` — bearer_simple, api_key_auth,
  cursor_token, custom_auth, link_header, multi_mode_auth,
  next_url_in_body, oauth2_*, post_form_body, scroll_id,
  session_login_cached, page_number, fanout, multi_field_cursor,
  api_key_query, format_verbs, dual_mode_url_*, on_status_dispatch.
- `progress.use_now` — async_poll, use_now.
- `progress.max_event_field` — max_event_field.
- `progress.time_window` (with `initial_offset` + `format`) —
  post_json_body.
- `progress.stateless` — basic_auth, ndjson_response, simple_get_object,
  post_raw_body, etag_conditional_middle (template + fixture),
  predicate_composition, session_cookie, token_cache_multistep,
  value_forms.
- `progress.async_job:` (submit / poll / fetch / on_complete /
  cursor_update) — async_poll, async_poll_latest_ts,
  async_poll_stateless (template + fixture).
- `pagination.page_number` (with `page_param`, `has_more_at`,
  `batch_size`) — etag_conditional, page_number, token_cache_multistep.
- `pagination.offset` (with `offset_param`, `batch_size`) —
  offset_pagination, post_json_body.
- `pagination.scroll_id` (with `scroll_id_at`, `complete_when`) —
  scroll_id, predicate_composition.
- `pagination.link_header` — link_header.
- `pagination.next_url_in_body` (with `next_url_at`) — next_url_in_body.
- `pagination.graphql_relay` (with `has_next_page_at`, `end_cursor_at`,
  `cursor_var`) — oauth2_relay (template + fixture).
- `pagination.cursor_token.token_at:` — cursor_token, oauth2_*,
  token_cache.
- `requests[].path:` — every file.
- `requests[].extract[].name:` / `.target:` — every file with extracts.
- `requests[].kind: async_job` and the async-job step descriptors —
  async_poll, async_poll_latest_ts, async_poll_stateless.
- `requests[].cache: {store_in, expiry_field, expiry_buffer,
  expiry_format}` — session_login_cached.
- `auth.oauth2.<grant>.cache: {store_in, expiry_field, expiry_buffer}` —
  oauth2_client_credentials, oauth2_password_grant, oauth2_relay,
  token_cache, token_cache_multistep.
- `auth.multi_mode.default.auth:` wrapper — multi_mode_auth.
- `response.placeholder_event:` — cursor_token_placeholder.
- `{now: true, offset: "-Xh"}` sibling form — value_forms.

---

## Walkthrough verifications

Three of the trickier reshapes, walked end-to-end to confirm the new
shape behaves as the design doc describes.

### async_poll — submit / poll / fetch via request-level loop

The old shape buried the three phases inside
`progress.async_job: {submit, poll, fetch, on_complete}` and the runner
ran a hand-coded phase machine. The new shape is three plain request
steps:

- `submit` (POST → 202) carries `extract: [{to: state.export_id, from: response.body.export_id}]`.
- `poll` (GET) carries `terminate_when: {and: [{present: response.body.status}, {eq: {path: response.body.status, value: complete}}]}` plus `extract: [{to: state.result_url, from: response.body.result_url}]`. The runner's request-level loop re-fires this step until the predicate evaluates true.
- `fetch` (GET, `url: {ref: state.result_url}`, `produces_events: true`) is the events producer.

The `url:` for the poll step is built with string interpolation:
`"${state.url}/async_poll/exports/${state.export_id}/status"` — the
ref segment is the export id extracted by the submit step. No phase
machine; no role discriminator; phase data lives in `state.export_id`
and `state.result_url` as plain extracts. progress writes a single
`{to: state.last_timestamp, from: {now: true}}` so the next drain runs
against a fresh time window.

### oauth2_client_credentials — token caching via the unified Cache block

The old shape carried
`auth.oauth2.client_credentials.cache: {store_in: token, expiry_field:
response.body.expires_in, expiry_buffer: 60s}` and the runner wrote the
token into `state.token` (an auto-registered framework slot). The new
shape uses the unified `Cache` struct shared with `requests[].cache`:

```yaml
auth:
  oauth2:
    client_credentials:
      ...
      cache:
        to:         cache.access_token
        expires_at: {ref: response.body.expires_in, default: "1h"}
        buffer:     60s
```

`cache.access_token` is a process-memory slot (cleared on runner restart,
never persisted — see `docs/stores.md`). The OAuth2 dispatcher writes
the access token into the slot and injects `Authorization: Bearer
<token>` on every subsequent request; the cached token is reused until
`now() + buffer >= expires_at`. The `{default: "1h"}` on `expires_at`
handles APIs that omit an `expires_in` field. No state slot is
auto-registered any more — the slot is named explicitly by the author.

### fanout — fan-out with absolute URLs

The list step's URL is `"${state.url}/fanout/incidents"`. The detail
step's URL is `"${state.url}/fanout/incidents/${incident.id}/events"`
— string interpolation handles both the operator-config root and the
per-item binding. `fan_out.as: incident` registers the binding name; the
validator rejects collisions with `state|cache|events|extract|steps|
response` (none of which equal `incident`). `events_at: ""` reads each
element of the merged list as one event. Progress writes a max-merge
over `events.*.timestamp` so `state.last_timestamp` advances after the
fan-out events are delivered.

---

## Downstream notes

- **Slice 17 — tests.** `schema/fixtures_test.go` (the existing
  TestSpecFixtures suite that walks both `templates/` and
  `schema/testdata/`) is the natural home for the validate + round-trip
  assertions baked into this slice's smoke check. Slice 17 will widen
  it with the new validator's full diagnostic surface and consolidate
  the legacy per-form unit tests into golden fixtures. The
  `internal/testserver/` fakes need URL-shape updates for every
  template that changed endpoint path (most kept the original paths;
  the etag templates pulled the `/etag_conditional/data` and
  `/etag_conditional_middle/data` endpoints).
- **Slice 18 — schema-reference regeneration.** No template-shape
  decision spills into the schema-reference document; the regenerator
  walks `*schema.Doc` types directly. The variant catalogue in
  `docs/api-methods.md` already enumerates the post-redesign shapes;
  the regenerated `docs/schema-reference.md` should match it
  type-for-type.
- **No template additions or removals.** The bundled set still has 27
  files (the handoff list). `cursor_token_placeholder.yml` survived
  with a new role (empty-page progress fixture) — the
  placeholder-event-rejection role disappeared with the field. The
  test fixture set still has 20 files.

---

## File-by-file walk

The mechanical part of each rewrite ran through the same checklist; the
walk below captures only the per-file deviations and quirks.

- **api_key_auth.yml** — vanilla rewrite.
- **async_poll.yml** — see [§Walkthrough verifications](#walkthrough-verifications).
- **async_poll_latest_ts.yml** — sibling of async_poll with the
  max-merge progress write replacing `{now: true}`.
- **async_poll_stateless.yml** — sibling with `progress: []` (no
  high-water mark persisted).
- **basic_auth.yml** — drops `initial_interval` + `page_size`
  (unused); empty progress.
- **bearer_simple.yml** — the canonical bearer + max-merge progress
  template; matches the recipe in `docs/api-methods.md` §5.10.
- **cursor_token.yml** — declares `state.next_token` (per-drain
  inferred from the pagination write) plus `state.last_timestamp`
  (persistent inferred from progress).
- **custom_auth.yml** — single custom header; max-merge progress.
- **etag_conditional.yml** — see [§Mechanical changes](#mechanical-changes-applied-uniformly) item 9.
- **etag_conditional_middle.yml** — select.branches build absolute
  URLs via `concat`.
- **fanout.yml** — see [§Walkthrough verifications](#walkthrough-verifications).
- **link_header.yml** — explicit regex + capture index.
- **multi_mode_auth.yml** — default arm is a bare `Auth` (was
  `{auth: <variant>}` wrapper).
- **ndjson_response.yml** — empty progress.
- **next_url_in_body.yml** — request `url:` uses
  `{ref: state.next_url, default: "${state.url}/next_url_in_body/alerts"}`.
- **oauth2_client_credentials.yml** — see [§Walkthrough verifications](#walkthrough-verifications).
- **oauth2_password_grant.yml** — password grant + same cache shape.
- **oauth2_relay.yml** — explicit terminate_when on
  `pageInfo.hasNextPage`.
- **offset_pagination.yml** — counter with `step: {ref: state.page_size}`;
  `end:` query computed at request time via `{add: [...]}`.
- **page_number.yml** — counter with explicit has_next terminate_when.
- **post_form_body.yml** — body.form variant; max-merge progress.
- **post_json_body.yml** — sliding window via two progress writes; the
  request body's `search_to` is `{add: [{ref: state.search_from,
  default: 0}, {ref: state.page_size}]}` evaluated at body-resolve time
  (before counter advance).
- **post_raw_body.yml** — `body.raw` is a single interpolated string
  (the old `{concat: [...]}` form is no longer necessary).
- **scroll_id.yml** — cursor_token with the original complete=true
  terminate predicate.
- **session_cookie.yml** — 2-step chain, `extract.session_cookie`
  per-iteration scratch.
- **session_login_cached.yml** — `requests[].cache.to:
  cache.session_token`; bearer reads
  `{ref: cache.session_token.token, default: "pending"}` (walks into
  the cached response body).
- **simple_get_object.yml** — minimal: no state beyond `url`, no
  pagination, no progress.

- **schema/testdata/api_key_query.yml** — in_query: true; max-merge.
- **schema/testdata/async_poll_latest_ts.yml** — sibling of the async
  templates above; absolute URLs.
- **schema/testdata/async_poll_stateless.yml** — sibling without
  progress.
- **schema/testdata/cursor_token_placeholder.yml** — repurposed (see
  rewrite-log).
- **schema/testdata/dual_mode_url_commercial.yml** /
  **dual_mode_url_gov.yml** — twins differing only by `state.gov_cloud`.
- **schema/testdata/etag_conditional_middle.yml** — sibling of the
  etag template.
- **schema/testdata/fanout.yml** — validator-surface fixture; absolute
  URLs.
- **schema/testdata/format_verbs.yml** — every closed format verb plus
  Go layout fallback (covered via `format: rfc3339` etc.).
- **schema/testdata/lookback_progress.yml** — explicit subtract over
  the max-merge.
- **schema/testdata/max_event_field.yml** — collapses into the generic
  max-merge progress write.
- **schema/testdata/multi_field_cursor.yml** — two persistent extracts
  + max-merge.
- **schema/testdata/oauth2_password_grant.yml** — cache via
  `cache.access_token`.
- **schema/testdata/oauth2_relay.yml** — explicit terminate_when on
  hasNextPage.
- **schema/testdata/on_status_dispatch.yml** — `429: empty_events` +
  `502: fail`.
- **schema/testdata/predicate_composition.yml** — every predicate verb
  exercised under `terminate_when:` of a cursor_token pagination
  block.
- **schema/testdata/token_cache.yml** /
  **token_cache_multistep.yml** — OAuth2 cache + cursor_token /
  counter pagination, the latter with fan_out.
- **schema/testdata/use_now.yml** — `{subtract: [{now: true}, "5m"]}`.
- **schema/testdata/value_forms.yml** — every uncommon Value form
  (base64, list, literal_int, subtract over now).
