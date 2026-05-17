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
- [Paths](#paths) — dotted-string references into the runtime
  namespaces (`state`, `cursor`, `extract`, `steps`, `response`,
  `item`).
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
      - when: {eq: {path: state.region, value: "gov"}}
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
| `fan_out`         | no                             | Per-item iteration over a list `Value`: the runner runs the step once per item with the `fan_out.as` name bound to the current value, then merges the per-item responses per `fan_out.merge`. Mutually exclusive with `cache`. |
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
  `{list: ...}`) is decided at lowering time when the runtime can see
  the resolved type.
- At most one request may carry `produces_events: true`. When no step
  is marked explicitly:
  - For non-`async_job` documents, the **last** request in
    `requests[]` is the implicit producer.
  - For `progress.async_job` documents, the implicit producer is the
    last-declared async role (`fetch` if set, else `poll`, else
    `submit`), NOT the last entry in `requests[]`. An explicit
    `produces_events: true` on any request overrides this rule.

### ExtractVar

| Field    | Required | Description                                                                                                                                                          |
|----------|----------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `name`   | yes      | Binding name.                                                                                                                                                        |
| `from`   | yes      | Namespace-rooted [Path](#paths). One of `response.body.<path>`, `response.header.<name>`, `steps.<id>.body.<path>`, `steps.<id>.header.<name>`. The runner dispatches body-walk vs header-lookup off the path root. |
| `coerce` | no       | Type coercion verb (e.g. `to_string`, `to_int`).                                                                                                                     |
| `target` | no       | `extract` (default; per-iteration) or `cursor` (persisted; auto-registers a cursor field).                                                                            |

`target: cursor` auto-registers a `cursor.<name>` field that persists
across iterations. This is the structured form for multi-field cursors
(worklists, freeze flags, rolling-max timestamps that aren't tied to
event timestamps).

### FanOut

```yaml
fan_out:
  over: <Value>               # a list Value
  as: <string>                # per-item binding name; refs use {ref: <as>.<path>}
  merge: flatten | wrap       # how per-item responses combine
```

The runner evaluates `over` to a list, then runs the step once per item
with the author-chosen `as` name bound to the current value. Inside the
step (and inside `extract.from` paths it owns), `{ref: <as>.<path>}`
resolves against the current item — `<as>` is whatever the operator
wrote, NOT a fixed `item.` prefix.

`merge: flatten` (default) concatenates per-item response bodies, which
must each decode as JSON lists; a non-list body is a template error.
`merge: wrap` returns the per-item bodies as elements of a list,
preserving each item's response shape.

Per-item errors run through the same `on_status` / `error.mode`
dispatcher as a single-request step: `skip` / `empty_events` drop the
item, `fail` aborts the drain, `invalidate_cache` clears the active auth
+ step caches and stops the fan-out early. `requests[].cache` cannot be
combined with `fan_out` — see the rules below.

### RequestCache

The non-OAuth2 counterpart of [TokenCache](#tokencache): wraps a
token-style step (custom JSON logins, session-key exchanges) in a
fresh-vs-cached conditional so the login round-trip is skipped while the
cached token is still inside its expiry buffer.

| Field           | Required | Description                                                                                                                       |
|-----------------|----------|-----------------------------------------------------------------------------------------------------------------------------------|
| `store_in`      | yes      | Names *both* the top-level response field captured *and* the state slot it lands in. Auto-registers as a runtime `string` field — do NOT declare under `state`. The paired expiry slot (`<store_in>_expires_at`) auto-registers the same way. |
| `expiry_field`  | yes      | [Path](#paths) rooted at `response.body.<path>` (of the cached step's own response) carrying the token's lifetime / expiry instant.                                |
| `expiry_buffer` | yes      | Go-style duration — re-run the step when the remaining lifetime drops below this.                                                 |
| `expiry_format` | no       | How `expiry_field` is read: `duration` (default — a remaining lifetime) or an absolute-instant format (`unix_seconds`, `unix_millis`, `rfc3339`, `rfc3339nano`). |

The expiry timestamp is tracked at `state.<store_in>_expires_at` as an
RFC 3339 string. A `on_status: invalidate_cache` verb clears both slots
(see [`on_status`](#request-rules)), forcing a re-login on the next
drain.

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
      store_in: session_token                       # body field captured + state slot (auto-registered as runtime)
      expiry_field: response.body.expires_in        # namespace-rooted Path
      expiry_buffer: 60s                            # Go duration; re-run ahead of expiry
      expiry_format: duration                       # optional; default "duration"
```

---

## `response`

How to decode the producer step's body and where to find the events
list.

| Field               | Required | Description                                                                                                                                                                            |
|---------------------|----------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `decode`            | yes      | `json` or `ndjson`.                                                                                                                                                                    |
| `events_at`         | yes      | [Path](#paths) rooted at `response.body.<path>` (or `steps.<id>.body.<path>`) locating the events list. The zero (empty) Path means "the body root IS the events list".                |
| `placeholder_event` | no       | [Value](#values) used when `events_at` resolves to an empty list and pagination wants another iteration.                                                                               |

```yaml
response:
  decode: json
  events_at: response.body.data.events
```

### Response rules

- `events_at` is namespace-rooted: `response.body.<path>` reads from the
  events-bearing step's own response; `steps.<id>.body.<path>` reads
  from a labelled prior step's response. The zero `Path` means "body
  root" — the whole decoded body IS the events list (or single event
  when ndjson). Bare dotted strings (`data.events`) are rejected.
- When `decode: ndjson` and `events_at` is empty (zero Path), each
  decoded line IS one event. When `decode: ndjson` and `events_at` is
  non-empty, the trailing body segments below the namespace root are
  applied to EACH decoded line and the flattened sequence is the
  events list.
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

Opaque server cursor token round-tripped on each page. The runner
captures the next token into `cursor.token`; the request reads it back
on the next iteration via an explicit `{ref: cursor.token, default: ""}`
in whichever slot (query / header / body) it belongs.

| Field      | Required | Description                                                                                                |
|------------|----------|------------------------------------------------------------------------------------------------------------|
| `token_at` | yes      | [Path](#paths) rooted at `response.body.<path>` (or `steps.<id>.body.<path>`) to the next-cursor field.    |

Default completion fires when `{ref: cursor.token}` resolves to a zero
`Value` (empty / null) after `token_at` is applied to the response.

### `pagination.page_number`

Incrementing 1-based page number. The active page lives at
`cursor.page`; read it back into a request slot with
`{ref: cursor.page}` (commonly under a `{format: string, ...}` wrapper
for query placement).

| Field         | Required | Description                                                                                                |
|---------------|----------|------------------------------------------------------------------------------------------------------------|
| `page_param`  | yes      | Author-supplied param name. Diagnostic-only — it does NOT control placement; placement follows the ref.    |
| `has_more_at` | no       | [Path](#paths) rooted at `response.body.<path>` (or `steps.<id>.body.<path>`) to a bool flag; loop stops when false. |
| `batch_size`  | no       | [Value](#values) — page size hint; the author writes it into the request explicitly.                       |

### `pagination.offset`

0-based offset, increments by `batch_size` (or observed event count).
The active offset lives at `cursor.offset` (and `cursor.offset_end`
when `batch_size` is set); read them back with
`{ref: cursor.offset}` / `{ref: cursor.offset_end}`.

| Field          | Required | Description                                                                                                |
|----------------|----------|------------------------------------------------------------------------------------------------------------|
| `offset_param` | yes      | Author-supplied param name. Diagnostic-only — placement follows the ref.                                   |
| `batch_size`   | no       | [Value](#values).                                                                                          |

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

| Field         | Required | Description                                                                                                |
|---------------|----------|------------------------------------------------------------------------------------------------------------|
| `next_url_at` | yes      | [Path](#paths) rooted at `response.body.<path>` (or `steps.<id>.body.<path>`) to the URL.                  |

The runner parses the URL into `cursor.next_url`. The request must read
it back in its `url` slot via
`{ref: cursor.next_url, default: <bootstrap-url>}`. A missing,
non-string, or empty value terminates the drain.

### `pagination.scroll_id`

Server-side scroll session. The active id lives at `cursor.scroll_id`;
the request reads it back via `{ref: cursor.scroll_id}` (typically with
no `default:` so the bootstrap iteration omits the slot and the server
opens a fresh session).

| Field            | Required | Description                                                                                                |
|------------------|----------|------------------------------------------------------------------------------------------------------------|
| `scroll_id_at`   | yes      | [Path](#paths) rooted at `response.body.<path>` (or `steps.<id>.body.<path>`) to the scroll id.            |
| `complete_when`  | no       | [Predicate](#predicates) — terminates the scroll when true and clears the scroll id.                       |

Default completion (when `complete_when` is omitted) fires when
`{ref: cursor.scroll_id}` resolves to a zero `Value` after `scroll_id_at`
is applied to the response.

### `pagination.graphql_relay`

GraphQL Relay-style cursors. The active end-cursor lives at
`cursor.<cursor_var>`; the request reads it back via
`{ref: cursor.<cursor_var>}` inside the GraphQL `variables:` object.

| Field              | Required | Description                                                                                                |
|--------------------|----------|------------------------------------------------------------------------------------------------------------|
| `has_next_page_at` | yes      | [Path](#paths) rooted at `response.body.<path>` (or `steps.<id>.body.<path>`) to the `pageInfo.hasNextPage` bool. |
| `end_cursor_at`    | yes      | [Path](#paths) rooted at `response.body.<path>` (or `steps.<id>.body.<path>`) to the `pageInfo.endCursor` string. |
| `cursor_var`       | yes      | Author-named cursor variable (typically `after`).                                                          |

### Pagination rules

- The active value for every strategy lives on `cursor.<name>` (see the
  [cursor namespace table](#cursor-namespace-inferred)). Authors wire it
  into a request explicitly with `{ref: cursor.<name>}` — there is no
  auto-injection. Placement follows where the ref is written
  (`query: {p: {ref: cursor.token}}` rides as a query param;
  `body: {json: {p: {ref: cursor.offset}}}` rides in the JSON body).
- `cursor_token` accepts an empty default (`{ref: cursor.token, default: ""}`)
  on the bootstrap iteration to preserve the historical
  `?cursor=` wire shape; without a default, the slot is simply absent
  until the cursor populates.
- `scroll_id` typically omits `default:` so the bootstrap iteration
  opens a fresh server session.
- `link_header` and `next_url_in_body` carry the next URL in
  `cursor.next_link` / `cursor.next_url`; read them back in the request's
  `url` slot via `{ref: cursor.next_link, default: <bootstrap-url>}` /
  `{ref: cursor.next_url, default: <bootstrap-url>}`.

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

| Field         | Required | Description                                                                                                                                                                  |
|---------------|----------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `submit`      | one of   | `{step: <id>, extract: {<name>: {from: <Path>}, ...}}` — submit step + per-extract bindings. Each `from` is rooted at `response.body.<path>` or `steps.<id>.body.<path>`.    |
| `poll`        | one of   | `{step: <id>, complete_when: <Predicate>, extract: {...}}` — poll step + completion predicate. `extract` follows the same `{from: <Path>}` shape as `submit.extract`.        |
| `fetch`       | one of   | `{step: <id>}` — fetch step (the producer).                                                                                                                                  |
| `on_complete` | no       | `{cursor_update: <CursorUpdateDirective>}` — how to advance the cursor after a completed fetch.                                                                              |

At least one of `submit`, `poll`, `fetch` must be set; the IR does not
encode a fixed three-phase contract. Each role is independently
optional and is exercised in declared order.

`poll.complete_when` is a `Predicate` that evaluates against the poll
step's response body. The `response.body.<path>` and
`response.header.<name>` roots ARE valid inside this predicate (and
only inside `complete_when` predicates —
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
- Every progress strategy publishes its active value on
  `cursor.<name>` (see the
  [cursor namespace table](#cursor-namespace-inferred)) — read it into
  a request slot with `{ref: cursor.<name>}`. The validator rejects
  `{ref: cursor.<name>}` for any name no active strategy populates, so
  e.g. `cursor.window_start` is rejected outside `progress.time_window`.
  `async_job` advances `cursor.last_timestamp` lazily via
  `on_complete.cursor_update.kind: latest_event_timestamp`, so the
  cursor namespace stays populated even when the value is unset on
  the first iteration.

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
| `{ref: cursor.last_timestamp}`                          | Reference into a namespace. Active pagination / progress signals (`cursor.token`, `cursor.page`, `cursor.last_timestamp`, `cursor.window_start`, …) ride through plain `ref` Values. |
| `{ref: state.url, default: "https://…"}`                | Reference with fallback when unset.                                                           |
| `{now: true, offset: "-1h"}`                            | Current time, optionally offset. Wrap with `{format: <verb>, value: {now: true}}` to coerce.  |
| `{concat: [<Value>, ...]}`                              | String concatenation; at least 2 elements (`{concat: []}` and `{concat: [<single>]}` are rejected). |
| `{select: {branches: [...], default: <Value>}}`         | Conditional select; first matching branch wins.                                               |
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
  provides `<name>` for the active strategies (see the
  [cursor namespace table](#cursor-namespace-inferred)).

---

## Paths

`Path` is the typed kind for dotted-string identifiers used in
`{ref: ...}`, `{present: ...}`, `{eq: {path: ...}}`, `events_at`,
`extract[].from`, `fan_out.over`, `pagination.*.token_at`, etc.

Primary form: `response.body.data.issues.nodes` (dotted string with a
namespace-root prefix).

Escape form for field names containing dots:

```yaml
{parts: ["steps", "my.weird.id", "body", "events"]}
```

The IR encoder emits the escape form **whenever any segment contains a
`.`**; all other paths emit as a dotted string. This guarantees
byte-stable round-trips through the dotted parser.

A Path is **empty** when `IsZero` is true OR `Parts` is
nil / zero-length. Both forms are equivalent at every reference site;
the validator and codec call `Path.IsEmpty()` for this combined
condition.

### Namespace table

Every Path begins with one of the roots below. Bare dotted strings
(e.g. `data.events` with no namespace prefix) are rejected at parse /
validate time, with a hint pointing at the new form.

| Root                       | Valid in                                                                                          | Populated by                                                          |
|----------------------------|---------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------|
| `state.<name>`             | anywhere                                                                                          | `state.fields` declarations + cache `store_in` auto-registrations.    |
| `cursor.<name>`            | anywhere                                                                                          | Inferred from `pagination` + `progress` + `async_job`; `extract.target=cursor`. |
| `extract.<name>`           | requests that follow the producing step                                                           | `requests[].extract`.                                                 |
| `steps.<id>.body.<path>`   | anywhere (after step `<id>` has run)                                                              | Prior step's decoded response body.                                   |
| `steps.<id>.header.<name>` | anywhere (after step `<id>` has run)                                                              | Prior step's response headers.                                        |
| `response.body.<path>`     | body-rooted IR slots (`events_at`, `token_at`, `scroll_id_at`, etc.) AND inside `complete_when`   | The active step's decoded response body.                              |
| `response.header.<name>`   | `extract[].from` AND inside `complete_when`                                                       | The active step's response headers.                                   |
| `<fan_out.as>.<path>`      | inside a step with `fan_out:`                                                                     | The author-chosen `fan_out.as` name (e.g. `incident.id` when `as: incident`); bound to the current item for the duration of one per-item iteration. |

Body-rooted IR slots — `response.events_at`, `pagination.*.token_at` /
`scroll_id_at` / `next_url_at` / `has_next_page_at` / `end_cursor_at` /
`has_more_at`, `progress.async_job.<phase>.extract.<name>.from`,
`auth.oauth2.<grant>.cache.expiry_field`,
`requests[].cache.expiry_field` — accept the body-flavoured roots
(`response.body.<path>`, `steps.<id>.body.<path>`) only.
`extract[].from` additionally accepts the matching header roots.
`complete_when` predicates (`pagination.scroll_id.complete_when`,
`progress.async_job.poll.complete_when`) are the only sites where the
bare `response.<...>` roots resolve against the predicate-step's
response without an `id:` qualifier.

Per-event sub-paths under `event_time: {path: ...}`
(`progress.{latest_event_timestamp,max_event_field}.event_time.path`,
`async_job.on_complete.cursor_update.event_time.path`) stay bare —
they index into each element of the events list and reject any
namespace root.

### Cursor namespace (inferred)

The following cursor fields are auto-provided based on active
strategies. Authors read them with plain `{ref: cursor.<name>}` Values;
they do not need to be declared anywhere.

| Strategy / form                       | Cursor fields provided                                                |
|---------------------------------------|------------------------------------------------------------------------|
| `pagination: cursor_token`            | `cursor.token`                                                         |
| `pagination: page_number`             | `cursor.page`                                                          |
| `pagination: offset`                  | `cursor.offset`; `cursor.offset_end` when `batch_size` is set          |
| `pagination: scroll_id`               | `cursor.scroll_id`                                                     |
| `pagination: next_url_in_body`        | `cursor.next_url`                                                      |
| `pagination: graphql_relay`           | `cursor.<cursor_var>` (the declared var name)                          |
| `pagination: link_header`             | `cursor.next_link`                                                     |
| `progress: latest_event_timestamp`    | `cursor.last_timestamp`                                                |
| `progress: max_event_field`           | `cursor.last_timestamp`                                                |
| `progress: use_now`                   | `cursor.last_timestamp`                                                |
| `progress: async_job` (when `on_complete.cursor_update.kind` is `use_now` or `latest_event_timestamp`) | `cursor.last_timestamp`         |
| `progress: time_window`               | `cursor.window_start`, `cursor.window_end`                             |
| `progress: async_job`                 | `cursor.phase`, plus all names from `submit.extract` + `poll.extract`  |
| `requests[].extract[].target: cursor` | `cursor.<name>` for each author-declared binding                       |

---

## Predicates

Boolean expressions used in `requests[].if`,
`auth.multi_mode.branches[].when`, `Value.select.branches[].when`,
`async_job.poll.complete_when`, and
`pagination.scroll_id.complete_when`. Discriminated union — exactly
one variant key.

| Form                                                | Meaning                                                  |
|-----------------------------------------------------|----------------------------------------------------------|
| `{eq:  {path: <Path>, value: <Value>}}`             | Path equals Value.                                       |
| `{gt:  {path: <Path>, value: <Value>}}`             | Path > Value (numbers, durations, RFC 3339 timestamps).  |
| `{lt:  {path: <Path>, value: <Value>}}`             | Path < Value.                                            |
| `{gte: {path: <Path>, value: <Value>}}`             | Path >= Value.                                           |
| `{lte: {path: <Path>, value: <Value>}}`             | Path <= Value.                                           |
| `{present: <Path>}`                                 | Path resolves to a non-null value.                       |
| `{not: <Predicate>}`                                | Negation.                                                |
| `{and: [<Predicate>, ...]}`                         | Conjunction (short-circuits).                            |
| `{or:  [<Predicate>, ...]}`                         | Disjunction (short-circuits).                            |
| `{literal_bool: true \| false}`                     | Constant truth value.                                    |

`gt | lt | gte | lte` are ordered comparisons. The LHS (`path`)
resolves to a namespace ref; the RHS (`value`) is any Value. Targets
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
