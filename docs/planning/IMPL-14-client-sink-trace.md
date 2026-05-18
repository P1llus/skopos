# IMPL-14 — `client/sink.go` + `filestore.go` + `redact.go` + `trace.go` + `doc.go`

## Scope

Close the post-redesign rewrite of the client package's diagnostic /
persistence surface: the `Sink` and `FileStore` docstrings against the
new `Snapshot` shape (`{"state": {...}}` only), the redact-value walk
across every new `schema.Value` variant, the `Tracer` cleanup
(`Exchange.Phase` drop, cache-HIT tombstone), and the package-level
preamble in `doc.go` against the current drain shape (no async_job
phase machine, no `placeholder_event`, no `Defaults.BaseURL`).

## Old → new map

| Legacy surface / mechanism                                  | New mechanism                                                                                                                                                                                                                                                                                                          |
|-------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `Snapshot.Cursor map[string]any` (persisted top-level key)  | Removed in Slice 8. `filestore.go` documents that `json.Unmarshal` silently drops the stale `"cursor"` key on Load; the next Save writes only `"state"`. No migration code (global rule #1).                                                                                                                          |
| `Exchange.Phase string` (async_job phase tag, "" otherwise) | Dropped. The async_job phase machine is gone in Slice 4 / 7; the runner passed `""` everywhere. The field had zero contributors and no consumers (grep across `client/`, `cmd/`, `internal/` finds none) — removal is mildly breaking on the JSONL wire format but the project carries no installed users.            |
| `buildExchange(..., iter int, phase string)`                | Same signature for ABI stability against the frozen `runner.go` + `fanout.go` call sites; the trailing string parameter is now named `_`. Slice 12's `runner.go` and Slice 13's `fanout.go` continue to pass `""`. Dropping the param outright would touch frozen files.                                              |
| `redact.valueShape` covers Concat / Select / Format / Base64 / List / Object only | Extended to recognise every Value variant from Slice 5: `Add`, `Subtract`, `Max`, `Min`, `First`, `Last`, `Count`, `Regex` (plus the desugared `{concat: [...]}` already covered). When a new variant lands in `schema/value.go`, this switch MUST grow to match.                                                  |
| `safeURL(nil)` returns the literal sentinel `"<nil-url>"`   | Returns `""`. With the new `omitempty` on `Exchange.URL`, an empty URL drops out of the JSONL line rather than emitting a sentinel; trace consumers tell "no wire call" from a populated `URL` via the absence of the key.                                                                                            |
| `Exchange.URL` / `Method` / `StartedAt` / `Elapsed` always serialised | All four carry `omitempty`. On a cache HIT they are zero-valued and drop out; on a wire call they populate normally. `time.Time` `omitempty` skips the zero `time.Time` (Go's standard `IsZero` test); `time.Duration` zero is `0` which `omitempty` also drops.                                                  |
| Cache HIT trace path emitted a noisy "<nil-url>" / 0 record | `buildExchange` detects `t.method == "" && runErr == nil` and returns a minimal Exchange carrying only `Iteration`, `StepID`, `CacheHit=true`. The runner still calls `OnExchange` on every step (frozen) but the record stays a clean tombstone.                                                                     |
| `Exchange.CacheHit bool` (new field, omitempty)             | Distinguishes "step was served from cache" from "step never reached the wire because of an early build error". Build failures (URL build, body build, applyAuth) carry a non-nil `runErr` and surface through the normal Exchange path with `Error` populated and `Method`/`URL` empty.                              |
| `doc.go` preamble mentioning `async_job phase machine` + `want_more` | Rewritten against the current Drain semantics: load → per-drain wipe → pagination loop (each iteration runs the requests chain, emits, fires progress, advances) → deferred Save+Flush. Mirrors `runner.go`'s top-of-file commentary plus a section listing namespace lifetimes (state / cache / events / extract). |
| Trace `valueShape` example `<ref cursor.last_timestamp>`    | Replaced with `<ref state.window_start>` so the `JSONLTracer` doc no longer leaks the legacy `cursor.*` namespace root in a comment.                                                                                                                                                                                  |
| `filestore.go` HTML-escape example "cursor tokens, opaque pagination state" | Reworded to "opaque server-issued tokens, embedded URLs, HTML-looking page markers" — the same reason for disabling HTML escaping, no `cursor` framing.                                                                                                                                                          |

## Removed content

Types / fields:

- `Exchange.Phase string` — gone.

Functions / methods: none. (Tests stay; Slice 17.)

Comments / docstrings:

- Every reference to `async_job phase machine`, `want_more`,
  `cursor.<name>`, `placeholder_event`, `Defaults.BaseURL`,
  `requests[].path`, `TokenCache`, `RequestCache`, `expiry_field`,
  `expiry_buffer`, `expiry_format` across the five slice-14 files.
- The "<nil-url>" sentinel-return docstring on `safeURL`.

Sentinels:

- `safeURL(nil) → "<nil-url>"` — now returns `""`.

## File-by-file walk

### `client/sink.go`

No content changes. A re-read confirms the package docstring already
holds the current invariants — one `Emit` per accepted event,
`Flush` from the deferred unwind, `JSONLSink` mutex-guarded for the
concurrent-Runner case. No `cursor.*` / `async_job` /
`placeholder_event` references; the file is post-redesign already.

### `client/filestore.go`

- The `FileStore` top docstring gains an "On-disk format" section
  spelling out the `{"state": {...}}` shape and that
  `json.Unmarshal`'s default unknown-key drop is what handles a
  pre-redesign file silently. No migration code (global rule #1).
- `Load`'s docstring picks up a sentence about the same unknown-key
  drop semantics.
- The HTML-escape rationale on `Save` re-frames its `<` / `>` / `&`
  example away from `cursor tokens, opaque pagination state` into
  the more general "opaque server-issued tokens, embedded URLs,
  HTML-looking page markers". The behaviour is identical.

### `client/redact.go`

- `safeURL(nil)` returns `""` (was `"<nil-url>"`). The docstring
  explains the new convention: an empty URL means "no wire call
  happened"; with `omitempty` on `Exchange.URL` the JSONL line drops
  the key entirely.
- `redactValue`'s docstring picks up an explicit list of the
  variants `schema.IsSecret` walks, with the desugared
  `{concat: [...]}` for interpolated strings called out so future
  readers know `${state.token}-${state.suffix}` taints the whole
  composition.
- `valueShape` switch gains arms for `Add`, `Subtract`, `Max`,
  `Min`, `First`, `Last`, `Count`, `Regex`. Each returns a
  variant-name tag (`"<add>"`, `"<max>"`, ...) consistent with the
  pre-existing arms. No operand recursion: the IR shape is what we
  surface, not a serialisation of the inner Values.

### `client/trace.go`

- `Exchange.Phase` field removed. The struct comment block gains a
  "Cache HIT tombstones" section describing the
  `{iteration, step_id, cache_hit}` shape.
- `Exchange.CacheHit bool` field added (omitempty).
- `Exchange.Method`, `URL`, `StartedAt`, `Elapsed` now carry
  `omitempty`. The behaviour for a wire call is unchanged
  (all four populate); the behaviour for a cache HIT is to drop
  out and leave the tombstone clean.
- `buildExchange` signature stays
  `(doc, req, t, runErr, iter, phase)`; the trailing string is
  now named `_`. The fast path detects
  `t.method == "" && runErr == nil` and returns the cache-HIT
  tombstone. Build-time failures (`runErr != nil`) flow through
  the normal path; `Method`/`URL` are zero, `Error` carries the
  redacted diagnostic.
- The `httpTrace` struct-doc gains a sentence describing the
  cache-HIT scratchpad shape (zero-valued, no wire error → the
  fast path fires).
- `NewJSONLTracer`'s HTML-escape rationale swaps
  `<ref cursor.last_timestamp>` for `<ref state.window_start>`.

### `client/doc.go`

Full rewrite. Drops:

- the `async_job phase machine` reference;
- the "the spec document is the program" framing replaced with
  "the parsed IR is the program";
- the disclaimer about the IR-shape list — replaced with explicit
  prose pointing at `runner.go`'s drain-lifecycle comment block
  and `docs/runtime.md` §2.

Gains:

- A "Drain lifecycle" section: load → per-drain wipe → pagination
  loop → deferred Save+Flush, per `runner.go`'s top-of-file
  documentation.
- A "Namespaces and lifetimes" section enumerating state (persisted),
  cache (process memory only), events / extract / steps / response
  (per-page or per-iteration scratch).
- A "Concurrency" section pointing at the one-Runner-per-source
  contract; the shared-Sink/Store requirement; the bundled
  thread-safe defaults.
- A "Redaction" section pointing at `docs/runtime.md` §7 and
  `trace.go`'s package comments for the per-field rationale.

## Build state

- `go build ./schema/...` — green.
- `go build ./client/...` — green (zero errors).
- `go build ./cmd/...` — green.
- `go build ./...` — green.
- `go vet ./client/...` reports stale `*_test.go` failures
  (Slice 17's territory): `auth_test.go`, `requestcache_test.go`,
  and the larger set already flagged at Slice 13 close. Production
  code carries zero new errors.
- Tests stay red until Slice 17.

## Walkthrough verification

End-to-end correctness is not checkable until Slices 16–17 land. The
trickier cases were walked against the code as follows.

### Case 1 — Cache HIT trace tombstone

```yaml
requests:
  - id: login
    method: POST
    url: ${state.base_url}/login
    body:
      json:
        api_key: ${state.api_key}
    cache:
      to: cache.session
      expires_at: {ref: response.body.expires_at}
  - id: events
    method: GET
    url: ${state.base_url}/events
    headers:
      X-Session-Token: {ref: cache.session.token}
    produces_events: true
```

Drain iteration 1 (cold start):

1. `runRequest(login)` calls `executeRequest`. `req.Cache != nil`,
   `cacheGet` returns `(nil, false)` → MISS. The request fires
   normally; the trace scratchpad is fully populated;
   `buildExchange` builds a normal Exchange (Method, URL, headers,
   status). `r.Tracer.OnExchange(ex)` emits a normal trace line.
2. `runRequest(events)` calls `executeRequest`. `req.Cache` is
   nil; request fires normally; same.

Drain iteration 2 (next page):

1. `runRequest(login)` calls `executeRequest`. `req.Cache != nil`,
   `cacheGet` returns `(<body>, true)` → HIT. Returns
   `&stepResult{200, <cached body>}` without populating `httpTrace`.
2. The runner's `runRequest` sees `r.Tracer != nil` and calls
   `OnExchange(buildExchange(doc, req, trace, nil, iter, ""))`
   regardless. Inside `buildExchange`, `t.method == ""` and
   `runErr == nil` → returns
   `Exchange{Iteration: 2, StepID: "login", CacheHit: true}`.
3. The JSONL line for that iteration's login step reads
   `{"iteration":2,"step_id":"login","cache_hit":true}`. The
   operator sees the step in the trace, can correlate it against
   the spec, and knows no wire call was made.

### Case 2 — Build-time failure (URL build) trace record

```yaml
state:
  base_url: {type: url}   # default unset

requests:
  - id: events
    method: GET
    url: ${state.base_url}/events   # state.base_url is unset → buildURL fails
```

`runRequest(events)` calls `executeRequest`. `req.Cache` is nil so
the HIT pre-check is skipped. `s.buildURL(req)` returns an error
(ref to unset state field). `executeRequest` returns
`(nil, fmt.Errorf("url: %w", ...))` before any trace field is set.

Back in the runner, `r.Tracer != nil` so `OnExchange` is called.
Inside `buildExchange`, `t.method == ""` BUT `runErr != nil` → falls
through to the normal path. The Exchange carries
`Method: ""`, `URL: ""` (safeURL(nil)), `Status: 0`, and
`Error: "url: ref state.base_url is unset"` (redacted via
`redactURLError`). All wire fields drop out under `omitempty`.

The operator sees a clean diagnostic record:
`{"iteration":1,"step_id":"events","error":"url: ref state.base_url is unset"}`.

### Case 3 — Secret-taint propagation through `{concat: [...]}`

```yaml
state:
  token:  {type: secret}
  prefix: {type: string, default: "tenant-42-"}

requests:
  - method: GET
    url: ${state.base_url}/events
    headers:
      X-Custom-Token: "${state.prefix}${state.token}"   # desugars to {concat: [...]}
```

The runner's redacted-headers walk in `trace.go::redactedHeaders`
iterates `req.Headers`. For `X-Custom-Token`, the IR Value is a
`Concat` of `state.prefix` and `state.token`. `schema.IsSecret(doc, v)`
recurses into `v.Concat` (line 1205-1210 of `schema/value.go`):
for each operand, it checks `isSecretStatePath` against the
declared field type. `state.token` is `secret` → returns true. The
header name is added to `redactByName`; the wire header value emits
as `<redacted>`.

The same chain works for `{format: ...}` (IsSecret recurses into
`Format.Value`), `{base64: ...}`, `{select: ...}` branches, and the
list reducers (Max / Min / First / Last / Count) — each Value form
the IR can build composes through the same recursion.

### Case 4 — Secret-taint propagation through `{max: {ref: events.*.token}}`

```yaml
state:
  events_high_water: {type: secret}

progress:
  - to: state.events_high_water
    value:
      max: {ref: events.*.token}    # tokens stored on each event
```

`schema.IsSecret(doc, v)` sees `v.Max != nil` and recurses into
`*v.Max` (line 1252-1255 of `schema/value.go`). The inner Value is
`{ref: events.*.token}`. `isSecretStatePath` checks the path's
first part: it's `events`, not `state`, so the path itself is NOT
secret — but the **target** field (`state.events_high_water`)
declared as `secret` is what marks the progress write secret on
the persistence side. The redact layer treats the IsSecret result
on `v` itself; for a non-`state.*` ref the answer is false.

This is the expected behaviour: the secret marker travels with the
state field declaration, not with arbitrary refs. The redact layer
only kicks in when the Value composes through a secret-typed state
field. Authors who route `events.*` data through a secret-typed
state slot get redaction on the slot's read sites; the events
themselves are not marked secret by the reducer choice.

The runner's safe-by-default posture is to also redact bodies as
metadata-only regardless of secret tagging — so even an
unsecret-tagged event body never lands in the trace verbatim.

### Case 5 — `Snapshot` JSON round-trip with a stale `cursor:` key

A file written by a pre-redesign runner might look like:

```json
{
  "cursor": {"page": 7, "last_timestamp": "2026-05-01T08:00:00Z"},
  "state":  {"high_water": "2026-05-12T08:00:00Z"}
}
```

`FileStore.Load` reads the bytes, `json.Unmarshal` into a `Snapshot`
struct. The struct has only `State map[string]any`; the
`"cursor"` key has no matching field, and Go's `encoding/json`
silently drops it by default. The in-memory Snapshot carries
`State: {"high_water": "2026-05-12T08:00:00Z"}`. The next
`FileStore.Save` writes:

```json
{
  "state": {
    "high_water": "2026-05-12T08:00:00Z"
  }
}
```

No migration code ran; no detection branch fired. The unknown key
disappeared on the first round trip, as the global rule requires.

### Case 6 — `Exchange.Phase` removal: wire format diff

Before:

```json
{"iteration":1,"step_id":"events","method":"GET","url":"https://api.example/events","status":200,"started_at":"2026-05-12T08:00:00Z","elapsed":12345}
```

After:

```json
{"iteration":1,"step_id":"events","method":"GET","url":"https://api.example/events","status":200,"started_at":"2026-05-12T08:00:00Z","elapsed":12345}
```

Identical — `Phase` was already `omitempty` and never had a
contributor. The user-visible JSONL on a non-cache-HIT, no-error
step is identical pre- and post-removal. The only diff visible to
downstream consumers is the loss of the `"phase"` key on records
where someone might have been parsing it as `""` (no one is — the
field has zero contributors anywhere in the codebase).

## Notes for downstream slices

### Slice 15 — `cmd/skopos/*`

- `cmd/skopos/cmd_run.go` already wires `client.JSONLTracer` via
  `--trace-file` (line 127-137 in the current file). No changes
  needed for the Phase drop / cache-HIT tombstone — the CLI emits
  whatever the Tracer emits.
- `client.RedactURLError` is exported and consumed at the
  `cmd_run.go` continuous-mode drain-error log site (line 194). The
  signature is unchanged.
- A cache-HIT JSONL line on `--trace-file` now reads
  `{"iteration":N,"step_id":"...","cache_hit":true}`. The CLI's
  `--help` may want to mention this in the trace-file documentation
  paragraph, but that's a CLI-doc decision for Slice 15, not a
  contract change.

### Slice 17 — test rewrite

- `client/trace_test.go` must drop any `Exchange.Phase` assertions.
  Slice 14's `git grep "\.Phase" client/` shows no remaining
  consumers in non-test code, but the test rewrite should confirm
  the JSON wire format on cache HIT (`{iteration, step_id,
  cache_hit}` only).
- `client/redact_test.go` needs cases for every new variant in
  `valueShape`: Add / Subtract / Max / Min / First / Last / Count /
  Regex. The IsSecret propagation cases already live in
  `schema/fixtures_test.go::TestIsSecret`; the client-side test is
  the `valueShape` output rendering only.
- `client/filestore_test.go` should drop its `Snapshot.Cursor`
  references (Slice 8 removed the field; the test was left stale
  per global rule #6). A new test confirming the silent-drop
  semantics on a stale `cursor:` key would be nice but is not
  strictly required — the behaviour falls out of `json.Unmarshal`'s
  default and the struct shape.

### Slice 18 — schema-reference regeneration

- The Exchange JSON wire format ships with the regenerated
  reference; that doc should describe the cache-HIT tombstone
  shape (`cache_hit:true`, no wire fields) and the new `omitempty`
  on `method` / `url` / `started_at` / `elapsed`.
- The `Phase` field's removal is mildly breaking on the wire
  format. The reference doc should NOT carry a "formerly" note
  (global rule #5); it simply describes the current shape.
