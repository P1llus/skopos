# API method reference

The canonical catalogue of API-communication shapes the runner can
express, grouped by concern (authentication, pagination, request shapes,
response parsing, state checkpointing, the async-job pattern, loop
primitives, cross-cutting). Each entry names the shape, gives a
one-paragraph description, and shows the IR knobs that express it. The
per-field reference lives in [`schema.md`](schema.md); the runtime
contract lives in [`runtime.md`](runtime.md); state and cache
persistence are in [`stores.md`](stores.md); end-to-end walkthroughs are
in [`usage.md`](usage.md).

When adding a new API variation, add an entry here in the same PR that
ships the runner change — this document is the "if we add a new API
shape, write it down here" surface.

---

## 1. Authentication

### 1.1 No auth (`auth.none`)

**What it does:** No `Authorization` header or query credential is added
by the runner. Use for public feeds, demo integrations, or APIs whose
credentials live entirely in `requests[].query` / `requests[].headers`.

**IR shape:**

```yaml
auth:
  none: {}
```

### 1.2 Bearer token (`auth.bearer`)

**What it does:** Sends `Authorization: Bearer <token>` where the token
is a `Value` (typically a `{ref: state.<name>}` against a `secret`-typed
state field, or a `{ref: extract.<name>}` captured by a prior step).

**IR shape:**

```yaml
auth:
  bearer:
    token: {ref: state.api_key}
```

**Variants:**

- *Operator-supplied bearer.* The token is a `secret`-typed state field
  with a `default:` (development) or supplied at runtime.
- *Long-lived refresh token.* A refresh token a user has pasted in is
  the same shape as an operator-supplied bearer — just a `secret`-typed
  `state.<name>` fed into `auth.bearer.token`.
- *Token captured from a prior step.* An earlier step in `requests:`
  POSTs to a login endpoint and pulls the access token via `extract:`
  with `to: extract.<name>`. The main request reads it as
  `{ref: extract.<name>}`. When the login itself has its own expiry,
  wrap it with `requests[].cache` (§3.7).
- *Raw-token / non-`Bearer` prefix schemes.* APIs that read the value
  as the literal `Authorization` header, or that demand a non-`Bearer`
  prefix, use `auth.custom` (§1.5).

### 1.3 Basic auth (`auth.basic`)

**What it does:** Base64-encodes `username:password` and sends it as
`Authorization: Basic <encoded>`.

**IR shape:**

```yaml
auth:
  basic:
    username: {ref: state.username}
    password: {ref: state.password}
```

APIs that reuse the lowercase `Authorization: basic` shape to carry an
opaque session token (rather than a base64 `user:pass` pair) are
expressed via `auth.custom` (§1.5) — the prefix is `basic ` but the
content is not basic auth.

### 1.4 API key in a header or query parameter (`auth.api_key`)

**What it does:** A static API key sent in a named header, or — with
`in_query: true` — as a query parameter.

**IR shape:**

```yaml
auth:
  api_key:
    header: X-Api-Key
    value: {ref: state.api_key}
    in_query: false
```

**Common variants:** mixed-case headers (`X-Api-Key`, `x-api-key`,
`api-key`), vendor-specific names (`X-Auth-Token`, `X-RFToken`,
`Private-Token`), compound formats (`accessKey=…;secretKey=…` as a
single header value composed with `{concat: [...]}` or string
interpolation; see §3.8).

### 1.5 Custom auth header / scheme (`auth.custom`)

**What it does:** A single-header auth scheme that does not fit
`bearer` / `basic` / `api_key`. The author supplies the header name and
the full value Value.

**IR shape:**

```yaml
auth:
  custom:
    header: Authorization
    value: "ApiKey ${state.api_token}"
```

**Use cases:** literal `Authorization: <token>` with no scheme prefix;
non-standard `Authorization: ApiKey …` / `Authorization: Token …` /
`Authorization: basic <opaque>` shapes; vendor-specific headers like
`PS-Auth key=…;runas=…`. Additional non-auth headers ride on
`requests[].headers`.

### 1.6 OAuth2 client-credentials grant (`auth.oauth2.client_credentials`)

**What it does:** The runner POSTs `grant_type=client_credentials` to
the configured `token_url` with HTTP Basic `client_id:client_secret`,
plus optional `scopes=...` and `audience=...` parameters (RFC 6749
§2.3.1). The returned access token rides as `Authorization: Bearer
<token>` on every IR-described request. Non-2xx token responses surface
with body-classification metadata only — never the raw bytes.

**IR shape:**

```yaml
auth:
  oauth2:
    client_credentials:
      token_url: {ref: state.token_url}
      client_id: {ref: state.client_id}
      client_secret: {ref: state.client_secret}
      scopes: [read:events, read:incidents]
      audience: "wiz-api"
```

### 1.7 OAuth2 password grant (`auth.oauth2.password_grant`)

**What it does:** POSTs `grant_type=password` with `username` /
`password` in the form body (RFC 6749 §4.3.2). `client_id` is optional
and rides in the form body when set. Same Bearer-injection and
token-fetch error contract as `client_credentials`.

**IR shape:**

```yaml
auth:
  oauth2:
    password_grant:
      token_url: {ref: state.token_url}
      username: {ref: state.username}
      password: {ref: state.password}
      client_id: {ref: state.client_id}
      scopes: [openid, profile]
```

### 1.8 Token caching with the unified `Cache` block

**What it does:** Both OAuth2 token fetches and bespoke login steps
(§3.7) cache their captured value the same way: a `Cache` block writes
into a named `cache.<name>` slot, records an `expires_at` Value, and
re-fetches when the remaining lifetime drops below `buffer`. The cache
namespace is process memory only — `cache.<name>` is cleared on runner
restart and never persisted (see [`stores.md`](stores.md)).

There is one `Cache` struct, not two: the same fields appear under
`auth.oauth2.<grant>.cache` and `requests[].cache`. Read the cached
value from any other slot with `{ref: cache.<name>}`.

**`Cache` fields:**

| Field         | Required | Description |
|---------------|----------|-------------|
| `to`          | yes      | `cache.<name>` slot. Authors do not declare it under `state:`; the runner allocates the slot. |
| `expires_at`  | yes      | [Value](schema.md#values) resolving to a `time.Time`. Accepts a `default:` for APIs that return no explicit expiry — e.g. `{ref: response.body.expires_in, default: "1h"}`. |
| `buffer`      | yes      | Go-style duration. Re-fetch when the remaining lifetime falls below this. |

**IR shape (OAuth2 token caching):**

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

**Cache invalidation.** A request step that declares
`on_status: {401: invalidate_cache}` drops every reachable `cache.*`
slot (the active auth's cache and every `requests[].cache` slot — see
§3.5), then advances as if the page came back empty. The next request
misses the cache and forces a fresh fetch.

### 1.9 Multi-mode auth (`auth.multi_mode`)

**What it does:** Selects an auth strategy at runtime by walking
`branches[].when` predicates in declaration order — first match wins;
`default:` fires when no branch matches. Common shape: a state flag
picks between Bearer, API key, and Custom.

**IR shape:**

```yaml
auth:
  multi_mode:
    branches:
      - when: {eq: {path: state.auth_mode, value: bearer}}
        auth: {bearer: {token: {ref: state.api_token}}}
      - when: {eq: {path: state.auth_mode, value: basic}}
        auth:
          basic:
            username: {ref: state.username}
            password: {ref: state.password}
    default:
      api_key:
        header: X-Api-Key
        value: {ref: state.api_key}
```

`multi_mode.default` is a bare `Auth` value (same shape as
`branches[].auth`). It may itself be `none: {}` when the operator wants
unauthenticated fallthrough. Nested `multi_mode` is forbidden by the
validator.

### 1.10 Session cookie via POST login

**What it does:** A login step POSTs credentials to a session endpoint;
the main step reads the resulting `Set-Cookie` and rides it as a
request header. Express as a multi-step `requests:` chain. The login
step extracts the cookie via `from: response.header.Set-Cookie` into
`extract.<name>`; the main step rides it as a `requests[].headers`
entry via `{ref: extract.<name>}`.

```yaml
requests:
  - id: login
    method: POST
    url: "${state.url}/session/login"
    body:
      json:
        username: {ref: state.username}
        password: {ref: state.password}
    extract:
      - {to: extract.session_cookie, from: response.header.Set-Cookie}

  - id: data
    method: GET
    url: "${state.url}/api/data"
    headers:
      Cookie: {ref: extract.session_cookie}
```

When the login endpoint advertises its own expiry, wrap it with
`requests[].cache` (§3.7) so the round-trip is paid once and skipped on
subsequent drains.

### 1.11 Refresh-token-as-state-field

**What it does:** Long-lived OAuth2 refresh tokens that a user has
already pasted in are NOT a separate auth variant. They are a
`secret`-typed state field that feeds `auth.bearer.token`:

```yaml
state:
  refresh_token:
    type: secret
auth:
  bearer:
    token: {ref: state.refresh_token}
```

The grant-exchange itself — swapping a refresh token for a fresh access
token — is **not** modelled as an auth variant. When such an exchange
is needed, it lives as a regular request step that hits the token
endpoint and writes the access token to `state.<name>` (or to a
`cache.<name>` slot via `requests[].cache`).

### 1.12 AWS Signature Version 4 (`auth.sigv4`)

**What it does:** Signs every request with AWS Signature Version 4 for
AWS APIs (API Gateway, OpenSearch, S3, …). The signature is computed
locally per request, immediately before send, and is never cached — it
is an HMAC over the exact request, so it cannot be reused across pages
or polls.

**IR shape (static credentials):**

```yaml
auth:
  sigv4:
    region: "us-east-1"
    service: "execute-api"
    access_key_id: {ref: state.aws_access_key_id}
    secret_access_key: {ref: state.aws_secret_access_key}
    # session_token: {ref: state.aws_session_token}   # optional, for STS creds
```

**IR shape (AWS default credential chain):** omit `access_key_id` and
`secret_access_key` to resolve credentials from the environment, shared
config files, or IMDS / container / IAM role:

```yaml
auth:
  sigv4:
    region: "us-east-1"
    service: "execute-api"
```

`region` and `service` are required. `access_key_id` and
`secret_access_key` are all-or-nothing (set both or omit both);
`session_token` may only accompany a full static pair. Credential
caching/refresh for the default chain (assumed-role / IMDS temporary
creds) is handled by the AWS SDK provider, not the `cache` block.

**Retry caveat:** because signing is per-request and time-stamped, a
caller-injected transport that re-sends an already-signed request is
only valid inside AWS's clock-skew window; prefer skopos's own
`on_status` / `error.mode` handling, which re-enters the request loop
and re-signs. See [runtime.md §10](runtime.md#10-http-transport-defaults).

---

## 2. Pagination

The runner expresses pagination as a discriminated union: exactly one
variant key per spec. The named variants (`none`, `cursor_token`,
`next_url`, `counter`) cover the common 80%; `custom` is the escape
hatch.

Every variant writes its active value into a **per-drain** `state.<name>`
slot (lifetime inferred from the pagination write site; see
[schema.md §state](schema.md#state)). Per-drain state is wiped at the
start of every drain — a drain that fails mid-page re-bootstraps
pagination on the next start. Recovery across drains is the author's
responsibility via persistent `state.*` writes from `progress:`
(§5).

The request reads the per-drain value back with an explicit
`{ref: state.<name>}` Value placed wherever the API expects it
(`query:`, `headers:`, the request's `url:`, the JSON body). There is
no auto-injection.

### 2.1 No pagination (`pagination.none`)

**What it does:** Each iteration fetches all available data in one
request. May still combine with a progress strategy (e.g. a high-water
timestamp) for incremental fetching across iterations.

```yaml
pagination: {none: {}}
```

### 2.2 Cursor token (`pagination.cursor_token`)

**What it does:** The server returns a next-page token in the response
body or a response header; the runner captures it into the per-drain
state slot. The next request reads it back with `{ref: state.<name>}`.
Default termination fires when the token's source path resolves to
absent.

Covers opaque cursors, GraphQL Relay end cursors (`pageInfo.endCursor`
paired with `pageInfo.hasNextPage`), session / scroll IDs, and
"next-page-number-from-body" patterns where the server hands back the
next page number rather than a token.

**IR shape (opaque cursor in body):**

```yaml
state:
  next_token:
    type: string

requests:
  - method: GET
    url: "${state.url}/v1/events"
    query:
      cursor: {ref: state.next_token, default: ""}

pagination:
  cursor_token:
    from: response.body.meta.next_token
    to:   state.next_token
```

`default: ""` keeps the bootstrap iteration's wire shape (sends
`?cursor=`). Omit `default` to skip the slot until the server-issued
token populates it.

**Header-borne token:** point `from:` at the response header.

```yaml
pagination:
  cursor_token:
    from: response.header.x-next-page
    to:   state.next_token
```

**Session / scroll ID:** the bootstrap iteration omits the slot
entirely so the server opens a fresh session; subsequent iterations
replay the captured id. Default termination (`{not: {present:
response.body.<from>}}`) already fires when the server stops returning
the id; pair with `terminate_when:` for an explicit server-completion
signal.

```yaml
state:
  scroll_id:
    type: string

requests:
  - method: GET
    url: "${state.url}/v1/scroll"
    query:
      scroll: {ref: state.scroll_id}      # no default — bootstrap omits

pagination:
  cursor_token:
    from: response.body.request_metadata.scroll
    to:   state.scroll_id
    terminate_when:
      eq:
        path: response.body.request_metadata.complete
        value: "true"
```

**GraphQL Relay end cursor:** the cursor variable name is whatever the
operator chooses for `to:`. Pair with `body.json` carrying `query` +
a `variables` Object (§3.4); the variable inside the GraphQL document
reads the cursor with `{ref: state.<name>}`.

```yaml
pagination:
  cursor_token:
    from: response.body.data.issues.pageInfo.endCursor
    to:   state.after
    terminate_when:
      eq:
        path: response.body.data.issues.pageInfo.hasNextPage
        value: false
```

### 2.3 Next URL (`pagination.next_url`)

**What it does:** The server returns a fully-formed next-page URL —
either as a body field or inside a `Link` header. The runner captures
the resolved URL into the per-drain state slot. The request reads it
back as its `url:` with a bootstrap fallback that fires on the first
iteration.

Covers both the body-borne next-page URL pattern (`response.body.meta.
next_page`, `response.body.links.next`, OData's
`@odata.nextLink`) and the RFC 5988 Link-header pattern (`Link: <url>;
rel="next"`). For the header pattern, set `regex:` / `capture:` to peel
the URL out of the header value.

**IR shape (next URL in body):**

```yaml
state:
  next_url:
    type: url

requests:
  - method: GET
    url: {ref: state.next_url, default: "${state.url}/v1/alerts"}

pagination:
  next_url:
    from: response.body.meta.next_page
    to:   state.next_url
```

The `{ref: ..., default: ...}` form is the natural fit here: string
interpolation cannot express "fall back to a composed URL only when
the slot is unset," so use the explicit ref-with-default and let
interpolation render the bootstrap path inside the default.

**Link header (RFC 5988):**

```yaml
pagination:
  next_url:
    from:   response.header.link
    to:     state.next_url
    regex:  '<(.*?)>;\s*rel="next"'
    capture: 1
```

When the source body field name contains a literal `.` (e.g. OData's
`@odata.nextLink`), use the segment-escape form so the dotted parser
does not split inside the field name:

```yaml
pagination:
  next_url:
    from:
      parts: [response, body, "@odata.nextLink"]
    to: state.next_url
```

The default termination predicate (`{not: {present: <from>}}`) handles
both the "no next URL" body shape and the "no `rel=\"next\"` entry"
header shape: when the source path resolves to absent (or the regex
finds no match), the loop ends.

### 2.4 Counter (`pagination.counter`)

**What it does:** A client-incremented integer that the runner advances
by `step:` after every accepted page. Covers both page-number
pagination (`start: 1, step: 1`) and offset pagination (`start: 0,
step: {ref: state.page_size}`).

Default termination is short-page detection: when the page returned
fewer events than `step:`, the loop ends. Override with a custom
`terminate_when:` when the API surfaces an explicit has-more flag.

**IR shape (page-number style):**

```yaml
state:
  page:
    type: int

requests:
  - method: GET
    url: "${state.url}/v1/events"
    query:
      page:  "${state.page|1}"
      limit: "${state.page_size}"

pagination:
  counter:
    to:    state.page
    start: 1
    step:  1
    terminate_when:
      not: {present: response.body.meta.has_next}
```

`"${state.page|1}"` supplies the bootstrap value via the interpolation
default — the very first iteration sees `state.page` unset and falls
back to `1`. The runner then advances the counter every page.

**IR shape (offset style):**

```yaml
state:
  page_size:
    type: int
    default: 100
  offset:
    type: int

requests:
  - method: GET
    url: "${state.url}/v1/events"
    query:
      offset: "${state.offset|0}"
      limit:  "${state.page_size}"

pagination:
  counter:
    to:    state.offset
    start: 0
    step:  {ref: state.page_size}
```

Default short-page termination is correct here: when the server returns
a page shorter than `state.page_size`, the loop ends.

### 2.5 Custom (`pagination.custom`)

**What it does:** Author-controlled primitive form for APIs that don't
fit the named variants. The author supplies a list of `{to, from, ...}`
writes (each `to:` is a declared per-drain state field; each `from:`
is any Value) plus an explicit `terminate_when:` predicate. The
predicate reads `response.*` directly, so it observes pre-advance
values; see [schema.md §pagination execution order](
schema.md#execution-order-per-page).

```yaml
state:
  next_token: {type: string}
  reset_at:   {type: timestamp}

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

Use `custom` when the API needs to advance two or more state slots in
lockstep, or when termination depends on a value that is not the
pagination cursor itself (rate-limit headers, a separate
`continue_token`, etc.).

### 2.6 Async-job (submit → poll → fetch)

**What it does:** The API returns a job/task ID on submission, the
client polls until completion, then fetches results. This is a
**request-level loop** (one request fires repeatedly via
`requests[].terminate_when:`), not a pagination loop. See §6 for the
full recipe.

### 2.7 Time-window pagination

**What it does:** Each drain covers a `[window_start, window_end)`
window; after completion the window slides forward. Modelled at the
progress layer (§5.4), not the pagination layer — the loop runs across
iterations, not within one. Combines orthogonally with the pagination
variants (the window bounds ride on `query:`; pagination iterates
within the window).

### 2.8 Export + blob download

**What it does:** A list step returns blob URLs; a dependent step GETs
each blob in turn. When the export is asynchronous (submit → poll →
fetch), the final fetch role is the blob download.

The list → per-blob shape is expressible via `requests:` + per-step
`fan_out:` (§3.10). The async-export shape is the request-level loop
recipe in §6. Downloaded blobs are surfaced as response bodies; any
gzip / CSV / NDJSON decoding lives in the ingest pipeline that
consumes the emitted events.

### 2.9 Worklist / multi-phase

**What it does:** Same-iteration list → per-item detail fan-out.

Expressible via `requests:` with a per-step `fan_out:` (§3.10).

---

## 3. Request shapes

### 3.1 URL composition

Every request declares an absolute `url:` Value. There is no
spec-level URL prefix; templates compose URLs themselves.

The two idioms:

**String interpolation** — preferred when the URL is made of literal
text plus refs:

```yaml
requests:
  - method: GET
    url: "${state.url}/api/v1/events"
```

`${...}` segments resolve against any namespace; defaults use the
`|` sigil — `"${state.region|us-east-1}"`. See
[schema.md §string interpolation](schema.md#string-interpolation) for
the full grammar.

**`{ref: ..., default: ...}`** — preferred when an entire URL is read
from a per-drain state slot with a bootstrap fallback. Pagination's
`next_url` variant (§2.3) is the canonical example:

```yaml
requests:
  - method: GET
    url: {ref: state.next_url, default: "${state.url}/v1/alerts"}
```

`{concat: [...]}` is accepted everywhere interpolation is; reach for it
when interpolation gets awkward (e.g. inside a nested Value where a
quoted string is hard to read).

### 3.2 GET with query parameters

**What it does:** Standard GET. Query slots accept any Value; each
value must resolve to a string at request time. Non-string Values are
coerced as if wrapped in `{format: string, value: ...}`; the
interpolation form (`"${state.page_size}"`) does the same coercion
inline.

```yaml
requests:
  - id: main
    method: GET
    url: "${state.url}/v1/events"
    query:
      since: {ref: state.last_timestamp}
      page:  "${state.page|1}"
      type:  {ref: state.event_type}
```

### 3.3 POST with JSON body

**What it does:** The body is a JSON object whose top-level keys are
literal strings; each value is a Value. Nested object literals at any
depth use the explicit `{object: {...}}` Value form — a bare YAML
mapping at a Value position is rejected unless it carries a
discriminator key.

```yaml
requests:
  - id: main
    method: POST
    url: "${state.url}/api/search"
    body:
      json:
        time_range:
          object:
            start: {ref: state.window_start}
            end:   {ref: state.window_end}
        limit: 100
```

The discriminator rule is "Values are discriminated unions." A literal
map needs the `{object: ...}` wrapper so the YAML decoder does not try
to interpret its keys as Value discriminator keys.

### 3.4 POST with GraphQL query

**What it does:** GraphQL has no dedicated body variant — encode it as
`body.json` with a `query` scalar and a `variables` Object. Pair with
`pagination.cursor_token` (§2.2) to drive the relay cursor variable.

```yaml
requests:
  - id: main
    method: POST
    url: "${state.url}/graphql"
    body:
      json:
        query: |
          query ($after: String, $first: Int) {
            assets(after: $after, first: $first) {
              nodes { id name }
              pageInfo { endCursor hasNextPage }
            }
          }
        variables:
          object:
            after: {ref: state.after}
            first: {ref: state.page_size}
```

### 3.5 POST with form-encoded body

**What it does:** Body is serialised as
`application/x-www-form-urlencoded`. Common for older APIs and for
token endpoints expressed as plain requests (OAuth2 token endpoints
handled by `auth.oauth2` set their own form body internally).

```yaml
requests:
  - id: main
    method: POST
    url: "${state.url}/api/events"
    body:
      form:
        since:  {ref: state.last_timestamp}
        limit:  "${state.page_size}"
        format: json
```

### 3.6 POST with raw body

**What it does:** Send an arbitrary opaque body. The resolved string is
sent verbatim; no `Content-Type` is set automatically (operator sets
one via `headers:` if required). Less common than JSON / form; reserved
for content types that don't match the structured cases (NDJSON
ingest, hand-tuned XML, GraphQL-via-text/plain).

```yaml
requests:
  - method: POST
    url: "${state.url}/ingest"
    headers:
      Content-Type: application/x-ndjson
    body:
      raw: |
        {"tenant":"${state.tenant_id}","event":"hello"}
```

### 3.7 Step-level cache (`requests[].cache`)

**What it does:** Wraps a token-style step (a custom JSON login, a
session-key exchange) in a fresh-vs-cached conditional — the non-OAuth2
counterpart of the OAuth2 token cache (§1.8). The same `Cache` struct
is reused: it writes into a `cache.<name>` slot and re-runs the step
when the remaining lifetime drops below `buffer`.

```yaml
requests:
  - id: login
    method: POST
    url: "${state.url}/api/v1/login"
    body:
      json:
        username: {ref: state.username}
        password: {ref: state.password}
    extract:
      - {to: cache.session_token, from: response.body.token}
    cache:
      to: cache.session_token
      expires_at: {ref: response.body.expires_in, default: "1h"}
      buffer: 60s

auth:
  bearer:
    token: {ref: cache.session_token, default: "pending"}
```

`default: "pending"` keeps auth resolution from failing on the very
first drain (before the slot is populated) when the login endpoint
itself ignores the `Authorization` header.

`requests[].cache` and `fan_out` are mutually exclusive — cache stores
one token-shaped value, fan-out runs the step N times, and combining
them would race writes into one slot.

Cache invalidation follows the same rule as §1.8: a downstream step
that declares `on_status: {401: invalidate_cache}` drops every
reachable `cache.*` slot.

### 3.8 Headers and dynamic header values

**What it does:** `requests[].headers` is a map of string → Value.
Header values use the full Value language — string interpolation for
composed strings, `{format: ...}` for typed coercions, `{ref: ...}`
for refs, etc.

```yaml
requests:
  - method: GET
    url: "${state.url}/v1/data"
    headers:
      X-Trace-Id: "${state.run_id}"
      X-API-Version: "2024-01-15"
      If-None-Match: {ref: state.etag, default: ""}
```

The standard auth header (`Authorization`, plus whatever
`auth.api_key.header` or `auth.custom.header` names) is injected by the
auth pipeline; templates should not duplicate it under
`requests[].headers`.

### 3.9 Sequential request chain

**What it does:** Multiple steps with `id:` per step. Later steps
reference earlier bodies via `{ref: steps.<id>.body.<path>}` and
earlier extracts via `{ref: extract.<name>}`. The producer step (the
one whose decoded body `response.events_at` walks) is the last request
by default; set `produces_events: true` on a different step to
override.

```yaml
requests:
  - id: login
    method: POST
    url: "${state.url}/auth/signin"
    body:
      json:
        user: {ref: state.username}
        pass: {ref: state.password}
    extract:
      - {to: extract.session_cookie, from: response.header.Set-Cookie}

  - id: main
    method: GET
    url: "${state.url}/api/data"
    headers:
      Cookie: {ref: extract.session_cookie}
```

There is no separate `pre_fetch:` block — a single pre-flight step is
just the first entry in `requests:`. There is also no separate `path:`
field; every request uses `url:`.

### 3.10 Per-item fan-out

**What it does:** A step iterates over a list Value (typically a prior
step's body list); the runner runs the step once per item with the
author-chosen `fan_out.as` name bound to the current value. `merge:
flatten` (default) concatenates per-item response bodies, which must
each decode as JSON lists; `merge: wrap` returns the per-item bodies as
elements of a list (use when each item's response is an object).
The merged body is what binds into `steps.<id>.body` and, when the
fan-out step is the producer, what `events_at` walks.

**`fan_out.as` chooses the binding name.** Refs inside the step use
that name as the namespace root — `{ref: incident.id}` when
`as: incident`, `{ref: blob.url}` when `as: blob`. The name must not
collide with a reserved root (`state`, `cache`, `events`, `extract`,
`steps`, `response`) or with any declared state field or earlier-step
id.

**Error handling per item.** A non-success item runs through the same
`on_status` / `error.mode` dispatch as a single-request step: `skip` /
`empty_events` drop the item, `fail` aborts the drain,
`invalidate_cache` clears the active auth and step caches and stops the
fan-out early.

**`requests[].cache` and `fan_out` are mutually exclusive.** Cache
stores one token-shaped value, fan-out runs the step N times — the
validator rejects the combination.

**IR shape:**

```yaml
requests:
  - id: list
    method: GET
    url: "${state.url}/incidents"

  - id: detail
    method: GET
    url: "${state.url}/incidents/${incident.id}"
    fan_out:
      over: {ref: steps.list.body.incidents}
      as:   incident
      merge: flatten
```

### 3.11 Per-step `if:` gating

**What it does:** Skip a step when its `if:` predicate is false.
Combines with multi-step `requests:` chains to express conditional
pre-fetches and cache short-circuits.

```yaml
requests:
  - id: data
    method: GET
    url: "${state.url}/events"
    if:
      eq:
        path: state.etag_check
        value: true
```

When a step is skipped, `{ref: steps.<id>.body.<...>}` resolves to a
zero Value; downstream `{select: ...}` branches should guard with
`{present: steps.<id>.body.<...>}`. Progress writes do NOT fire for a
step skipped by `if:`.

### 3.12 Per-step `on_status:`

**What it does:** When a request returns a status outside
`expect_status:` (default `[200]`), dispatch to one of the closed verb
set: `skip`, `fail`, `empty_events`, or `invalidate_cache`.

| Verb               | Semantics                                                                                          |
|--------------------|----------------------------------------------------------------------------------------------------|
| `skip`             | Drop the response, emit no events, advance progress as if successful. Canonical "304 Not Modified" handling. |
| `fail`             | Non-success: emit no events and stop the iteration with an error.                                  |
| `empty_events`     | Non-success: emit no events but DO fire `progress:` writes. Pattern: "429 with `ignore_api_errors`". |
| `invalidate_cache` | Drop the cached value backing the active auth's `cache.<name>` slot AND every `requests[].cache` slot, then treat the response as a non-event "retry next iteration" signal. |

`on_status` is a map keyed by exact HTTP status code in `[100, 599]`.
There is no `retry` verb (see #56).

```yaml
requests:
  - id: main
    method: GET
    url: "${state.url}/events"
    on_status:
      401: invalidate_cache
      429: empty_events
      304: skip
```

`error.mode` (`standard` / `warn` / `fail`) is the catch-all for
statuses not named in `on_status:`. `requests[].on_status` takes
precedence over `error.mode` for the statuses it lists.

### 3.13 `extract:` captures

**What it does:** Capture fields out of a step's response into either
the persistent `state.*` namespace or the per-iteration `extract.*`
namespace. The destination's namespace prefix decides persistence;
there is no separate `target:` field.

```yaml
requests:
  - id: data
    method: GET
    url: "${state.url}/events"
    extract:
      - {to: state.last_event_id, from: response.body.meta.last_id}
      - {to: extract.request_id,  from: response.header.x-request-id}
```

| Destination     | Lifetime                                      | Notes                                                       |
|-----------------|-----------------------------------------------|-------------------------------------------------------------|
| `state.<name>`  | persistent (lifetime inferred from this write)| The field must be declared under `state:`.                  |
| `extract.<name>`| per-iteration                                 | No declaration needed; reset at the top of every iteration. |

`from:` is a namespace-rooted [Path](schema.md#paths):
`response.body.<path>`, `response.header.<name>`,
`steps.<id>.body.<path>`, or `steps.<id>.header.<name>`. Bare dotted
strings without a namespace prefix are rejected.

---

## 4. Response parsing

### 4.1 Decoder selection (`response.decode`)

**What it does:** Selects how the producer step's body is decoded.
`decode` is a chain: zero or more byte-transform stages feeding one
terminal decoder. The scalar forms `json` / `ndjson` are the single-terminal
common case.

| Stage    | Kind           | Semantic                                                  |
|----------|----------------|----------------------------------------------------------|
| `gzip`   | byte-transform | Decompress the upstream stream (RFC 1952).               |
| `zip`    | byte-transform | Expand a ZIP archive; optional `glob` selects members.   |
| `json`   | terminal       | Decode the full body as a single JSON document.          |
| `ndjson` | terminal       | Decode each non-empty line as one JSON value.            |
| `csv`    | terminal       | Decode delimited rows; `header: present` → map per row, `absent` → list per row. |

```yaml
response:
  decode: json
  events_at: response.body.data.events
```

For a file payload the HTTP transport does not transparently undo, list the
stages — e.g. a gzipped CSV export:

```yaml
response:
  decode:
    - gzip: {}
    - csv:
        header: present
  events_at: ""
```

Transparent `Content-Encoding: gzip` is undone by the transport and needs no
`decode` stage; the chain is for file payloads (typically `Content-Type:
application/gzip | application/zip | text/csv`). See
[schema.md](schema.md#decode-chain) for the full chain rules.

### 4.2 Locating events (`response.events_at`)

**What it does:** A namespace-rooted [Path](schema.md#paths) telling
the runner where the events list lives. Accepted roots:

- `response.body.<path>` — the active (events-bearing) step's own body.
- `steps.<id>.body.<path>` — a labelled prior step's body.

The empty (zero) Path means "the body root IS the events list" — used
for top-level arrays and top-level single objects (the runner wraps a
single object in a one-element list).

```yaml
response:
  decode: json
  events_at: response.body.data.items
```

Bare dotted strings (`data.items` with no namespace prefix) are
rejected at parse / validate time.

When `decode: ndjson` and `events_at` is empty, each decoded line IS
one event. When `decode: ndjson` and `events_at` is non-empty, the
trailing body segments below the namespace root are applied to EACH
decoded line and the flattened sequence is the events list.

### 4.3 JSON, single object at root

**What it does:** The response body is a single JSON object; the
runner wraps it in a one-element list to produce one event.

```yaml
response:
  decode: json
  events_at: ""
```

### 4.4 JSON array at root

**What it does:** The response body IS the JSON array.

```yaml
response:
  decode: json
  events_at: ""
```

### 4.5 JSON array at a nested path

```yaml
response:
  decode: json
  events_at: response.body.data.items
```

Common nest patterns the field handles: `data`, `value`, `result`,
`items`, `resources`, `hits.hits`,
`data.<queryName>.nodes`. Path segments containing a literal `.`
(e.g. OData's `@odata.nextLink`) use the segment-escape form
(`{parts: [...]}`).

### 4.6 Events list inside a prior step's body

**What it does:** When a prior step has a labelled `id:` and that
step's response carries the events list, `events_at` walks
`steps.<id>.body.<path>` directly — the producer step does NOT have
to be the same step whose body the events live in.

```yaml
requests:
  - id: list
    method: GET
    url: "${state.url}/incidents"
    fan_out:
      over: {ref: state.tenants}
      as:   tenant
      merge: flatten

  - id: detail
    method: GET
    url: "${state.url}/incidents/${tenant.id}"
    fan_out:
      over: {ref: steps.list.body}
      as:   incident
      merge: flatten
    produces_events: true

response:
  decode: json
  events_at: steps.detail.body
```

### 4.7 NDJSON

**What it does:** The body is newline-delimited JSON, one object per
line. Each non-empty line decodes to one event when `events_at` is
empty; otherwise the path is applied per line and the flattened
sequence is the events list.

```yaml
response:
  decode: ndjson
  events_at: ""
```

### 4.8 The `events.*` namespace

**What it does:** Once `events_at` resolves, the decoded events list is
exposed as the `events.*` namespace. This separates the events
projection from response-body field access — even when a response body
also has a top-level field literally named `events`. References:

| Form                       | Resolves to                                              |
|----------------------------|----------------------------------------------------------|
| `events.*.<field>`         | `<field>` projected across every event.                  |
| `events.first.<field>`     | `<field>` from the first event in declared order.        |
| `events.last.<field>`      | `<field>` from the last event in declared order.         |
| `events.<int>.<field>`     | `<field>` from the event at position `<int>` (0-based).  |
| `events.count`             | Number of events on the current page.                    |

The `events.*` namespace is per-iteration. The runner does not buffer
events across pages; reducers (`max`, `min`, `first`, `last`, `count`)
are streaming operations over the current page.

### 4.9 Empty pages are valid

**What it does:** An empty page (zero events) is still a valid accepted
page-response when its status passes `expect_status:` and `on_status:`.
The loop controls itself; an empty page simply triggers the next page
fetch under the active pagination variant. There is no synthetic
"placeholder" event needed — the page loop does not require a non-empty
event list to advance.

Progress writes (§5) fire on empty pages too. Server-provided cursors,
ingestion timestamps, and other response-body fields are persistable
independently of event production.

---

## 5. State checkpointing (`progress:`)

`progress:` is a flat list of state writes evaluated **once per
accepted page-response** (including empty pages). There are no named
variants — every recipe below is a list of `{to, from, ...}` entries.

The destination of every entry is a persistent `state.<name>` field
(lifetime inferred from this write site). First-run seeding is the
`default:` on the destination state field — there is no separate
first-run mechanism.

```yaml
progress: []                            # no progress tracking
```

| Field    | Required | Description |
|----------|----------|-------------|
| `to`     | yes      | `state.<name>`. Persistent across drains. Must be declared under `state:`. |
| `from`   | yes      | Any [Value](schema.md#values). Has access to `state.*`, `cache.*`, `events.*`, `response.body.*`, `response.header.*`, `steps.<id>.body.*`, `steps.<id>.header.*`. May use reducers (`max` / `min` / `first` / `last` / `count`) over `events.*`, arithmetic primitives, refs, etc. |
| `coerce` | no       | Type coercion verb. |
| `regex`  | no       | Optional regex transform. |

See [schema.md §progress](schema.md#progress) for batch-semantics
details (all `from:` expressions evaluate against the same pre-write
snapshot of `state.*`).

### 5.1 Stateless

**What it does:** No cursor advance; every drain fetches the full
dataset. Public feeds, demo integrations. Just omit `progress:` or set
it to the empty list:

```yaml
progress: []
```

### 5.2 Max-of-events high-water mark

**What it does:** After each accepted page, advance
`state.last_timestamp` to the maximum event-time across the page,
merged with the prior state value. The next drain reads `since` from
`{ref: state.last_timestamp}`; first-run seeding comes from the state
field's `default:`.

The explicit `max` against the prior state value is the
restart-safety idiom: it makes the write a no-op when an empty page
arrives after a partial failure (so a transient drain cannot regress
the cursor).

```yaml
state:
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}    # first-run lookback

requests:
  - method: GET
    url: "${state.url}/v1/events"
    query:
      since: {ref: state.last_timestamp}

response:
  decode: json
  events_at: response.body.events

progress:
  - to: state.last_timestamp
    from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}
```

For "max over an arbitrary monotonically-increasing field" (numeric id,
sequence number), drop the `timestamp` connotation and project the
desired field: `from: {max: [{ref: state.last_id}, {max: {ref:
events.*.id}}]}`. The shape is identical.

### 5.3 Clock-driven cursor

**What it does:** Advance `state.last_timestamp` to `{now: true}` after
every accepted page — no events walk. Suits APIs where the drain itself
defines the cutoff (the next iteration is "everything since the last
invocation").

```yaml
progress:
  - to: state.last_timestamp
    from: {now: true}
```

For a per-iteration lookback (overlap the window on every advance to
guard against clock skew or late-arriving events):

```yaml
progress:
  - to: state.last_timestamp
    from: {subtract: [{now: true}, "5m"]}
```

### 5.4 Sliding window

**What it does:** Each drain covers `[window_start, window_end)`;
after completion the window slides forward. Two progress entries
advance the window in lockstep. The pre-write snapshot rule
([schema.md §progress evaluation semantics](
schema.md#evaluation-semantics)) makes declaration order irrelevant:
when the new `window_start` reads `state.window_end`, it always sees
the *old* `window_end` value.

```yaml
state:
  window_start:
    type: timestamp
  window_end:
    type: timestamp

requests:
  - method: GET
    url: "${state.url}/v1/events"
    query:
      start: {ref: state.window_start}
      end:   {ref: state.window_end}

progress:
  - to: state.window_start
    from: {ref: state.window_end, default: {subtract: [{now: true}, "30d"]}}
  - to: state.window_end
    from: {now: true}
```

The first-run window is seeded by the `default:` on the
`{ref: state.window_end}` inside the `state.window_start` entry — on
the very first drain `state.window_end` is unset, so the default
fires and seeds the start of the lookback window. The
`state.window_end` entry then writes `{now: true}` to close the window.

`state.window_end` clamps to ≥ `state.window_start` so a clock that
moves the wrong way cannot invert the window — express this with an
explicit `max` when needed:

```yaml
progress:
  - to: state.window_start
    from: {ref: state.window_end, default: {subtract: [{now: true}, "30d"]}}
  - to: state.window_end
    from: {max: [{ref: state.window_start}, {now: true}]}
```

### 5.5 Pinned-id checkpoint

**What it does:** Persist the first event's id (or any positional
field) into `state.*` for sort-order-resistant tracking. Useful when
the API returns events in newest-first order and the consumer wants
to remember the last "newest" id rather than a timestamp.

```yaml
progress:
  - to: state.last_event_id
    from: {ref: events.first.id}
```

`events.first` is a declared-order shortcut into the `events.*`
namespace (§4.8). `events.last` is the symmetric form.

### 5.6 First-write-wins / first-run seeding

**What it does:** "Persist on the very first drain only" is just a
`{ref: ...}` with a `default:` — when the destination is set, it
writes itself back (no-op); when absent, the default kicks in.

```yaml
progress:
  - to: state.created_at
    from: {ref: state.created_at, default: {now: true}}
```

Equivalent at the declaration site: put the seed value as the
destination's `default:` and have `progress:` write a richer value
(e.g. a max merge). The `default:` accepts the full Value language,
including `{subtract: [{now: true}, "720h"]}` for the canonical
first-run lookback.

```yaml
state:
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}
```

### 5.7 Per-page checkpointing on empty pages

**What it does:** Progress fires once per accepted page-response — empty
pages included. Server-provided cursors (`response.body.meta.next_token`
on a page that returned no new events), ingestion timestamps
(`response.body.meta.fetched_at`), and other body fields can be
persisted to `state.*` independently of event production:

```yaml
progress:
  - to: state.last_server_cursor
    from: {ref: response.body.meta.next_token}
  - to: state.last_ingest_at
    from: {ref: response.body.meta.fetched_at}
```

Progress does NOT fire when a step is skipped by `if:`, aborted by
`on_status: fail`, or errors out before a response was decoded.

### 5.8 ETag / conditional fetch

**What it does:** Skip the work when the upstream has not changed —
store an `ETag` (or `Last-Modified`) in `state.*`, send `If-None-Match`
on the next iteration, and treat 304 as "advance state without emitting
events".

```yaml
state:
  etag:
    type: string

requests:
  - id: data
    method: GET
    url: "${state.url}/events"
    headers:
      If-None-Match: {ref: state.etag, default: ""}
    extract:
      - {to: state.etag, from: response.header.ETag}
    on_status:
      304: skip

progress:
  - to: state.last_seen_at
    from: {now: true}
```

`on_status: {304: skip}` makes the 304 response a no-event accepted
page — `progress:` still fires (so `state.last_seen_at` advances), the
extracted ETag from a 200 response persists, and the next iteration
sends `If-None-Match` against the latest known ETag.

### 5.9 Cross-iteration state in `state.*`

**What it does:** Values that need to survive across iterations but
are not pagination state live in persistent `state.*` fields. The
field is declared once; writes come from `progress:` or from
`requests[].extract` with `to: state.<name>` (§3.13). There is no
separate `state.passthrough:` mechanism.

The runner persists every state field whose lifetime is inferred as
"persistent" (written by `progress:` or `extract.to: state.*`) and
every field that is operator-config (declared with `default:`, no
writes). Per-drain scratch fields (written by `pagination:`) are wiped
at drain start. See [`stores.md`](stores.md) for the persistence
contract.

### 5.10 Working recipes (side-by-side templates)

Two end-to-end recipes assemble the pieces above against the two most
common shapes.

**Simple GET with timestamp progress** — single GET, single page,
high-water timestamp from the events list:

```yaml
ir_version: "1"

state:
  url:
    type: url
    default: "http://localhost:9999"
  page_size:
    type: int
    default: 5
  api_key:
    type: secret
    default: "test-bearer-token-12345"
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    url: "${state.url}/bearer_simple/events"
    query:
      since: {ref: state.last_timestamp}
      limit: "${state.page_size}"

response:
  decode: json
  events_at: response.body.events

pagination:
  none: {}

progress:
  - to: state.last_timestamp
    from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}

error:
  mode: standard
```

**URL-from-body pagination with timestamp progress** — server returns
the next-page URL inside the response body; the drain follows the
chain until no `next_page` is present:

```yaml
ir_version: "1"

state:
  url:
    type: url
    default: "http://localhost:9999"
  page_size:
    type: int
    default: 5
  api_key:
    type: secret
    default: "test-bearer-token-12345"
  next_url:
    type: url
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    url: {ref: state.next_url, default: "${state.url}/v1/alerts"}
    query:
      since: {ref: state.last_timestamp}
      limit: "${state.page_size}"

response:
  decode: json
  events_at: response.body.alerts

pagination:
  next_url:
    from: response.body.meta.next_page
    to:   state.next_url

progress:
  - to: state.last_timestamp
    from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.created_at}}]}

error:
  mode: standard
```

---

## 6. Async-job pattern (request-level loop)

**What it does:** The API returns a job/task ID on submission; the
client polls until completion; then fetches the results. The IR
expresses this as **three plain requests** with `terminate_when:` on
the poll step and ordinary `extract:` writes to `state.*`. There is
no dedicated async-job variant, no submit/poll/fetch role machine, and
no runner-allocated state slots — every state field is declared by the
author.

**IR shape:**

```yaml
state:
  export_id:
    type: string
  result_url:
    type: url

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
    produces_events: true
```

**How the loop works.** `terminate_when:` on the poll step is the
request-level loop primitive — after each poll response, the predicate
is evaluated against `response.*` and the active `state.*`. If true,
the loop exits and the runner moves to the fetch step. If false, the
same poll step re-fires. The submit and fetch steps run once per
iteration; only the poll step loops.

**Variants:**

- *Asynchronous export with completion timestamp.* `progress:` writes
  a max-merge over `events.*.timestamp` after the fetch step's events
  are emitted (§5.2). The fetch step is the producer because it is the
  last request and carries `produces_events: true`.
- *Stateless export.* Omit `progress:` (or set `progress: []`) — every
  drain re-submits, re-polls, and re-fetches without persisting any
  high-water mark.
- *Poll-without-fetch.* Some APIs return events directly inside the
  poll response when the job completes — drop the fetch step and mark
  the poll step `produces_events: true`. The poll loop's
  `terminate_when:` doubles as the "events are ready" signal.

**Phase information lives in plain state.** `state.export_id` and
`state.result_url` are author-declared persistent state fields,
populated by ordinary extracts on the submit and poll steps. There is
no runner-managed `phase` field, no role discriminator, no
auto-registered state slots.

---

## 7. Loop primitives

The runner expresses two loops, each with one predicate hook.

| Loop                | Hook                                      | Variant-specific default                                                             |
|---------------------|-------------------------------------------|--------------------------------------------------------------------------------------|
| Pagination loop     | `pagination.<variant>.terminate_when:`    | `cursor_token` / `next_url`: `{not: {present: <from>}}`. `counter`: short-page (`{lt: {path: events.count, value: <step>}}`). `custom`: no default — author must supply. `none`: no loop. |
| Request loop        | `requests[].terminate_when:`              | None — the request fires once when omitted. Used for async-poll patterns (§6).       |

Both loops use the [Predicate](schema.md#predicates) language and read
`response.*` plus the active `state.*`. Both are absent-tolerant: a
Path that resolves to absent makes `present` return false and makes
comparison predicates return false, never throws.

There are no other loop primitives.

---

## 8. Cross-cutting

### 8.1 Token caching across drains

`auth.oauth2.<grant>.cache` (§1.8) for OAuth2 tokens;
`requests[].cache` (§3.7) for custom session-token logins. Both write
into a `cache.<name>` slot and use the same `Cache` struct.

The `cache.*` namespace is process memory only. Tokens DO NOT persist
across runner restarts — the first request after a restart misses the
cache and forces a fresh token fetch. This is by design: the persisted
surface ([`stores.md`](stores.md)) is `state.*` only.

### 8.2 Secret redaction in logs / traces

Every log line, error message, and `Tracer` field that mentions a
`Value` routes through the secret-detector. URLs are emitted with
scheme + host + path only; query strings and userinfo are stripped.
`Authorization` / `Cookie` / `Proxy-Authorization` headers are always
redacted by name; the runtime credential surfaces named by
`auth.custom.header` and `auth.api_key.header` are redacted by name;
any `requests[].headers` / `requests[].query` entry whose IR Value is
secret-tainted is redacted by content. Request and response bodies are
metadata-only (byte length + leading-byte classification), never raw
bytes.

`secret`-typed state fields propagate their secret status through every
composite Value form they appear in — `{concat}`, `{format}`, `{base64}`,
`{select}`, `{list}`, `{object}`, and interpolated strings. See
[schema.md §secret propagation](schema.md#secret-propagation).

### 8.3 HTTP transport: timeouts, proxies, retries

Default 30-second per-request timeout. Callers inject a custom
`*http.Client` to control proxy, mTLS, custom transport, redirect
policy, DNS overrides.

Retry / backoff / rate-limit policy is **never** modelled at the runner
layer. Authors who need retries plug them into the injected transport.

### 8.4 Observability: structured trace per HTTP exchange

`Runner.Tracer` receives one record per HTTP exchange, carrying
iteration, step id, redacted URL, redacted query map, redacted
post-auth headers, body metadata, status, elapsed, error string. The
`JSONLTracer` writes one JSON object per exchange.

Drain duration, events/page, and per-endpoint error rate are not
emitted by the runner directly. A custom `Tracer` aggregating
per-exchange records can derive them.

### 8.5 Branching the request shape on a state flag

Auth-level branching is `auth.multi_mode` (§1.9). Within a single
request, branching the URL / body / headers on a flag is covered by
`{select: {branches: [...], default: ...}}` on any Value slot.

Whole-request-shape branching (e.g. `gov_cloud` selecting a completely
different endpoint set) is **not** modelled as a first-class form —
express it via `select` on individual `url:` / `body:` slots, or via
`if:` gating two parallel request chains.
