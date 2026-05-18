# Using skopos

End-to-end walkthroughs and operator-facing how-tos. Targets the
operator who wants to point the runner at an API and get events
flowing — either via the `skopos` CLI or by embedding the `client`
package.

For the YAML spec the runner consumes see [`schema.md`](schema.md);
for the catalogue of vendor patterns mapped to schema fragments see
[`api-methods.md`](api-methods.md); for the runtime contract see
[`runtime.md`](runtime.md); for the persistence contract see
[`stores.md`](stores.md).

---

## Contents

- [The CLI surface](#1-the-cli-surface)
- [Picking a bundled template](#2-picking-a-bundled-template)
- [Writing a new template from scratch](#3-writing-a-new-template-from-scratch)
- [Embedding the runner from Go](#4-embedding-the-runner-from-go)
- [Sinks: receiving events](#5-sinks-receiving-events)
- [Stores: persisting state](#6-stores-persisting-state)
- [Custom HTTP client / transport](#7-custom-http-client--transport)
- [Tracing](#8-tracing)
- [Scheduling: one-shot vs interval](#9-scheduling-one-shot-vs-interval)
- [Multiple specs in one process](#10-multiple-specs-in-one-process)
- [Errors](#11-errors)

---

## 1. The CLI surface

`skopos` is the single binary. Four subcommands:

```
skopos validate [-i spec.yml]
skopos run      [-c config.yml] -i spec.yml [flags]
skopos init     [-o config.yml] [--force]
skopos template list | show [-o spec.yml] <name>
```

### `skopos validate`

Validates a spec document. Reads from `-i` (or stdin when omitted),
writes diagnostics to stdout. Exits `1` when any error-severity
diagnostic is present.

```
skopos validate -i spec.yml
```

Validation is **structural only**: references resolve, unions have
exactly one variant key, required fields are present, mutual
exclusivities hold, write destinations exist under `state:`. It does
NOT check whether the runner can execute the spec end-to-end — those
checks happen at run time. See [`schema.md` §design rules](
schema.md#design-rules).

### `skopos run`

Loads + validates the spec, builds a `Runner`, drives it.

```
skopos run -i spec.yml --state state.json --once --out events.jsonl
```

Key flags:

| Flag                | Default        | Notes                                                                                |
|---------------------|----------------|--------------------------------------------------------------------------------------|
| `-i path`           | (required)     | Spec file (YAML or JSON). May come from `config.input` instead.                      |
| `-c path`           | none           | Config file (YAML) supplying defaults. Explicit flags override the config.           |
| `--state path`      | in-memory      | JSON state file path. Wires a `FileStore` at that path; state survives restarts.     |
| `--once`            | (one-shot default) | Run a single drain and exit. Mutually exclusive with `--interval`.               |
| `--interval dur`    | none           | Sleep this duration between drains and repeat until SIGINT / SIGTERM.                |
| `--out path`        | stdout         | Per-event JSONL output.                                                              |
| `--trace path`      | none           | Per-exchange JSONL trace (one record per HTTP request/response pair).                |
| `--http-timeout dur`| 30s            | Per-request HTTP timeout. Applies to data fetches AND OAuth2 token fetches.          |
| `--max-pages n`     | 10000          | Pagination-loop cap per drain. See [`runtime.md` §3](runtime.md#3-pagination-loop).  |

The state file is the on-disk surface of the `Store` interface — see
[§6](#6-stores-persisting-state) and [`stores.md`](stores.md). Per-drain
scratch state and `cache.*` slots are not stored.

`--once` runs one drain and exits. `--interval` polls continuously
until SIGINT / SIGTERM, sleeping the chosen duration between drains.
Continuous-mode error policy is "always retry": even `error.mode: fail`
from the spec does not stop the loop. Operators who want fail-fast run
with `--once` and let their orchestrator decide.

`--trace` appends one redacted `Exchange` record per HTTP exchange to
the named file (`O_APPEND`). Stdout has no sentinel — stdout is
already claimed by `--out` and mixing redacted Exchange JSON into the
event stream would silently corrupt downstream JSONL consumers. The
trace format is the JSON encoding of `client.Exchange`; see
[`runtime.md` §9](runtime.md#9-tracer-surface).

### `skopos init`

Writes a fully-commented default config to `-o` (or stdout):

```
skopos init -o run.yml
```

The generated file has every supported key listed with its default,
commented out. Edit it, then pass it to `skopos run -c run.yml`.
Explicit CLI flags override config values.

### `skopos template list` / `skopos template show`

Browse bundled spec templates. Each template is a self-contained
example you can use as a starting point.

```
skopos template list                       # print every bundled name
skopos template show bearer_simple         # print to stdout
skopos template show -o spec.yml bearer_simple
```

The template name accepts either `bearer_simple` or `bearer_simple.yml`.
A printed template is just a spec document — feed it to `skopos
validate -i` or `skopos run -i`.

---

## 2. Picking a bundled template

The bundled templates cover the common API shapes (token caching,
header-borne auth, body-borne pagination URLs, ETag-conditional fetch,
async-export polling, fan-out over a list step, multi-mode auth, etc.).
The typical path:

1. List the bundled templates and pick the one that matches the API
   shape you want to talk to:

   ```
   skopos template list
   ```

2. Print the template to a working file:

   ```
   skopos template show -o vendor.yml bearer_simple
   ```

3. Open `vendor.yml`. Every dynamic value lives under the top-level
   `state:` block. Operator-config fields carry a `default:` that
   you override with your real URL, credentials, etc.

   ```yaml
   state:
     url:
       type: url
       default: "https://api.vendor.example"   # change this
     api_key:
       type: secret
       default: "REPLACE_ME"                   # change this
     page_size:
       type: int
       default: 50
   ```

4. Override `default:` in place for any operator-config field you
   want to set permanently, or leave them and supply values at run
   time via the persisted state file. Operator-config fields are
   loaded from `--state` if present, falling back to the declared
   `default:` if absent.

5. Validate, then run:

   ```
   skopos validate -i vendor.yml
   skopos run -i vendor.yml --state vendor.state.json --once
   ```

The `state:` block is the only knob you need to touch on a bundled
template. The auth wiring, pagination strategy, response parsing, and
progress writes are already tuned for the shape the template name
advertises.

---

## 3. Writing a new template from scratch

This walkthrough builds a new spec end-to-end, growing it from the
minimum viable shape into a multi-step pagination-and-progress
template. Each step ends in a runnable spec. The recipe vocabulary
matches [`api-methods.md`](api-methods.md); reach for it for variant
catalogues and longer-form recipes.

### 3.1 Minimum: bearer-authed GET, single page

The smallest functioning spec:

```yaml
ir_version: "1"

state:
  url:
    type: url
    default: "https://api.vendor.example"
  api_key:
    type: secret

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    url: "${state.url}/v1/events"

response:
  decode: json
  events_at: response.body.events

pagination:
  none: {}

progress: []
```

Every spec carries `ir_version`, `auth`, at least one entry in
`requests:`, a `response:`, and a `pagination:` variant. `state:`
holds operator config (a URL and an API key here). `progress: []`
declares no checkpointing — every drain re-fetches the same window.

Run it once:

```
skopos validate -i v1.yml
skopos run -i v1.yml --once
```

### 3.2 Add pagination

Most APIs paginate. Pick the pagination variant that matches the
response shape (see [`api-methods.md` §2](
api-methods.md#2-pagination)):

- `cursor_token` — server returns a next-page token.
- `next_url` — server returns a fully-formed next-page URL.
- `counter` — client increments a page number or offset.
- `custom` — escape hatch for APIs that need two-or-more advances or
  termination on a value that's not the cursor itself.

For an opaque-cursor API:

```yaml
state:
  url:
    type: url
    default: "https://api.vendor.example"
  api_key:
    type: secret
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

`default: ""` keeps the bootstrap iteration's wire shape (`?cursor=`)
when no token is yet known. Omit `default` to skip the slot entirely on
the first iteration. `state.next_token` is a per-drain scratch field
(its lifetime is inferred from the pagination write site) and is wiped
at the start of every drain.

### 3.3 Add progress

Persist a high-water mark so the next drain only fetches new events.
Add a `last_timestamp` state field with a first-run seed, ride it as
a query parameter, and advance it on every accepted page:

```yaml
state:
  # ... earlier fields ...
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}    # 30 days back

requests:
  - method: GET
    url: "${state.url}/v1/events"
    query:
      since: {ref: state.last_timestamp}
      cursor: {ref: state.next_token, default: ""}

progress:
  - to: state.last_timestamp
    from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}
```

`{max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}`
is the canonical restart-safety idiom: the new value is the larger of
the prior persisted value and the maximum event timestamp on the
current page. An empty page (zero events) yields a no-op write because
the reducer over `events.*.timestamp` produces a zero Value, and the
outer `max` falls back to the prior `state.last_timestamp`.

### 3.4 Add a multi-step chain

When the API needs a login step (or any pre-fetch dependency), add a
second request and chain it via `extract:`:

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

  - id: data
    method: GET
    url: "${state.url}/api/data"
    headers:
      Cookie: {ref: extract.session_cookie}
```

`extract.session_cookie` is per-iteration scratch — it lives for one
iteration of the pagination loop and resets at the top of the next.
`steps.<id>.body.<path>` and `steps.<id>.header.<name>` are also
available for cross-step references; see
[`schema.md` §namespaces](schema.md#namespaces). The producer step
defaults to the last entry in `requests:`; mark a different step
`produces_events: true` to override.

### 3.5 Add caching

When the login step has its own expiry, wrap it with a `Cache` block
so the round-trip is paid once and skipped on subsequent drains
(within the same runner process):

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
      - {to: cache.session_cookie, from: response.header.Set-Cookie}
    cache:
      to: cache.session_cookie
      expires_at: {ref: response.body.expires_in, default: "1h"}
      buffer: 60s

  - id: data
    method: GET
    url: "${state.url}/api/data"
    headers:
      Cookie: {ref: cache.session_cookie, default: "pending"}
```

The `cache.*` namespace is process memory only — slots clear on
runner restart and never persist. The same `Cache` struct is used by
`auth.oauth2.<grant>.cache` for OAuth2 token caching; see
[`api-methods.md` §1.8](api-methods.md#18-token-caching-with-the-unified-cache-block).

### 3.6 Add explicit error handling

Define a per-step status-handling table when the upstream uses status
codes for non-error meaning (304 Not Modified, 404 for "no work this
window", 401 to signal stale cache):

```yaml
requests:
  - id: data
    method: GET
    url: "${state.url}/v1/events"
    on_status:
      401: invalidate_cache
      404: empty_events
      304: skip
```

`requests[].on_status` is keyed by exact HTTP status code; the value is
one of the closed verb set:

| Verb               | Meaning                                                                                          |
|--------------------|--------------------------------------------------------------------------------------------------|
| `skip`             | Drop the response, emit no events, fire `progress:` writes as if successful. Canonical 304 handling. |
| `fail`             | Non-success: emit no events and stop the iteration with an error.                                |
| `empty_events`     | Non-success: emit no events but DO fire `progress:` writes. Pattern: "429 with `ignore_api_errors`". |
| `invalidate_cache` | Drop every reachable `cache.*` slot, then treat the response as a non-event "retry next iteration" signal. |

`error.mode` is the catch-all for statuses not named in
`on_status:` — `standard` (default), `warn`, or `fail`. See
[`runtime.md` §6](runtime.md#6-error-semantics).

---

## 4. Embedding the runner from Go

The CLI is one client of the `client` package. Embedding it directly
gives you control over `Sink`, `Store`, `Tracer`, and the HTTP
transport.

```go
import (
    "context"

    "github.com/p1llus/skopos/client"
    "github.com/p1llus/skopos/schema"
)

doc, err := schema.Load(reader)               // parse YAML or JSON
if err != nil { return err }
if diags := schema.Validate(doc); len(diags) > 0 {
    return fmt.Errorf("invalid spec: %v", diags)
}

r := &client.Runner{
    Doc:  doc,
    Sink: client.NewJSONLSink(os.Stdout),
}
if err := r.Drain(ctx); err != nil { return err }
```

`schema.Load(io.Reader)` and `schema.Parse([]byte)` both auto-detect
YAML or JSON. `schema.Validate` returns a slice of `Diagnostic` —
call it before constructing a `Runner` because the runner assumes the
document is structurally valid.

### The `Runner` struct

`client.Runner` is a struct, not a constructor. Set the fields you
care about; defaults cover the rest.

| Field      | Required | Default                  | Notes                                                                 |
|------------|----------|--------------------------|-----------------------------------------------------------------------|
| `Doc`      | yes      | —                        | `*schema.Doc`. One Runner = one logical pull source.                      |
| `Sink`     | yes      | —                        | `Sink` interface. Bundled: `JSONLSink`.                                |
| `Store`    | no       | `&MemoryStore{}`         | Persists state between drains. Bundled: `MemoryStore`, `FileStore`.   |
| `Client`   | no       | 30s-timeout `*http.Client` | Plug in for proxy / TLS / different timeout.                        |
| `Logger`   | no       | `log.Default()`          | Operational diagnostics (skipped steps, on_status decisions).         |
| `Tracer`   | no       | nil                      | Optional per-exchange capture. Bundled: `JSONLTracer`.                |
| `MaxPages` | no       | 10 000                   | Per-drain pagination-loop cap. `< 0` disables (tests only).            |
| `Now`      | no       | `time.Now`               | Clock; useful for tests.                                              |

`r.Drain(ctx)` runs one full pull session: paginate, emit events,
evaluate `progress:`, persist via `Store.Save`, return. The Runner
does NOT loop — the caller decides whether to sleep between drains.

---

## 5. Sinks: receiving events

```go
type Sink interface {
    Emit(event any) error
    Flush() error
}
```

The runner does not buffer or batch: `Emit` is called once per event,
in declared order (the order the events appear at `response.events_at`
on the producer step's body). `event` is the decoded value at
`response.events_at` — typically `map[string]any` (or one `map[string]any`
per line under `decode: ndjson`).

`Flush` is called once at the end of `Drain` so file-backed sinks can
sync to disk.

The bundled `JSONLSink` writes one JSON line per event to any
`io.Writer` and is safe for concurrent `Emit` calls (mutex-guarded):

```go
sink := client.NewJSONLSink(os.Stdout)
```

A custom sink can forward to an in-memory channel, a logger, a message
broker, etc. — anything that can accept an `any`:

```go
type chanSink struct{ out chan<- any }

func (s *chanSink) Emit(event any) error { s.out <- event; return nil }
func (s *chanSink) Flush() error          { return nil }
```

A `Sink` shared between concurrent Runners MUST be safe for concurrent
calls; the bundled `JSONLSink` is.

---

## 6. Stores: persisting state

```go
type Store interface {
    Load() (Snapshot, error)
    Save(Snapshot) error
}

type Snapshot struct {
    State map[string]any
}
```

The runner persists `state.*` fields whose lifetime infers to
**persistent** plus those whose lifetime infers to **operator config**.
Per-drain scratch (every pagination `to:` destination) is wiped at the
start of every drain and is not persisted. `cache.*` and every other
namespace (`events.*`, `extract.*`, `steps.*`, `response.*`,
`<fan_out.as>.*`) live in process memory only. See
[`stores.md`](stores.md) for the full contract and lifetime rules.

Bundled implementations:

- **`MemoryStore`** — in-memory; lost when the process exits. Default
  when `Runner.Store` is nil.
- **`FileStore`** — single JSON file, atomic writes (temp file +
  rename), single-writer per path. `client.NewFileStore("state.json")`.

For SQLite, BoltDB, or any other backing store, implement the
two-method `Store` interface. See [`stores.md`](stores.md) for a
worked example and the concurrency contract.

---

## 7. Custom HTTP client / transport

The runner uses `Runner.Client` for every request, including OAuth2
token fetches. To install a proxy, custom TLS config, or different
per-request timeout:

```go
r.Client = &http.Client{
    Timeout: 60 * time.Second,
    Transport: &http.Transport{
        Proxy: http.ProxyFromEnvironment,
        TLSClientConfig: tlsConfig,
    },
}
```

> Always set `Client.Timeout` to a finite value. `http.DefaultClient`
> has no timeout, and a hung server otherwise wedges `Drain` until
> `ctx` is cancelled.

Retry / backoff / rate-limit policy is not modelled at the runner
layer. Authors who need retries plug them into the injected transport.

---

## 8. Tracing

`Runner.Tracer` receives one `Exchange` per HTTP request/response
pair — useful for debugging and audit. The trace is redaction-safe by
default (see [`runtime.md` §7](runtime.md#7-secret-redaction)):

- URL paths preserved, query and userinfo stripped.
- Headers preserved, with `Authorization` / `Cookie` /
  `Proxy-Authorization` and every auth-block-declared header value
  replaced with `<redacted>`.
- Query values tainted by a secret-typed Value replaced with
  `<redacted>`.
- Request and response bodies emit length + content classification
  only; raw bytes never appear.

```go
tf, _ := os.OpenFile("trace.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
defer tf.Close()
jt := client.NewJSONLTracer(tf)
defer jt.Flush()

r.Tracer = jt
```

Custom `Tracer` implementations can route the same `Exchange` records
to your own pipeline. See [`runtime.md` §9](runtime.md#9-tracer-surface)
for the per-field shape.

---

## 9. Scheduling: one-shot vs interval

The runner does not own scheduling. The two common patterns:

```go
// One-shot — drain once and exit. Matches `skopos run --once`.
if err := r.Drain(ctx); err != nil { return err }

// Continuous polling — caller decides the interval. Matches `--interval`.
t := time.NewTicker(30 * time.Second)
defer t.Stop()
for {
    if err := r.Drain(ctx); err != nil { return err }
    select {
    case <-ctx.Done(): return ctx.Err()
    case <-t.C:
    }
}
```

A `Drain` that returns `nil` did NOT necessarily emit events — an empty
page is a valid accepted page-response, and `progress:` still fires.
The persistent `state.*` cursor advances per the configured
`progress:` writes whether the page was empty or not.

---

## 10. Multiple specs in one process

A common pattern: run several specs from the same binary, each on its
own schedule.

```go
specs := []struct{ Name, Path string }{
    {"github", "specs/github.yml"},
    {"okta",   "specs/okta.yml"},
}

var wg sync.WaitGroup
for _, s := range specs {
    wg.Add(1)
    go func(name, path string) {
        defer wg.Done()
        f, _ := os.Open(path)
        defer f.Close()
        doc, _ := schema.Load(f)
        r := &client.Runner{
            Doc:   doc,
            Store: client.NewFileStore(name + ".state.json"),
            Sink:  client.NewJSONLSink(os.Stdout),  // safe to share
        }
        // schedule loop for r ...
    }(s.Name, s.Path)
}
wg.Wait()
```

Two things to watch:

1. **Distinct state files.** `FileStore` is single-writer per path; two
   Runners pointing at the same file silently clobber each other's
   snapshots.
2. **Distinct loggers** if you care about which spec emitted a line.
   The runner does not prefix its own log lines with a spec id.

---

## 11. Errors

`Drain` returns `nil` on the normal "iteration completed, see you next
tick" outcome. Non-nil returns by source:

- **Document-level**: `Runner.Doc is required`, `Runner.Sink is
  required` — both surface immediately at the top of `Drain`.
- **Context cancelled** — `ctx.Err()` is returned. SIGINT / SIGTERM
  under the CLI's signal handler causes a graceful exit (the CLI
  swallows the context error and exits `0`).
- **`MaxPages` hit** — the pagination loop ran for `MaxPages`
  iterations without termination. Usually means a buggy server that
  keeps returning the same cursor token. The deferred `Save` still
  fires.
- **HTTP non-success** — when `error.mode: fail` is active and a
  status is not named in `on_status:`, OR when `on_status: <code>:
  fail` fires explicitly.
- **Network / decode failures** — DNS, connection refused, TLS
  handshake, response body decode. Dispatched through `error.mode`
  (transport-level errors bypass `on_status:`).

`error.mode: warn` logs the failure but returns `nil` from `Drain` —
events not produced this drain are gone, but the pagination loop ends
gracefully and the deferred `Save` runs. `error.mode: fail` returns
the underlying error from `Drain` and still persists state via the
deferred `Save`. Both modes are safe in continuous polling — the CLI's
`--interval` loop logs the error and retries on the next tick.

For per-step errors that should NOT abort the drain — 304 Not
Modified, 429 with `ignore_api_errors`, 401 against a stale cache —
use `requests[].on_status` (see [§3.6](#36-add-explicit-error-handling))
rather than `error.mode`. `on_status` takes precedence over
`error.mode` for the statuses it lists.

### Operator-facing diagnostics

`skopos validate` (and `skopos run` during preflight validation)
emits diagnostics in the form:

```
state.last_timestamp: error: undeclared state field (spec.yml:42:5)
```

The path prefix (`state.last_timestamp` here) names the spec location;
`error` / `warning` is the severity; the trailing `(file:line:col)` is
the source position. Validation diagnostics are structural — see
[`schema.md` §design rules](schema.md#design-rules) for the rule set.

Field-by-field error message details live in [`schema.md`](schema.md);
the catalogue of supported API shapes (and the recipes for combining
them) is in [`api-methods.md`](api-methods.md).
