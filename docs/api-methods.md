# API method reference

The canonical list of API-communication shapes the runner can express,
grouped by concern (authentication, pagination, request body, response
parsing, cursor state, cross-cutting). Each entry names the shape,
gives a one-paragraph description, and names the IR knobs that express
it. The IR vocabulary itself lives in [`schema.md`](schema.md); the
generated per-field reference lives in
[`schema-reference.md`](schema-reference.md); the runtime contract
lives in [`runtime.md`](runtime.md).

Entries fall into three buckets:

- **Default — supported.** No status line; the runner expresses the
  pattern end-to-end with test coverage.
- **Deferred.** The IR schema accepts the form (validator passes) but
  the runner does not yet execute it. Tagged inline.
- **Out of scope.** Not modelled in the IR. Tagged inline; no escape
  hatch is available.

When adding a new API variation, add an entry here in the same PR that
ships the runner change — this document is the "if we add a new API
shape, write it down here" surface.

---

## 1. Authentication

### 1.1 No auth (`auth.none`)

**What it does:** No `Authorization` header or query credential is added by
the runner. Use for public feeds, demo integrations, or APIs whose credentials
live entirely in `request.query:` / `request.headers:`.

**IR shape:**

```yaml
auth:
  none: {}
```

### 1.2 Bearer token (`auth.bearer`)

**What it does:** Sends `Authorization: Bearer <token>` where the token is a
`Value` (typically a `{ref: state.<name>}` to a secret-typed field, or an
`{ref: extract.<name>}` captured by a prior step).

**IR shape:**

```yaml
auth:
  bearer:
    token: {ref: state.api_key}
```

**Variants:**

- *Static from state* — the token is declared as a `secret`-typed state
  field and supplied by the operator.
- *From a prior step* — an earlier step in `requests:` POSTs to a token
  endpoint and pulls the access token via `extract:`. The main request
  references it as `{ref: extract.<name>}`. (When the login step has its
  own expiry, wrap it with `requests[].cache` — see §3.10.)
- *Raw-token / non-`Bearer` prefix schemes* — APIs that read the value as
  the literal `Authorization` header value, or that demand a non-`Bearer`
  prefix, are expressed via `auth.custom` (§1.5), not `auth.bearer`.

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

**Note:** Some APIs reuse the `Authorization: basic` header shape (lowercase
`basic`) to carry an opaque session token rather than a base64 `user:pass`
pair. Express that as `auth.custom` (§1.5) — the prefix is `basic ` but the
content is not basic auth.

### 1.4 API key in a header or query parameter (`auth.api_key`)

**What it does:** A static API key sent in a named header, or (with
`in_query: true`) as a query parameter.

**IR shape:**

```yaml
auth:
  api_key:
    header: X-Api-Key             # or any vendor-specific header / query-param name
    value: {ref: state.api_key}
    in_query: false               # set true to send via query param instead
```

**Common variants:** mixed-case headers (`X-Api-Key`, `x-api-key`,
`api-key`), vendor-specific names (`X-Auth-Token`, `X-RFToken`,
`Private-Token`), compound formats (`accessKey=…;secretKey=…` as a single
header value composed via a `{concat: [...]}` Value).

### 1.5 Custom auth header / scheme (`auth.custom`)

**What it does:** A single-header auth scheme that does not fit `bearer` /
`basic` / `api_key`. Author supplies the header name and the full value
`Value`.

**IR shape:**

```yaml
auth:
  custom:
    header: Authorization
    value:
      concat:
        - "ApiKey "
        - {ref: state.api_token}
```

**Use cases:** literal `Authorization: <token>` with no scheme prefix;
non-standard `Authorization: ApiKey …` / `Authorization: Token …` /
`Authorization: basic <opaque>` shapes; vendor-specific headers like
`PS-Auth key=…;runas=…`. Additional non-auth headers ride on
`request.headers:`.

### 1.6 OAuth2 client-credentials grant (`auth.oauth2.client_credentials`)

**What it does:** The runner POSTs `grant_type=client_credentials` to a
configured `token_url` with HTTP Basic `client_id:client_secret`, plus
optional `scopes=...` / `audience=...` parameters (RFC 6749 §2.3.1). The
returned access token rides as `Authorization: Bearer <token>` on every
IR-described request. Non-2xx token responses surface with body-classification
metadata only — never the raw bytes.

**IR shape:**

```yaml
auth:
  oauth2:
    client_credentials:
      token_url: {ref: state.token_url}
      client_id: {ref: state.client_id}
      client_secret: {ref: state.client_secret}
      scopes: [read:events, read:incidents]   # optional; joined with spaces
      audience: "wiz-api"                     # optional; plain string
```

### 1.7 OAuth2 password grant (`auth.oauth2.password_grant`)

**What it does:** POSTs `grant_type=password` + `username` / `password` in
the form body (RFC 6749 §4.3.2). `client_id` is optional and rides in the
form body when set. Same Bearer-injection and token-fetch error contract as
`client_credentials`.

**IR shape:**

```yaml
auth:
  oauth2:
    password_grant:
      token_url: {ref: state.token_url}
      username: {ref: state.username}
      password: {ref: state.password}
      client_id: {ref: state.client_id}     # optional
      scopes: [openid, profile]             # optional
```

### 1.8 OAuth2 token caching (`auth.oauth2.<grant>.cache`)

**What it does:** Wraps the OAuth2 token fetch in a fresh-vs-cached
conditional. `store_in` names the state key the cached token lands in;
`expiry_field` is a `Path` rooted at `response.body.<path>` of the token
endpoint's response carrying the TTL (RFC 6749 `expires_in`, integer
seconds, with `string→int` and `string→Go-duration` fallbacks);
`expiry_buffer` is the "don't cut it too close" margin. The `store_in`
key is auto-registered as a runtime `string` state field — authors must
NOT also declare it under `state.fields`. The paired expiry slot
(`<store_in>_expires_at`) auto-registers the same way. Fresh fetch fires
when `now + expiry_buffer >= cached_expires_at`. The expiry timestamp
lives at `state.<store_in>_expires_at` as RFC 3339 (slice 7 moved this
slot out of the cursor namespace).

**IR shape:**

```yaml
auth:
  oauth2:
    client_credentials:
      token_url: {ref: state.token_url}
      client_id: {ref: state.client_id}
      client_secret: {ref: state.client_secret}
      cache:
        store_in: token
        expiry_field: expires_in
        expiry_buffer: 60s
```

**Cache invalidation:** A request step that declares
`on_status: {401: invalidate_cache}` drops every reachable OAuth2 cache
slot (including each branch of an `auth.multi_mode` dispatch) and every
`requests[].cache` step-cache slot, then advances as if the page came
back empty. The next request misses the cache and forces a fresh token
fetch. When the active auth has no cache (and no step-cache is reachable)
the verb degrades to `empty_events` with a log line.

### 1.9 Multi-mode auth (`auth.multi_mode`)

**What it does:** Selects an auth strategy at runtime by walking
`branches[].when` predicates in declaration order — first match wins;
`default.auth` fires when no branch matches. Common shape: a state flag picks
between Bearer, API key, and Custom.

**IR shape:**

```yaml
auth:
  multi_mode:
    branches:
      - when:
          eq:
            path: state.auth_mode
            value: bearer
        auth:
          bearer:
            token: {ref: state.api_token}
      - when:
          eq:
            path: state.auth_mode
            value: basic
        auth:
          basic:
            username: {ref: state.username}
            password: {ref: state.password}
    default:
      auth:
        api_key:
          header: X-Api-Key
          value: {ref: state.api_key}
```

Nested `multi_mode` is forbidden by the validator (recursion is unsafe).
The default arm's `auth` may itself be `none: {}` when the operator wants
unauthenticated fallthrough.

### 1.10 Session cookie via POST login

**What it does:** A login step POSTs credentials to a session endpoint; the
main step reads the resulting `Set-Cookie` and rides it as a request header.

The simple two-step shape is supported — express as a multi-step
`requests:` chain. The login step extracts the cookie via
`from: response.header.Set-Cookie`; the main step rides it as a
`request.headers:` entry via `{ref: extract.<name>}`.

```yaml
requests:
  - id: login
    method: POST
    path: /session/login
    body:
      json:
        username: {ref: state.username}
        password: {ref: state.password}
    extract:
      - name: session_cookie
        from: response.header.Set-Cookie

  - id: data
    method: GET
    path: /api/data
    headers:
      Cookie: {ref: extract.session_cookie}
```

When the login itself has its own expiry, wrap it with `requests[].cache`
(§3.10) so the round-trip is paid once and skipped on subsequent drains.

**Out of scope:** redirect-following for cookie extraction
(`resp.Request.Response.Header` chain across redirect hops). No
escape hatch.

### 1.11 HMAC / SigV4 / OAuth1 signing

**What it does:** Computes a request signature at send-time (HMAC-SHA1,
HMAC-SHA256, AWS SigV4, vendor-proprietary canonical-string schemes).

**Out of scope.** The IR has no canonical-string builder, no body-hash
construction, and no signing-header assembly. A structural signing form
lands when a concrete template motivates the shape; until then there is
no escape hatch.

### 1.12 OAuth2 grants requiring an interactive user

**What it does:** `authorization_code`, `device_code`, and PKCE flows.

**Out of scope.** The pull-loop runtime has no browser-roundtrip surface.
Long-lived refresh tokens that a user has already pasted in are just a
`secret`-typed state field fed into `auth.bearer.token` via
`{ref: state.<name>}`.

---

## 2. Pagination

### 2.1 No pagination (`pagination.none`)

**What it does:** Each iteration fetches all available data in one request.
May still combine with a progress strategy (e.g. a time cursor) for
incremental fetching across iterations.

```yaml
pagination:
  none: {}
```

### 2.2 Page number (`pagination.page_number`)

**What it does:** Increment a page counter each iteration; stop on an
explicit has-next signal or when the returned page is shorter than
`batch_size`.

**IR shape:**

```yaml
pagination:
  page_number:
    page_param: page              # just the param name (no "query." prefix)
    has_more_at: meta.has_next    # optional; body Path to a boolean
    batch_size: {ref: state.page_size}   # optional; short page also terminates
```

The page number is read by the request via `{ref: cursor.page}` —
placement (query vs body) follows where the author writes that Value.

**Variants observed in the wild:** `body.has_more` boolean,
`body.pagination.next != 0` integer signal, `page < total_pages`
arithmetic, page-fills-result heuristic (no `has_more_at`). All
expressible through the `has_more_at` predicate or by relying on the
short-page termination.

### 2.3 Offset (`pagination.offset`)

**What it does:** Increment `offset` / `skip` by `batch_size` each
iteration; terminate when the returned page is shorter than `batch_size`.
The cursor field is `cursor.offset`. When `batch_size` is set, an
`offset_end` role (the exclusive end of the page) is also available for
APIs that want both bounds.

**IR shape:**

```yaml
pagination:
  offset:
    offset_param: offset          # just the param name
    batch_size: {ref: state.page_size}
```

Place the offset in the request via `{ref: cursor.offset}` (query,
header, or body slot). Numeric coercion to string for a query slot uses
`{format: string, value: {ref: cursor.offset}}`.

**Variants:** OData `$skip`/`$top`, body-slot offsets (`start`,
`search_from`), ID-based "start-after" offsets (express by binding the
next offset to a state extract via `target: cursor`).

### 2.4 Cursor token (`pagination.cursor_token`)

**What it does:** Server returns an opaque token in the response body; the
runner captures it into `cursor.token`, and the request reads it back on
the next iteration via an explicit `{ref: cursor.token, default: ""}`.
Default completion fires when `{ref: response.body.<token_at>}` resolves
to zero.

**IR shape:**

```yaml
requests:
  - method: GET
    path: /v1/events
    query:
      cursor: {ref: cursor.token, default: ""}   # explicit; default keeps
                                                 # the bootstrap iteration's
                                                 # wire shape (sends "cursor=").

pagination:
  cursor_token:
    token_at: response.body.next_cursor          # namespace-rooted Path to the next-page token
```

The token is no longer auto-injected — the request slot is declared
explicitly. Use `default: ""` to keep the bootstrap iteration sending an
empty value (rather than omitting the parameter entirely); omit `default`
to skip the slot until the cursor populates.

**Header-based tokens:** when the next-page token rides on a response header
rather than the body, capture it via
`extract: [{from: response.header.<name>, name: <var>, target: cursor}]`
on the step and feed it forward via `{ref: cursor.<var>}` on the next
iteration.

**Search-after subtype:** APIs that advance pages by passing the last seen
sort value or ID (rather than an opaque server-issued token) model
identically — `token_at` points at a sortable response field, and the
client carries that field's value forward as the next-page cursor.

### 2.5 Link header (`pagination.link_header`)

**What it does:** Extract the next-page URL from a response header. RFC 5988
`Link: <url>; rel="next"` is the default pattern; non-standard header
names or formats override via `pattern:` (a regex whose first capture group
is the next URL).

The runner parses the header into `cursor.next_link` but does **not**
substitute it into the request — wiring the URL back is the template's
job. Read it in the request's `url` slot via
`{ref: cursor.next_link, default: <bootstrap-url>}`: the first iteration
falls through to the bootstrap URL, every later iteration follows the
server's link. The drain ends when a response carries no `rel="next"`
entry.

**IR shape:**

```yaml
requests:
  - method: GET
    url:
      ref: cursor.next_link
      default:
        concat:
          - {ref: state.url}
          - /v1/incidents

pagination:
  link_header:
    pattern: '<([^>]+)>;\s*rel="next"'   # optional; default = RFC 5988
```

### 2.6 Next URL in body (`pagination.next_url_in_body`)

**What it does:** Response body carries the full next-page URL at
`next_url_at`. The runner parses it into `cursor.next_url` but — like
`link_header` — does **not** substitute it into the request automatically.
Read it back in the request's `url` slot via
`{ref: cursor.next_url, default: <bootstrap-url>}`. A missing, non-string,
or empty value terminates the drain.

**IR shape:**

```yaml
requests:
  - method: GET
    url:
      ref: cursor.next_url
      default:
        concat:
          - {ref: state.url}
          - /v1/alerts

pagination:
  next_url_in_body:
    next_url_at: meta.next_page             # or "@odata.nextLink", links.next, etc.
```

**Encoded-token URL repair:** some APIs double-encode their next-page
token inside the next-page URL. Express the decode-and-re-encode as a
`Value` chain on the request URL, or accept the URL verbatim and let the
target round-trip it. The runner does not silently repair the encoding.

### 2.7 Scroll / session ID (`pagination.scroll_id`)

**What it does:** First iteration leaves the slot unset (server opens a
fresh session); later iterations replay the id captured at `scroll_id_at`
into `cursor.scroll_id`, read back through an explicit
`{ref: cursor.scroll_id}` on the request. When the id resolves to a zero
`Value`, the drain terminates and the cursor slot is cleared so the next
drain opens a fresh session. Optional `complete_when` predicate forces
termination on a server signal — its predicate may reference
`response.body.<path>` against the producer body.

**IR shape:**

```yaml
requests:
  - method: GET
    path: /v1/scroll
    query:
      scroll: {ref: cursor.scroll_id}      # no default — bootstrap omits the slot

pagination:
  scroll_id:
    scroll_id_at: response.body.request_metadata.scroll
    complete_when:                         # optional
      eq:
        path: response.body.request_metadata.complete
        value: "true"
```

### 2.8 GraphQL relay cursor (`pagination.graphql_relay`)

**What it does:** Relay-style pagination with `pageInfo.endCursor` and
`pageInfo.hasNextPage`. The cursor variable name carries the cursor into
GraphQL `variables:` on the next iteration; the runner reads from
`cursor.<cursor_var>` and writes the new end cursor back. Read it inside
the request via `{ref: cursor.<cursor_var>}` (e.g. `{ref: cursor.after}`).

**IR shape:**

```yaml
pagination:
  graphql_relay:
    has_next_page_at: response.body.data.issues.pageInfo.hasNextPage
    end_cursor_at: response.body.data.issues.pageInfo.endCursor
    cursor_var: after
```

Pair with `body.json` carrying `query` + a `variables` Object (§3.4) —
there is no separate `body.graphql` variant.

### 2.9 Async-job polling (202 → poll → fetch)

**What it does:** The API returns 202 with a job/task ID; the runner polls
until completion, then fetches results. Modelled at the progress layer, not
the pagination layer, because the loop runs across iterations rather than
within one.

See §5.5 (`progress.async_job`).

### 2.10 Time-window pagination

**What it does:** Each drain covers a `[start_time, end_time]` window;
after completion the window slides forward. Combines orthogonally with
`pagination.offset` (offset paginates *within* the window). Modelled at
the progress layer, not the pagination layer.

See §5.2 (`progress.time_window`).

### 2.11 Export + blob download

**What it does:** A list step returns blob URLs; a dependent step GETs each
blob in turn. When the export is asynchronous (submit → poll → fetch), the
final fetch role is the blob download.

The list → per-blob shape is expressible via `requests:` + per-step
`fan_out: {over, as, merge: flatten}` — `fan_out` is **deferred** (the
validator accepts it, the runner does not yet execute it). The
async-export shape is supported via `progress.async_job`. MIME-decoding
the downloaded blobs (gzip / CSV / NDJSON chains) is **out of scope** and
is expected to live in the ingest pipeline.

### 2.12 Worklist / multi-phase

**What it does:** Same-iteration list → per-item detail fan-out, or
cross-iteration worklist draining.

Same-iteration `list → detail` is expressible via `requests:` with a
per-step `fan_out: {over, as, merge: flatten}` — `fan_out` is
**deferred**. Cross-iteration worklist draining (seed worklist, drain one
per evaluation, LIFO/FIFO queues, retry budgets) is **out of scope**. No
escape hatch.

### 2.13 Wiring pagination signals into the request

**What it does:** Every pagination strategy surfaces its active value
through a plain `cursor.<name>` field; the author wires it into the
request explicitly. There is no auto-injection — the slot the value
rides in (query, header, body) is always wherever the author writes
the ref.

| Strategy             | Cursor field            | How to wire it back                                                |
|----------------------|-------------------------|--------------------------------------------------------------------|
| `cursor_token`       | `cursor.token`          | `{ref: cursor.token, default: ""}` in `query` / `headers` / body.  |
| `page_number`        | `cursor.page`           | `{ref: cursor.page}` (typically as a `{format: string, ...}`).     |
| `offset`             | `cursor.offset` (+ `cursor.offset_end` when `batch_size` is set) | `{ref: cursor.offset}` / `{ref: cursor.offset_end}`. |
| `scroll_id`          | `cursor.scroll_id`      | `{ref: cursor.scroll_id}` (no default — bootstrap omits the slot). |
| `graphql_relay`      | `cursor.<cursor_var>`   | `{ref: cursor.<cursor_var>}` inside the GraphQL `variables` object. |
| `link_header`        | `cursor.next_link`      | `{ref: cursor.next_link, default: <bootstrap-url>}` in the `url` slot. |
| `next_url_in_body`   | `cursor.next_url`       | `{ref: cursor.next_url, default: <bootstrap-url>}` in the `url` slot. |

`default: ""` on `cursor.token` keeps the bootstrap iteration sending
the slot with an empty value (preserves the historical
`?cursor=` wire shape); omit `default` to skip the slot until the
cursor populates. `scroll_id` typically omits `default` so the server
opens a fresh session on the bootstrap iteration.

---

## 3. Request types

### 3.1 GET with query parameters

**What it does:** Standard GET. Query slots accept any `Value`; each value
must resolve to a string at request time (use `{format: string, value: ...}`
to coerce a numeric `Value` to a query string).

**IR shape:**

```yaml
requests:
  - id: main
    method: GET
    path: /v1/events
    query:
      since: {ref: cursor.window_start}
      page: {format: string, value: {ref: cursor.page}}
      type: {ref: state.event_type}
```

### 3.2 GET with no parameters

**What it does:** Simple GET to a fixed or cursor-constructed URL. Express
as a `path:` (relative to `defaults.base_url`) or a fully-qualified `url:`
resolved from a `Value`. `path:` and `url:` are mutually exclusive on a
single request.

### 3.3 POST with JSON body

**What it does:** Body is a JSON object whose top-level keys are literal
strings; each value is a `Value`. Nested object literals at any depth use
the explicit `{object: {...}}` Value form (a bare YAML mapping at a
`Value` position is rejected unless it carries a discriminator key).

**IR shape:**

```yaml
requests:
  - id: main
    method: POST
    path: /api/search
    body:
      json:
        time_range:
          object:
            start: {ref: cursor.window_start}
            end:   {ref: cursor.window_end}
        limit: 100
```

### 3.4 POST with GraphQL query

**What it does:** GraphQL has no dedicated body variant — encode it as
`body.json` with a `query` scalar and a `variables` Object. Pair with
`pagination.graphql_relay` (§2.8) to drive the cursor variable.

**IR shape:**

```yaml
requests:
  - id: main
    method: POST
    path: /graphql
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
            after: {ref: cursor.after}
            first: {ref: state.page_size}
```

### 3.5 POST with form-encoded body

**What it does:** Body is serialised as `application/x-www-form-urlencoded`.
Common for legacy APIs and for token endpoints expressed as plain
requests (OAuth2 token endpoints handled by `auth.oauth2` set their own
form body internally).

**IR shape:**

```yaml
requests:
  - id: main
    method: POST
    path: /api/events
    body:
      form:
        since: {ref: cursor.last_timestamp}
        limit: {format: string, value: {ref: state.page_size}}
        format: json
```

### 3.6 POST with raw body

**What it does:** Send an arbitrary opaque body. The resolved string is
sent verbatim; no `Content-Type` is set automatically (the operator sets
one via `headers:` if required). Less common than JSON / form; reserved
for APIs whose content type does not match the structured cases (NDJSON
ingest endpoints, hand-tuned XML payloads, GraphQL-via-text/plain).

```yaml
requests:
  - method: POST
    path: /ingest
    headers:
      Content-Type: application/x-ndjson
    body:
      raw:
        concat:
          - '{"tenant":"'
          - {ref: state.tenant_id}
          - '","event":"hello"}'
```

### 3.7 Per-step `if:` gating

**What it does:** Skip a step when its `if:` predicate is false. Combines
with multi-step `requests:` chains to express conditional pre-fetches and
cache short-circuits. Predicates use the discriminated map form:

```yaml
requests:
  - id: data
    method: GET
    path: /events
    if:
      eq:
        path: state.etag_check
        value: true
```

When a step is skipped, `{ref: steps.<id>.body.<...>}` resolves to a zero
`Value`; downstream `{select: ...}` branches should guard with
`{present: steps.<id>.body.<...>}`.

### 3.8 Per-step `on_status:`

**What it does:** When a request returns a status outside `expect_status:`
(default `[200]`), dispatch to one of: `skip`, `fail`, `empty_events`, or
`invalidate_cache`. `invalidate_cache` is OAuth2-aware (see §1.8) and
also drops `requests[].cache` slots; the other verbs are unconditional.

`on_status:` is a `map[int]string` keyed by exact HTTP status code in
`[100, 599]`. There is no `retry` verb until the retry/backoff contract
lands.

```yaml
requests:
  - id: main
    method: GET
    path: /events
    on_status:
      401: invalidate_cache
      429: empty_events
      304: skip
```

### 3.9 Sequential request chain (A → B → C)

**What it does:** Multiple steps with `id:` per step. Later steps reference
earlier bodies via `{ref: steps.<id>.body.<path>}` and earlier extracts via
`{ref: extract.<name>}`. The producer step (the one whose decoded body
`response.events_at` applies to) is the last request by default; set
`produces_events: true` on a different step to override.

**IR shape:**

```yaml
requests:
  - id: login
    method: POST
    path: /auth/signin
    body:
      json:
        user: {ref: state.username}
        pass: {ref: state.password}
    extract:
      - name: session_cookie
        from: response.header.Set-Cookie

  - id: main
    method: GET
    path: /api/data
    headers:
      Cookie: {ref: extract.session_cookie}
```

There is no separate `pre_fetch:` block — a single pre-flight step is
just the first entry in `requests:`.

### 3.10 Step-level cache (`requests[].cache`)

**What it does:** Wraps a token-style step (a custom JSON login, a
session-key exchange) in a fresh-vs-cached conditional — the non-OAuth2
counterpart of the OAuth2 token cache (§1.8). `store_in` names *both* the
top-level response field captured *and* the state slot it lands in.
`expiry_field` is a `Path` rooted at `response.body.<path>` of the cached
step's own response carrying the lifetime; `expiry_buffer` is the "don't
cut it too close" margin; `expiry_format` selects how `expiry_field` is
read — `duration` (default: a remaining lifetime, integer seconds or a
Go duration string, same as OAuth2 `expires_in`) or one of the
absolute-instant formats (`unix_seconds`, `unix_millis`, `rfc3339`,
`rfc3339nano`). The `store_in` key is auto-registered as a runtime
`string` state field and survives across iterations; its paired
`<store_in>_expires_at` slot auto-registers the same way. The step is
re-run when `now + expiry_buffer >= cached_expires_at`; the expiry
timestamp lives at `state.<store_in>_expires_at` as RFC 3339 (slice 7
moved this slot out of the cursor namespace).

**IR shape:**

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
      store_in: session_token
      expiry_field: expires_in
      expiry_buffer: 60s
      expiry_format: duration

auth:
  bearer:
    token: {ref: state.session_token, default: "pending"}
```

`default: "pending"` keeps auth resolution from failing on the very first
drain (before the slot is populated) when the login endpoint itself
ignores the `Authorization` header.

**Cache invalidation:** A request step that declares
`on_status: {401: invalidate_cache}` drops every step-cache slot
(`state.<store_in>` + `state.<store_in>_expires_at`) alongside the
OAuth2 cache slots (§1.8), then advances as if the page came back empty.
The next drain misses the cache and re-runs the login step.

### 3.11 Per-item fan-out

**What it does:** A step iterates over a list `Value` (typically extracted
from the previous step's response); the runner runs the step once per item
with `item.<path>` bound. `merge: flatten` concatenates per-item response
lists; `wrap` keeps them as a list-of-lists.

**Deferred.** `scope.item` is reserved in the state machine and `fan_out`
is accepted by the validator; per-item dispatch is not yet executed by
the runner.

**IR shape (validator passes, runner does not yet execute):**

```yaml
requests:
  - id: list
    method: GET
    path: /incidents
    extract:
      - name: ids
        from: response.body.items   # list of objects with .id

  - id: detail
    method: GET
    path:
      concat:
        - /incidents/
        - {ref: item.id}
    fan_out:
      over: {ref: extract.ids}
      as: item
      merge: flatten
```

---

## 4. Response parsing

### 4.1 JSON, single object at root

**What it does:** The response body is a single JSON object; the runner
wraps it in a one-element list to produce one event. There is no
`wrap_single` flag — the wrap is inferred from `events_at` being empty
(body root) with a non-array body.

**IR shape:**

```yaml
response:
  decode: json
  events_at: ""
```

### 4.2 JSON array at root

**What it does:** The response body *is* the JSON array.

**IR shape:**

```yaml
response:
  decode: json
  events_at: ""
```

### 4.3 JSON array at a nested path

**What it does:** The body is an object; events live at a known dotted body
Path (`data`, `value`, `result`, `items`, `resources`, `hits.hits`,
`data.<queryName>.nodes`, etc.).

**IR shape:**

```yaml
response:
  decode: json
  events_at: response.body.data.items
```

`events_at` is a [Path](schema.md#paths) rooted at
`response.body.<path>` (or `steps.<id>.body.<path>` for a labelled
prior step). The empty Path (`events_at: ""`) means "body root" — the
whole decoded body IS the events list. The validator rejects bare
dotted strings (`data.items`) and unknown roots.

### 4.4 NDJSON

**What it does:** The body is newline-delimited JSON, one object per line.
The runner decodes each non-empty line. When `events_at` is empty, each
decoded line IS one event; when `events_at` is non-empty, the Path is
applied to EACH decoded line and the flattened sequence is the events
list.

```yaml
response:
  decode: ndjson
  events_at: ""
```

`response.decode` is a closed enum (`json | ndjson`). Per-line custom
decoding and bespoke empty-line filtering are out of scope.

### 4.5 Placeholder events (`response.placeholder_event`)

**What it does:** When a real-events list is empty AND pagination wants
another iteration, the runner queues a synthetic event (typically an
operator-supplied object literal expressed as `{object: {...}}`).
Emission is confirmed at the start of the next iteration (after
ctx-cancel and `MaxPages` checks have cleared). Three guarantees:
variant-independent across all pagination strategies, wire-order matches
drain order, conservative under early exit.

**IR shape:**

```yaml
response:
  decode: json
  events_at: response.body.results
  placeholder_event:
    object:
      message: "retry"
```

The ingest-pipeline rule that filters the synthetic event lives outside the
runner — that is operator-side configuration, not IR.

### 4.6 ZIP / gzip / CSV / MIME-chain decoding

**What it does:** Decoding compressed or tabular response bodies, optionally
chained (`gzip → CSV`, `gzip → NDJSON`, `zip → JSON`).

**Out of scope.** `response.decode` is a closed `json | ndjson` enum by
design; MIME chaining is expected to live in the ingest pipeline.

> Note: there is no `response.success_condition` /
> `response.success_status` field. The HTTP status-code success set is
> configured per-step via `requests[].expect_status` (default `[200]`).
> Per-status verbs (treat 429 as `empty_events`, 304 as `skip`, etc.)
> live on `requests[].on_status` — see §3.8.

---

## 5. Cursor and progress

### 5.1 Stateless (`progress.stateless`)

**What it does:** No cursor advance; every drain fetches the full dataset.
Public feeds, demo integrations.

```yaml
progress:
  stateless: {}
```

### 5.2 Time-window cursor (`progress.time_window`)

**What it does:** Each drain covers `[window_start, window_end]`. First
drain runs over `[now() - initial_offset, now()]`; advance slides
`window_start` to the just-finished `window_end`. `window_end` clamps to
≥ `window_start` so a backwards-running clock cannot invert the window.
Format choices: `rfc3339` (default), `rfc3339nano`, `unix_seconds`,
`unix_millis`.

**IR shape:**

```yaml
progress:
  time_window:
    initial_offset: 24h
    format: rfc3339
```

Window bounds land on request slots via `{ref: cursor.window_start}`
and `{ref: cursor.window_end}`. Combines orthogonally with
`pagination.offset` (offset paginates *within* the window).

### 5.3 Latest-event timestamp (`progress.latest_event_timestamp`)

**What it does:** After each drain, advance `cursor.last_timestamp` to the
maximum event-time across the drain's emitted events. The next drain
reads `since` from `{ref: cursor.last_timestamp}` with first-run
fallback to `initial.lookback`. Optional per-iteration `lookback` lets
the window overlap on every advance, not just the first.

**IR shape:**

```yaml
progress:
  latest_event_timestamp:
    event_time:
      path: created_at
    initial:
      lookback: 7d
    lookback: 60s            # optional; subtracted on every advance
```

### 5.4 Max-of-list cursor (`progress.max_event_field`)

**What it does:** Same shape as `latest_event_timestamp` but advances to
the *maximum* value of the named field across the drain's emitted events,
treated as an arbitrary monotonically-increasing field (numeric id, etc.)
rather than specifically a timestamp. Shares the events-walk helper with
`latest_event_timestamp`.

```yaml
progress:
  max_event_field:
    event_time:
      path: id
```

### 5.5 Async-job dispatcher (`progress.async_job`)

**What it does:** Submit → poll → fetch state machine. The runner
dispatches on `cursor.phase` (default `submit`): one HTTP step per
evaluation. The `async_job:` block assigns step ids to roles (via
`step:`), declares per-role `extract:` maps (each entry is
`<name>: {from: response.body.<path>}` — or `steps.<id>.body.<path>` for
a labelled prior step) that flow into the cursor, and declares
`poll.complete_when` (a `Predicate`, with `response.body.<path>` valid
inside it) for the loop-exit signal. Inner re-poll (stay-in-poll while the
completion predicate is false) is handled by the runner on slow jobs.
`on_complete.cursor_update.kind` chooses how the cursor advances after
the fetch: `stateless`, `use_now`, or `latest_event_timestamp`
(the last form requires `event_time: {path: ...}`).

Each role is independently optional: at least one of `submit`, `poll`,
`fetch` must be set. The events-bearing step is the last-declared role
(`fetch` > `poll` > `submit`) unless one of the requests carries
`produces_events: true`.

**IR shape:**

```yaml
requests:
  - id: submit
    method: POST
    path: /exports
    expect_status: [202]

  - id: poll
    method: GET
    path:
      concat:
        - /exports/
        - {ref: cursor.export_id}
        - /status

  - id: fetch
    method: GET
    url: {ref: cursor.result_url}

progress:
  async_job:
    submit:
      step: submit
      extract:
        export_id: {from: response.body.export_id}
    poll:
      step: poll
      complete_when:
        eq:
          path: response.body.status
          value: complete
      extract:
        result_url: {from: response.body.result_url}
    fetch:
      step: fetch
    on_complete:
      cursor_update:
        kind: stateless
```

The cursor namespace auto-provides `cursor.phase`, plus every name
declared in `submit.extract` / `poll.extract`. The scalar shorthand
(`cursor_update: use_now`) is rejected at parse time — use the map form
`{kind: use_now}`.

### 5.6 Use-now (`progress.use_now`)

**What it does:** Advance writes `cursor.last_timestamp = now() - lookback`
every drain — no events walk. Suits APIs where the drain itself defines
the cutoff (the next iteration is "everything since the last invocation").

```yaml
progress:
  use_now:
    lookback: 5m         # optional
```

### 5.7 Multi-field cursor

**What it does:** Multiple cursor fields advance together (timestamp +
worklist tail; timestamp + offset; per-entity timestamps).

Multiple `progress.*` strategies do not compose today. Authors can stash
auxiliary cursor fields via `requests[].extract` with `target: cursor`
(an auto-registered cursor field that persists across iterations).
Complex multi-field cursors with conditional advance logic remain out of
scope.

```yaml
requests:
  - id: data
    method: GET
    path: /events
    extract:
      - name: high_water_id
        from: response.body.meta.last_id
        target: cursor       # writes to cursor.high_water_id
```

### 5.8 Conditional fetch / ETag

**What it does:** Skip the work when the upstream has not changed —
e.g. store an `ETag` (or `Last-Modified`) in the cursor, send
`If-None-Match` on the next iteration, and treat 304 as "advance cursor
without emitting events".

**Supported.** Express via:

- `extract:` with `from: response.header.<name>, target: cursor` to
  capture the ETag into the cursor namespace,
- `headers:` reading it back as `If-None-Match` via
  `{ref: cursor.<name>, default: <empty-string>}`,
- `on_status: {304: skip}` so the 304 response yields the skip-block
  (empty events, cursor preserved).

```yaml
requests:
  - id: data
    method: GET
    path: /events
    headers:
      If-None-Match: {ref: cursor.etag, default: ""}
    extract:
      - name: etag
        from: response.header.ETag
        target: cursor
    on_status:
      304: skip
```

A first-class "wrap the fetch in a conditional HEAD then GET" branch is
not yet modelled — `if:` is evaluated before the step runs, so the two
ways to dispatch on a prior response status are `on_status:` on the
prior step or a `select` over the prior body.

### 5.9 State machine across iterations

**What it does:** Three-or-more-phase orchestration with LIFO / FIFO
worklists, retry budgets, conditional branching, or cross-iteration
queue draining.

**Out of scope.** The two-phase shapes are covered: submit → poll →
fetch via `progress.async_job` (§5.5); list → detail via `requests:` +
`fan_out:` (§3.11, deferred). Beyond that, no escape hatch.

---

## 6. Cross-cutting

### 6.1 Token caching across iterations

**What it does:** Cache an OAuth2 / session token in state with an expiry
field; the runner checks expiry before re-fetching, avoiding a token
round-trip on every iteration.

See `auth.oauth2.<grant>.cache` (§1.8) for OAuth2 token caching.

For non-OAuth2 cached-login endpoints (custom session tokens with their own
expiry), `requests[].cache` (§3.10) wraps the login step in the same
fresh-vs-cached conditional — the step is skipped while the cached token is
still inside its expiry buffer.

### 6.2 Cross-iteration state

**What it does:** Values that are not cursor fields but must survive
across iterations (cached tokens, derived config).

There is no `state.passthrough:` block. Persistence rules:

- `state.fields.<name>` with `mutability: runtime` is round-tripped
  through the `Store`; runtime writes survive across iterations and
  drains.
- `auth.oauth2.<grant>.cache.store_in` and `requests[].cache.store_in`
  auto-register their slot as a `mutability: runtime` `string` field
  (authors must NOT also declare it under `state.fields`).
- Auxiliary cursor fields go through `extract:` with `target: cursor`
  (§5.7) — these auto-register a cursor field that persists alongside
  the strategy-inferred ones.
- `state.fields.<name>` with `mutability: config` (the default) is
  operator-supplied and NOT written back at runtime.

### 6.3 Dual-mode behaviour (`auth.multi_mode` and beyond)

**What it does:** Runtime branching on a state flag. Auth-level branching
is covered by `auth.multi_mode` (§1.9). Within a single request, branching
the URL / body / headers on a flag is covered by `{select: {branches: [...], default: ...}}`
on any `Value` slot. URL/endpoint or request-shape branching that needs
to swap a whole request out for a different one (e.g. `gov_cloud` selecting
a completely different endpoint set) is *not* yet modelled as a first-class
form — express it via `select` on individual `path` / `body` slots, or via
`if:` gating two parallel request chains.

### 6.4 Secret redaction in logs / traces

**What it does:** Every log line, error message, and `Tracer` field that
mentions an `schema.Value` routes through `redactValue(doc, v)`. URLs go
through `safeURL` (scheme + host + path; query and userinfo stripped).
`Authorization` / `Cookie` / `Proxy-Authorization` headers are always
redacted by name; the runtime credential surfaces named by
`auth.custom.header` and `auth.api_key.header` are redacted by name; any
`request.headers` / `request.query` entry whose IR `Value` is
`schema.IsSecret` is redacted by content. Request and response bodies
are metadata-only (byte length + leading-byte classification), never
raw bytes.

`schema.IsSecret(doc, v)` walks the full `Value` tree (Ref + Default,
Now.Offset, Concat, Select branches + default, Format.Value, Base64,
List, Object) so secret-ness propagates through composition.

### 6.5 HTTP transport: timeouts, proxies, retries

**What it does:** Default 30-second per-request timeout. Callers inject a
custom `*http.Client` to control proxy, mTLS, custom transport, redirect
policy, DNS overrides.

Retry / backoff / rate-limit policy is **never** modelled at the runner
layer. Authors who need retries plug them into the injected transport.

### 6.6 Observability: structured trace per HTTP exchange

**What it does:** `Runner.Tracer` receives one `Exchange` record per HTTP
exchange, carrying iteration, phase, step id, redacted URL, redacted query
map, redacted post-auth headers, body metadata, status, elapsed, error
string. The `JSONLTracer` writes one JSON object per exchange.

Metrics surface (drain duration, events/page, error rate by endpoint)
is deferred — a `Tracer` can compute most of these by aggregating
per-exchange records.
