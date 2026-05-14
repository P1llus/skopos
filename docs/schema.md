# Schema reference

Per-field lookup for the YAML spec the runner consumes — every field,
every rule, every namespace, every Value form. Grep this file by field
name. For runtime behaviour see [`runtime.md`](runtime.md); for the
catalogue of vendor patterns see [`api-methods.md`](api-methods.md).

> **Looking for the bare Go-struct → YAML mapping?**
> [`schema-reference.md`](schema-reference.md) is a generated sidecar
> listing every exported field on every struct in `schema/` with its
> YAML name, Go type, optionality, and one-line description. This
> document carries the prose — authoring rules, namespace tables,
> Value forms, examples, design rules — that the generator cannot
> derive from Go types alone.

A spec is one YAML (or JSON) document with these top-level keys:

| Key            | Required | Section                            |
|----------------|----------|------------------------------------|
| `ir_version`   | yes      | [#ir_version](#ir_version)         |
| `state`        | no       | [#state](#state)                   |
| `defaults`     | no       | [#defaults](#defaults)             |
| `auth`         | yes      | [#auth](#auth)                     |
| `requests`     | yes      | [#requests](#requests)             |
| `response`     | yes      | [#response](#response)             |
| `pagination`   | yes      | [#pagination](#pagination)         |
| `progress`     | yes      | [#progress](#progress)             |
| `error`        | no       | [#error](#error)                   |

Cross-cutting:

- [Values](#values) — the universal dynamic-field type used everywhere
  a string, number, or boolean could appear.
- [Paths](#paths) — dotted-string references into the decoded body /
  namespaces (`state`, `cursor`, `extract`, `steps`, `item`, `body`).
- [Predicates](#predicates) — boolean expressions for `if:`,
  `complete_when:`, `multi_mode.branches[].when:`,
  `Value.select.branches[].when:`.
- [Design rules](#design-rules) — invariants that hold across the
  whole schema.

---

## `ir_version`

The wire-format identifier. Must be the string `"1"`; the validator
rejects any other value.

```yaml
ir_version: "1"
```

`ir_version` is a wire-format identifier, not a release version. It
bumps when the wire shape changes, not when a release is cut. Additive
changes (new optional fields, new union variants) do not require a
bump.

---

## `state`

Typed field declarations for operator-supplied configuration (URLs, API
keys, durations) and runtime-mutated state slots that authors name
explicitly. Optional — omit if the spec has no operator input.

```yaml
state:
  fields:
    url:           {type: url,      default: "https://api.example.com"}
    api_key:       {type: secret}
    page_size:     {type: int,      default: 100}
    poll_interval: {type: duration, default: "30s"}
    region:        {type: enum,     values: [us, eu, ap], default: us}
```

### Field shape

| Field        | Required | Description                                                                                  |
|--------------|----------|----------------------------------------------------------------------------------------------|
| `type`       | yes      | One of `string`, `int`, `bool`, `secret`, `duration`, `url`, `enum`.                         |
| `default`    | no       | Default value when the operator does not supply one. Must match `type`.                      |
| `values`     | no       | Allowed values; only valid when `type: enum`.                                                |
| `mutability` | no       | `config` (default; operator-set, not persisted) or `runtime` (program-mutated, persisted).   |

### Field types

| Type | Wire type | Semantic |
|------|-----------|----------|
| `string` | YAML/JSON string | UTF-8 string; no special parsing. |
| `int` | YAML/JSON integer | 64-bit signed integer. |
| `bool` | YAML/JSON boolean | `true` or `false`. |
| `secret` | YAML/JSON string | Like `string` but marked non-logging. |
| `duration` | YAML/JSON string | Go-style duration (e.g. `"720h"`, `"-30m"`). |
| `url` | YAML/JSON string | URL string; no validation at load time. |
| `enum` | YAML/JSON string | One of the declared `values`. |

### Mutability

- `config` (default): operator-set; never written back at runtime. The
  most common case for credentials, URLs, intervals, page sizes, etc.
- `runtime`: program-mutated. The runner persists the value across
  iterations (e.g. an OAuth2 access token cached by
  `auth.oauth2.<grant>.cache`, or any application-level cached login).
  The OAuth2 cache `store_in` slot auto-registers a `runtime`-typed
  `string` field with the same name; authors must not redeclare that
  name under `state.fields`.

### Secret propagation

`secret`-typed fields carry a non-logging contract that propagates
through every `Value` form they participate in:

- Runtime callers MUST omit the value (and any `Format` / `Concat` /
  `Base64` Value that transitively contains it) from rendered error
  messages, logs, and debug output. The contract attaches to the
  value's reference path, not just the field declaration: a `Format`
  wrapping `{ref: state.api_key}` is also secret.
- Where the runtime cannot redact a transitive composition (e.g. a
  `Concat` whose inputs include both a literal URL and a secret
  token), the entire composed Value MUST be treated as secret.

Callers MUST call `schema.IsSecret(d, v)` on any Value before rendering
it into a log line, error message, or debug surface. The helper walks
`Concat` / `Format.Value` / `Base64` / `Object` values / `List` /
`Select` branches / `Select.Default` / `Now.Offset` / `Ref.Default` and
returns true when any reachable `Ref` resolves to a secret-typed state
field.

### Rules

- Field names must be unique within `state.fields`.
- `enum` fields must carry a non-empty `values` list.
- `auth.oauth2.<grant>.cache.store_in` implicitly registers a
  `runtime` `string` state slot. Authors must not also declare that
  name under `state.fields`.
- All `state.<name>` references in `Value` and `Predicate` must
  resolve to a declared field.

---

## `defaults`

Cross-cutting defaults applied to every request.

| Field      | Required | Description                                                                  |
|------------|----------|------------------------------------------------------------------------------|
| `base_url` | yes      | A [Value](#values) prepended to every `requests[].path`. Use `state.url`.    |

```yaml
defaults:
  base_url: {ref: state.url}
```

A request can override by setting `url:` (absolute) instead of `path:`
— `base_url` is NOT applied when a request uses `url:`. When
`defaults.base_url` is unset and a request uses `path:`, the bare path
string IS the URL — no prefix is applied. Authors who want a host
prefix must declare `defaults.base_url` explicitly.

---

## `auth`

Discriminated union — exactly one variant key. Applied to every
request (including OAuth2 token fetches, which use the operator's
`Client`).

### `auth.none`

No authentication.

```yaml
auth:
  none: {}
```

### `auth.bearer`

`Authorization: Bearer <token>` on every request.

| Field   | Required | Description                          |
|---------|----------|--------------------------------------|
| `token` | yes      | [Value](#values) — typically secret. |

```yaml
auth:
  bearer:
    token: {ref: state.api_key}
```

### `auth.basic`

`Authorization: Basic <base64(user:pass)>`.

| Field      | Required | Description       |
|------------|----------|-------------------|
| `username` | yes      | [Value](#values). |
| `password` | yes      | [Value](#values). |

### `auth.api_key`

Key in a named header (or query param).

| Field      | Required | Description                                                          |
|------------|----------|----------------------------------------------------------------------|
| `header`   | yes      | Header (or query-param) name.                                        |
| `value`    | yes      | [Value](#values).                                                    |
| `in_query` | no       | When `true`, send `?<header>=<value>` instead of the header.         |

### `auth.custom`

Single custom header with an arbitrary value.

| Field    | Required | Description       |
|----------|----------|-------------------|
| `header` | yes      | Header name.      |
| `value`  | yes      | [Value](#values). |

### `auth.oauth2`

Discriminated sub-union by grant type. Exactly one grant key is
required.

#### `auth.oauth2.client_credentials`

| Field           | Required | Description                                                                |
|-----------------|----------|----------------------------------------------------------------------------|
| `token_url`     | yes      | [Value](#values) — the token endpoint URL.                                 |
| `client_id`     | yes      | [Value](#values).                                                          |
| `client_secret` | yes      | [Value](#values) — typically secret.                                       |
| `scopes`        | no       | List of strings; joined with spaces.                                       |
| `audience`      | no       | String; sent as the `audience` form field when set.                        |
| `cache`         | no       | [TokenCache](#tokencache) — when set, the token is cached across drains.   |

#### `auth.oauth2.password_grant`

OAuth2 Resource Owner Password Credentials (RFC 6749 §4.3).

| Field       | Required | Description                                          |
|-------------|----------|------------------------------------------------------|
| `token_url` | yes      | [Value](#values).                                    |
| `username`  | yes      | [Value](#values).                                    |
| `password`  | yes      | [Value](#values) — typically secret.                 |
| `client_id` | no       | [Value](#values); some servers Basic-auth instead.   |
| `scopes`    | no       | List of strings; joined with spaces.                 |
| `cache`     | no       | [TokenCache](#tokencache).                           |

#### TokenCache

Caches the fetched OAuth2 token between drains so the next request
hits the API directly rather than re-fetching a token.

| Field           | Required | Description                                                                                  |
|-----------------|----------|----------------------------------------------------------------------------------------------|
| `store_in`      | yes      | State slot name. Auto-registers as a runtime `string` field — do NOT declare under `state`.  |
| `expiry_field`  | yes      | Body-relative [Path](#paths) to the response field carrying the token's lifetime.            |
| `expiry_buffer` | yes      | Go-style duration — re-fetch when remaining lifetime drops below this.                       |

### `auth.multi_mode`

Dispatch between auth strategies based on a [Predicate](#predicates)
over state or cursor.

| Field      | Required | Description                                                          |
|------------|----------|----------------------------------------------------------------------|
| `branches` | yes      | List of `{when: <predicate>, auth: <Auth>}`; first matching wins.    |
| `default`  | yes      | `{auth: <Auth>}` — fallback when no branch matches.                  |

```yaml
auth:
  multi_mode:
    branches:
      - when: {eq: {path: state.region, equal: "gov"}}
        auth: {bearer: {token: {ref: state.gov_token}}}
    default:
      auth: {bearer: {token: {ref: state.commercial_token}}}
```

### Auth rules

- `multi_mode` may not nest another `multi_mode`.
- `multi_mode.default.auth` is required and may itself be `none: {}`
  — the explicit form when the operator wants the request to fire
  unauthenticated whenever no branch's predicate matches.
- `auth.oauth2` must carry exactly one grant key
  (`client_credentials` or `password_grant`). Zero or multiple grant
  keys are rejected.
- `oauth2.<grant>.cache.store_in` must not match any `state.fields`
  key.
- Every `{ref: state.<name>}` used inside any auth Value must resolve
  to a declared field.

### Refresh tokens

Long-lived OAuth2 refresh tokens that a user pastes in are **not** a
separate auth variant. They are a secret-typed state field that feeds
`auth.bearer.token`:

```yaml
state:
  fields:
    refresh_token:
      type: secret
auth:
  bearer:
    token: {ref: state.refresh_token}
```

Interactive grants — `authorization_code`, `device_code`, PKCE — are
out of scope: the pull-loop runtime has no browser-roundtrip surface,
so the grant exchange itself cannot happen inside the IR's iteration
model.

---

## `requests`

Ordered list of HTTP requests run on every iteration. At least one
required. The producer step (the one whose body holds events) is the
last request by default; set `produces_events: true` on a different
step to override.

Per-entry fields:

| Field             | Required                       | Description                                                                                                                            |
|-------------------|--------------------------------|----------------------------------------------------------------------------------------------------------------------------------------|
| `id`              | when referenced from elsewhere | Step identifier. Required for `steps.<id>.body.<path>` references and `async_job.*.step`.                                              |
| `method`          | yes                            | HTTP verb (`GET`, `POST`, …).                                                                                                          |
| `path`            | when `url` unset               | Path appended to `defaults.base_url`. [Value](#values).                                                                                |
| `url`             | when `path` unset              | Absolute URL. [Value](#values). Mutually exclusive with `path`.                                                                        |
| `query`           | no                             | Map of name → [Value](#values).                                                                                                        |
| `headers`         | no                             | Map of name → [Value](#values).                                                                                                        |
| `body`            | no                             | Discriminated union: `json:` (map of [Value](#values)), `form:` (same), or `raw:` ([Value](#values)). Exactly one.                     |
| `extract`         | no                             | List of [ExtractVar](#extractvar) — capture fields out of the response.                                                                |
| `fan_out`         | no                             | Per-item iteration (deferred — accepted by the validator, not yet runnable).                                                           |
| `expect_status`   | no                             | List of HTTP status codes treated as success. Defaults to `[200]`.                                                                     |
| `if`              | no                             | [Predicate](#predicates) — skip the step when false.                                                                                   |
| `on_status`       | no                             | Map of status code → verb (`skip`, `fail`, `empty_events`, `invalidate_cache`). Per-step override of `error.mode` for that status.     |
| `produces_events` | no                             | Marks this step as the events producer. At most one in the chain; defaults to the last request.                                       |
| `cache`           | no                             | [RequestCache](#requestcache) — generic step-level cache for non-OAuth2 cached logins.                                                 |

### Request rules

- At least one element.
- `path:` and `url:` are mutually exclusive.
- `if:` and `on_status:` may appear on **any** step. When a step is
  skipped (by `if=false` or by `on_status: <code>: skip`):
  - `{ref: steps.<that-id>.body.<...>}` resolves to a zero `Value`.
  - `{present: steps.<that-id>.body.<...>}` returns `false`.
  - Downstream `{select}` branches must guard with `{present: ...}` to
    handle the skipped path.
- `on_status` keys must be HTTP status codes in `[100, 599]`. Values
  are one of the closed action set:
  - `skip` — drop the response, emit no events, advance the cursor as
    if successful (the canonical "304 Not Modified" handling).
  - `fail` — non-success: emit no events and stop the iteration with
    an error.
  - `empty_events` — non-success: emit no events but ADVANCE the
    cursor as if successful (the cisco_duo
    429-with-`ignore_api_errors` pattern).
  - `invalidate_cache` — drop the cached value backing the active
    auth's `oauth2.<grant>.cache` slot AND every `requests[].cache`
    step-cache slot, then treat the response as a non-event "retry next
    iteration" signal.

  `retry` is intentionally absent until the retry/backoff contract
  lands.
- Steps with `id:` must have unique IDs across the list.
- `fan_out.as` must not shadow any existing namespace name.
- `fan_out.over` must resolve to a list-typed `Value`. The validator
  rejects obvious non-list top-level forms (literal scalars,
  `{now: ...}`, `{format: ...}`, `{base64: ...}`, `{object: ...}`);
  list-ness for composite forms (`{ref: ...}`, `{concat: ...}`,
  `{list: ...}`, `{from_pagination: ...}`, `{from_progress: ...}`) is
  decided at lowering time when the runtime can see the resolved type.
- At most one request may carry `produces_events: true`. When no step
  is marked explicitly:
  - For non-`async_job` documents, the **last** request in
    `requests[]` is the implicit producer.
  - For `progress.async_job` documents, the implicit producer is the
    last-declared async role (`fetch` if set, else `poll`, else
    `submit`), NOT the last entry in `requests[]`. An explicit
    `produces_events: true` on any request overrides this rule.

### ExtractVar

| Field    | Required | Description                                                                                                |
|----------|----------|------------------------------------------------------------------------------------------------------------|
| `name`   | yes      | Binding name.                                                                                              |
| `path`   | no       | Body-relative [Path](#paths). Default: body root.                                                          |
| `coerce` | no       | Type coercion verb (e.g. `to_string`, `to_int`).                                                           |
| `source` | no       | `body` (default) or `header`.                                                                              |
| `header` | when `source: header` | Header name when reading from headers.                                                                     |
| `target` | no       | `extract` (default; per-iteration) or `cursor` (persisted; auto-registers a cursor field).                 |

`target: cursor` auto-registers a `cursor.<name>` field that persists
across iterations. This is the structured form for multi-field cursors
(worklists, freeze flags, rolling-max timestamps that aren't tied to
event timestamps).

### FanOut

```yaml
fan_out:
  over: <Value>               # a list Value
  as: <string>                # per-item variable name → item.<path> namespace
  merge: flatten | wrap       # how per-item responses combine
```

Deferred — accepted by the validator, not yet runnable.

### RequestCache

The non-OAuth2 counterpart of [TokenCache](#tokencache): wraps a
token-style step (custom JSON logins, session-key exchanges) in a
fresh-vs-cached conditional so the login round-trip is skipped while the
cached token is still inside its expiry buffer.

| Field           | Required | Description                                                                                                                       |
|-----------------|----------|-----------------------------------------------------------------------------------------------------------------------------------|
| `store_in`      | yes      | Names *both* the top-level response field captured *and* the state slot it lands in. Auto-registers as a runtime `string` field — do NOT declare under `state`. |
| `expiry_field`  | yes      | Body-relative [Path](#paths) to the response field carrying the token's lifetime / expiry instant.                                |
| `expiry_buffer` | yes      | Go-style duration — re-run the step when the remaining lifetime drops below this.                                                 |
| `expiry_format` | no       | How `expiry_field` is read: `duration` (default — a remaining lifetime) or an absolute-instant format (`unix_seconds`, `unix_millis`, `rfc3339`, `rfc3339nano`). |

The expiry timestamp is tracked at `cursor.__step_<store_in>_expires_at` as
an RFC 3339 string. A `on_status: invalidate_cache` verb clears the slot
(see [`on_status`](#request-rules)), forcing a re-login on the next drain.

```yaml
requests:
  - id: login
    method: POST
    path: /api/v1/login
    body:
      json:
        username: {ref: state.username}
        password: {ref: state.password}
    cache:
      store_in: session_token        # body field captured + state slot (auto-registered as runtime)
      expiry_field: expires_in       # body-relative Path
      expiry_buffer: 60s             # Go duration; re-run ahead of expiry
      expiry_format: duration        # optional; default "duration"
```

---

## `response`

How to decode the producer step's body and where to find the events
list.

| Field               | Required | Description                                                                                                                |
|---------------------|----------|----------------------------------------------------------------------------------------------------------------------------|
| `decode`            | yes      | `json` or `ndjson`.                                                                                                        |
| `events_at`         | yes      | Body [Path](#paths) to the events list. The zero (empty) Path means "the body root IS the events list".                    |
| `placeholder_event` | no       | [Value](#values) used when `events_at` resolves to an empty list and pagination wants another iteration.                   |

```yaml
response:
  decode: json
  events_at: data.events
```

### Response rules

- `events_at` is a **body-relative `Path`**. Its segments are not
  resolved against the namespace table; they index into the
  events-bearing step's decoded body. The zero `Path` means "body
  root" — the whole decoded body IS the events list (or single event
  when ndjson).
- When `decode: ndjson` and `events_at` is empty (zero Path), each
  decoded line IS one event. When `decode: ndjson` and `events_at` is
  non-empty, the `events_at` Path is applied to EACH decoded line and
  the flattened sequence is the events list.
- The HTTP status-code success set is configured per-step via
  `requests[].expect_status`. There is no `response.success_status`.

---

## `pagination`

Discriminated union — exactly one variant key.

### `pagination.none`

No pagination — one request per drain.

```yaml
pagination: {none: {}}
```

### `pagination.cursor_token`

Opaque server cursor token round-tripped on each page.

| Field      | Required | Description                                                                          |
|------------|----------|--------------------------------------------------------------------------------------|
| `token_at` | yes      | Body [Path](#paths) to the next-cursor field.                                        |
| `send_as`  | yes      | `query.<param>` or `header.<name>`. Auto-injected on the producer step.              |

Default completion fires when `{ref: <token_at>}` resolves to a zero
`Value` (empty / null).

### `pagination.page_number`

Incrementing 1-based page number.

| Field         | Required | Description                                                            |
|---------------|----------|------------------------------------------------------------------------|
| `page_param`  | yes      | Query-param name carrying the page number.                             |
| `has_more_at` | no       | Body [Path](#paths) to a bool flag; loop stops when false.             |
| `batch_size`  | no       | [Value](#values) — page size hint; not auto-injected.                  |

### `pagination.offset`

0-based offset, increments by `batch_size` (or observed event count).

| Field          | Required | Description                                          |
|----------------|----------|------------------------------------------------------|
| `offset_param` | yes      | Query-param name carrying the offset.                |
| `batch_size`   | no       | [Value](#values).                                    |

### `pagination.link_header`

RFC 5988 `Link: <url>; rel="next"`.

| Field     | Required | Description                                                       |
|-----------|----------|-------------------------------------------------------------------|
| `pattern` | no       | Override the regex (first capture group = next URL).              |

The runner parses the header into `cursor.next_link`; it is **not**
auto-injected. The request must read it back in its `url` slot via
`{ref: cursor.next_link, default: <bootstrap-url>}`. The drain ends when a
response carries no `rel="next"` entry.

### `pagination.next_url_in_body`

Fully-formed next-page URL inside the body.

| Field         | Required | Description                          |
|---------------|----------|--------------------------------------|
| `next_url_at` | yes      | Body [Path](#paths) to the URL.      |

The runner parses the URL into `cursor.next_url`; it is **not**
auto-injected. The request must read it back in its `url` slot via
`{ref: cursor.next_url, default: <bootstrap-url>}`. A missing, non-string,
or empty value terminates the drain.

### `pagination.scroll_id`

Server-side scroll session.

| Field            | Required | Description                                                                            |
|------------------|----------|----------------------------------------------------------------------------------------|
| `scroll_id_at`   | yes      | Body [Path](#paths) to the scroll id.                                                  |
| `send_as`        | yes      | `query.<param>` or `header.<name>` (`body.<key>` is deferred).                         |
| `complete_when`  | no       | [Predicate](#predicates) — terminates the scroll when true and clears the scroll id.   |

Default completion (when `complete_when` is omitted) fires when
`{ref: <scroll_id_at>}` resolves to a zero `Value`.

### `pagination.graphql_relay`

GraphQL Relay-style cursors.

| Field              | Required | Description                                                          |
|--------------------|----------|----------------------------------------------------------------------|
| `has_next_page_at` | yes      | Body [Path](#paths) to the `pageInfo.hasNextPage` bool.              |
| `end_cursor_at`    | yes      | Body [Path](#paths) to the `pageInfo.endCursor` string.              |
| `cursor_var`       | yes      | Author-named cursor variable (typically `after`).                    |

### Pagination rules

- `send_as` is required for `cursor_token` and `scroll_id` and is the
  single source of truth for the auto-injection slot. The codec
  rejects values that do not start with `query.` or `header.`.
- `cursor_token` and `scroll_id` are auto-injected at the producer
  step. Authors may also write `{from_pagination: token}` /
  `{from_pagination: scroll_id}` explicitly at any request slot
  (`query`, `headers`, `body.json` / `body.form`). When both forms are
  present, the explicit Value wins; the slot named by `send_as` MUST
  agree with the slot where the explicit Value lives.
- `page_number.page_param` and `offset.offset_param` name the **param
  key**, not its placement. Placement follows the
  `{from_pagination: ...}` Value's location in the request
  (`query: {<name>: {from_pagination: page}}` rides as a query param;
  `body: {json: {<name>: {from_pagination: offset}}}` rides in the
  JSON body).

---

## `progress`

Discriminated union — exactly one variant key. Controls how the cursor
advances at the end of a drain.

### `progress.stateless`

Cursor never advances. Use for endpoints that always return "current
state" with no time dimension.

```yaml
progress: {stateless: {}}
```

### `progress.latest_event_timestamp`

Cursor advances to the maximum event timestamp seen across the drain.

| Field        | Required | Description                                                                                              |
|--------------|----------|----------------------------------------------------------------------------------------------------------|
| `event_time` | yes      | `{path: <body-Path>}` — per-event timestamp field inside the events list.                                |
| `initial`    | no       | `{lookback: <Value>}` — first-run lookback (e.g. `"720h"`).                                              |
| `lookback`   | no       | [Value](#values) — every-iteration lag, subtracted from the reference on every advance.                  |

### `progress.max_event_field`

Same as `latest_event_timestamp` but the `event_time.path` is treated
as an arbitrary monotonically-increasing field (numeric id, etc.), not
specifically a timestamp.

### `progress.use_now`

Cursor advances to `now()` on every iteration.

| Field      | Required | Description                                                                       |
|------------|----------|-----------------------------------------------------------------------------------|
| `lookback` | no       | [Value](#values) — subtract from `now()` so late events still land on next drain. |

### `progress.time_window`

Sliding `[window_start, window_end)` window.

| Field            | Required | Description                                                              |
|------------------|----------|--------------------------------------------------------------------------|
| `initial_offset` | yes      | [Value](#values) — first-run window size.                                |
| `format`         | no       | Format verb for the window timestamps; defaults to `rfc3339`.            |

`format`, when set, must be one of the format verbs in
[Values](#values). Only the timestamp-producing verbs (`rfc3339`,
`rfc3339nano`, `unix_seconds`, `unix_millis`) make semantic sense as
the window representation; the validator enforces closed-set
membership but not the timestamp-only subset.

### `progress.async_job`

Three-phase submit → poll → fetch loop. State machine lives on
`cursor.phase`.

| Field         | Required | Description                                                                                                       |
|---------------|----------|-------------------------------------------------------------------------------------------------------------------|
| `submit`      | one of   | `{step: <id>, extract: {<name>: {path: <Path>}, ...}}` — submit step + per-extract bindings.                      |
| `poll`        | one of   | `{step: <id>, complete_when: <Predicate>, extract: {...}}` — poll step + completion predicate.                    |
| `fetch`       | one of   | `{step: <id>}` — fetch step (the producer).                                                                       |
| `on_complete` | no       | `{cursor_update: <CursorUpdateDirective>}` — how to advance the cursor after a completed fetch.                   |

At least one of `submit`, `poll`, `fetch` must be set; the IR does not
encode a fixed three-phase contract. Each role is independently
optional and is exercised in declared order.

`poll.complete_when` is a `Predicate` that evaluates against the poll
step's response body. The `body.<path>` namespace IS valid inside this
predicate (and only inside `complete_when` predicates —
`pagination.scroll_id.complete_when` follows the same rule).

The async job's events-bearing step is the last-declared role
(`fetch` > `poll` > `submit`) unless one of the requests carries
`produces_events: true`. The validator rejects HEAD as the
events-bearing step (HEAD has no response body for `response.decode` /
`events_at` to operate on).

#### CursorUpdateDirective

Map form only. The scalar shorthand (`cursor_update: use_now`) is
rejected at parse time.

| Field        | Required                              | Description                                                                                |
|--------------|---------------------------------------|--------------------------------------------------------------------------------------------|
| `kind`       | yes                                   | `use_now`, `latest_event_timestamp`, or `stateless`.                                       |
| `lookback`   | no                                    | [Value](#values). Rejected when `kind: stateless` (no advance to subtract from).           |
| `event_time` | when `kind: latest_event_timestamp`   | `{path: <body-Path>}`. Rejected for the other kinds.                                       |

#### Async job cursor fields

`progress.async_job` auto-provides these cursor fields:

- `cursor.phase` — current phase. Default `submit` when `submit` is
  set, then `poll` when `poll` is set, then `fetch`.
- `cursor.<name>` for every name declared in `submit.extract` and
  `poll.extract`.

### Progress rules

- `latest_event_timestamp.lookback` / `max_event_field.lookback` /
  `use_now.lookback` (per-iteration) is a duration `Value` subtracted
  from the chosen reference on EVERY advance, not just the first run.
  It pairs with `initial.lookback` (first-run only) for time cursors
  that need both a first-run lookback and an every-iteration lag.
- The `{from_progress: <role>}` Value's role must match the active
  progress strategy: `latest_timestamp` is valid for
  `latest_event_timestamp` / `max_event_field` / `use_now`;
  `window_start` and `window_end` are valid for `time_window`;
  `stateless` accepts no role. `async_job` advances
  `cursor.last_timestamp` via
  `on_complete.cursor_update.kind: latest_event_timestamp` — the
  validator cannot resolve that statically, so role/strategy matching
  is not enforced for async_job documents and stays a
  target-lowering responsibility.

---

## `error`

How non-success HTTP responses (and network / decode failures) are
surfaced.

| Field          | Required | Description                                                                              |
|----------------|----------|------------------------------------------------------------------------------------------|
| `mode`         | yes      | `standard` (default), `warn`, or `fail`. See [`runtime.md`](runtime.md) for semantics.   |
| `include_body` | no       | When `true`, include the response body in the error message. Off by default.             |

```yaml
error:
  mode: standard
```

Per-step overrides go in `requests[].on_status` — they take precedence
over `error.mode` for the named statuses.

---

## Values

A `Value` is the universal field type for any dynamic input. It is a
discriminated union — exactly one form is active. Scalars work as-is;
structured forms use a discriminator key.

### Authoring rules

- **Bare YAML scalar (string):** treated as a string literal.
  `path: /api/v1/events` is the literal string `"/api/v1/events"`.
- **YAML integer scalar:** `LiteralInt`.
- **YAML boolean scalar:** `LiteralBool`.
- **YAML null:** zero Value (`IsZero`). Authors should omit the field
  entirely rather than writing `key: null`.
- **YAML mapping:** discriminated by the presence of a recognised key.
  If no discriminator key is found, the mapping decodes as `Object`.

### Discriminated forms

| Scalar / form                                          | Meaning                                                                                       |
|--------------------------------------------------------|-----------------------------------------------------------------------------------------------|
| `/api/v1/events`, `100`, `true`, `null`                 | LiteralString, LiteralInt, LiteralBool, zero.                                                 |
| `{literal_string: "x"}`                                 | Explicit literal string form (when YAML would otherwise misparse as int/bool).                |
| `{ref: cursor.last_timestamp}`                          | Reference into a namespace.                                                                   |
| `{ref: state.url, default: "https://…"}`                | Reference with fallback when unset.                                                           |
| `{now: true, offset: "-1h"}`                            | Current time, optionally offset. Wrap with `{format: <verb>, value: {now: true}}` to coerce.  |
| `{concat: [<Value>, ...]}`                              | String concatenation; at least 2 elements (`{concat: []}` and `{concat: [<single>]}` are rejected). |
| `{select: {branches: [...], default: <Value>}}`         | Conditional select; first matching branch wins.                                               |
| `{from_pagination: token}`                              | Active pagination signal for THIS page. Roles: `token`, `page`, `offset`, `offset_end`, `scroll_id`, `relay_cursor`. |
| `{from_progress: latest_timestamp}`                     | Active progress signal for THIS drain. Roles: `latest_timestamp`, `window_start`, `window_end`. |
| `{format: rfc3339, value: <Value>}`                     | Coerce `value` to a formatted representation.                                                 |
| `{base64: <Value>}`                                     | Base64-encode the inner Value's string.                                                       |
| `{list: [<Value>, ...]}`                                | List literal.                                                                                 |
| `{object: {<key>: <Value>, ...}}`                       | Object literal. Required for any map-shaped Value; inner keys are NOT re-interpreted.         |

### Format verbs

The format-verb set is closed.

| Verb | Converts to |
|------|-------------|
| `string` | string representation. |
| `int` | integer (parse or truncate). |
| `bool` | boolean. |
| `rfc3339` | RFC 3339 timestamp string. |
| `rfc3339nano` | RFC 3339 with nanoseconds. |
| `unix_seconds` | integer Unix timestamp (seconds). |
| `unix_millis` | integer Unix timestamp (milliseconds). |
| `duration` | Go-style duration string. |
| `url_encode` | percent-encoded URL component (RFC 3986 unreserved + percent). |
| `parse_duration` | Go-style duration string → integer (nanoseconds). |

`parse_duration` outputs a 64-bit signed integer count of nanoseconds.
Targets that cannot represent a 64-bit signed integer natively MUST
surface a lowering error rather than silently truncating.

Not in the set today: `regex_extract`, `sha256`, `hmac_sign`,
`json_encode`. New verbs land when a concrete template motivates them.

### Value rules

- A map-shaped Value MUST carry exactly one discriminator key. There
  is no silent fallback for arbitrary maps — wrap a literal map in
  `{object: {…}}`. A map carrying more than one discriminator key is
  rejected at parse time (e.g. `now` may not also carry `format`; use
  `{format: <verb>, value: {now: true}}`).
- Each discriminator has a closed set of allowed sibling keys
  (`ref` → `default`; `now` → `offset`; `format` → `value`; the rest
  → no siblings). Unknown sibling keys are rejected at parse time so
  typos like `{ref: state.x, defualt: "y"}` do not drop into the void.
- Empty `{and: []}` and `{or: []}` in Predicates are rejected as
  authoring errors. Use `{literal_bool: true | false}` for a constant
  predicate.
- `{ref: cursor.<name>}` is valid only when the cursor namespace
  provides `<name>` for the active strategies.
- `{from_pagination: ...}` and `{from_progress: ...}` are
  semantic-role Values. They do not hard-code a cursor path; the
  runtime resolves them to the appropriate cursor field based on the
  active strategy.

---

## Paths

`Path` is the typed kind for dotted-string identifiers used in
`{ref: ...}`, `{present: ...}`, `{eq: {path: ...}}`, `events_at`,
`extract.path`, `fan_out.over`, `pagination.*.token_at`, etc.

Primary form: `data.issues.nodes` (dotted string).

Escape form for field names containing dots:

```yaml
{parts: ["steps", "my.weird.id", "body"]}
```

The IR encoder emits the escape form **whenever any segment contains a
`.`**; all other paths emit as a dotted string. This guarantees
byte-stable round-trips through the dotted parser.

### Two Path flavours

- **Namespace-rooted Paths** appear in `{ref: ...}`, `{present: ...}`,
  and `{eq: {path: ...}}`. The root segment is resolved against the
  namespace table below; the validator rejects unknown roots and
  unresolved leaves.
- **Body-relative Paths** appear in `response.events_at`,
  `extract[].path`, `pagination.*.token_at` / `scroll_id_at` /
  `next_url_at` / `has_next_page_at` / `end_cursor_at` /
  `has_more_at`, `progress.async_job.<phase>.extract.<name>.path`,
  `auth.oauth2.<grant>.cache.expiry_field`, and
  `requests[].cache.expiry_field`. The validator only checks the
  structural parse; segments index into a response body whose shape
  is target-specific.

A Path is **empty** when `IsZero` is true OR `Parts` is
nil / zero-length. Both forms are equivalent at every reference site;
the validator and codec call `Path.IsEmpty()` for this combined
condition.

### Namespace shadow warning

Body-relative paths whose first segment matches one of the namespace
roots (`state`, `cursor`, `extract`, `steps`, `item`, `body`) are
accepted but emit a `warning`-severity diagnostic. The path resolves
against the body, NOT the namespace table — but it reads like a
namespace ref to a human, which is a common authoring trap. Authors
who genuinely have a body field named `state` / `cursor` / etc. can
silence the warning by renaming the body field; the IR will not break
the document.

### Namespace table

| Root        | Valid in                                  | Populated by                                                              |
|-------------|-------------------------------------------|---------------------------------------------------------------------------|
| `state.<name>` | anywhere                                | `state.fields` declarations.                                              |
| `cursor.<name>` | anywhere                               | Inferred from `pagination` + `progress` + `async_job`.                    |
| `extract.<name>` | requests that follow the producing step | `requests[].extract`.                                                    |
| `steps.<id>.body.<path>` | requests that follow step `<id>` | Previous step's response body.                                            |
| `item.<path>` | inside a step with `fan_out:`             | `fan_out.as` name (deferred).                                             |
| `body.<path>` | inside `complete_when` predicates         | The predicate-step's decoded body.                                        |

### Cursor namespace (inferred)

The following cursor fields are auto-provided based on active
strategies:

| Strategy / form                       | Cursor fields provided |
|---------------------------------------|----------------------|
| `pagination: cursor_token`            | `cursor.token` |
| `pagination: page_number`             | `cursor.page` |
| `pagination: offset`                  | `cursor.offset` |
| `pagination: scroll_id`               | `cursor.scroll_id` |
| `pagination: next_url_in_body`        | `cursor.next_url` |
| `pagination: graphql_relay`           | `cursor.<cursor_var>` (the declared var name) |
| `pagination: link_header`             | `cursor.next_link` |
| `progress: latest_event_timestamp`    | `cursor.last_timestamp` |
| `progress: max_event_field`           | `cursor.last_timestamp` |
| `progress: use_now`                   | `cursor.last_timestamp` |
| `progress: time_window`               | `cursor.window_start`, `cursor.window_end` |
| `progress: async_job`                 | `cursor.phase`, plus all names from `submit.extract` + `poll.extract` |

---

## Predicates

Boolean expressions used in `requests[].if`,
`auth.multi_mode.branches[].when`, `Value.select.branches[].when`,
`async_job.poll.complete_when`, and
`pagination.scroll_id.complete_when`. Discriminated union — exactly
one variant key.

| Form                                                | Meaning                                                  |
|-----------------------------------------------------|----------------------------------------------------------|
| `{eq:  {path: <Path>, equal: <Value>}}`             | Path equals Value.                                       |
| `{gt:  {path: <Path>, equal: <Value>}}`             | Path > Value (numbers, durations, RFC 3339 timestamps).  |
| `{lt:  {path: <Path>, equal: <Value>}}`             | Path < Value.                                            |
| `{gte: {path: <Path>, equal: <Value>}}`             | Path >= Value.                                           |
| `{lte: {path: <Path>, equal: <Value>}}`             | Path <= Value.                                           |
| `{present: <Path>}`                                 | Path resolves to a non-null value.                       |
| `{not: <Predicate>}`                                | Negation.                                                |
| `{and: [<Predicate>, ...]}`                         | Conjunction (short-circuits).                            |
| `{or:  [<Predicate>, ...]}`                         | Disjunction (short-circuits).                            |
| `{literal_bool: true \| false}`                     | Constant truth value.                                    |

`gt | lt | gte | lte` are ordered comparisons. The LHS (`path`)
resolves to a namespace ref; the RHS (`equal`) is any Value. Targets
that cannot type-check both operands at lower time (e.g. when the path
resolves to a string and the Value is an int literal) MUST surface a
lowering error rather than silently coercing.

The verbs `matches` (regex) and `in` (set membership) are not
modelled — `in` is expressible today as nested `{or: [...]}`;
`matches` would need a regex Value form.

---

## Design rules

These invariants hold across the whole schema. The validator
(`schema.Validate`) enforces them; violations are rejected at load
time.

### No target-specific leaks

- No embedded expressions or scripting language anywhere in the
  schema.
- All Value forms are structurally tagged. Bare YAML scalars in Value
  positions are **string literals**, never interpreted as expressions.

### Pure discriminated unions

Every union block (`auth`, `pagination`, `progress`, `body`, `Value`,
`Predicate`) carries **exactly one** variant key. The IR codec rejects
any mapping that carries zero or more than one discriminator key at
parse time — this rule applies equally to `Value` and `Predicate`, not
just to the top-level union blocks.

Authors who want a literal map at a `Value` position must use the
explicit `{object: {...}}` wrapper. There is no silent fallback to
`Object`.

### Structural validation only

`schema.Validate` checks structure: references resolve, unions have
exactly one key, required fields are present, mutual exclusivities
hold, type shapes match. It does NOT check whether the in-process
runner currently implements the combination — runtime capability
checks live in the runner (see [`runtime.md`](runtime.md)).

### Cursor is fully inferred

Cursor fields are derived by the lowering layer from `pagination`,
`progress`, and `async_job.<step>.extract` declarations. Authors do
not write a `cursor.fields:` block.

### Codec invariants

- Every `Value` round-trips through YAML and JSON byte-identically
  (same map shapes, same key names).
- Slice fields (`requests`, `branches`, `extract`, `concat`, `list`)
  preserve YAML/JSON parse order.
- Map fields (`state.fields`, `query`, `headers`, `body.json`,
  `body.form`, `object`) are emitted alphabetically by key during
  marshal. Authors who rely on a specific emission order (e.g. an
  HMAC signature pre-image hashing the canonical form) must use a
  slice-shaped construction.
- `schema.Load` rejects `ir_version != "1"` before returning a
  `*Doc`.
- `schema.Validate` operates on a parsed `*Doc`, never on raw bytes.
