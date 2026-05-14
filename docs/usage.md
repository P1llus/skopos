# Embedding the runner

This guide covers using `github.com/p1llus/skopos/client` from
your own Go program. For the YAML spec the runner consumes see
[`schema.md`](schema.md); for the runtime contract see
[`runtime.md`](runtime.md).

## Contents

- [Two-step lifecycle](#two-step-lifecycle)
- [The Runner struct](#the-runner-struct)
- [Sinks: receiving events](#sinks-receiving-events)
- [Stores: persisting state](#stores-persisting-state)
- [Custom HTTP client / transport](#custom-http-client--transport)
- [Tracing](#tracing)
- [Scheduling: one-shot vs interval](#scheduling-one-shot-vs-interval)
- [Concurrency model](#concurrency-model)
- [Multiple specs in one process](#multiple-specs-in-one-process)
- [Errors](#errors)

## Two-step lifecycle

A spec is loaded once and drained many times:

```go
doc, err := schema.Load(reader)                  // parse YAML or JSON
if err != nil { ... }
if diags := schema.Validate(doc); len(diags) > 0 {
    // diags has Severity, Path, Message, Hint
    return fmt.Errorf("invalid spec: %v", diags)
}

r := &client.Runner{Doc: doc, Sink: sink}  // construct once
for {
    if err := r.Drain(ctx); err != nil { ... }
    time.Sleep(interval)
}
```

`schema.Load(io.Reader)` and `schema.Parse([]byte)` both auto-detect YAML or JSON.
`schema.Validate` returns diagnostics — call it before constructing a Runner
because the runner assumes the document is structurally valid.

## The Runner struct

`client.Runner` is a struct, not a constructor. Set the fields you care
about; defaults cover the rest.

| Field      | Required | Default                  | Notes                                                                 |
|------------|----------|--------------------------|-----------------------------------------------------------------------|
| `Doc`      | yes      | —                        | `*schema.Doc`. One Runner = one logical pull source.                      |
| `Sink`     | yes      | —                        | `Sink` interface. Bundled: `JSONLSink`.                                |
| `Store`    | no       | `&MemoryStore{}`         | Persists state between drains. Bundled: `MemoryStore`, `FileStore`.   |
| `Client`   | no       | 30s-timeout `*http.Client` | Plug in for proxy / TLS config / different timeout.                  |
| `Logger`   | no       | `log.Default()`          | Operational diagnostics (skipped steps, retried requests).            |
| `Tracer`   | no       | nil                      | Optional per-exchange capture. Bundled: `JSONLTracer`.                |
| `MaxPages` | no       | 10 000                   | Per-drain iteration cap. `< 0` disables (tests only).                  |
| `Now`      | no       | `time.Now`               | Clock; useful for tests.                                              |

`r.Drain(ctx)` runs one full pull session: paginate until `want_more=false`
(or until an `async_job` phase machine hits a wait state), persist via
`Store.Save`, return. The Runner does NOT loop — the caller decides whether
to sleep between drains.

## Sinks: receiving events

```go
type Sink interface {
    Emit(event any) error
    Flush() error
}
```

The runner does not buffer or batch: `Emit` is called once per event, in
the order they appear in the producer step's body. `event` is the decoded
value at `response.events_at` — typically `map[string]any` (or
`[]map[string]any` for an NDJSON producer).

`Flush` is called once at the end of `Drain` so file-backed sinks can sync
to disk.

The bundled `JSONLSink` writes one JSON line per event to any `io.Writer`
and is safe for concurrent `Emit` calls (mutex-guarded):

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

## Stores: persisting state

```go
type Store interface {
    Load() (Snapshot, error)
    Save(Snapshot) error
}

type Snapshot struct {
    State  map[string]any
    Cursor map[string]any
}
```

Bundled implementations:

- `MemoryStore` — in-memory; lost when the process exits. Default when
  `Runner.Store` is nil.
- `FileStore` — single JSON file, atomic writes (temp file + rename),
  single-writer per path. `client.NewFileStore("state.json")`.

For SQLite, BoltDB, or any other backing store, implement the two-method
`Store` interface. See [`stores.md`](stores.md) for a worked example and
the concurrency contract.

## Custom HTTP client / transport

The runner uses `r.Client` for every request, including OAuth2 token
fetches. To install a proxy, custom TLS config, or different per-request
timeout:

```go
r.Client = &http.Client{
    Timeout: 60 * time.Second,
    Transport: &http.Transport{
        Proxy: http.ProxyFromEnvironment,
        TLSClientConfig: tlsConfig,
    },
}
```

> Always set `Client.Timeout` to a finite value. `http.DefaultClient` has no
> timeout, and a hung server otherwise wedges `Drain` until `ctx` is
> cancelled.

## Tracing

`Runner.Tracer` receives one `Exchange` per HTTP request/response pair —
useful for debugging and audit. The trace is redaction-safe by default:

- URL paths preserved, query/userinfo stripped.
- Headers preserved, `Authorization`, `Cookie`, `Proxy-Authorization`, and
  every auth-block-declared header value replaced with `<redacted>`.
- Query values typed as `secret` replaced with `<redacted>`.
- Request and response bodies emit length + content classification
  (`body 42 bytes, json-like`); raw bytes never appear.

```go
tf, _ := os.OpenFile("trace.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
defer tf.Close()
jt := client.NewJSONLTracer(tf)
defer jt.Flush()

r.Tracer = jt
```

Custom `Tracer` implementations can route the same `Exchange` records to
your own pipeline. Do NOT log `schema.Value` content directly — see
[`CONTRIBUTING.md`](../CONTRIBUTING.md) "Runner doc strings" for the
`redactValue` policy.

## Scheduling: one-shot vs interval

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
page is a successful drain. The cursor still advances per the configured
`progress` strategy.

## Concurrency model

One Runner is one logical pull source. `Drain` mutates internal scope
state (cursor, extract, steps); calling `Drain` concurrently on the same
Runner WILL race. Don't do it.

Fan-out across N pull sources is your responsibility: build N Runner
values (each with its own `Doc`, `Store`, `Sink`) and call `Drain` from N
goroutines. The Runner type holds no package-global state, so independent
Runners are independent.

A `Sink` or `Store` shared between Runners MUST be safe for concurrent
calls. Bundled `JSONLSink` and `MemoryStore` are safe; `FileStore` is
safe for in-process sharing but its single-writer-per-path contract still
applies.

## Multiple specs in one process

A common pattern: run several specs from the same binary, each on its own
schedule.

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
2. **Distinct loggers** if you care about which spec emitted a line. The
   runner does not prefix its own log lines with a spec id.

## Errors

`Drain` returns `nil` on the normal "iteration completed, see you next
tick" outcome. Non-nil returns by source:

- IR-level: `Runner.Doc is required`, `Runner.Sink is required`.
- Context cancelled (`ctx.Err()`).
- Iteration cap hit (`errMaxPagesExceeded` — usually a buggy server
  returning the same cursor token forever).
- HTTP non-success when `error.mode: fail` is active, or when a
  `requests[].on_status[<code>]: fail` fires.
- Network / decode failures dispatched through `error.mode: fail`.

`error.mode: warn` logs the failure but returns `nil` from `Drain` —
events not produced this drain are gone, but the cursor advances. The
deferred `Store.Save` runs in all three paths (success, warn, fail) so a
partial drain doesn't lose progress.
