# Schema reference

Per-field lookup for the YAML spec the runner consumes — every field,
every rule, every namespace, every Value form. Grep this file by field
name. For runtime behaviour see [`runtime.md`](runtime.md); for the
catalogue of vendor patterns see [`api-methods.md`](api-methods.md); for
state and cache persistence see [`stores.md`](stores.md); for end-to-end
usage walkthroughs see [`usage.md`](usage.md).

A spec is one YAML (or JSON) document with these top-level keys:

| Key            | Required | Section                              |
|----------------|----------|--------------------------------------|
| `ir_version`   | yes      | [#ir_version](#ir_version)           |
| `state`        | no       | [#state](#state)                     |
| `auth`         | yes      | [#auth](#auth)                       |
| `requests`     | yes      | [#requests](#requests)               |
| `pagination`   | yes      | [#pagination](#pagination)           |
| `progress`     | yes      | [#progress](#progress)               |
| `error`        | no       | [#error](#error)                     |

Cross-cutting:

- [Values](#values) — the universal dynamic-field type used everywhere
  a string, number, or boolean could appear.
- [Paths](#paths) — namespace-rooted dotted-string references.
- [Predicates](#predicates) — boolean expressions for `if:`,
  `terminate_when:`, `multi_mode.branches[].when:`,
  `Value.select.branches[].when:`.
- [Namespaces](#namespaces) — every root that paths and refs can start
  with, and where each is written.
- [Types](#types) — the closed set of `state.<name>.type` verbs.
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

A flat map of typed field declarations. Each key is a state field name;
each value is a [field declaration](#state-field-declaration). The map
covers operator-supplied configuration (URLs, API keys, page sizes),
high-water marks persisted across drains (timestamps, server cursors),
and per-drain scratch slots written by `pagination:`.

Optional — omit when the spec has no state at all (rare; most templates
declare at least a URL and credentials).

```yaml
state:
  url:
    type: url
    default: "http://localhost:9999"
  api_key:
    type: secret
  page_size:
    type: int
    default: 5
  last_timestamp:
    type: timestamp
    format: rfc3339
    default: {subtract: [{now: true}, "720h"]}
  next_token:
    type: string
```

### State field declaration

| Field      | Required | Description |
|------------|----------|-------------|
| `type`     | yes      | One of `string`, `int`, `bool`, `secret`, `duration`, `timestamp`, `url`, `enum`. See the [type table](#types). |
| `default`  | no       | Any [Value](#values), not just a literal. The default is applied on first run (or whenever the operator supplies no input) and may compose `{now: true}`, `{subtract: [...]}`, `{ref: ...}`, etc. |
| `values`   | no       | Closed enumeration. Required and valid only when `type: enum`. |
| `format`   | no       | Wire-format hint for `type: timestamp` and `type: duration`. Closed-set verb (`rfc3339`, `rfc3339nano`, `unix_seconds`, `unix_millis`) or a Go layout string (e.g. `"2006-01-02T15:04:05.000-0700"`). Default for `timestamp` is `rfc3339`. Ignored on other types. |

### Inferred lifetime

A state field's *lifetime* — whether it is operator config, per-drain
scratch, or a persistent high-water mark — is **not** declared on the
field. It is derived from where the field appears as the `to:` of a
write:

| Lifetime          | Inference rule                                                                   | Examples                                          |
|-------------------|----------------------------------------------------------------------------------|---------------------------------------------------|
| Operator config   | Has a `default:` (or is operator-supplied) and is never the `to:` of any write.  | `state.url`, `state.api_key`, `state.page_size`   |
| Per-drain scratch | Appears as the `to:` of any `pagination.*.to` write.                             | `state.next_token`, `state.page`, `state.next_url`|
| Persistent        | Appears as the `to:` of any `progress:` write, or of any `requests[].extract` with `to: state.*`. | `state.last_timestamp`, `state.window_start` |

The runner wipes per-drain fields at the start of every drain. Operator
config and persistent fields survive restarts (see
[`stores.md`](stores.md)).

A field written from both `pagination:` and `progress:` is rejected at
validate time. This conflict almost never arises in practice; when it
does, the author renames one of the destinations.

### Secret propagation

`secret`-typed fields carry a non-logging contract that propagates
through every `Value` form they participate in:

- Runtime callers MUST omit the value (and any `Format` / `Concat` /
  `Base64` / interpolated string Value that transitively contains it)
  from rendered error messages, logs, and debug output.
- Where the runtime cannot redact a transitive composition (e.g. a
  `Concat` whose inputs include both a literal URL and a secret token),
  the entire composed Value MUST be treated as secret.

Callers MUST call `schema.IsSecret(d, v)` on any Value before rendering
it into a log line, error message, or debug surface. The helper walks
`Concat` / `Format.Value` / `Base64` / `Object` values / `List` /
`Select` branches / `Select.Default` / `Ref.Default` and returns true
when any reachable `Ref` resolves to a secret-typed state field.

### Rules

- Field names must be unique within `state`.
- `enum` fields must carry a non-empty `values` list.
- Every `to: state.<name>` write (from `pagination:`, `progress:`, or
  `requests[].extract`) must target a declared state field.
- Every `{ref: state.<name>}` in any Value or Predicate must resolve
  to a declared state field.

---

## `auth`

Discriminated union — exactly one variant key. Applied to every
request, including OAuth2 token fetches (which use the operator's
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

### `auth.sigv4`

Signs every request with AWS Signature Version 4. The signature is
computed locally per request, immediately before send; it is never
cached.

| Field               | Required | Description                                                                          |
|---------------------|----------|--------------------------------------------------------------------------------------|
| `region`            | yes      | [Value](#values) — AWS region for the credential scope (e.g. `us-east-1`).           |
| `service`           | yes      | [Value](#values) — AWS service name for the credential scope (e.g. `execute-api`).   |
| `access_key_id`     | no       | [Value](#values) — AWS access key id.                                                 |
| `secret_access_key` | no       | [Value](#values) — AWS secret access key; typically secret.                          |
| `session_token`     | no       | [Value](#values) — STS session token for temporary credentials; typically secret.   |

Credentials are all-or-nothing: set `access_key_id` **and**
`secret_access_key` together for static credentials, or omit both to use
the AWS default credential chain (environment, shared config, IMDS /
container / IAM role). `session_token` may only accompany a full static
pair.

```yaml
# Static credentials from spec state.
auth:
  sigv4:
    region: "us-east-1"
    service: "execute-api"
    access_key_id: {ref: state.aws_access_key_id}
    secret_access_key: {ref: state.aws_secret_access_key}

# Default credential chain (no keys in the spec).
auth:
  sigv4:
    region: "us-east-1"
    service: "execute-api"
```

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
| `cache`         | no       | [Cache](#cache) — when set, the token is cached in `cache.<name>`.         |

#### `auth.oauth2.password_grant`

OAuth2 Resource Owner Password Credentials (RFC 6749 §4.3).

| Field       | Required | Description                                          |
|-------------|----------|------------------------------------------------------|
| `token_url` | yes      | [Value](#values).                                    |
| `username`  | yes      | [Value](#values).                                    |
| `password`  | yes      | [Value](#values) — typically secret.                 |
| `client_id` | no       | [Value](#values); some servers Basic-auth instead.   |
| `scopes`    | no       | List of strings; joined with spaces.                 |
| `cache`     | no       | [Cache](#cache).                                     |

### `auth.multi_mode`

Dispatch between auth strategies based on a [Predicate](#predicates)
over `state.*` or any other namespace.

| Field      | Required | Description                                                                                 |
|------------|----------|---------------------------------------------------------------------------------------------|
| `branches` | yes      | List of `{when: <Predicate>, auth: <Auth>}`; first matching branch wins.                    |
| `default`  | yes      | Bare `Auth` value (same shape as `branches[].auth`). Fallback when no branch matches.       |

```yaml
auth:
  multi_mode:
    branches:
      - when: {eq: {path: state.region, value: "gov"}}
        auth: {bearer: {token: {ref: state.gov_token}}}
    default: {bearer: {token: {ref: state.commercial_token}}}
```

### Auth rules

- `multi_mode` may not nest another `multi_mode`.
- `multi_mode.default` is required and may itself be `none: {}` — the
  explicit form when the operator wants the request to fire
  unauthenticated whenever no branch's predicate matches.
- `auth.oauth2` must carry exactly one grant key
  (`client_credentials` or `password_grant`). Zero or multiple grant
  keys are rejected.
- `auth.sigv4` requires `region` and `service`. `access_key_id` and
  `secret_access_key` must be set together or both omitted (omitting
  both selects the AWS default credential chain); `session_token` may
  only be set alongside a full static-credential pair.
- Every `{ref: state.<name>}` used inside any auth Value must resolve
  to a declared field.

### Cache

Both `auth.oauth2.<grant>.cache` and `requests[].cache` (§
[Per-entry fields](#per-entry-fields)) carry the same `Cache` struct.
The cache writes its captured value into a `cache.<name>` slot, which
other request slots read back with `{ref: cache.<name>}`. Cache slots
are process-memory only; they are cleared on runner restart and never
persisted (see [`stores.md`](stores.md)).

| Field         | Required | Description |
|---------------|----------|-------------|
| `to`          | yes      | `cache.<name>` slot. Reads use `{ref: cache.<name>}`. The runner allocates the slot if absent; authors do not declare it under `state:`. |
| `expires_at`  | yes      | [Value](#values) resolving to a `time.Time`. Accepts a `default:` for APIs that return no explicit expiry (e.g. `{ref: response.body.expires_in, default: "1h"}`). |
| `buffer`      | yes      | Go-style duration. Re-fetch when the remaining lifetime falls below this. |

```yaml
auth:
  oauth2:
    client_credentials:
      token_url: {ref: state.token_url}
      client_id: {ref: state.client_id}
      client_secret: {ref: state.client_secret}
      cache:
        to: cache.access_token
        expires_at: {ref: response.body.expires_in, default: "1h"}
        buffer: 60s
```

The cached value is whatever the cached step writes — the OAuth2 access
token for a grant, or the captured body field for a `requests[].cache`
login. Subsequent requests reference it with `{ref: cache.<name>}`.

### Refresh tokens

Long-lived OAuth2 refresh tokens that a user pastes in are **not** a
separate auth variant. They are a secret-typed state field that feeds
`auth.bearer.token`:

```yaml
state:
  refresh_token:
    type: secret
auth:
  bearer:
    token: {ref: state.refresh_token}
```

---

## `requests`

Ordered list of HTTP requests run on every iteration. At least one
required. Exactly one request marks itself as the events producer by
setting [`events_at`](#the-events-producer-events_at) — the request
whose decoded body holds the events list. Every other request runs for
its side effects (auth, extracts, async polling) and emits no events.

Each request decodes its own body with [`decode`](#decode-chain).
Omitting `decode` defaults to JSON, so a chain only needs it when the
payload is something the HTTP transport does not transparently undo (a
gzipped file, a ZIP archive, a CSV export). Decoding never consults
`Content-Type`: an omitted `decode` is always JSON and an explicit
chain always wins.

Every request declares an absolute `url:` Value. There is no
spec-level URL prefix; templates compose their URLs either with
[string interpolation](#string-interpolation) (`"${state.url}/events"`)
or with `{concat: [...]}`.

```yaml
requests:
  - method: GET
    url: {concat: [{ref: state.url}, "/events"]}
    query:
      since: {ref: state.last_timestamp}
      limit: "${state.page_size}"
```

### Per-entry fields

| Field              | Required                       | Description |
|--------------------|--------------------------------|-------------|
| `id`               | when referenced from elsewhere | Step identifier. Required for `steps.<id>.body.<path>` references and for fan_out. |
| `method`           | yes                            | HTTP verb (`GET`, `POST`, …). |
| `url`              | yes                            | Absolute URL [Value](#values). |
| `query`            | no                             | Map of name → [Value](#values). |
| `headers`          | no                             | Map of name → [Value](#values). |
| `body`             | no                             | Discriminated union: `json:` (map of [Value](#values)), `form:` (same), or `raw:` ([Value](#values)). Exactly one. |
| `extract`          | no                             | List of [ExtractVar](#extractvar) — capture fields out of the response. |
| `fan_out`          | no                             | [FanOut](#fanout) — per-item iteration over a list Value. Mutually exclusive with `cache`. |
| `expect_status`    | no                             | List of HTTP status codes treated as success. Defaults to `[200]`. |
| `if`               | no                             | [Predicate](#predicates) — skip the step when false. |
| `terminate_when`   | no                             | [Predicate](#predicates) — when true, stop looping this step and proceed to the next request. See [Request-level loops](#request-level-loops). |
| `on_status`        | no                             | Map of status code → verb (`skip`, `fail`, `empty_events`, `invalidate_cache`). Per-step override of `error.mode` for that status. |
| `decode`           | no                             | A scalar `json` / `ndjson`, or a list of decode stages (the [decode chain](#decode-chain)). Omitted ⇒ `json`. Explicit `decode: []` is rejected. |
| `events_at`        | when this step is the producer  | [Path](#the-events-producer-events_at) rooted at `response.body.<path>` or `steps.<id>.body.<path>` locating the events list. Empty (`events_at: ""`) means "the body root IS the events list". Presence marks this step as the producer; exactly one request must set it. |
| `cache`            | no                             | [Cache](#cache) — generic step-level cache for non-OAuth2 cached logins. Mutually exclusive with `fan_out`. |

### Request rules

- At least one element.
- `if:` and `on_status:` may appear on any step. When a step is skipped
  (by `if=false` or by `on_status: <code>: skip`):
  - `{ref: steps.<that-id>.body.<...>}` resolves to a zero Value.
  - `{present: steps.<that-id>.body.<...>}` returns `false`.
  - Downstream `{select}` branches must guard with `{present: ...}` to
    handle the skipped path.
- `on_status` keys must be HTTP status codes in `[100, 599]`. Values
  are one of the closed action set:
  - `skip` — drop the response, emit no events, advance progress as if
    successful (the canonical "304 Not Modified" handling).
  - `fail` — non-success: emit no events and stop the iteration with an
    error.
  - `empty_events` — non-success: emit no events but DO fire `progress:`
    writes (the "429-with-`ignore_api_errors`" pattern).
  - `invalidate_cache` — drop the cached value backing the active
    auth's `cache.<name>` slot AND every `requests[].cache` slot, then
    treat the response as a non-event "retry next iteration" signal.

  `retry` is intentionally absent until the retry/backoff contract
  lands.
- Steps with `id:` must have unique IDs across the list.
- `fan_out.as` must not shadow any existing namespace name.
- `fan_out.over` must resolve to a list-typed Value. The validator
  rejects obvious non-list top-level forms (literal scalars,
  `{now: ...}`, `{format: ...}`, `{base64: ...}`, `{object: ...}`);
  list-ness for composite forms (`{ref: ...}`, `{concat: ...}`,
  `{list: ...}`) is decided at lowering time when the runtime can see
  the resolved type.
- Exactly one request must set `events_at` to mark the events producer.
  Zero producers and more than one producer are both rejected. There is
  no implicit-last fallback; the producer is always the explicit
  `events_at` carrier. A `HEAD` request cannot be the producer (no body
  to decode).

### Request-level loops

`terminate_when:` is the request-level loop primitive. After the
response is received, the predicate is evaluated against the response
and the active `state.*`. If true, the loop exits and the runner moves
to the next request. If false, the same request is re-fired.

`terminate_when:` is the schema's only loop construct outside
[`pagination:`](#pagination); it covers async-job submit/poll/fetch
patterns without any dedicated state machine. Every phase is a regular
request:

```yaml
requests:
  - id: submit
    method: POST
    url: "${state.url}/exports"
    expect_status: [202]
    extract:
      - {to: state.export_id, from: response.body.export_id}

  - id: poll
    method: GET
    url: "${state.url}/exports/${state.export_id}/status"
    expect_status: [200, 404]
    terminate_when:
      and:
        - {present: response.body.status}
        - {eq: {path: response.body.status, value: complete}}
    extract:
      - {to: state.result_url, from: response.body.result_url}

  - id: fetch
    method: GET
    url: {ref: state.result_url}
    events_at: response.body.data
```

The phase information lives in `state.export_id` and `state.result_url`
as plain extracts. No phase machine; no dedicated async variant; no
state field declared by the runner on the author's behalf.

### ExtractVar

| Field    | Required | Description |
|----------|----------|-------------|
| `to`     | yes      | `state.<name>` (persistent — must be declared under `state:`) or `extract.<name>` (per-iteration, no declaration needed). |
| `from`   | yes      | Namespace-rooted [Path](#paths). One of `response.body.<path>`, `response.header.<name>`, `steps.<id>.body.<path>`, `steps.<id>.header.<name>`. The runner dispatches body-walk vs header-lookup off the path root. |
| `coerce` | no       | Type coercion verb (e.g. `to_string`, `to_int`). |
| `regex`  | no       | Optional regex transform applied to the resolved value before writing. See [`regex`](#values) under Values. |

The destination's namespace prefix (`state.*` vs `extract.*`) decides
persistence. There is no separate `target:` field.

### FanOut

```yaml
fan_out:
  over: <Value>               # a list Value
  as:   <string>              # per-item binding name; refs use {ref: <as>.<path>}
  merge: flatten | wrap       # how per-item responses combine
```

The runner evaluates `over:` to a list, then runs the step once per
item with the author-chosen `as:` name bound to the current value.
Inside the step (and inside `extract.from` paths it owns),
`{ref: <as>.<path>}` resolves against the current item — `<as>` is
whatever the operator wrote, NOT a fixed prefix.

`merge: flatten` (default) concatenates per-item response bodies, which
must each decode as JSON lists; a non-list body is a template error.
`merge: wrap` returns the per-item bodies as elements of a list,
preserving each item's response shape.

Per-item errors run through the same `on_status` / `error.mode`
dispatcher as a single-request step: `skip` / `empty_events` drop the
item, `fail` aborts the drain, `invalidate_cache` clears the active
auth + step caches and stops the fan-out early. `requests[].cache`
cannot be combined with `fan_out`.

### Decode chain

`decode` is a per-request pipeline. Omitting it defaults to `json`. The
scalar forms `json` and `ndjson` are the common case — a single terminal
decoder. For payloads the HTTP transport does not transparently undo (a
gzipped file, a ZIP archive, a CSV export), `decode` takes a list of
stages instead:

```yaml
requests:
  - id: manifest
    method: GET
    url: "${state.url}/manifest"          # plain JSON — decoded as json (default)
    extract:
      - {to: state.export_url, from: response.body.export_url}
  - id: consume
    method: GET
    url: {ref: state.export_url}          # a gzipped CSV export
    decode:
      - gzip: {}
      - csv:
          header: present
    events_at: ""
```

A chain is zero or more **byte-transform** stages followed by exactly one
**terminal decoder** as the last element:

| Stage     | Kind           | Argument | Effect |
|-----------|----------------|----------|--------|
| `gzip`    | byte-transform | none (`{}`) | Decompresses the upstream stream (RFC 1952). |
| `zip`     | byte-transform | optional `glob` | Expands a ZIP archive into its members. |
| `csv`     | terminal       | required `header` | Decodes delimited rows into events. |
| `json`    | terminal       | none (`{}`) | Decodes the stream as one JSON value. |
| `ndjson`  | terminal       | none (`{}`) | Decodes one JSON value per line. |

- `csv.header` is `present` (the first row names the fields; each later row
  decodes to a map) or `absent` (each row decodes to a positional list).
- `zip.glob` (optional) selects members by their base name, e.g. `"*.csv"`;
  empty selects every member. Members are decoded in name-sorted order and
  their events concatenated. A `glob` that selects nothing yields no events.
- The scalar form accepts only `json` / `ndjson` (the argument-less
  terminals); `gzip`, `zip`, and `csv` must use the list form.

Transparent `Content-Encoding: gzip` is handled by the HTTP transport and
needs no `decode` stage — the chain is for file payloads the transport leaves
alone (typically signalled by `Content-Type: application/gzip | application/zip
| text/csv`).

### The events producer (`events_at`)

Setting `events_at` on a request marks it as the events producer.
Exactly one request must do so; that request's success/skip/empty
verdict decides the iteration outcome.

- `events_at` is namespace-rooted: `response.body.<path>` reads the
  producer's own decoded response; `steps.<id>.body.<path>` reads a
  labelled prior step's response (the producer is still the request
  carrying `events_at`, even when the body it walks belongs to another
  step). The empty (zero) Path means "body root" — the whole decoded
  body IS the events list (or a single event when the terminal is
  row-oriented). Bare dotted strings (`data.events` with no namespace
  prefix) are rejected.
- The row-oriented terminals (`csv`, `ndjson`) decode the body to a list of
  rows. When `events_at` is empty (zero Path), each row IS one event. `csv`
  requires `events_at` empty. `ndjson` also accepts a non-empty `events_at`:
  the trailing body segments below the namespace root are applied to EACH
  decoded line and the flattened sequence is the events list.
- A `HEAD` producer is rejected — there is no body to decode.
- The HTTP status-code success set is configured per-step via
  `requests[].expect_status`; there is no chain-wide success-status field.

### `events.*` namespace

Once `events_at` resolves, the decoded events list is exposed as the
[`events.*`](#namespaces) namespace. This separates the events
projection from response-body field access — even when a response body
also has a top-level field literally named `events`.

| Form                       | Resolves to                                              |
|----------------------------|----------------------------------------------------------|
| `events.*.<field>`         | The `<field>` value projected across every event.        |
| `events.first.<field>`     | `<field>` from the first event in declared order.        |
| `events.last.<field>`      | `<field>` from the last event in declared order.         |
| `events.<int>.<field>`     | `<field>` from the event at position `<int>` (0-based).  |
| `events.count`             | Number of events on the current page.                    |

The `events.*` namespace is per-iteration. The runner does not buffer
events across pages; reducers (`max`, `min`, `first`, `last`, `count`)
are streaming operations over the current page.

An empty page (zero events) is still a valid accepted page-response
when its status passes `expect_status:` and `on_status:`. The loop
controls itself; an empty page simply triggers the next page fetch.

---

## `pagination`

Discriminated union — exactly one variant key. Named variants cover the
common 80%; the `custom:` variant exposes the primitive form for APIs
that don't fit.

### Execution order per page

1. Request fires; response received.
2. `terminate_when` (variant-specific default or author-supplied) is
   evaluated against `response.*` and the pre-advance `state.*`. If
   true, the loop ends — no advance writes fire for this page.
3. Otherwise, the variant's `to:` write (or `custom.advance:` writes)
   runs, populating the per-drain state slot.
4. Loop.

This ordering is important for `custom:` blocks: termination should
not depend on the side effect of `advance:`, so the predicate reads
`response.*` directly.

### `pagination.none`

No pagination — one request per drain.

```yaml
pagination: {none: {}}
```

### `pagination.cursor_token`

Server returns a next-cursor token (or `null`/missing when no more
pages). Covers opaque cursors, GraphQL Relay end cursors, scroll IDs,
and "next page number from body".

| Field             | Required | Description |
|-------------------|----------|-------------|
| `from`            | yes      | [Path](#paths) rooted at `response.body.<path>`, `response.header.<name>`, or `steps.<id>.body.<path>` to the next-cursor field. |
| `to`              | yes      | `state.<name>` destination. Per-drain (lifetime inferred). The state field must be declared under `state:`. |
| `terminate_when`  | no       | [Predicate](#predicates). Default: `{not: {present: <from>}}` — loop ends when the source path resolves to absent. |

```yaml
pagination:
  cursor_token:
    from: response.body.meta.next_token
    to:   state.next_token
```

The request reads the active token back with
`{ref: state.next_token, default: ""}` (or with no default when the
bootstrap iteration should omit the slot entirely).

### `pagination.next_url`

Server returns a fully-formed next-page URL — either as a body field or
inside a `Link` header.

| Field             | Required | Description |
|-------------------|----------|-------------|
| `from`            | yes      | [Path](#paths) — `response.body.<path>`, `response.header.<name>`, or `steps.<id>.body.<path>`. |
| `to`              | yes      | `state.<name>` destination, typed `url`. Per-drain. |
| `regex`           | no       | Regex applied to the resolved string before writing. Useful for `Link: <url>; rel="next"` parsing. |
| `capture`         | no       | Capture group index for `regex:` (1-based). |
| `terminate_when`  | no       | [Predicate](#predicates). Default: `{not: {present: <from>}}` — empty / missing / non-matching URL ends the drain. |

```yaml
pagination:
  next_url:
    from: response.header.link
    to:   state.next_url
    regex: '<(.*?)>;\s*rel="next"'
    capture: 1
```

The request reads the next URL back with
`{ref: state.next_url, default: "${state.url}/<bootstrap-path>"}`. The
default fires on the first iteration when no next URL is yet known.

### `pagination.counter`

Client-incremented counter. Replaces both `page_number` and `offset`
patterns: choose `start:`/`step:` to match the API.

| Field             | Required | Description |
|-------------------|----------|-------------|
| `to`              | yes      | `state.<name>` destination, typed `int`. Per-drain. |
| `start`           | no       | Starting value. Default `1` (page number); use `0` for offset. |
| `step`            | no       | Increment per accepted page. Default `1`. For offset-style pagination, `{ref: state.page_size}`. |
| `terminate_when`  | no       | [Predicate](#predicates). Default: short-page detection — `{lt: {path: events.count, value: <step>}}`. |

```yaml
pagination:
  counter:
    to:    state.page
    start: 1
    step:  1
    terminate_when:
      not: {present: response.body.meta.has_next}
```

The request reads the counter back with `{ref: state.page}` (typically
wrapped as `"${state.page}"` for query placement).

### `pagination.custom`

Author-controlled primitive form for APIs that don't fit the named
variants.

| Field             | Required | Description |
|-------------------|----------|-------------|
| `advance`         | yes      | List of `{to, from, regex?, coerce?}` writes. Each `to:` must be a declared per-drain state field; each `from:` is any [Value](#values). |
| `terminate_when`  | yes      | [Predicate](#predicates). No default — `custom:` blocks state termination explicitly. |

```yaml
pagination:
  custom:
    advance:
      - to: state.next_token
        from: {ref: response.body.cursor}
      - to: state.reset_at
        from: {ref: response.header.x-rate-limit-reset}
    terminate_when:
      or:
        - {not: {present: response.body.cursor}}
        - {lt: {path: response.body.remaining, value: 1}}
```

Termination reads `response.*` directly so the predicate sees the
pre-advance values; see [execution order](#execution-order-per-page).

### Pagination rules

- The destination of every named variant's `to:` (and every
  `custom.advance[].to:`) is a per-drain `state.<name>` field. Per-drain
  fields are wiped at the start of every drain — a drain that fails
  mid-page re-bootstraps pagination on the next start. Recovery is the
  author's responsibility via `progress:` writes that persist to
  long-lived state fields.
- The request reads per-drain state back explicitly with
  `{ref: state.<name>}` Values. Placement follows where the ref is
  written: `query: {p: {ref: state.next_token}}` rides as a query
  param; `body: {json: {p: {ref: state.page}}}` rides in the JSON
  body.
- `next_url`'s `to:` field must be typed `url`; `counter`'s `to:` field
  must be typed `int`; `cursor_token`'s `to:` field is typically
  `string`.
- A request reading per-drain state on the first iteration must supply
  a `default:` on the `{ref: ...}` (or rely on the field's declared
  `default:` if any) — the per-drain slot is empty at drain start.

---

## `progress`

A flat list of state writes evaluated after each accepted page. There
are no named variants; the primitive form IS the only form. The empty
list (or an omitted `progress:` block) means "no progress tracking".

Every entry writes one persistent `state.*` field.

```yaml
progress: []                            # no progress tracking
```

```yaml
# max-of-events high-water mark, merged with prior state
progress:
  - to: state.last_timestamp
    from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}
```

```yaml
# clock-driven cursor
progress:
  - to: state.last_timestamp
    from: {now: true}
```

```yaml
# sliding [window_start, window_end) window
progress:
  - to: state.window_start
    from: {ref: state.window_end, default: {subtract: [{now: true}, "30d"]}}
  - to: state.window_end
    from: {now: true}
```

```yaml
# pin the first event's id for sort-order-resistant tracking
progress:
  - to: state.last_event_id
    from: {ref: events.first.id}
```

### Entry shape

| Field    | Required | Description |
|----------|----------|-------------|
| `to`     | yes      | `state.<name>`. Persistent across drains (lifetime inferred). The field must be declared under `state:`. |
| `from`   | yes      | Any [Value](#values). Has access to every namespace the request scope has: `state.*`, `cache.*`, `events.*`, `response.body.*`, `response.header.*`, `steps.<id>.body.*`, `steps.<id>.header.*`. May use reducers (`max`/`min`/`first`/`last`/`count`) over `events.*`, arithmetic primitives, refs, etc. |
| `coerce` | no       | Type coercion verb. |
| `regex`  | no       | Optional regex transform. |

### Evaluation semantics

- **One firing per accepted page-response.** A page-response is
  accepted when its status passes `requests[].expect_status` and any
  `on_status:` action did not abort the drain. Progress fires even when
  `events.*` is empty — server-provided cursors, ingestion timestamps,
  and other response-body fields can be persisted independently of
  event production. Progress does NOT fire when a request is skipped by
  `if:`, aborted by `on_status: fail`, or errored out before a response
  was decoded.
- **Batch semantics across entries.** All `from:` expressions evaluate
  against the same snapshot of state. A later entry referencing
  `state.window_start` sees the pre-write value of that field,
  whatever order the entries appear in. The window pattern above is
  correct regardless of declaration order: when the new
  `window_start` reads `state.window_end`, it always sees the *old*
  `window_end` value.
- **Sink delivery before commit.** The runner emits the page's events
  to the sink, then evaluates progress writes, then commits state.
  Failure between events-sent and state-committed is acceptable
  (at-least-once); failure before events-sent leaves state unchanged.
- **No implicit accumulation.** A cumulative high-water mark is written
  explicitly:
  `from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}`.
  The runner never guesses what "merging" means for a given field.
- **First-write-wins via ref default.** "Persist on the very first
  drain only" is just `from: {ref: state.x, default: <new-value>}`:
  when `state.x` is set, it writes itself back (no-op); when absent,
  the default kicks in.

### First-run seeding

First-run seeding is the `default:` on the destination state field.
There is no separate first-run mechanism.

```yaml
state:
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}
```

A `default:` accepts the full Value language, including `{now: true}`,
`{subtract: [...]}`, `{ref: ...}`, and reducers. The runner applies it
on first run (or whenever the operator does not supply a value), and
`progress:` writes take over from there.

---

## `error`

How non-success HTTP responses (and network / decode failures) are
surfaced.

| Field          | Required | Description |
|----------------|----------|-------------|
| `mode`         | yes      | `standard` (default), `warn`, or `fail`. See [`runtime.md`](runtime.md) for semantics. |
| `include_body` | no       | When `true`, include the response body in the error message. Off by default. |

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

- **Bare YAML string scalar:** treated as a string literal, with one
  exception — any string in a Value position is scanned for
  [`${...}` interpolation segments](#string-interpolation). A string
  with no `$` is a plain literal.
- **YAML integer scalar:** `LiteralInt`.
- **YAML boolean scalar:** `LiteralBool`.
- **YAML null:** zero Value (`IsZero`). Authors should omit the field
  entirely rather than writing `key: null`.
- **YAML mapping:** discriminated by the presence of a recognised key.
  If no discriminator key is found, the mapping decodes as `Object`.

### Discriminated forms

| Form                                                | Produces |
|-----------------------------------------------------|----------|
| `"foo"`                                             | string literal (with interpolation; see below). |
| `100`                                               | int64. |
| `true` / `false`                                    | bool. |
| `{literal_string: "x"}`                             | string literal, no interpolation. Use when YAML would otherwise misparse (e.g. a string of all digits). |
| `{ref: state.url}`                                  | resolved value from any namespace. |
| `{ref: state.token, default: ""}`                   | resolved value, falling back to the default when unset. |
| `{now: true}`                                       | `time.Time` for the current moment. |
| `{concat: [<Value>, ...]}`                          | string concatenation; at least 2 elements (`{concat: []}` and `{concat: [<single>]}` are rejected). |
| `{select: {branches: [...], default: <Value>}}`     | conditional; first matching branch wins, else `default`. |
| `{format: <verb-or-layout>, value: <Value>}`        | coerce `value` to a formatted representation. |
| `{base64: <Value>}`                                 | base64-encode the inner Value's string. |
| `{list: [<Value>, ...]}`                            | list literal. |
| `{object: {<key>: <Value>, ...}}`                   | map literal; inner keys are NOT re-interpreted as discriminators. |
| `{add: [<Value>, <Value>]}`                         | `time + duration → time`; `duration + duration → duration`; `int + int → int`. |
| `{subtract: [<Value>, <Value>]}`                    | `time - duration → time`; `time - time → duration`; `duration - duration → duration`; `int - int → int`. |
| `{max: <list-or-projection>}`                       | reducer — largest value. |
| `{min: <list-or-projection>}`                       | reducer — smallest value. |
| `{first: <list-or-projection>}`                     | reducer — first element in declared order. |
| `{last: <list-or-projection>}`                      | reducer — last element in declared order. |
| `{count: <list-or-projection>}`                     | reducer — number of elements. |
| `{slice: {<list-operand>, from?: <int>, to?: <int>}}` | contiguous sub-list of a list-shaped operand. |
| `{regex: {pattern: <string>, from: <Value>, capture?: <int>, default?: <Value>}}` | extracted substring. |

### String interpolation

Any YAML string literal in a Value position is scanned for `${<path>}`
segments. Each segment resolves against the same namespaces as
`{ref: <path>}`. The string desugars to a `{concat: [...]}` Value with
interleaved literal segments and refs:

```yaml
url: "${state.url}/api/${state.endpoint}/events"
```

is exactly:

```yaml
url: {concat: [{ref: state.url}, "/api/", {ref: state.endpoint}, "/events"]}
```

Each `${...}` segment may use a default with the `|` sigil:

- `"${state.next_token|}"` → `{ref: state.next_token, default: ""}`.
- `"${state.page|1}"` → the text after the pipe is parsed as a YAML
  scalar, so non-string defaults work too (here, integer `1`).

Literal `$` needs escaping as `\$`; once inside a `${...}` segment, a
literal `{` after `$` is `\{`. Outside an `${...}` segment, `$` and
`{` are plain text.

Interpolation produces a string. Refs that resolve to non-string values
inside `${...}` are coerced as if wrapped in
`{format: string, value: ...}`. Secret-tainted refs propagate their
secret status to the composed string (same rule as `{concat}` today).

A string literal that should NOT be interpolated (e.g. an opaque token
that may itself contain `${...}`) uses the `{literal_string: "x"}`
form.

### Reducers

A reducer accepts either:

- **A list literal:** `{max: [v1, v2, v3]}` — sugar for
  `{max: {list: [v1, v2, v3]}}`.
- **A list-shaped Value:** `{max: {ref: events.*.timestamp}}` —
  projects the `.timestamp` field across every event and returns the
  max.

`first`, `last`, and `count` operate over the same list-shaped inputs;
`first` and `last` return one element, `count` returns the cardinality.

Reducer inputs may be empty; in that case the reducer's result is a
zero Value (no exception). Predicates downstream of an empty reducer
behave the same way as predicates over any other absent path
(`{present: ...}` returns false; comparisons return false).

### Slice

`slice` returns a contiguous sub-list of a list-shaped operand. Unlike
the reducers it returns a *list*, so it composes as the operand of
another list-consumer (a reducer, `fan_out.over`, or a further slice).

The operand is any list-shaped Value, written under its own
discriminator key alongside the optional bounds:

| Field   | Required | Meaning |
|---------|----------|---------|
| operand | yes      | A list-shaped Value (`ref`, `list`, `concat`, `select`) under its own key. |
| `from`  | no       | Zero-based inclusive start index. Defaults to 0. |
| `to`    | no       | Zero-based exclusive end index. Defaults to the operand length. |

```yaml
{slice: {ref: state.queue, from: 1}}        # drop the head
{slice: {ref: state.queue, from: 1, to: 5}} # bounded window
{slice: {list: [a, b, c, d], to: 2}}        # first two of a literal list
```

Out-of-range and inverted bounds **clamp** to a (possibly empty)
sub-list rather than erroring: `from` past the end yields an empty
list, `to` past the end clamps to the length, and `from >= to` yields
an empty list. An absent operand resolves to an empty list.

The canonical use is a *worklist*: a queue of pending work items held
in a persistent `state` field, refilled by `extract` and consumed one
item per page. `{first: {ref: state.queue}}` reads the head and
`{count: {ref: state.queue}}` tests emptiness; popping the head is a
`progress` write that slices it off:

```yaml
progress:
  - to: state.queue
    from: {slice: {ref: state.queue, from: 1}}
```

The bundled `worklist` template (`skopos template show worklist`) is a
complete worked example.

### Arithmetic

`add` and `subtract` operate over the type pairs in the discriminated
forms table above. Mixed-type operations that don't match a documented
pair (e.g. `time + time`, `int + duration`) are rejected at lowering
time.

`{subtract: [{now: true}, "720h"]}` is the canonical first-run
lookback. `{add: [{now: true}, "1h"]}` is the canonical "expires in 1
hour" cache fallback.

### Format verbs

The `format:` verb set is closed.

| Verb            | Converts to |
|-----------------|-------------|
| `string`        | string representation. |
| `int`           | integer (parse or truncate). |
| `bool`          | boolean. |
| `rfc3339`       | RFC 3339 timestamp string. |
| `rfc3339nano`   | RFC 3339 with nanoseconds. |
| `unix_seconds`  | integer Unix timestamp (seconds). |
| `unix_millis`   | integer Unix timestamp (milliseconds). |
| `duration`      | Go-style duration string. |
| `url_encode`    | percent-encoded URL component (RFC 3986 unreserved + percent). |
| `parse_duration`| Go-style duration string → integer (nanoseconds). |

`parse_duration` outputs a 64-bit signed integer count of nanoseconds.
Targets that cannot represent a 64-bit signed integer natively MUST
surface a lowering error rather than silently truncating.

**Go layout strings.** In addition to the closed-set verbs, `format:`
accepts a Go date layout string (e.g.
`"2006-01-02T15:04:05.000-0700"`). The validator distinguishes by
closed-set membership: a recognised verb is the named parser; anything
else is tried as a Go layout.

For state fields whose wire form is a non-default timestamp shape,
prefer declaring `format:` on the state field
([§state](#state-field-declaration)) and never wrap the ref. The
Value-time `format:` verb is most useful for ad-hoc coercions.

### Regex

`{regex: {pattern: ..., from: ..., capture?: ..., default?: ...}}`
applies a Go regular expression to the resolved string of `from:`. The
return value is the matched substring (or the chosen capture group when
`capture:` is set, 1-based). When no match, the optional `default:`
fires; absent both match and default, the result is a zero Value.

`pagination.next_url`'s `regex:` / `capture:` fields use the same
engine; the same applies to `regex:` on extracts and progress writes.

### Value rules

- A map-shaped Value MUST carry exactly one discriminator key. There
  is no silent fallback for arbitrary maps — wrap a literal map in
  `{object: {…}}`. A map carrying more than one discriminator key is
  rejected at parse time.
- Each discriminator has a closed set of allowed sibling keys
  (`ref` → `default`; `format` → `value`; `regex` → `pattern`, `from`,
  `capture`, `default`; the rest → no extra siblings beyond their
  declared shape). Unknown sibling keys are rejected at parse time so
  typos like `{ref: state.x, defualt: "y"}` do not drop into the void.
- Empty `{and: []}` and `{or: []}` in Predicates are rejected as
  authoring errors. Use `{literal_bool: true | false}` for a constant
  predicate.
- `{ref: <namespace>.<name>}` is valid only when the namespace
  populates `<name>` (see [Namespaces](#namespaces)).

---

## Paths

`Path` is the typed kind for namespace-rooted dotted-string identifiers
used in `{ref: ...}`, `{present: ...}`, `{eq: {path: ...}}`,
`requests[].events_at`, `requests[].extract[].from`,
`pagination.cursor_token.from`, `pagination.next_url.from`, etc.

Primary form: `response.body.data.issues.nodes` (dotted string with a
namespace-root prefix).

Escape form for field names containing dots:

```yaml
{parts: ["steps", "my.weird.id", "body", "events"]}
```

The IR encoder emits the escape form **whenever any segment contains a
`.`**; all other paths emit as a dotted string. This guarantees
byte-stable round-trips through the dotted parser.

A Path is **empty** when `IsZero` is true OR `Parts` is nil /
zero-length. Both forms are equivalent at every reference site; the
validator and codec call `Path.IsEmpty()` for this combined condition.

Bare dotted strings without a namespace prefix (`data.events`) are
rejected at parse / validate time, with a hint pointing at the
namespace-rooted form.

---

## Namespaces

Every Path begins with one of the roots below.

| Root                       | Lifetime                          | Written by                                                                                                                                                                                                              |
|----------------------------|-----------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `state.<name>`             | persisted (or per-drain)          | `state.<name>.default`, `requests[].extract` with `to: state.*`, `pagination.*.to`, `progress[].to`. Lifetime sub-flavour (operator-config / per-drain / persistent) is inferred from write sites; see [§state](#state). |
| `cache.<name>`             | process memory only               | [Cache](#cache) blocks on auth grants or requests. Cleared on runner restart; never persisted.                                                                                                                          |
| `events.<...>`             | per-iteration (current page)      | The runner, after `requests[].events_at` resolves. `events.*.field` projects across all events; `events.first.field` / `events.last.field` are declared-order shortcuts; `events.<int>.field` is positional; `events.count` is cardinality. |
| `extract.<name>`           | per-iteration                     | `requests[].extract` with `to: extract.*`. Reset at the top of every iteration.                                                                                                                                         |
| `steps.<id>.body.<path>`   | per-iteration (after step runs)   | The decoded response body of a labelled prior step.                                                                                                                                                                     |
| `steps.<id>.header.<name>` | per-iteration                     | The response headers of a labelled prior step.                                                                                                                                                                          |
| `response.body.<path>`     | per-evaluation                    | The active step's decoded response body. Valid in `requests[].events_at`, `pagination.*.from`, `progress[].from`, `requests[].extract.from`, `requests[].terminate_when`, `requests[].cache.expires_at`, and inside predicates. |
| `response.header.<name>`   | per-evaluation                    | The active step's response headers. Same sites as `response.body.*`.                                                                                                                                                    |
| `<fan_out.as>.<path>`      | per-fan-out-iteration             | The author-chosen `fan_out.as` name; bound to the current item for the duration of one per-item iteration.                                                                                                              |

Body-rooted IR slots — `requests[].events_at`, `pagination.*.from`,
`progress[].from`, `requests[].extract.from`,
`requests[].terminate_when`, `requests[].cache.expires_at`,
`auth.oauth2.<grant>.cache.expires_at` — accept the body-flavoured
roots (`response.body.<path>`, `steps.<id>.body.<path>`) and (where
useful) the matching header roots
(`response.header.<name>`, `steps.<id>.header.<name>`).
`pagination.*.from` and `requests[].extract.from` are the two sites
that explicitly accept header roots.

`response.*` roots resolve against the active step's response without
an `id:` qualifier. Authors who need a prior step's response use the
`steps.<id>.body.<path>` / `steps.<id>.header.<name>` form.

---

## Types

The closed set of `state.<name>.type` verbs.

| Type        | Wire shape   | Semantic |
|-------------|--------------|----------|
| `string`    | string       | UTF-8; no parsing. |
| `int`       | integer      | 64-bit signed. |
| `bool`      | boolean      | true/false. |
| `secret`    | string       | Non-logging marker; otherwise like `string`. See [secret propagation](#secret-propagation). |
| `duration`  | string       | Go-style (`"720h"`, `"-30s"`). |
| `timestamp` | string       | Parsed per the declared `format:` ([§state](#state-field-declaration)). Refs always resolve to `time.Time` in-process. |
| `url`       | string       | URL string; no validation at load time. |
| `enum`      | string       | One of the declared `values`. |

The wire form of `timestamp` and `duration` is configurable via the
field's `format:`; the in-process representation is always `time.Time`
(for `timestamp`) or `time.Duration` (for `duration`).

---

## Predicates

Boolean expressions used in `requests[].if`,
`requests[].terminate_when`, `auth.multi_mode.branches[].when`,
`Value.select.branches[].when`, and `pagination.*.terminate_when`.
Discriminated union — exactly one variant key.

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

All predicates are **absent-tolerant**. A Path that resolves to absent
makes `present` return false, makes `eq` / `gt` / `lt` / `gte` / `lte`
return false, and never throws. There is no "force a failure to
terminate" idiom; the loop primitives (`terminate_when:` on a request
and `terminate_when:` on a pagination variant) read predicates
directly.

`gt | lt | gte | lte` are ordered comparisons. The LHS (`path`)
resolves to a namespace ref; the RHS (`value`) is any Value. Targets
that cannot type-check both operands at lower time (e.g. when the path
resolves to a string and the Value is an int literal) MUST surface a
lowering error rather than silently coercing.

The verbs `matches` (regex) and `in` (set membership) are not modelled
— `in` is expressible today as nested `{or: [...]}`; `matches` is
expressible as `{present: {regex: {...}}}` against a string Value.

---

## Design rules

These invariants hold across the whole schema. The validator
(`schema.Validate`) enforces them; violations are rejected at load
time.

### No target-specific leaks

- No embedded expressions or scripting language anywhere in the
  schema.
- All Value forms are structurally tagged. Bare YAML scalars in Value
  positions are string literals (subject to
  [string interpolation](#string-interpolation)), never interpreted as
  expressions.

### Pure discriminated unions

Every union block (`auth`, `pagination`, `body`, `Value`, `Predicate`)
carries **exactly one** variant key. The IR codec rejects any mapping
that carries zero or more than one discriminator key at parse time —
this rule applies equally to `Value` and `Predicate`, not just to the
top-level union blocks.

`progress:` is NOT a union — it is a flat list of writes. The empty
list (or an omitted `progress:` block) is the no-progress form.

Authors who want a literal map at a `Value` position must use the
explicit `{object: {...}}` wrapper. There is no silent fallback to
`Object`.

### Structural validation only

`schema.Validate` checks structure: references resolve, unions have
exactly one key, required fields are present, mutual exclusivities
hold, type shapes match, write destinations exist under `state:`. It
does NOT check whether the in-process runner currently implements the
combination — runtime capability checks live in the runner (see
[`runtime.md`](runtime.md)).

### No hidden namespaces

Every name the runner reads or writes belongs to a namespace the author
can see. There is no auto-injection — pagination, progress, and
extracts all write to explicit `state.<name>` (or `cache.<name>` /
`extract.<name>`) slots that the author named, and the request reads
them back with `{ref: <namespace>.<name>}` Values placed where the
author chose.

### Single source of lifetime

A state field's lifetime is inferred from its write sites; there is no
separate annotation on the declaration. A field written only by
`pagination:` is per-drain. A field written by `progress:` (or
extracted with `to: state.*`) is persistent. A field with `default:`
and no writes is operator config. Conflicts are rejected at validate
time.

### Codec invariants

- Every `Value` round-trips through YAML and JSON byte-identically
  (same map shapes, same key names).
- Slice fields (`requests`, `branches`, `extract`, `concat`, `list`,
  `progress`, `pagination.custom.advance`) preserve YAML/JSON parse
  order.
- Map fields (`state`, `query`, `headers`, `body.json`, `body.form`,
  `object`) are emitted alphabetically by key during marshal. Authors
  who rely on a specific emission order (e.g. an HMAC signature
  pre-image hashing the canonical form) must use a slice-shaped
  construction.
- `schema.Load` rejects `ir_version != "1"` before returning a
  `*Doc`.
- `schema.Validate` operates on a parsed `*Doc`, never on raw bytes.
