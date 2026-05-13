# Skopos

Skopos is an in-process HTTP-pull runner. You hand it a YAML document
describing an API (auth, requests, pagination, how the cursor advances), it
drains events out and persists state across runs. No code generation, no
agent — the spec document is the program, the runner interprets it.

```
                ┌─────────────────┐
   spec.yaml ──►│                 │──► events (JSONL on stdout)
                │  skopos runner  │
state.json ◄──►│                 │──► trace (optional, JSONL)
                └─────────────────┘
                        │
                        ▼
                  HTTP API
```

## Status

Pre-1.0. The runner covers the variants listed in
[`docs/runtime.md`](docs/runtime.md) §8.

## Install

```sh
go install github.com/p1llus/skopos/cmd/skopos@latest
```

Or build from source:

```sh
git clone https://github.com/p1llus/skopos
cd skopos
go build ./cmd/skopos
```

## 5-minute trial

`examples/bearer_simple.yaml` polls a localhost endpoint with a
bearer token, paginates with `pagination: none`, and advances the cursor by
the max event timestamp:

```sh
skopos validate -i examples/bearer_simple.yaml
skopos run     -i examples/bearer_simple.yaml --once
```

`skopos run` writes one JSON event per line to stdout. Add `--state s.json`
to persist the cursor between runs, `--interval 30s` to poll continuously
until SIGINT, and `--trace trace.jsonl` to capture a redaction-safe record
of every HTTP exchange.

Browse [`examples/`](examples) for one fixture per supported pattern
(cursor_token, page_number, offset, link_header, scroll_id, async_job,
multi_mode auth, oauth2, etc.). Internal matrix-coverage fixtures live
under [`schema/testdata/`](schema/testdata).

## Common patterns

A starter map from "what shape is this API?" to "which example matches?":

| API shape                                                         | Example                                                                                          |
|-------------------------------------------------------------------|--------------------------------------------------------------------------------------------------|
| Bearer token, no pagination, max-event-timestamp cursor           | [`bearer_simple.yaml`](examples/bearer_simple.yaml)                                              |
| API key in header, time-window cursor                             | [`api_key_auth.yaml`](examples/api_key_auth.yaml)                                                |
| OAuth2 client-credentials with cached access token                | [`oauth2_client_credentials.yaml`](examples/oauth2_client_credentials.yaml)                      |
| `multi_mode` auth dispatched on a state flag                      | [`multi_mode_auth.yaml`](examples/multi_mode_auth.yaml)                                          |
| `cursor_token` pagination                                         | [`cursor_token.yaml`](examples/cursor_token.yaml)                                                |
| `page_number` pagination + `has_more_at`                          | [`page_number.yaml`](examples/page_number.yaml)                                                  |
| `offset` pagination                                               | [`offset_pagination.yaml`](examples/offset_pagination.yaml)                                      |
| Link-header pagination (RFC 5988)                                 | [`link_header.yaml`](examples/link_header.yaml)                                                  |
| Next-URL-in-body pagination                                       | [`next_url_in_body.yaml`](examples/next_url_in_body.yaml)                                        |
| `scroll_id` session                                               | [`scroll_id.yaml`](examples/scroll_id.yaml)                                                      |
| NDJSON response decode                                            | [`ndjson_response.yaml`](examples/ndjson_response.yaml)                                          |
| POST with JSON body                                               | [`post_json_body.yaml`](examples/post_json_body.yaml)                                            |
| Async submit / poll / fetch                                       | [`async_poll.yaml`](examples/async_poll.yaml)                                                    |
| Session cookie via POST login                                     | [`session_cookie.yaml`](examples/session_cookie.yaml)                                            |

For the canonical catalogue of API shapes and the IR knobs that
express each one see [`docs/api-methods.md`](docs/api-methods.md).

## Embedding in Go

```go
import (
    "context"
    "os"

    "github.com/p1llus/skopos/client"
    "github.com/p1llus/skopos/schema"
)

func main() {
    f, _ := os.Open("spec.yaml")
    defer f.Close()

    doc, err := schema.Load(f)
    if err != nil { panic(err) }
    if diags := schema.Validate(doc); len(diags) > 0 {
        panic(diags)
    }

    r := &client.Runner{
        Doc:   doc,
        Store: client.NewFileStore("state.json"),
        Sink:  client.NewJSONLSink(os.Stdout),
    }
    if err := r.Drain(context.Background()); err != nil {
        panic(err)
    }
}
```

See [`docs/usage.md`](docs/usage.md) for the full embedding guide:
scheduling, custom HTTP clients, sharing state across goroutines, sinks,
and tracing.

## Docs

| Doc                                          | When to read it                                  |
|----------------------------------------------|--------------------------------------------------|
| [`docs/schema.md`](docs/schema.md)           | Per-field reference for the YAML spec            |
| [`docs/runtime.md`](docs/runtime.md)         | Runtime contract + supported-variant table       |
| [`docs/api-methods.md`](docs/api-methods.md) | Vendor-neutral catalogue of API patterns         |
| [`docs/usage.md`](docs/usage.md)             | Embed the runner in your own Go program          |
| [`docs/stores.md`](docs/stores.md)           | Plug in a custom state store (SQLite, BoltDB, …) |

