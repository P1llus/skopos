# API method reference

The canonical list of API-communication shapes the runner can express,
grouped by concern (authentication, pagination, request body, response
parsing, cursor state, cross-cutting). Each entry names the shape,
gives a one-paragraph description, and names the IR knobs that express
it. The IR vocabulary itself lives in [`schema.md`](schema.md); the
runtime contract lives in [`runtime.md`](runtime.md).

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
auth: { type: none }
```

### 1.2 Bearer token (`auth.bearer`)

**What it does:** Sends `Authorization: Bearer <token>` where the token is a
`Value` (typically a `state.*` ref or an extract from a `pre_fetch:` step).

**IR shape:**

```yaml
auth:
  type: bearer
  bearer:
    token: { ref: state.access_token }
```

**Variants:**

- *Static from state* — the token is set on the state block (e.g. supplied by
  the operator or by an upstream OAuth2 dance outside the runner).
- *From a pre-fetch* — a `pre_fetch:` step POSTs to a token endpoint and
  extracts the access token; the main request references it via
  `{from_pre_fetch: access_token}`.
- *Raw-token / non-`Bearer` prefix schemes* — APIs that read the value with
  no `Bearer ` prefix or as a literal `Authorization` header value are
  expressed via `auth.custom` (§1.5), not `auth.bearer`.

### 1.3 Basic auth (`auth.basic`)

**What it does:** Base64-encodes `username:password` and sends it as
`Authorization: Basic <encoded>`.

**IR shape:**

```yaml
auth:
  type: basic
  basic:
    username: { ref: state.username }
    password: { ref: state.password }
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
  type: api_key
  api_key:
    header: X-Api-Key            # or any vendor-specific header name
    value: { ref: state.api_key }
    in_query: false              # set true to send via query param instead
```

**Common variants:** mixed-case headers (`X-Api-Key`, `x-api-key`,
`api-key`), vendor-specific names (`X-Auth-Token`, `X-RFToken`,
`Private-Token`), compound formats (`accessKey=…;secretKey=…` as a single
header value composed via a `Value`).

### 1.5 Custom auth header / scheme (`auth.custom`)

**What it does:** A single-header auth scheme that does not fit `bearer` /
`basic` / `api_key`. Author supplies the header name and the full value
`Value`.

**IR shape:**

```yaml
auth:
  type: custom
  custom:
    header: Authorization
    value:
      concat:
        - { literal_string: "ApiKey " }
        - { ref: state.api_token }
```

**Use cases:** literal `Authorization: <token>` with no scheme prefix;
non-standard `Authorization: ApiKey …` / `Authorization: Token …` /
`Authorization: basic <opaque>` shapes; vendor-specific headers like
`PS-Auth key=…;runas=…`. Additional non-auth headers ride on
`request.headers:`.

### 1.6 OAuth2 client-credentials grant (`auth.oauth2.client_credentials`)

**What it does:** The runner POSTs `grant_type=client_credentials` to a
configured `token_url` with HTTP Basic `client_id:client_secret`, plus
optional `scope=...` / `audience=...` parameters (RFC 6749 §2.3.1). The
returned access token rides as `Authorization: Bearer <token>` on every
IR-described request. Non-2xx token responses surface with body-classification
metadata only — never the raw bytes.

**IR shape:**

```yaml
auth:
  type: oauth2
  oauth2:
    grant: client_credentials
    token_url: https://example.com/oauth/token
    client_id: { ref: state.client_id }
    client_secret: { ref: state.client_secret }
    scope: { ref: state.scope }      # optional
    audience: { literal_string: "wiz-api" }  # optional
```

### 1.7 OAuth2 password grant (`auth.oauth2.password_grant`)

**What it does:** POSTs `grant_type=password` + `username` / `password` in
the form body (RFC 6749 §4.3.2). `client_id` is optional and rides in the
form body when set. Same Bearer-injection and token-fetch error contract as
`client_credentials`.

**IR shape:**

```yaml
auth:
  type: oauth2
  oauth2:
    grant: password
    token_url: https://example.com/oauth/token
    username: { ref: state.username }
    password: { ref: state.password }
    client_id: { ref: state.client_id }   # optional
```

### 1.8 OAuth2 token caching (`auth.oauth2.<grant>.cache`)

**What it does:** Wraps the OAuth2 token fetch in a fresh-vs-cached
conditional. `store_in` names the state key the cached token struct lands in;
`expiry_field` is the response-body field carrying the TTL (interpreted as
integer seconds, RFC 6749 `expires_in`, with `string→int` and
`string→Go-duration` fallbacks); `expiry_buffer` is the "don't cut it too
close" margin. The store_in key is auto-registered as a runtime string and
auto-added to state passthrough so the cached token survives across
iterations. Fresh fetch fires when
`now + expiry_buffer >= cached_expires_at`. The expiry timestamp lives at
`cursor.__oauth2_<store_in>_expires_at` as RFC 3339.

**IR shape:**

```yaml
auth:
  type: oauth2
  oauth2:
    grant: client_credentials
    # ... grant fields above ...
    cache:
      store_in: access_token
      expiry_field: expires_in
      expiry_buffer: 60s
```

**Cache invalidation:** A request step that declares `on_status:
{code: 401, do: invalidate_cache}` drops every reachable OAuth2 cache slot
(including each branch of an `auth.multi_mode` dispatch), then advances as
if the page came back empty. The next request misses the cache and forces a
fresh token fetch. When the active auth has no cache (or is non-OAuth2) the
verb degrades to `empty_events` with a log line.

### 1.9 Multi-mode auth (`auth.multi_mode`)

**What it does:** Selects an auth strategy at runtime by walking
`branches[].when` predicates in declaration order — first match wins;
`default.auth` fires when no branch matches. Common shape: a state flag picks
between OAuth2, Basic, and API key.

**IR shape:**

```yaml
auth:
  type: multi_mode
  multi_mode:
    branches:
      - when: { eq: [{ ref: state.auth_type }, { literal_string: "bearer" }] }
        auth:
          type: bearer
          bearer:
            token: { ref: state.api_token }
      - when: { eq: [{ ref: state.auth_type }, { literal_string: "basic" }] }
        auth:
          type: basic
          basic:
            username: { ref: state.username }
            password: { ref: state.password }
    default:
      auth:
        type: api_key
        api_key:
          header: X-Api-Key
          value: { ref: state.api_key }
```

Nested `multi_mode` is forbidden by the validator (recursion is safe).

### 1.10 Session cookie via POST login

**What it does:** A login step POSTs credentials to a session endpoint; the
main step reads the resulting `Set-Cookie` and rides it as a request header.

The simple two-step shape is supported — express as a multi-step
`requests:` chain. The login step extracts the cookie from
`{ref: steps.login.resp.Header.Set-Cookie}`; the main step rides it as a
`request.headers:` entry.

**Out of scope:** redirect-following for cookie extraction
(`resp.Request.Response.Header` chain across redirect hops). No
escape hatch.

### 1.11 HMAC / SigV4 / OAuth1 signing

**What it does:** Computes a request signature at send-time (HMAC-SHA1,
HMAC-SHA256, AWS SigV4, vendor-proprietary canonical-string schemes).

**Out of scope.** The IR has no canonical-
string builder, no body-hash construction, and no signing-header assembly.
A structural signing form lands when a concrete template motivates the
shape; until then there is no escape hatch (the previous `Value.raw`
target-keyed hatch was removed).

### 1.12 OAuth2 grants requiring an interactive user

**What it does:** `authorization_code`, `device_code`, and PKCE flows.

**Out of scope.** The pull-loop runtime has no
browser-roundtrip surface. Long-lived refresh tokens that a user has
already pasted in are just `state.<name>.type: secret` +
`auth.bearer.token: {ref: state.<name>}`.

---

## 2. Pagination

### 2.1 No pagination (`pagination.none`)

**What it does:** Each iteration fetches all available data in one request.
May still combine with a progress strategy (e.g. a time cursor) for
incremental fetching across iterations.

```yaml
pagination: { strategy: none }
```

### 2.2 Page number (`pagination.page_number`)

**What it does:** Increment a page counter each iteration; stop on an
explicit has-next signal or when the returned page is shorter than
`batch_size`.

**IR shape:**

```yaml
pagination:
  strategy: page_number
  page_param: query.page
  has_more_at: body.has_more     # optional; dotted body path to a boolean
  batch_size: { literal_int: 100 }  # optional; short page also terminates
```

**Variants observed in the wild:** `body.has_more` boolean,
`body.pagination.next != 0` integer signal, `page < total_pages`
arithmetic, page-fills-result heuristic (no `has_more_at`). All expressible
through the `has_more_at` predicate or by relying on the short-page
termination.

### 2.3 Offset (`pagination.offset`)

**What it does:** Increment `offset` / `skip` by `batch_size` each
iteration; terminate when the returned page is shorter than `batch_size`.
Cursor key naming derives from the slot path (`query.offset` →
`cursor.offset`). When `batch_size` is set, an `offset_end` role is
available for APIs that want both bounds.

**IR shape:**

```yaml
pagination:
  strategy: offset
  offset_param: query.offset      # or body.start, query.$skip, etc.
  batch_size: { literal_int: 1000 }
```

**Variants:** OData `$skip`/`$top`, body-slot offsets (`start`, `search_from`),
ID-based "start-after" offsets (`startid = last_id + 1`) expressed by binding
the next offset to a state extract.

### 2.4 Cursor token (`pagination.cursor_token`)

**What it does:** Server returns an opaque token in the response body; the
runner sends it back on the next request. The runner derives the cursor
field name from the `send_as` slot.

 Default completion fires when `{ref: <token_at>}`
resolves to zero.

**IR shape:**

```yaml
pagination:
  strategy: cursor_token
  token_at: body.meta.pagination.next     # dotted body path to the token
  send_as: query.cursor                   # query.<key> or body.<key>
```

**Header-based tokens:** when the next-page token rides on a response header
rather than the body, extract it via `extract: [{ source: header, ... }]`
on the step and use the extracted value as the cursor input on the next
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
substitute it into the request — `link_header` has no `send_as` slot, so
wiring the URL back is the template's job. Read it in the request's `url`
slot via `{ref: cursor.next_link, default: <bootstrap-url>}`: the first
iteration falls through to the bootstrap URL, every later iteration follows
the server's link. The drain ends when a response carries no `rel="next"`
entry.

**IR shape:**

```yaml
requests:
  - method: GET
    url:
      ref: cursor.next_link
      default: { concat: [ { ref: state.url }, /v1/incidents ] }

pagination:
  strategy: link_header
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
      default: { concat: [ { ref: state.url }, /v1/alerts ] }

pagination:
  strategy: next_url_in_body
  next_url_at: body."@odata.nextLink"   # or body.next, body.links.next, etc.
```

**Encoded-token URL repair:** some APIs double-encode their next-page
token inside the next-page URL. Express the decode-and-re-encode as a
`Value` chain on the request URL, or accept the URL verbatim and let the
target round-trip it. The runner does not silently repair the encoding.

### 2.7 Scroll / session ID (`pagination.scroll_id`)

**What it does:** First iteration leaves the role unset (server opens a
fresh session); later iterations replay the id captured at `scroll_id_at`.
When the id resolves to a zero Value, the drain terminates and the cursor
slot is cleared so the next drain opens a fresh session. Optional
`complete_when` predicate forces termination on a server signal.

**IR shape:**

```yaml
pagination:
  strategy: scroll_id
  scroll_id_at: body.responseEnvelope.scrollId
  complete_when:                    # optional
    eq:
      - { body: complete }
      - { literal_bool: true }
```

### 2.8 GraphQL relay cursor (`pagination.graphql_relay`)

**What it does:** Relay-style pagination with `pageInfo.endCursor` and
`pageInfo.hasNextPage`. The cursor variable name carries the cursor into
GraphQL `variables:` on the next iteration; the runner reads from
`cursor.<cursor_var>` and writes the new end cursor back.

**IR shape:**

```yaml
pagination:
  strategy: graphql_relay
  has_next_page_at: body.data.results.pageInfo.hasNextPage
  end_cursor_at: body.data.results.pageInfo.endCursor
  cursor_var: after
```

Pair with `request.body.graphql: { query, variables }` (§3.4).

### 2.9 Async-job polling (202 → poll → fetch)

**What it does:** The API returns 202 with a job/task ID; the runner polls
until completion, then fetches results. Modelled at the progress layer, not
the pagination layer, because the loop runs across iterations rather than
within one.

See §5.5 (`progress.async_job`).

### 2.10 Time-window pagination

**What it does:** Each drain covers a `[start_time, end_time]` window;
after completion the window slides forward. Combines orthogonally with
`pagination.offset` (offset paginates *within* the window). Modelled at the
progress layer, not the pagination layer.

See §5.2 (`progress.time_window`).

### 2.11 Export + blob download

**What it does:** A list step returns blob URLs; a dependent step GETs each
blob in turn. When the export is asynchronous (submit → poll → fetch), the
final fetch role is the blob download.

The list → per-blob shape is expressible via
`requests:` + per-step `fan_out: {over, as, merge: flatten}` —
`fan_out` is deferred. The async-export shape is
supported via `progress.async_job`. MIME-decoding the downloaded blobs
(gzip / CSV / NDJSON chains) is out of scope and is expected to live
in the ingest pipeline.

### 2.12 Worklist / multi-phase

**What it does:** Same-iteration list → per-item detail fan-out, or
cross-iteration worklist draining.

Same-iteration `list → detail` is expressible via
`requests:` with a per-step `fan_out: {over, as, merge: flatten}` —
`fan_out` is deferred. Cross-iteration worklist
draining (seed worklist, drain one per evaluation, LIFO/FIFO queues, retry
budgets) is out of scope. No
escape hatch.

### 2.13 `send_as` implicit auto-injection

**What it does:** A pagination strategy's slot (`query.cursor`, `body.start`,
etc.) is auto-injected into the producer step's outgoing request whenever
the slot is not already declared explicitly. Explicit
`{from_pagination: ...}` wins when both are present.

---

## 3. Request types

### 3.1 GET with query parameters

**What it does:** Standard GET. Query slots accept any `Value`; a `Value`
whose `omit_if_empty:` is true is dropped from the query map when it
resolves empty.

**IR shape:**

```yaml
requests:
  - id: main
    method: GET
    path: /v1/events
    query:
      since: { from_progress: window_start }
      page: { from_pagination: page }
      type: { ref: state.event_type, omit_if_empty: true }
```

### 3.2 GET with no parameters

**What it does:** Simple GET to a fixed or cursor-constructed URL. Express
as a `path:` (relative to a base URL on the doc) or a fully-qualified
`url:` resolved from a `Value`.

### 3.3 POST with JSON body

**What it does:** Body is a JSON object literal. Nested object literals are
accepted via the Object `Value` fallback; values are `Value`s.

**IR shape:**

```yaml
requests:
  - id: main
    method: POST
    path: /api/search
    body:
      json:
        time_range:
          start: { from_progress: window_start }
          end: { from_progress: window_end }
        limit: { literal_int: 100 }
```

### 3.4 POST with GraphQL query

**What it does:** Specialised JSON body of shape
`{"query": "...", "variables": {...}}`. The query is a YAML scalar;
`variables` accepts nested object literals. Pair with
`pagination.graphql_relay` to drive the cursor variable.

**IR shape:**

```yaml
requests:
  - id: main
    method: POST
    path: /graphql
    body:
      graphql:
        query: |
          query ($after: String) {
            assets(after: $after) {
              nodes { id name }
              pageInfo { endCursor hasNextPage }
            }
          }
        variables:
          after: { from_pagination: relay_cursor }
```

### 3.5 POST with form-encoded body

**What it does:** Body is serialised as `application/x-www-form-urlencoded`.
Common for OAuth2 token endpoints (when authored as a `pre_fetch:` step
rather than via `auth.oauth2`) and legacy APIs.

**IR shape:**

```yaml
requests:
  - id: token
    method: POST
    path: /oauth/token
    body:
      form:
        grant_type: { literal_string: "client_credentials" }
        client_id: { ref: state.client_id }
        client_secret: { ref: state.client_secret }
```

### 3.6 POST with raw body

**What it does:** Send an arbitrary opaque body (text, bytes from a
`Value`). Less common than JSON / form / graphql; reserved for APIs whose
content type does not match the structured cases.

```yaml
body:
  raw: { ref: state.canned_request_body }
```

### 3.7 Per-step `if:` gating

**What it does:** Skip a step when its `if:` predicate is false. Combines
with multi-step `requests:` chains to express conditional pre-fetches and
cache short-circuits.

### 3.8 Per-step `on_status:`

**What it does:** When a request returns a status outside `expect_status:`
(default 200), dispatch to one of: `skip`, `fail`, `empty_events`, or
`invalidate_cache`. `invalidate_cache` is OAuth2-aware (see §1.8); the
other verbs are unconditional.

```yaml
requests:
  - id: main
    method: GET
    path: /events
    on_status:
      - { code: 401, do: invalidate_cache }
      - { code: 429, do: empty_events }
```

### 3.9 Sequential request chain (A → B → C)

**What it does:** Multiple steps with `id:` per step. Later steps reference
earlier bodies via `{ref: steps.<id>.body.<path>}` and earlier extracts via
brace interpolation in `path:` / `url:` / body slots.

**IR shape:**

```yaml
requests:
  - id: login
    method: POST
    path: /auth/signin
    body: { json: { user: { ref: state.username }, pass: { ref: state.password } } }
    extract:
      - { name: session, source: header, path: Set-Cookie }
  - id: main
    method: GET
    path: /api/data
    headers:
      Cookie: { from_extract: session }
```

### 3.10 Pre-fetch + main

**What it does:** A single pre-flight step (typically an OAuth2 / session
fetch) whose extracts are visible to the main request. Equivalent in
expressive power to a two-step `requests:` chain, with a slightly tighter
shape for the common case.

**IR shape:**

```yaml
pre_fetch:
  method: POST
  path: /oauth/token
  body: { form: { grant_type: { literal_string: "client_credentials" } } }
  extract:
    - { name: access_token, source: body, path: access_token }

auth:
  type: bearer
  bearer:
    token: { from_pre_fetch: access_token }
```

Wrap with `auth.cache:` (§1.8) to add token caching.

### 3.11 Per-item fan-out

**What it does:** A step iterates over a list `Value` (typically extracted
from the previous step's response); the runner runs the step once per item
with `item.<path>` bound. `merge: flatten` concatenates per-item response
lists.

**Deferred.** `scope.item` is reserved in
the state machine; per-item dispatch is missing.

**IR shape (validator passes, runner does not yet execute):**

```yaml
requests:
  - id: list
    method: GET
    path: /incidents
    extract:
      - { name: ids, source: body, path: items[*].id }
  - id: detail
    method: GET
    path: /incidents/{item.id}
    fan_out:
      over: { from_extract: ids }
      as: item
      merge: flatten
```

### 3.12 Step-level cache (`requests[].cache`)

**What it does:** Wraps a token-style step (a custom JSON login, a
session-key exchange) in a fresh-vs-cached conditional — the non-OAuth2
counterpart of the OAuth2 token cache (§1.8). `store_in` names *both* the
top-level response field captured *and* the state slot it lands in (a
Splunk login uses `store_in: sessionKey`, a Lacework login `store_in:
token`), so the cached value survives across iterations and drains.
`expiry_field` is the body-relative path to the response field carrying
the lifetime; `expiry_buffer` is the "don't cut it too close" margin;
`expiry_format` selects how `expiry_field` is read — `duration` (the
default: a remaining lifetime, integer seconds or a Go duration string,
same as OAuth2 `expires_in`) or one of the absolute-instant formats
(`unix_seconds`, `unix_millis`, `rfc3339`, `rfc3339nano`). The `store_in`
key is auto-registered as a runtime string and survives across iterations.
The step is re-run when `now + expiry_buffer >= cached_expires_at`; the
expiry timestamp lives at `cursor.__step_<store_in>_expires_at` as RFC 3339.

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
```

**Cache invalidation:** A request step that declares `on_status:
{code: 401, do: invalidate_cache}` drops every step-cache slot
(`state.<store_in>` + `cursor.__step_<store_in>_expires_at`) alongside the
OAuth2 cache slots (§1.8), then advances as if the page came back empty.
The next drain misses the cache and re-runs the login step.

---

## 4. Response parsing

### 4.1 JSON, single object at root (`response.wrap_single`)

**What it does:** The response body is a single JSON object; the runner
wraps it in a one-element list to produce one event.

**IR shape:**

```yaml
response:
  decode: json
  events_at: ""
  wrap_single: true
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

**What it does:** The body is an object; events live at a known dotted path
(`data`, `value`, `result`, `items`, `resources`, `hits.hits`,
`data.<queryName>.nodes`, etc.).

**IR shape:**

```yaml
response:
  decode: json
  events_at: body.data.items
```

### 4.4 NDJSON

**What it does:** The body is newline-delimited JSON, one object per line.
The runner decodes each non-empty line.

```yaml
response:
  decode: ndjson
```

Per-line custom decoding and bespoke empty-line filtering are out of
scope.

### 4.5 Placeholder events (`response.placeholder_event`)

**What it does:** When a real-events list is empty AND pagination wants
another iteration, the runner queues a synthetic event (typically an
operator-supplied object literal). Emission is confirmed at the start of
the next iteration (after ctx-cancel and `maxPages` checks have cleared).
Three guarantees: variant-independent across all pagination strategies,
wire-order matches drain order, conservative under early exit.

**IR shape:**

```yaml
response:
  decode: json
  events_at: body.results
  placeholder_event:
    object:
      message: { literal_string: "retry" }
```

The ingest-pipeline rule that filters the synthetic event lives outside the
runner — that is operator-side configuration, not IR.

### 4.6 Custom success expressions (`success_condition`)

**What it does:** Treats certain non-default conditions as success when the
HTTP status alone is misleading — for example, a 200 with `body.fail[]`
populated should fail; a 429 with `ignore_api_errors` should advance with
no events. Modes: `status_only` (default) or `custom_expr` (a predicate
over the response).

### 4.7 ZIP / gzip / CSV / MIME-chain decoding

**What it does:** Decoding compressed or tabular response bodies, optionally
chained (`gzip → CSV`, `gzip → NDJSON`, `zip → JSON`).

**Out of scope.**
`response.decode` is a closed `json | ndjson` enum by design; MIME chaining
is expected to live in the ingest pipeline.

---

## 5. Cursor and progress

### 5.1 Stateless (`progress.stateless`)

**What it does:** No cursor advance; every drain fetches the full dataset.
Public feeds, demo integrations.

```yaml
progress: { strategy: stateless }
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
  strategy: time_window
  window:
    initial_offset: 24h
    format: rfc3339
```

Window bounds land on request slots via `{from_progress: window_start}`
and `{from_progress: window_end}`. Combines orthogonally with
`pagination.offset`.

### 5.3 Latest-event timestamp (`progress.latest_event_timestamp`)

**What it does:** After each drain, advance `cursor.last_timestamp` to the
last item's event-time field. The next drain reads `start_time` from
`cursor.last_timestamp` with a default derived from
`progress.initial.lookback`. Optional per-iteration `lookback` lets the
window overlap.

**IR shape:**

```yaml
progress:
  strategy: latest_event_timestamp
  event_time:
    path: created_at
  initial:
    lookback: 7d
  lookback: 60s            # optional; per-iter overlap
```

### 5.4 Max-of-list cursor (`progress.max_event_field`)

**What it does:** Same shape as `latest_event_timestamp` but advances to
the *maximum* value of the named field across the drain's emitted events.
Shares the events-walk helper with `latest_event_timestamp`.

### 5.5 Async-job dispatcher (`progress.async_job`)

**What it does:** Submit → poll → fetch sequence. The runner dispatches on
`cursor.phase` (default `submit`): one HTTP step per evaluation. The
`progress.async_job:` block assigns step ids to roles, declares per-role
`extract:` maps that flow into the cursor (e.g. submit's `body.export_id`
→ `cursor.export_id`), and declares `poll.complete_when: {path, equals}`
for the loop-exit signal. Inner re-poll (stay-in-poll while
`<status_path> != <equals>`) is handled by the runner on slow jobs.
`on_complete.cursor_update.kind` chooses how the cursor advances after the
fetch: `stateless`, `use_now`, or `latest_event_timestamp`.

**IR shape:**

```yaml
requests:
  - id: submit
    method: POST
    path: /exports
    extract:
      - { name: export_id, source: body, path: export_id }
  - id: poll
    method: GET
    path: /exports/{cursor.export_id}/status
  - id: fetch
    method: GET
    path: /exports/{cursor.export_id}/results

progress:
  strategy: async_job
  async_job:
    submit:
      id: submit
      extract: [{ to: cursor.export_id, from_extract: export_id }]
    poll:
      id: poll
      complete_when:
        path: body.status
        equals: { literal_string: "complete" }
    fetch:
      id: fetch
    on_complete:
      cursor_update: { kind: stateless }
```

### 5.6 Use-now (`progress.use_now`)

**What it does:** Advance writes `cursor.last_timestamp = now() - lookback`
every drain — no events walk. Suits APIs where the drain itself defines
the cutoff (the next iteration is "everything since the last invocation").

### 5.7 Multi-field cursor

**What it does:** Multiple cursor fields advance together (timestamp +
page + worklist tail; timestamp + offset; per-entity timestamps).

Multiple `progress.*` strategies do not yet compose
today. Authors can stash auxiliary cursor fields via
`state.passthrough:` (between-iteration values) or via `requests:` extracts
with `target: cursor` (in-iteration values). Complex multi-field cursors
with conditional advance logic remain out of scope.

### 5.8 Conditional fetch / ETag

**What it does:** Store an `ETag` in the cursor, send `If-None-Match` on
the next iteration, skip processing and preserve the cursor on 304.

**Deferred.** The two-step shape is expressible via `requests:`
(HEAD then conditional GET), but the runner does not yet have a
structured "skip the second step on 304" branch — that needs a per-step
predicate that can dispatch on response status (`if:` today is evaluated
before the step runs).

### 5.9 State machine across iterations

**What it does:** Three-or-more-phase orchestration with LIFO / FIFO
worklists, retry budgets, conditional branching, or cross-iteration
queue draining.

**Out of scope.**
The two-phase shapes are covered: submit → poll → fetch via
`progress.async_job` (§5.5); list → detail via `requests:` +
`fan_out:` (§3.11, deferred). Beyond that, no escape hatch.

---

## 6. Cross-cutting

### 6.1 Token caching across iterations

**What it does:** Cache an OAuth2 / session token in state with an expiry
field; the runner checks expiry before re-fetching, avoiding a token
round-trip on every iteration.

See `auth.oauth2.<grant>.cache` (§1.8).

For non-OAuth2 cached-login endpoints (custom session tokens with their own
expiry), `requests[].cache` (§3.12) wraps the login step in the same
fresh-vs-cached conditional — the step is skipped while the cached token is
still inside its expiry buffer.

### 6.2 State passthrough (`state.passthrough`)

**What it does:** Names state keys that are not cursor fields but must
survive across iterations (cached tokens, session cookies, derived
config). `auth.cache:` auto-adds its `store_in` key so the cached token
struct survives across iterations.

```yaml
state:
  passthrough: [cookies, retries, manager_url]
```

### 6.3 Dual-mode behaviour (`auth.multi_mode` and beyond)

**What it does:** Runtime branching on a state flag. Auth-level branching
is covered by `auth.multi_mode` (§1.9). URL/endpoint or request-shape
branching on a state flag (e.g. `gov_cloud` selecting a different request
shape) is *not* yet modelled as a first-class form.

Auth-level branching supported; request-shape branching
not modelled, no escape hatch.

### 6.4 Secret redaction in logs / traces

**What it does:** Every log line, error message, and `Tracer` field that
mentions an `schema.Value` routes through `redactValue(doc, v)`. URLs go
through `safeURL` (scheme + host + path; query and userinfo stripped).
`Authorization` / `Cookie` / `Proxy-Authorization` headers are always
redacted by name; the runtime credential surfaces named by
`auth.custom.header` and `auth.api_key.header` are redacted by name; any
`req.Headers` / `req.Query` entry whose IR `Value` is `schema.IsSecret` is
redacted by content. Request and response bodies are metadata-only (byte
length + leading-byte classification), never raw bytes.

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
