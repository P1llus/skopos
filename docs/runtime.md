# Runtime

The contract `client` provides to callers — drain semantics, scope
lifetimes, state persistence, error policy, the guardrail cap, and the
concurrency model — plus the capability table that lists every IR
variant the runner supports today.

For the YAML field reference see [`schema.md`](schema.md); for the
embedding API see [`usage.md`](usage.md); for the catalogue of vendor
patterns mapped to schema fragments see [`api-methods.md`](api-methods.md).

---

## 1. Architectural position

```
                                  ┌──────────────────────┐
            templates/*.yml ──▶  │ schema.Parse+Validate │
                                  └──────────┬───────────┘
                                             │  *schema.Doc
                                             ▼
                                  ┌──────────────────────┐
            state.json ◀──────────│       client         │──────▶ events.jsonl
                                  │                      │
                                  └──────────────────────┘
```

`schema/` owns the schema and validation; it does not know `client`
exists. `client` consumes `*schema.Doc` and does not own scheduling —
the caller (`cmd/skopos run` or an embedder) decides one-shot vs.
interval.

---

## 2. Drain loop

A `Runner.Drain(ctx)` call is one full pull session — paginate until
`want_more = false`, until an `async_job` phase machine hits a wait
state, or until `MaxPages` fires. The caller decides what "next" means
(sleep + redrain, exit, etc.).

The drain sequence:

1. `Store.Load()` seeds scope from the persisted snapshot and merges IR
   `state.fields` defaults.
2. `progress.seed` runs **once per drain**. It pins
   `{from_progress: ...}` signals (e.g. `cursor.last_timestamp` →
   `since=<...>`) so every page in this drain shares the same
   window-start.
3. Page loop:
   1. `pagination.seed` populates `{from_pagination: ...}` for THIS
      page.
   2. `scope.extract` and `scope.steps` reset (per-iteration).
   3. Requests run in declared order. `extract[]` writes hit `extract`
      (default) or `cursor`. Step ids cache decoded bodies in
      `scope.steps`.
   4. The producer step's body is located via `response.events_at`;
      events emit to `Sink`.
   5. `pagination.advance` updates the cursor and returns `want_more`.
4. After the loop, `progress.advance` runs **once** over accumulated
   events and writes `cursor.last_timestamp` from the drain's
   high-water mark.
5. `Store.Save` persists the snapshot via a **deferred** call — even
   on error — so a partial drain does not lose progress.

---

## 3. Scope lifetimes

`client` exposes four distinct lifetimes for scope namespaces:

| Field            | Lifetime              | Notes                                                              |
| ---------------- | --------------------- | ------------------------------------------------------------------ |
| `state`          | per-drain (persisted) | Seeded from snapshot + IR defaults. Runtime writes persist.        |
| `cursor`         | per-drain (persisted) | Mutated by pagination/progress drivers + `extract.target=cursor`.  |
| `extract`        | per-iteration         | Reset at the top of every iteration.                               |
| `steps`          | per-iteration         | Step bodies do not survive into the next iteration.                |
| `item`           | per-fan-out-iteration | Reserved; unused until `fan_out` lands.                            |
| `body`           | per-`complete_when`   | Active only during `complete_when` predicate evaluation.           |
| `fromPagination` | per-iteration         | Re-seeded each page.                                               |
| `fromProgress`   | per-drain             | Seeded once; window-start is stable across pages.                  |

A value that must survive into the next iteration belongs in `state`
(runtime mutability) or `cursor`. Steps and extracts are explicitly
fresh each page; relying on stale values is a bug.

---

## 4. Persistence contract

- `Snapshot.State` holds only `mutability: runtime` state fields.
  Config-mutability fields come from `schema.State.Fields[].Default` or
  the operator config and are NOT round-tripped through `Save`.
- `Snapshot.Cursor` is persisted whole. The cursor schema is inferred
  from the document's active strategies; unset keys read as `nil` at
  evaluation time and the `Ref.Default` branch covers first-drain
  absence.
- `Store.Save` runs as a deferred call in `Drain` — partial drains
  persist whatever was reached. This is deliberate: an `async_job` that
  hit `phase=poll` halfway through should not lose its job id.
- Two implementations ship today: `MemoryStore` (process-local, useful
  for tests) and `FileStore` (atomic write via temp + rename). External
  callers can plug in BoltDB / etcd / SQLite by implementing `Store`;
  see [`stores.md`](stores.md).

---

## 5. Error policy

Dispatch order for a failed HTTP exchange:

1. **`requests[].on_status[<code>]`** — per-step override for that
   exact status code. Verbs: `skip`, `fail`, `empty_events`,
   `invalidate_cache` (drops every reachable OAuth2 cache slot and
   advances as if the page came back empty).
2. **`document.error.mode`** — fallback when `on_status` does not name
   the status:
   - `standard` (default): iteration ends, cursor does NOT advance,
     next drain re-runs the same window.
   - `warn`: log, advance cursor as if empty page, continue.
   - `fail`: drain returns non-nil; deferred `Save` still persists
     whatever cursor state was reached.

Non-status errors (DNS, connection refused, decode failure) skip
`on_status` lookup and dispatch directly through `error.mode`. The
cursor never advances on a transport-level error in `standard` or
`fail` mode.

---

## 6. MaxPages safety cap

A buggy server returning the same `cursor_token` forever would loop
indefinitely. `Runner.MaxPages` (default `10_000`) bounds one drain.
Hitting the cap returns an "iteration cap exceeded" error and persists
state reached so far. A negative value disables the cap (tests only).

---

## 7. Concurrency model

- **One `Runner` is one logical pull source.** `Drain` mutates internal
  scope; calling `Drain` concurrently on the same `Runner` races.
- **Multiple independent `Runner`s are independent.** The package holds
  no global state. Fan-out across N templates = N `Runner`s, each with
  its own `Doc` / `Store` / `Sink`, each on its own goroutine.
- **Shared `Sink` / `Store` between Runners MUST be safe for concurrent
  use.** The bundled `JSONLSink` is mutex-guarded. `MemoryStore` is
  single-threaded by design. `FileStore` is **single-writer-per-path
  by contract** — an in-process mutex serialises `Save` / `Load` on a
  single `*FileStore`, but no cross-process lock is acquired. Two
  skopos processes writing the same path will race on the temp-file →
  rename sequence and the second writer silently clobbers the first.
  Operators running multiple processes must give each its own path or
  plug in a `Store` backed by something with multi-writer semantics
  (BoltDB, SQLite, etc.).

---

## 8. Supported variants

### 8.1 Authentication

| Variant                              | Notes |
| ------------------------------------ | ----- |
| `auth.none`                          | `auth.go`. |
| `auth.bearer`                        | `auth.go`. |
| `auth.basic`                         | `auth.go`. |
| `auth.api_key` (header + `in_query`) | `auth.go`. |
| `auth.custom`                        | `auth.go`. |
| `auth.oauth2.client_credentials`     | `oauth2.go`. POSTs `grant_type=client_credentials` to `token_url` with HTTP Basic `client_id:client_secret` (RFC 6749 §2.3.1) plus optional `scope=...` / `audience=...`. Access token rides as `Authorization: Bearer <token>`. Non-2xx token responses surface with body-classification only — never the response bytes. |
| `auth.oauth2.password_grant`         | `oauth2.go`. POSTs `grant_type=password` + `username` / `password` in the form body (RFC 6749 §4.3.2). Optional `client_id` rides in the form body. Same Bearer-injection + token-fetch error contract as `client_credentials`. |
| `auth.oauth2.<grant>.cache`          | `oauth2.go`. Cached access token at `state.<cache.store_in>` (auto-registered as a runtime string). Expiry timestamp at `cursor.__oauth2_<store_in>_expires_at` as RFC 3339. `expiry_field` is body-relative; parsed value interpreted as integer seconds (RFC 6749 `expires_in`) with `string→int` / `string→Go-duration` fallbacks. Fresh fetch fires when `now + expiry_buffer >= cached_expires_at`. |
| `auth.multi_mode`                    | `auth.go`. `applyMultiMode` evaluates each `branches[].when` predicate in declaration order, first match wins. `default.auth` fires when no branch matches. The validator forbids nested `multi_mode`. |

### 8.2 Requests

| Feature                                          | Notes |
| ------------------------------------------------ | ----- |
| HTTP methods, `path` vs `url`, query, headers    | `http.go`. |
| `body.json`, `body.form`, `body.raw`             | `http.go`. |
| `expect_status` (default 200)                    | `http.go`. |
| `if:` predicate gating                           | `runner.go`. |
| `on_status: skip \| fail \| empty_events`        | `runner.go`. |
| `on_status: invalidate_cache`                    | `runner.go` + `oauth2.go`. Drops every OAuth2 token-cache slot reachable from `doc.Auth` (`state.<store_in>` + `cursor.__oauth2_<store_in>_expires_at`, including each branch of `auth.multi_mode`), then advances as if the page came back empty. When the active auth has no cache (or is non-OAuth2) the verb degrades to `empty_events` with a log line. |
| `produces_events` explicit + implicit (last)     | `runner.go`. |
| `produces_events` implicit for `async_job` role  | Last-declared role wins. |
| `extract[]` body source + `target: extract`      | `extract.go`. |
| `extract[]` header source                        | `extract.go`. |
| `extract[]` `target: cursor`                     | Writes into `scope.cursor`. |

### 8.3 Response

| Feature                                       | Notes |
| --------------------------------------------- | ----- |
| `decode: json`                                | `http.go`. |
| `decode: ndjson` (lines list)                 | `http.go`. |
| `events_at` zero path = body root             | `bodypath.go`. |
| `events_at` per-line for ndjson               | Each line treated as JSON object. |
| `placeholder_event`                           | Two-pass detector in `runner.go`. An iteration that produces zero events with `paginationMore=true` queues a placeholder; emission is confirmed (and ordered ahead of the next page's events) at the START of the next iteration. ctx-cancel, `MaxPages`, and standard-mode-on-unexpected-status all suppress the trailing placeholder. Variant-independent across cursor_token / scroll_id / page_number / link_header / next_url_in_body / graphql_relay. |

### 8.4 Pagination

| Variant                                                 | Notes |
| ------------------------------------------------------- | ----- |
| `pagination.none`                                       | |
| `pagination.cursor_token`                               | `pagination.go`. Default completion when `{ref: <token_at>}` resolves to zero. |
| `pagination.page_number` + `has_more_at` + `batch_size` | |
| `pagination.offset` + `batch_size`                      | Roles `offset` and (when `batch_size` set) `offset_end`. |
| `pagination.link_header` (+ optional `pattern`)         | RFC 5988 default; pattern overrides with a regex whose first capture group is the next URL. |
| `pagination.next_url_in_body`                           | Role `next_url`; missing / non-string / empty value terminates the drain. |
| `pagination.scroll_id` + `complete_when`                | First iteration leaves the role unset (server opens a fresh session); later iterations replay the id captured at `scroll_id_at`. |
| `pagination.graphql_relay`                              | Role `relay_cursor` reads from `cursor.<cursor_var>` (the GraphQL variable name); `has_next_page_at` (boolean) drives termination. |
| `send_as` implicit auto-injection                       | `pagination.go` (`autoInjectSlot` / `paginationAutoInjector`) + `http.go` (`queryDeclared` / `headerDeclared`). Producer-step only; explicit `{from_pagination: ...}` wins when both are present. |

### 8.5 Progress

| Variant                                                                       | Notes |
| ----------------------------------------------------------------------------- | ----- |
| `progress.stateless`                                                          | |
| `progress.latest_event_timestamp` + `initial.lookback` + per-iter `lookback`  | `progress.go`. |
| `progress.max_event_field`                                                    | `progress.go`. Role `latest_timestamp`. Shares the `maxEventTime` events-walk helper with `latest_event_timestamp` and async_job's `kind=latest_event_timestamp`. |
| `progress.use_now`                                                            | `progress.go`. Advance writes `cursor.last_timestamp = s.now() - lookback`; no events walk. |
| `progress.time_window`                                                        | `progress.go`. Roles `window_start` / `window_end` (formatted via `cfg.Format`; default `rfc3339`; closed set rfc3339 / rfc3339nano / unix_seconds / unix_millis). First run is `[now() - initial_offset, now()]`; advance slides `window_start` to the just-finished `window_end`. `window_end` clamps to ≥ `window_start` so a backwards-running clock cannot invert the window. |
| `progress.async_job` (submit / poll / fetch phase machine)                    | `progress.go`. |
| `async_job.on_complete.cursor_update.kind = stateless`                        | |
| `async_job.on_complete.cursor_update.kind = use_now`                          | |
| `async_job.on_complete.cursor_update.kind = latest_event_timestamp`           | `applyOnComplete` locates events at `doc.Response.EventsAt`, walks them with `maxEventTime`, picks the max at `cu.event_time.path`, subtracts the optional `cu.lookback`, writes `cursor.last_timestamp`. |

### 8.6 Value forms

All `Value` discriminator keys are wired: `literal_string`,
`literal_int`, `literal_bool`, `ref` (+ `default`), `now` (+ `offset`),
`concat`, `select` (Predicate branches + default), `from_pagination`
(roles `token` / `page` / `offset` / `offset_end` / `next_link` /
`next_url` / `scroll_id` / `relay_cursor`), `from_progress`
(`latest_timestamp` for `latest_event_timestamp` / `max_event_field` /
`use_now`; `window_start` / `window_end` for `time_window`), `format`
(all verbs: `string`, `int`, `bool`, `rfc3339`, `rfc3339nano`,
`unix_seconds`, `unix_millis`, `duration`, `url_encode`,
`parse_duration`), `base64`, `object`, `list`.

### 8.7 Predicate forms

All `Predicate` forms are wired and tested (`predicate.go`,
`predicate_test.go`): `eq`, `gt`, `lt`, `gte`, `lte`, `present`, `and`,
`or`, `not`, `literal_bool`.

Ordered comparisons coerce time strings → `time.Time`, int / int64 →
numeric, otherwise lexicographic fallback. Equality uses
`reflect.DeepEqual` first, then `toString` fallback. Body-relative
`body.<path>` predicates are valid only inside `complete_when`.

### 8.8 Path

Dotted-string and `{parts: [...]}` escape form are decoded by
`schema.Path`; the runner consumes parsed segments. Namespace
resolution (`state`, `cursor`, `extract`, `steps`, `item`, `body`)
lives in `state.go`. Body-relative traversal — including NDJSON arrays
— lives in `bodypath.go`.

### 8.9 State

- Type coercion at load is a pass-through (`state.go`'s
  `coerceDefault`). yaml.v3 gives the right Go scalar for
  `string` / `int` / `bool`; `duration` / `url` / `enum` / `secret`
  are coerced at Value-eval time by consumers.
- Default merging is "snapshot wins over IR default".
- Runtime vs config mutability persistence lives in `state.go`.
- Secret redaction in logs and errors lives in `redact.go`:
  `safeURL`, `redactURLError`, and `redactValue(doc, v)`. Every
  transport error path in `http.go` passes through `redactURLError`;
  the `toTime` RFC 3339 parse error reports byte length only so a
  secret-typed state field piped through `format:rfc3339` cannot leak
  its runtime value. `redactValue(doc, v)` is the helper any log line
  or error message MUST use when it needs to mention an `schema.Value`.

### 8.10 Sink, Store, CLI

- `JSONLSink` to any `io.Writer`, mutex-guarded, file-aware `Flush`.
- `MemoryStore` (process-local) and `FileStore` (atomic write).
- `cmd/skopos run -i <file> [--state ...] [--once] [--interval ...]
  [--out ...] [--trace ...]`. Signal handling via `signal.NotifyContext`
  for SIGINT / SIGTERM (graceful exit 0 from both one-shot and
  continuous modes). `--once` and `--interval` are mutually exclusive.
  `--state` parent-dir writability is preflight-probed at flag-parse
  time. `--trace` appends one redacted `Exchange` record per HTTP
  exchange to the named file (O_APPEND). Continuous-mode error policy
  is "always retry": even `error.mode: fail` from the IR does not stop
  the loop — operators who want fail-fast run with `--once` and let
  their orchestrator decide.
- `cmd/skopos validate`. Exits 1 when any error-severity diagnostic is
  printed; diagnostics go to stdout.

### 8.11 HTTP transport

- Default 30-second per-request timeout.
- Caller can inject a custom `*http.Client` (proxy, mTLS, custom
  transport, redirect policy).
- Retry / rate-limit / backoff are not modelled at the runner layer
  (out of scope; authors who need retries plug them into the injected
  transport).
- HMAC / SigV4 / OAuth1 signing are not modelled until the IR ships a
  structured signing `Value`.

### 8.12 Observability

- Operational logging via `Runner.Logger` (skipped steps, `on_status`
  decisions, `error.mode` dispatches).
- HTTP exchange trace via `Runner.Tracer` (per-request `Exchange`
  record carrying iteration, phase, step id, redacted URL, redacted
  query map, redacted post-auth headers, body metadata, status,
  elapsed, error string). `trace.go` provides the `Tracer` interface,
  the `Exchange` struct, a `TracerFunc` adapter, and a `JSONLTracer`
  that writes one JSON object per exchange.

  Redaction policy pinned by `trace_test.go`: `Authorization` /
  `Cookie` / `Proxy-Authorization` always redacted by name; the
  runtime credential surfaces named by `auth.custom.header` and
  `auth.api_key.header` redacted by name; any `req.Headers` /
  `req.Query` entry whose IR `Value` is `schema.IsSecret` is redacted
  by content; request and response bodies are metadata-only (byte
  length + leading-byte classification), never raw bytes. URLs go
  through `safeURL` (scheme + host + path; query and userinfo
  stripped); the query map is exposed separately so debugging "did we
  send the right `page=` parameter?" stays possible without leaking
  secrets.

---

## 9. Non-goals

- **Code generation.** The runner is in-process. A future
  code-emitting backend is a separate package, not a runner concern.
- **Retry / backoff / rate-limit policy.** IR non-goal; runner
  inherits.
- **mTLS, proxy, custom DNS, redirect policy.** Caller plugs an
  `*http.Client`.
- **Multi-process / RPC dispatch.** Out of scope.
- **OAuth2 interactive flows (`authorization_code`, `device_code`,
  PKCE).** The pull-loop runtime has no browser-roundtrip surface.
  Long-lived refresh tokens stay as `secret`-typed state fields.
- **HMAC / SigV4 / OAuth1 signing.** Out of scope until a structured
  signing `Value` lands.
- **Scheduling.** `Runner.Drain` is one pull session. Sleeping,
  cron-like dispatch, and multi-template orchestration belong above
  the runner.
