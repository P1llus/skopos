# Runtime

What the runner does at run time, given a parsed spec. Covers the drain
lifecycle, the two loops (pagination, request), progress evaluation
timing, error semantics, the `cache.*` namespace lifetime, secret
redaction, the `Tracer` surface, HTTP transport defaults, the
concurrency model, and the per-drain page cap.

For the YAML field reference see [`schema.md`](schema.md); for the
catalogue of vendor patterns mapped to schema fragments see
[`api-methods.md`](api-methods.md); for the persistence contract see
[`stores.md`](stores.md); for end-to-end walkthroughs of embedding the
runner see [`usage.md`](usage.md).

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

`schema/` owns the YAML/JSON parser and the structural validator; it
does not know `client` exists. `client` consumes `*schema.Doc` and does
not own scheduling — the caller (`cmd/skopos run` or an embedder)
decides one-shot vs. interval.

---

## 2. Drain lifecycle

A `Runner.Drain(ctx)` call is one full pull session. The drain runs the
[pagination loop](#3-pagination-loop) over the spec's `requests:` chain,
emits the producer step's events to the configured `Sink`, evaluates
`progress:` writes after each accepted page, and persists the snapshot
to `Store` at the end via a deferred call. The caller decides what
"next" means (sleep + redrain, exit, etc.).

The drain sequence:

1. **Load.** `Store.Load()` returns the persisted snapshot; the runner
   seeds the in-memory `state.*` map by layering the IR's
   `state.<name>.default` Values under the persisted values. Any
   declared field absent from the snapshot is filled from its `default:`
   (which may be a composed Value — `{subtract: [{now: true}, "720h"]}`,
   `{ref: state.x}`, etc.).
2. **Per-drain wipe.** Every state field whose lifetime infers to
   "per-drain scratch" (the `to:` destination of any `pagination:`
   write) is reset to its declared `default:` (or unset when no default
   is declared). Per-drain state is wiped at the start of every
   drain — a drain that fails mid-page re-bootstraps pagination on the
   next start. Persistent and operator-config fields are untouched.
3. **Pagination loop.** See [§3](#3-pagination-loop). One request fires
   per iteration (or one `requests:` chain when the spec has more than
   one step); the loop ends when the active pagination variant's
   `terminate_when:` predicate returns true.
4. **Sink delivery.** For every accepted page-response, the runner
   resolves `response.events_at`, decodes the events list (one event
   per element, or one event per NDJSON line under `decode: ndjson`),
   and calls `Sink.Emit(event)` once per event in declared order.
5. **Progress.** After events emit, the runner evaluates every entry in
   the `progress:` list and writes each `to:` destination. See
   [§5](#5-progress-evaluation-timing) for the timing rule.
6. **Commit.** `Store.Save(snapshot)` runs via a deferred call — on
   normal exit, on `error.mode: warn`, AND on `error.mode: fail`. A
   partial drain (e.g. a poll loop that hit the `MaxPages` cap) still
   persists whatever was reached so the next drain resumes from the
   high-water mark.

Sink delivery happens **before** the deferred `Save`. Failure between
events-sent and state-committed is acceptable (at-least-once); failure
before events-sent leaves persistent state unchanged. Sinks downstream
of this contract are responsible for their own dedup if they need
exactly-once semantics.

---

## 3. Pagination loop

The pagination loop fires the `requests:` chain repeatedly until the
active pagination variant's `terminate_when:` predicate returns true.
Each iteration of the loop is one page. The execution order per page is
fixed and matches [§pagination → execution order in
`schema.md`](schema.md#execution-order-per-page):

1. **Request.** The `requests:` chain runs end-to-end (or as far as
   `if:` / `terminate_when:` / `on_status:` direct). The producer step
   contributes its decoded body to `response.*`.
2. **Terminate.** The variant's `terminate_when:` predicate (or its
   default, for the named variants) evaluates against `response.*` and
   the pre-advance `state.*`. If true, the loop ends — no `to:` write
   fires for this page.
3. **Advance.** The variant's `to:` write (or every entry in
   `pagination.custom.advance:`) runs, populating the per-drain state
   slots that drive the next iteration.
4. **Loop.** Go back to step 1.

The advance step is skipped on the terminating iteration: the predicate
reads `response.*` directly so it observes the pre-advance values. The
named variants' default `terminate_when:` predicates encode the common
shapes:

| Variant         | Default `terminate_when:`                                        |
|-----------------|------------------------------------------------------------------|
| `none`          | (no loop — one iteration only).                                  |
| `cursor_token`  | `{not: {present: <from>}}`.                                      |
| `next_url`      | `{not: {present: <from>}}`.                                      |
| `counter`       | `{lt: {path: events.count, value: <step>}}` (short-page).        |
| `custom`        | No default; the author MUST supply `terminate_when:` explicitly. |

The pagination loop is the only loop that walks the **same request
chain** repeatedly. The [request loop](#4-request-loop) is a distinct
primitive that loops one specific request.

### MaxPages safety cap

A buggy server that returns the same cursor token forever, or that
omits the termination signal, would loop indefinitely. `Runner.MaxPages`
(default `10_000`) bounds one drain. Hitting the cap returns an
"iteration cap exceeded" error from `Drain`; the deferred `Save` still
persists whatever state was reached. A negative value disables the cap
(tests only).

---

## 4. Request loop

`requests[].terminate_when:` is the request-level loop primitive: after
the response is received, the predicate evaluates against `response.*`
and the active `state.*`. If true, the loop exits and the runner moves
to the next request in the chain. If false, the same request re-fires.
When `terminate_when:` is omitted, the request fires exactly once.

The request loop is what expresses the async-job pattern: a `poll` step
with `terminate_when:` keyed on the job's completion signal sits
between an unlooped `submit` step and an unlooped `fetch` step. No
phase machine; no role discriminator; no runner-allocated state fields.
Every field used to chain the three requests (job id, result URL) is a
plain `state.*` field populated by ordinary `extract:` writes. See
[`api-methods.md` §6](api-methods.md#6-async-job-pattern-request-level-loop)
for the full recipe.

The request loop and the pagination loop compose: each iteration of the
pagination loop runs every step in the `requests:` chain, and any step
that carries `terminate_when:` runs its own inner request loop before
the next step starts.

---

## 5. Progress evaluation timing

`progress:` is a flat list of `{to, from, ...}` writes evaluated
**once per accepted page-response, including empty pages**. The
"accepted" qualifier is load-bearing:

- A page-response is **accepted** when its HTTP status passes the
  request's `expect_status:` AND any `on_status:` entry that fires for
  that status does not abort the drain. `skip` and `empty_events` count
  as accepted; `fail` does not.
- A page-response with an empty events list (zero elements after
  `response.events_at` resolves) is still accepted. Progress writes
  fire on empty pages so that server-provided cursors, ingestion
  timestamps, and other response-body fields can persist independently
  of event production.
- Progress does NOT fire when a step is skipped by `if:`, when
  `on_status: <code>: fail` aborts the drain, or when a transport-level
  failure (DNS, connection refused, decode error) interrupts the drain
  before any response is decoded.

All `from:` expressions inside one `progress:` evaluation read from the
same pre-write snapshot of `state.*`. Order of declaration does not
matter for cross-entry references: when one entry's `from:` references
another entry's `to:`, it always sees the old value. The runner stages
every `from:` resolution and only then writes every `to:` destination.

A cumulative high-water mark is written explicitly:
`from: {max: [{ref: state.last_timestamp}, {max: {ref:
events.*.timestamp}}]}`. The runner never guesses what "merging" means
for a given field.

---

## 6. Error semantics

The runner dispatches non-success in two stages: per-step
`requests[].on_status[<code>]` takes precedence; the document-level
`error.mode` is the fallback.

### `requests[].on_status`

`on_status` is a map keyed by exact HTTP status code (`[100, 599]`).
The value is one of a closed set of verbs:

| Verb               | Semantic                                                                                          |
|--------------------|---------------------------------------------------------------------------------------------------|
| `skip`             | Drop the response, emit no events, advance `progress:` as if successful. Canonical 304 handling.  |
| `fail`             | Non-success: emit no events and stop the iteration with an error.                                 |
| `empty_events`     | Non-success: emit no events but DO fire `progress:` writes. Pattern: "429 with `ignore_api_errors`". |
| `invalidate_cache` | Drop every reachable `cache.*` slot (the active auth's cache and every `requests[].cache` slot), then treat the response as a non-event "retry next iteration" signal. |

`retry` is intentionally absent. Retry / backoff / rate-limit policy is
not modelled at the runner layer; authors who need retries plug them
into the injected `*http.Client`.

When `on_status: <code>: invalidate_cache` fires and the active auth
has no `cache:` block and no `requests[].cache` slot resolves either,
the verb degrades to `empty_events` with a log line.

### `error.mode`

`document.error.mode` is the catch-all for statuses not named in any
`requests[].on_status` and for transport / decode failures:

| Mode       | Semantic                                                                                         |
|------------|--------------------------------------------------------------------------------------------------|
| `standard` | (default) The pagination loop ends, the per-drain state is wiped on the next drain start, and the next drain re-bootstraps. Persistent `state.*` writes from `progress:` that already fired survive (the deferred `Save` runs). |
| `warn`     | The runner logs the failure and continues — the iteration advances as if the page came back empty (no events emit). `progress:` does NOT fire for this iteration. The deferred `Save` runs normally. |
| `fail`     | `Drain` returns non-nil; the iteration ends. The deferred `Save` still persists whatever was reached. |

Non-status errors (DNS, connection refused, TLS handshake, response
decode failure) bypass `on_status:` lookup and dispatch directly
through `error.mode`. The pagination loop does not advance on a
transport-level error in any mode.

### `error.include_body`

When `true`, the runner attaches the response body to error messages.
Default `false`. Secret-tainted Value composition still redacts before
the body is attached — see [§7](#7-secret-redaction).

---

## 7. Secret redaction

`secret`-typed state fields propagate their secret status through every
`Value` form they participate in. A composite Value (`{concat: [...]}`,
`{format: ...}`, `{base64: ...}`, `{select: ...}`, `{list: [...]}`,
`{object: {...}}`, string interpolation) inherits the secret status of
any reachable `Ref` that resolves to a secret-typed `state.*` field. See
[`schema.md` §secret propagation](schema.md#secret-propagation).

Every log line, error message, and `Tracer` field that mentions a
`Value` routes through the secret-detector:

- **URLs** emit with scheme + host + path only. Query strings and
  userinfo are stripped. The runtime exposes the query map separately
  so debugging "did we send the right `page=` parameter?" stays
  possible without leaking secrets.
- **Headers** named `Authorization`, `Cookie`, and `Proxy-Authorization`
  are always redacted by name. The runtime credential surfaces named by
  `auth.api_key.header` and `auth.custom.header` are also redacted by
  name. Any `requests[].headers` or `requests[].query` entry whose IR
  `Value` is secret-tainted is redacted by content.
- **Request and response bodies** emit metadata only — byte length plus
  a leading-byte classification (`body 42 bytes, json-like`). Raw bytes
  never appear in trace records or error messages.
- **`time.Time` parse errors** on `secret`-typed inputs report only the
  byte length of the input, so a secret-typed state field piped through
  `format: rfc3339` cannot leak its runtime value when the parse fails.

The redaction policy is pinned by tests in `client/trace_test.go` and
`client/redact_test.go`; runtime callers that need to log a `Value`
MUST route through the helper rather than calling `fmt.Sprintf` against
the raw Value.

---

## 8. Cache namespace

`cache.<name>` slots are written by `Cache` blocks on
`auth.oauth2.<grant>.cache` and `requests[].cache` (one struct, two
sites). The cache namespace is **process memory only**:

- Cache slots live for the lifetime of the `Runner` process.
- `Store.Save` does NOT persist `cache.*` (or any other non-`state.*`
  namespace — see [`stores.md`](stores.md)).
- After a runner restart, the first request that needs a cached value
  misses, the `Cache` block re-runs the underlying step (OAuth2 token
  fetch, custom-login POST), and the slot is repopulated.

`on_status: <code>: invalidate_cache` drops every reachable `cache.*`
slot reachable from the active auth and from every `requests[].cache`
in the spec, then advances the pagination loop as if the page came back
empty. The next iteration misses the cache and forces a fresh fetch.

Re-fetch policy on a populated slot: the `Cache` block records the
slot's `expires_at` Value and re-runs the step when
`now() + buffer >= expires_at`. The default for an OAuth2 grant that
omits a response-body expiry is `{ref: response.body.expires_in,
default: "1h"}`.

---

## 9. `Tracer` surface

`Runner.Tracer` receives one record per HTTP request/response pair.
The interface is in `client/trace.go`:

```go
type Tracer interface {
    Trace(Exchange)
}
```

Each `Exchange` record carries:

| Field           | Notes                                                                                              |
|-----------------|----------------------------------------------------------------------------------------------------|
| `Iteration`     | Pagination-loop iteration index (0-based for the bootstrap iteration).                             |
| `StepID`        | The `requests[].id` of the step that issued this exchange, if any.                                 |
| `URL`           | Scheme + host + path only. Query and userinfo are stripped.                                        |
| `Query`         | Map of redacted query values; secret-tainted values replaced with `<redacted>`.                    |
| `Headers`       | Map of post-auth request headers, with the per-name redaction policy from [§7](#7-secret-redaction). |
| `RequestBody`   | Metadata only (byte length + leading-byte classification). Raw bytes never appear.                 |
| `Status`        | HTTP status code (or 0 on a transport-level failure).                                              |
| `ResponseBody`  | Same metadata-only shape as `RequestBody`.                                                         |
| `Elapsed`       | Round-trip duration.                                                                               |
| `Error`         | Redacted error string when present (transport / decode failures).                                  |

`client.NewJSONLTracer(w io.Writer)` returns a `Tracer` that encodes
one JSON object per exchange and calls `Flush()` from `Drain`'s
deferred unwind. A custom `Tracer` can forward exchanges to any
pipeline that accepts the record shape.

`StartedAt` and `Elapsed` are measured against the real wall clock and
are independent of `Runner.Now`. `Runner.Now` is the *spec* clock: it
backs `{now: true}` evaluation and `cache.expires_at` checks only.
Pinning it makes every wall-clock value a spec writes (into a request
body, query, or state slot) byte-stable, which is what the CLI does
when `SOURCE_DATE_EPOCH` is set (see [usage](usage.md)). Round-trip
timing keeps reporting real durations regardless.

---

## 10. HTTP transport defaults

- **Per-request timeout.** Default 30 seconds. The caller can inject a
  custom `*http.Client` (proxy, mTLS, alternate transport, redirect
  policy, DNS overrides) by setting `Runner.Client`.
- **Always set `Client.Timeout` to a finite value.** `http.DefaultClient`
  has no timeout, and a hung server otherwise wedges `Drain` until
  `ctx` is cancelled.
- **Retry / backoff / rate-limit.** Not modelled at the runner layer.
  Authors who need retries plug them into the injected transport.
- **HMAC / SigV4 / OAuth1 signing.** Not modelled. A structural signing
  Value lands when a concrete template motivates the shape.
- **OAuth2 interactive grants.** `authorization_code`, `device_code`,
  and PKCE all require a browser round-trip; the pull-loop runtime has
  no interactive surface. Refresh tokens that an operator pasted in are
  a plain `secret`-typed state field that feeds `auth.bearer.token`.

The same `Runner.Client` is used for OAuth2 token fetches, so proxy /
TLS config applies uniformly to data fetches and token fetches alike.

---

## 11. Concurrency model

- **One `Runner` is one logical pull source.** `Drain` mutates internal
  per-drain state; calling `Drain` concurrently on the same `Runner`
  races.
- **Multiple independent `Runner`s are independent.** The package holds
  no global state. Fan-out across N templates is N `Runner` values,
  each with its own `Doc` / `Store` / `Sink` / `Logger`, each on its
  own goroutine.
- **Shared `Sink` / `Store` between Runners MUST be safe for
  concurrent use.** The bundled `JSONLSink` is mutex-guarded.
  `MemoryStore` is single-threaded by design. `FileStore` is
  **single-writer-per-path by contract** — an in-process mutex
  serialises `Save` / `Load` on a single `*FileStore`, but no
  cross-process lock is acquired. Two skopos processes writing the
  same path race on the temp-file → rename sequence and the second
  writer silently clobbers the first. Operators running multiple
  processes give each its own path or plug in a `Store` backed by
  something with multi-writer semantics (BoltDB, SQLite, etc.). See
  [`stores.md`](stores.md).

---

## 12. Fan-out

`requests[].fan_out` runs one step once per element of a list Value.
The author-chosen `fan_out.as` name is bound to the current item for
the duration of one per-item iteration; refs inside the step (and
inside `extract.from` paths it owns) use that name as the namespace
root — `{ref: incident.id}` when `as: incident`.

`fan_out.as` must not collide with a reserved namespace root. The
reserved list is:

- `state`
- `cache`
- `events`
- `extract`
- `steps`
- `response`

Nor may it collide with any declared state field name or earlier step
id.

Per-item errors run through the same `on_status` / `error.mode`
dispatcher as a single-request step: `skip` / `empty_events` drop the
item; `fail` aborts the drain; `invalidate_cache` clears the active
auth and step caches and stops the fan-out early.

`requests[].fan_out` and `requests[].cache` are mutually exclusive —
cache stores one token-shaped value, fan-out runs the step N times.
The validator rejects the combination.

---

## 13. Observability

- **Operational logging via `Runner.Logger`** — skipped steps,
  `on_status:` decisions, `error.mode` dispatches, cache invalidations,
  retry-after-failure messages in continuous mode. The default logger
  is `log.Default()`; the CLI installs a `log.New(os.Stderr, "skopos:
  ", log.LstdFlags|log.Lmsgprefix)` so log lines are prefixed with
  `skopos:`.
- **HTTP exchange trace via `Runner.Tracer`** — see
  [§9](#9-tracer-surface).
- **Metrics surface.** Not provided by the runner. A `Tracer` can
  compute drain duration, events per page, error rate per endpoint by
  aggregating per-exchange records.

---

## 14. Non-goals

- **Code generation.** The runner is in-process. A future
  code-emitting backend would be a separate package, not a runner
  concern.
- **Retry / backoff / rate-limit policy.** Inherited from the IR's
  non-goal; the runner runs whatever the injected transport does.
- **mTLS, proxy, custom DNS, redirect policy.** Caller plugs an
  `*http.Client`.
- **Multi-process / RPC dispatch.** Out of scope.
- **OAuth2 interactive grants** (`authorization_code`, `device_code`,
  PKCE). The pull-loop has no browser-roundtrip surface.
- **HMAC / SigV4 / OAuth1 signing.** Out of scope until a structured
  signing `Value` lands.
- **Scheduling.** `Runner.Drain` is one pull session. Sleeping,
  cron-like dispatch, and multi-template orchestration belong above
  the runner.
- **MIME-chain decoding** (ZIP, gzip, CSV inside an HTTP body).
  `response.decode` is a closed `json | ndjson` enum; MIME chaining
  is expected to live in the ingest pipeline.
