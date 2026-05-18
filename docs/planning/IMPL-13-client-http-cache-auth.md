# IMPL-13 — `client/http.go` + `auth.go` + `cache.go` + `fanout.go`

## Scope

Rewrite the HTTP execution layer, the auth dispatcher, the unified
`Cache` runtime (merging `oauth2.go` + `requestcache.go` into one
`cache.go`), and the fan-out per-item dispatcher against the
post-redesign IR. Every request carries an absolute `url:` Value (no
`Defaults.BaseURL`, no `req.Path`); the unified `schema.Cache` block
backs both `auth.oauth2.<grant>.cache` and `requests[].cache` with one
store helper writing into `scope.cache` (process memory only, never
persisted); `auth.multi_mode.default` is a bare `Auth` value; the
runner's `dropReachableCaches` is the single cache-invalidation walk
shared by `runRequest` and `runFanOut`.

## Old → new map

| Legacy call site / mechanism                          | New mechanism                                                                                                                                                                                                                                                                                  |
|-------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `s.doc.Defaults.BaseURL` + `req.Path` combination     | `s.buildURL(req)` evaluates `req.URL` (an absolute URL Value, often a `"${state.base_url}/path"` interpolation) and parses the result. No prefix join, no defaults block.                                                                                                                       |
| `req.URL *schema.Value` (optional pointer)            | `req.URL schema.Value` (always present); `s.evalValue(req.URL)` returns the runtime string.                                                                                                                                                                                                    |
| `req.Cache` pre-check fired by the runner             | `executeRequest` consults `req.Cache` itself: a fresh slot returns a synthetic `stepResult{statusCode: 200, body: <cached>}` without an HTTP round trip; a stale slot fires the request and writes the slot on success.                                                                         |
| `schema.TokenCache{StoreIn, ExpiryField, ExpiryBuffer, ExpiryFormat, ...}` | `schema.Cache{To Path, ExpiresAt Value, Buffer string}`; both auth and request call sites share the same struct.                                                                                                                                                                                |
| `schema.RequestCache{StoreIn, ExpiryField, ExpiryBuffer, ExpiryFormat}` | Same `schema.Cache`. One struct, two sites.                                                                                                                                                                                                                                                    |
| `s.cachedOAuth2Token(cache)` / `s.cachedStepValue(cache)` | `s.cacheGet(c)` — one helper, one lookup. Slot key is `cache.To.Parts[1]`; freshness is `s.now().Add(c.Buffer).Before(<stored expires_at>)`.                                                                                                                                                   |
| `s.storeOAuth2Token(cache, tok, lifetime)` / `s.storeStepValue(cache, body)` | `s.cacheStore(c, value)` — evaluates `c.ExpiresAt` against the currently bound `scope.body` (the just-decoded body of the cached step) and writes value + resolved instant to `scope.cache`.                                                                                                  |
| `state.<store_in>` (the access token / login token slot) | `cache.<name>` — slot name is the suffix of `c.To`. OAuth2 stores the access-token STRING; `requests[].cache` stores the WHOLE decoded body so `{ref: cache.<name>.<path>}` walks into it. Slots live in `scope.cache`, NEVER `scope.state`.                                                  |
| `state.<store_in>_expires_at` (the paired expiry slot) | Internal scope.cache key `__exp_<slot>` (the `"__exp_"` prefix is reserved — the validator rejects any cache.<name> ref where `<name>` isn't in some declared Cache.To). Process memory, never persisted.                                                                                       |
| `s.invalidateAuthCaches(auth)` + `s.invalidateStepCaches(doc)` | `dropReachableCaches(s, doc)` — declared on Slice 12's `runner.go`. Walks `doc.Auth` (recursing into `multi_mode`) and `doc.Requests` for every `Cache` block, deleting `scope.cache[<slot>]` for each. `runRequest` (runner.go) and `runFanOut` (fanout.go) both call this same helper.        |
| `auth.MultiMode.Default.Auth` (wrapped `{auth: ...}`) | `auth.MultiMode.Default` — bare `Auth`. The dispatcher in `applyMultiMode` reads the variant directly off the value.                                                                                                                                                                            |
| `oauth2ExpiryKey(storeIn)` / `stepCacheExpiryKey(storeIn)` helpers | `cacheExpiryKey(slot)` — one function, "__exp_<slot>" return.                                                                                                                                                                                                                                  |
| `oauth2CacheFor(o)` helper                            | Folded into `oauth2Grant.cache()`. The pre-`schema.Cache` shape had two parallel cache pointers across the two grant variants; the new `oauth2Grant` tagged wrapper carries one pointer per active variant.                                                                                     |
| `extractOAuth2Lifetime(body, *schema.TokenCache)` + `stepCacheExpiry(now, body, *schema.RequestCache)` + `coerceLifetime(raw)` | `coerceCacheExpiry(v, now)` — one helper. The input is the Value-resolved `expires_at`; the helper accepts `time.Time`, `time.Duration`, integer seconds (`int / int64 / float64`), or a string (Go duration, RFC 3339, plain integer). Authors who need explicit timestamp parsing use `{format: rfc3339, value: ...}` upstream. |
| `parseStoredTime` (re-parse the persisted RFC 3339 string back into a `time.Time`) | Removed. The expiry instant is stored as a `time.Time` directly in `scope.cache[__exp_<slot>]`; no serialise / re-parse round trip.                                                                                                                                                            |
| `s.now().Add(buffer).Before(expAt)` test (old)        | Same test, same direction. Centralised in `cacheGet`.                                                                                                                                                                                                                                          |
| Fan-out per-item `s.invalidateAuthCaches` + `s.invalidateStepCaches` | `dropReachableCaches(s, r.Doc)`. One call. Both call sites (runner + fanout) use the same helper now.                                                                                                                                                                                           |
| `runFanOut(..., iter, phase)` signature                | Same signature; `phase string` is now an unused trailing parameter (named `_`). The runner-side caller still passes `""`. Dropping the param outright would require touching `runner.go` (frozen); the no-op param is the conservative move.                                                  |

## Removed content

Files:

- `client/oauth2.go` — deleted.
- `client/requestcache.go` — deleted.

Types:

- `schema.TokenCache` / `schema.RequestCache` — gone in Slice 4; the
  client side stops referencing them here.

Functions / methods on the client package side:

- `(*scope).cachedOAuth2Token(*schema.TokenCache)`.
- `(*scope).storeOAuth2Token(*schema.TokenCache, string, time.Duration)`.
- `(*scope).cachedStepValue(*schema.RequestCache)`.
- `(*scope).storeStepValue(*schema.RequestCache, any)`.
- `(*scope).clearOAuth2Cache(*schema.TokenCache)`.
- `(*scope).invalidateAuthCaches(schema.Auth)`.
- `(*scope).invalidateStepCaches(*schema.Doc)`.
- `extractOAuth2Lifetime(map[string]any, *schema.TokenCache)`.
- `stepCacheExpiry(time.Time, map[string]any, *schema.RequestCache)`.
- `oauth2CacheFor(*schema.OAuth2Auth)`.
- `oauth2ExpiryKey(string)`.
- `stepCacheExpiryKey(string)`.
- `parseStoredTime(string)`.
- `coerceLifetime(any)`.

Call sites:

- `s.buildURL(req)` no longer reads `s.doc.Defaults.BaseURL` or
  `req.Path` — the only path through is `s.evalValue(req.URL)`.
- `s.executeRequest` no longer accepts `req.Path` as an alternative to
  `req.URL`.
- `applyMultiMode` no longer indexes `m.Default.Auth`; it reads
  `m.Default` directly.

Comments / docstrings:

- Every reference to `state.<store_in>`, `state.<store_in>_expires_at`,
  `cursor.__oauth2_<storeIn>_expires_at`, `Defaults.BaseURL`,
  `req.Path`, `TokenCache`, `RequestCache`, `expiry_field`,
  `expiry_buffer`, `expiry_format`.

## File-by-file walk

### `client/cache.go` (new — absorbs `oauth2.go` + `requestcache.go`)

Owns the unified `Cache` runtime plus the OAuth2 token-fetch helpers
(`applyOAuth2`, `oauth2Grant`, `fetchOAuth2Token`,
`buildOAuth2TokenRequest`).

Exports / package-private symbols:

| Symbol                              | Role                                                                                                                                                                                                                                                                                                  |
|-------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `cacheExpiryKey(slot)`              | "__exp_<slot>" — reserved scope.cache key for a slot's expiry instant. The "__" prefix never appears in a declared cache.<name> ref (validator rejects), so the bookkeeping slot cannot collide with author intent.                                                                                  |
| `(*scope).cacheGet(c)`              | Returns (value, fresh). Miss when the slot is empty, the paired expiry is missing or malformed, or `now() + c.Buffer >= expires_at`.                                                                                                                                                                  |
| `(*scope).cacheStore(c, value)`     | Evaluates `c.ExpiresAt` against the currently bound `scope.body`; writes value + the resolved instant to `scope.cache[slot]` + `scope.cache[__exp_<slot>]`. Caller is responsible for binding `scope.body` to the cached step's body before calling and restoring it after.                          |
| `coerceCacheExpiry(v, now)`         | Promotes a Value-resolved `expires_at` to a `time.Time`. Accepts `time.Time`, `time.Duration`, integer seconds (canonical OAuth2 expires_in), Go duration strings, RFC 3339 strings, or plain integer strings. Falls through to an error otherwise; authors who need explicit shapes use `{format:}`. |
| `(*scope).applyOAuth2(...)`         | Dispatches between `oauth2.client_credentials` and `oauth2.password_grant`; reads-or-fetches a token through `oauth2Token`; sets "Authorization: Bearer <token>" on req.                                                                                                                              |
| `oauth2Grant{cc, pg}` + `cache()`   | Tagged wrapper over the two grant variants. `cache()` returns the grant's `*schema.Cache` (or nil).                                                                                                                                                                                                   |
| `(*scope).oauth2Token(ctx, client, grant)` | The cache-orchestration core. Cache hit → return the stored token. Cache miss → `fetchOAuth2Token` → bind scope.body to the token endpoint body → `cacheStore` → restore scope.body → return token.                                                                                                  |
| `(*scope).fetchOAuth2Token(...)`    | POSTs to `grant.TokenURL` with form-urlencoded credentials. Returns `(access_token, response_body, err)`; the body is passed back so the caller can bind it before evaluating `cache.expires_at`.                                                                                                     |
| `(*scope).buildOAuth2TokenRequest(grant)` | Composes the wire details: resolves `token_url` / `client_id` / `client_secret` / `username` / `password`, builds the form body, returns the Basic-auth pair separately so `fetchOAuth2Token` can set the Authorization header explicitly.                                                            |

### `client/auth.go`

Same six-variant dispatcher (`none|bearer|basic|api_key|custom|oauth2|multi_mode`).
The only structural change is `applyMultiMode` reading
`m.Default` (bare `Auth`) instead of `m.Default.Auth` (the
pre-redesign wrapped shape).

### `client/http.go`

- `executeRequest` opens with a `req.Cache != nil` pre-check.
  Cache HIT: return `&stepResult{statusCode: 200, body: <cached>}`
  without an HTTP round trip. Cache MISS: fire the request normally.
- `s.buildURL(req)` reduces to `s.evalValue(req.URL)` + `url.Parse`. No
  prefix concatenation; no `req.Path` branch. The validator already
  enforces that every request carries a `url:`.
- After a successful decode (and only on the cache-miss path), if
  `req.Cache != nil`, bind `scope.body` to `res.body`, call
  `s.cacheStore(req.Cache, res.body)`, and restore `scope.body`. The
  bind / restore keeps the cache write hermetic — a downstream
  predicate or extract still sees whatever the runner explicitly
  binds, not whatever the cache layer transiently bound.
- The cache stores the WHOLE decoded body for `requests[].cache`;
  authors walk in with `{ref: cache.<name>.<path>}` (a session login's
  `body.session_token` becomes `{ref: cache.session.session_token}`).
  This is symmetric with the OAuth2 case in storage (both use
  `scope.cache`) and asymmetric in content (OAuth2 stores the parsed
  access token string; request caches store the body so authors can
  pick out their own fields with the existing Path layer).
- The trace path is untouched in semantics: cache HITS skip the trace
  (no wire exchange happened); cache MISSES emit a normal trace
  record. The runner-side `r.Tracer.OnExchange` call site already
  branches on `r.Tracer != nil`, so a HIT just means `httpTrace` is
  nil-ed and the OnExchange call receives an empty record — but Slice
  14 is the right slice to teach the trace surface to skip empty
  records if that becomes a problem.

### `client/fanout.go`

- Drops the per-item legacy invalidate path (`s.invalidateAuthCaches`
  + `s.invalidateStepCaches`) in favour of the runner's
  `dropReachableCaches(s, r.Doc)` walk.
- Keeps the trailing `phase string` parameter (named `_`) so the
  frozen `runner.go` call site
  `r.runFanOut(ctx, client, logger, s, req, errMode, iter, "")`
  continues to compile. The parameter is unused. Slice 13's plan
  permits dropping the parameter but the runner.go-frozen constraint
  takes precedence; the no-op param is the conservative resolution.

## Build state

- `go build ./schema/...` — green.
- `go build ./client/...` — green (zero errors).
- `go build ./cmd/...` — green.
- `go build ./...` — green.
- `go vet ./client/...` reports stale `*_test.go` failures
  (Slice 17's territory): `auth_test.go`, `oauth2_test.go` →
  references to deleted `schema.TokenCache`, `schema.RequestCache`,
  `schema.Defaults`, `schema.State`, `Stateless`; `requestcache_test.go`
  → same `schema.RequestCache`. Production code carries zero errors.
- Tests stay red until Slice 17.

## Walkthrough verification

End-to-end correctness is not checkable until Slices 16–17 land. The
trickier cases were walked against the code as follows.

### Case 1 — OAuth2 client_credentials, cache HIT then MISS

```yaml
auth:
  oauth2:
    client_credentials:
      token_url: ${state.token_url}
      client_id: ${state.client_id}
      client_secret: ${state.client_secret}
      cache:
        to: cache.access_token
        expires_at: {ref: response.body.expires_in, default: "1h"}
        buffer: 60s

requests:
  - method: GET
    url: ${state.base_url}/events
    produces_events: true
```

Drain iteration 1 (cold start):

1. `executeRequest` is called for the `events` request. `req.Cache` is
   nil (no cache on the request) → skip the cache pre-check.
2. `applyAuth` dispatches to `applyOAuth2` (cache.go). `applyOAuth2`
   builds an `oauth2Grant{cc: <cc>}` and calls `oauth2Token`.
3. `oauth2Token` calls `cacheGet(grant.cache())`. `scope.cache["access_token"]`
   is unset → MISS.
4. `fetchOAuth2Token` POSTs to `state.token_url`, parses
   `body.access_token` → `"abc123"`. Returns `("abc123", body, nil)`
   where `body = {access_token: "abc123", expires_in: 3600}`.
5. Bind `scope.body = body`. Call `cacheStore(grant.cache(),
   "abc123")`. `cacheStore` evaluates
   `c.ExpiresAt = {ref: response.body.expires_in, default: "1h"}`:
   - `response.body.expires_in` → 3600 (float64 / int from JSON
     decode).
   - `coerceCacheExpiry(3600, now)` → `now + 3600s`.
   - Writes `scope.cache["access_token"] = "abc123"` +
     `scope.cache["__exp_access_token"] = now + 1h`.
6. Restore `scope.body = prev` (nil).
7. `applyOAuth2` sets `Authorization: Bearer abc123`.
8. Request fires. Response decodes. `events` are emitted.

Drain iteration 2 (within the same Drain — next page):

1. `executeRequest` is called again for `events`.
2. `applyAuth` → `applyOAuth2` → `oauth2Token` → `cacheGet(...)`.
   `scope.cache["access_token"]` = "abc123",
   `scope.cache["__exp_access_token"]` = now₁ + 1h. Buffer is 60s.
   `now₂.Add(60s).Before(now₁ + 1h)` — for any `now₂` within ~59m of
   `now₁`, this is true. HIT.
3. `oauth2Token` returns "abc123" without re-fetching.
4. Authorization header set. Request fires.

Drain iteration 3 (`now₃ = now₁ + 61m`, slot is stale):

1. `cacheGet` → `now₃.Add(60s).Before(now₁ + 1h)` → false. MISS.
2. `fetchOAuth2Token` runs again; new token + new expiry are stored.

### Case 2 — Request-level cache (custom JSON login) preceding an event fetch

```yaml
requests:
  - id: login
    method: POST
    url: ${state.base_url}/login
    body:
      json:
        user: ${state.user}
        pass: ${state.pass}
    cache:
      to: cache.session
      expires_at: {ref: response.body.ttl, default: "30m"}
      buffer: 60s

  - id: events
    method: GET
    url: ${state.base_url}/events
    headers:
      Authorization: ${cache.session.token}
    produces_events: true
```

Drain iteration 1:

1. `executeRequest(login)` — `req.Cache != nil`. `cacheGet(cache.session)`
   — slot empty → MISS.
2. Request fires; response decodes to `{token: "sess-1", ttl: 1800}`.
3. After decode, `req.Cache != nil`. Bind `scope.body = res.body`; call
   `cacheStore(req.Cache, res.body)`.
   - `expires_at` resolves: `{ref: response.body.ttl, default: "30m"}`
     → 1800 (int). `coerceCacheExpiry(1800, now)` → `now + 30m`.
   - Writes `scope.cache["session"] = {token: "sess-1", ttl: 1800}`
     + `scope.cache["__exp_session"] = now + 30m`.
   - Restore `scope.body = nil`.
4. `executeRequest(events)` — uses `{ref: cache.session.token}` in
   Authorization header. The `scope.cache` lookup returns the body
   map; `walk("token")` returns "sess-1". Authorization set.
5. Events fetch normally.

Drain iteration 2:

1. `executeRequest(login)` — `cacheGet` returns the cached body. HIT.
   Returns `&stepResult{statusCode: 200, body: <cached>}` synthesised.
2. `runIteration` runs `runExtracts(res, login.Extract)` on the
   synthetic result — idempotent (the body is the same as last iter's
   captured body).
3. `s.steps["login"] = res.body` — same content. `s.stepHeaders["login"]`
   left empty (the synthetic result has nil headers, which is fine: no
   author of a cached login should reference `steps.login.header.*`
   — the response wasn't actually re-fetched).
4. `executeRequest(events)` — proceeds as before, cache still fresh.

### Case 3 — `on_status: 401: invalidate_cache` with an OAuth2 cache

```yaml
auth:
  oauth2:
    client_credentials:
      ...
      cache:
        to: cache.access_token
        ...

requests:
  - id: list
    method: GET
    url: ${state.base_url}/events
    on_status: {401: invalidate_cache}
    produces_events: true
```

Drain iteration N (server has rotated the token in the meantime):

1. `executeRequest(list)` runs with the cached `cache.access_token`
   (Slice 13's HTTP layer reads `scope.cache["access_token"]` via
   `applyOAuth2` → `oauth2Token` → `cacheGet`, binds it into the
   `Authorization: Bearer` header).
2. Response: status 401. `executeRequest` returns
   `(res, *unexpectedStatusError{status: 401})`.
3. `runRequest` (runner.go) dispatches `on_status[401] = "invalidate_cache"`.
   Calls `dropReachableCaches(s, doc)`:
   - Walks `doc.Auth.OAuth2.ClientCredentials.Cache.To = cache.access_token`.
     `cacheSlotName` returns `("access_token", true)`.
     `scope.cache["access_token"]` exists → delete.
     `cleared = ["access_token"]`.
   - Walks `doc.Requests[*].Cache` — none in this example.
4. Returns `stepInvalidate`.
5. `runIteration` returns `iterInvalidate`. The drain loop `continue`s.
6. Next iteration's `executeRequest` runs `applyOAuth2` →
   `oauth2Token` → `cacheGet`. `scope.cache["access_token"]` is gone
   (the `__exp_access_token` slot still lingers but is overwritten on
   the next store). MISS → `fetchOAuth2Token` fires; fresh token.
7. `cacheStore` writes both slots; `Authorization` header set; request
   retried.

### Case 4 — `multi_mode` with `none:` default

```yaml
auth:
  multi_mode:
    branches:
      - when: {present: state.api_token}
        auth: {bearer: {token: {ref: state.api_token}}}
      - when: {present: state.basic_pair}
        auth: {basic: {username: ..., password: ...}}
    default: {none: {}}
```

Iteration with `state.api_token` set, `state.basic_pair` unset:

1. `applyMultiMode` walks `m.Branches[0]`. Predicate `{present:
   state.api_token}` → true. Recurses into
   `applyAuth(ctx, client, req, b.Auth)` where `b.Auth.Bearer` is set.
2. `applyAuth` dispatches to the `Bearer` arm; sets
   `Authorization: Bearer <token>`. Returns nil.

Iteration with both fields unset:

1. `applyMultiMode` walks all branches; all predicates false.
2. `applyAuth(ctx, client, req, m.Default)` — bare `Auth{None: &struct{}{}}`.
3. The `None` arm returns nil; no Authorization header is set.

The old shape required `m.Default.Auth.None` (a wrapped form); the
new shape is bare. The dispatcher reads `m.Default` directly.

### Case 5 — fan_out with `merge: flatten`, one item returns a non-list body

```yaml
requests:
  - method: GET
    url: ${state.base_url}/issues/${tenant}
    fan_out:
      over: {ref: state.tenants}
      as: tenant
      merge: flatten
    produces_events: true
```

Iteration:

1. `state.tenants = ["a", "b", "c"]`.
2. `runFanOut` evaluates over → list of 3 items.
3. Iterates: per-item `executeRequest`. The per-tenant URL resolves
   via `{ref: tenant}` (the `fan_out.as` binding). Three responses
   come back; items "a" and "c" return `[{event}, {event}]`; item "b"
   returns `{error: "..."}` (a map, not a list).
4. After the loop, `mergeFanOutBodies("flatten", bodies)` iterates
   `bodies`. Item 1 is `[]any` → append. Item 2 is `map[string]any` →
   error: `merge: flatten requires every per-item response body to be
   a list; item 1 returned map[string]interface {} (use merge: wrap
   for non-list bodies)`.
5. `runFanOut` returns the error. `runIteration` surfaces it as the
   drain's return value (after `redactURLError`); no events emit on
   that iteration.

This is "fatal at merge time" — the operator sees a clear diagnostic,
not a silent loss of events.

### Case 6 — `dropReachableCaches` walks multi_mode

```yaml
auth:
  multi_mode:
    branches:
      - when: ...
        auth:
          oauth2:
            client_credentials:
              cache: {to: cache.branch_token, ...}
              ...
    default:
      oauth2:
        password_grant:
          cache: {to: cache.default_token, ...}
          ...
```

A 401 on a request with `on_status: {401: invalidate_cache}` calls
`dropReachableCaches(s, doc)`. Inside `runner.go`:

1. `dropAuthCaches(s, doc.Auth)` enters the `MultiMode` arm.
2. For each branch: recursive `dropAuthCaches(s, b.Auth)`. The branch's
   `OAuth2.ClientCredentials.Cache.To = cache.branch_token` →
   `scope.cache["branch_token"]` deleted (if present).
3. `dropAuthCaches(s, m.Default)` — `m.Default` is a bare `Auth` now.
   Same walk: `OAuth2.PasswordGrant.Cache.To = cache.default_token` →
   `scope.cache["default_token"]` deleted.

Both branches and the default fire even though only one was actively
serving requests this iteration. The validator forbids nested
`multi_mode` so recursion bottoms out one hop deep.

## Notes for downstream slices

### Slice 14 — `client/{sink,filestore,redact,trace,doc}.go`

- `Exchange.Phase` is no longer populated by the runner or fanout
  (every `buildExchange` call passes `""`). Slice 14 can drop the
  field from `Exchange` and from `httpTrace`; alternatively, leave it
  as `omitempty` with no contributors — both shapes produce identical
  wire output for the JSONLTracer.
- The cache HIT path in `executeRequest` returns without populating
  the `httpTrace` scratchpad. The runner-side call still fires
  `r.Tracer.OnExchange(buildExchange(...))` when `r.Tracer != nil`,
  passing an empty `httpTrace`. Slice 14 may want to teach
  `buildExchange` to detect "no wire activity" (e.g. `t.method == ""`)
  and skip the OnExchange call rather than emitting an empty record.
  This is a trace-surface decision, not a cache-runtime one.
- The fan-out per-item dispatcher routes through the same
  `r.Tracer.OnExchange` path — same caveat applies to per-item cache
  HITs (fan_out and cache are mutually exclusive at the spec level,
  so there are no such HITs today; the redundancy is defensive).
- `redact.go` already covers the post-auth header set;
  `applyOAuth2`'s Authorization write is captured by the existing
  always-redact allowlist (`authorization`). No new redaction surface
  is introduced.
- `doc.go` still references the deleted "async_job phase machine"
  vocabulary; Slice 14 owns the package-preamble rewrite.

### Slice 15 — `cmd/skopos/*`

- The CLI's continuous-mode (`--interval`) loop calls `Drain` once
  per interval tick. `scope.cache` is currently rebuilt each Drain
  (newScope makes a fresh map). The pull-loop's cache lifetime is
  therefore "one Drain" in practice — auth tokens are re-fetched on
  the first request of every Drain unless the CLI is teaches the
  Runner to reuse the scope across drains. The lifetime spec in
  `docs/runtime.md` §8 says "the lifetime of the Runner process";
  closing that gap is Slice 15's call (rebuild scope vs reuse,
  whichever the CLI decides). Slice 13 implements the per-Drain
  contract that Slice 8 set up; cross-drain reuse is not in scope
  here.
- The CLI does not need to know about cache slot names. Slot reads /
  writes / invalidations flow through the `Cache` struct on each
  declared site.

### Slice 17 — Tests

- `client/oauth2_test.go` (now `auth_test.go` after the file deletion
  here) is fully red — references to `schema.TokenCache`, `state.<store_in>`,
  `state.<store_in>_expires_at`, `Defaults`, `Stateless`,
  `*schema.Value` (URL) all stale. Slice 17 rewrites against the new
  contract: golden fixtures driven by `internal/testserver/` fakes
  exercising:
  - OAuth2 client_credentials with cache: cold start → hit → buffer
    cross → re-fetch.
  - OAuth2 password_grant variant — same lifecycle.
  - `requests[].cache` (custom JSON login): cold start → hit →
    buffer cross → re-fetch.
  - `on_status: 401: invalidate_cache` against an OAuth2 cache.
  - `on_status: 401: invalidate_cache` against a request-level
    cache.
  - `multi_mode` auth dispatch (each branch + the bare default).
  - Fan-out merge: flatten with non-list per-item body (the error
    case in Case 5 above).
  - URL-with-interpolation requests (no more Defaults.BaseURL).
  - `cache.<name>` ref walk for the request-cache body case.
- `requestcache_test.go` should fold into the new
  `client/cache_test.go` (or wherever Slice 17 lands the cache tests),
  not survive as a parallel file.
