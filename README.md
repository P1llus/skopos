# Skopos

**Declarative HTTP-pull integrations for Go.**

Skopos turns a YAML document into a working ingest agent. You describe
an API — authentication, requests, pagination, how its cursor advances
— and the in-process runner drains events out of it and persists
progress across runs. No code generation, no scheduler to wire up, no
sidecar: the spec document *is* the program, the runner interprets it.

```
                ┌─────────────────┐
   spec.yml ──►│                 │──► events
                │  skopos runner  │
   state.json ◄►│                 │──► trace (optional)
                └─────────────────┘
                         │
                         ▼
                    HTTP API
```

## Highlights

- **One spec, many APIs.** Bearer, basic, API-key, OAuth2
  client-credentials, session-cookie, and dispatched multi-mode auth;
  cursor-token, page-number, offset, Link-header, next-URL-in-body,
  scroll-ID, and async submit/poll pagination.
- **Stateful by default.** Cursor and bookkeeping live in pluggable
  stores (in-memory, file, or your own) and survive process restarts.
- **CLI or library.** Use the `skopos` binary for operations, or import
  the runner as a Go package — same behaviour, same spec.
- **Operable.** Optional per-exchange tracing with built-in redaction
  for secrets, structured diagnostics from `skopos validate`, and a
  guardrail on per-drain pagination.
- **Pure Go, no plugins.** A single binary (or a single import path)
  with no required external services.

## Install

**As a CLI** — `go install` fetches a prebuilt-from-source binary into `$GOBIN`:

```sh
go install github.com/p1llus/skopos/cmd/skopos@latest
```

**As a Go library** — add it to your module and import `client` / `schema`:

```sh
go get github.com/p1llus/skopos@latest
```

```go
import (
    "github.com/p1llus/skopos/client"
    "github.com/p1llus/skopos/schema"
)
```

**From source** — clone and build:

```sh
git clone https://github.com/p1llus/skopos
cd skopos
go build ./cmd/skopos
```

**Verifying a release** — every release artifact is signed with [cosign](https://github.com/sigstore/cosign) keyless signing via GitHub Actions. To verify a downloaded binary:

```sh
# Download the archive, checksum file, signature, and certificate from the release page, then:
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity "https://github.com/p1llus/skopos/.github/workflows/release.yml@refs/tags/VERSION" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com"

# Then verify your archive against the checksum:
sha256sum --check --ignore-missing checksums.txt
```

## Quickstart

No repo clone needed. The binary embeds all templates. The fastest path from
zero to running events:

```sh
# 1. Grab a starter template (bearer token, single endpoint)
skopos template show bearer_simple > spec.yml

# 2. (Optional) generate a config file with all defaults documented
skopos init -o config.yml
# edit config.yml to set your API key, URL, interval, state file, …

# 3. Validate the spec
skopos validate -i spec.yml

# 4. Run once
skopos run -c config.yml -i spec.yml --once

# 5. Or poll continuously
skopos run -c config.yml -i spec.yml --interval 30s
```

`skopos run` writes one JSON event per line to stdout. Common flags:

| Flag                    | Effect                                              |
| ----------------------- | --------------------------------------------------- |
| `-c config.yml`        | Load defaults from a YAML config file               |
| `--state s.json`        | Persist the cursor between runs                     |
| `--interval 30s`        | Poll continuously until SIGINT                      |
| `--out events.jsonl`    | Write events to a file instead of stdout            |
| `--trace trace.jsonl`   | Record every HTTP exchange (with secret redaction)  |
| `--http-timeout 45s`    | Per-request HTTP timeout (default 30s)              |
| `--max-pages 500`       | Pagination cap per drain (default 10 000)           |

## Templates

The [`templates/`](templates) directory contains one spec per pattern
the runner supports today. Every template is also embedded in the binary — use
`skopos template list` to browse and `skopos template show <name>` to print one.

| API shape                                          | Template                                                                          |
| -------------------------------------------------- | --------------------------------------------------------------------------------- |
| Bearer token, no pagination                        | [`bearer_simple.yml`](templates/bearer_simple.yml)                              |
| API key in header, time-window cursor              | [`api_key_auth.yml`](templates/api_key_auth.yml)                                |
| OAuth2 client-credentials with cached access token | [`oauth2_client_credentials.yml`](templates/oauth2_client_credentials.yml)      |
| Multi-mode auth dispatched on a state flag         | [`multi_mode_auth.yml`](templates/multi_mode_auth.yml)                          |
| `cursor_token` pagination                          | [`cursor_token.yml`](templates/cursor_token.yml)                                |
| `page_number` pagination + `has_more_at`           | [`page_number.yml`](templates/page_number.yml)                                  |
| `offset` pagination                                | [`offset_pagination.yml`](templates/offset_pagination.yml)                      |
| Link-header pagination (RFC 5988)                  | [`link_header.yml`](templates/link_header.yml)                                  |
| Next-URL-in-body pagination                        | [`next_url_in_body.yml`](templates/next_url_in_body.yml)                        |
| `scroll_id` session                                | [`scroll_id.yml`](templates/scroll_id.yml)                                      |
| NDJSON response decode                             | [`ndjson_response.yml`](templates/ndjson_response.yml)                          |
| POST with JSON body                                | [`post_json_body.yml`](templates/post_json_body.yml)                            |
| Async submit / poll / fetch                        | [`async_poll.yml`](templates/async_poll.yml)                                    |
| Session cookie via POST login                      | [`session_cookie.yml`](templates/session_cookie.yml)                            |

For the canonical catalogue of API shapes and the schema knobs that
express each one, see [`docs/api-methods.md`](docs/api-methods.md).
Internal matrix-coverage fixtures live under
[`schema/testdata/`](schema/testdata).

## Embedding in Go

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/p1llus/skopos/client"
    "github.com/p1llus/skopos/schema"
)

func main() {
    f, err := os.Open("spec.yml")
    if err != nil {
        log.Fatal(err)
    }
    defer f.Close()

    doc, err := schema.Load(f)
    if err != nil {
        log.Fatal(err)
    }
    if diags := schema.Validate(doc); len(diags) > 0 {
        log.Fatalf("invalid spec: %v", diags)
    }

    runner := &client.Runner{
        Doc:   doc,
        Store: client.NewFileStore("state.json"),
        Sink:  client.NewJSONLSink(os.Stdout),
    }
    if err := runner.Drain(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

See [`docs/usage.md`](docs/usage.md) for the full embedding guide —
scheduling, custom HTTP clients, sharing state across goroutines,
sinks, and tracing.

## Documentation

| Doc                                                       | When to read it                                  |
| ----------------------------------------------------------| -------------------------------------------------|
| [`docs/schema.md`](docs/schema.md)                        | Per-field reference for the YAML spec            |
| [`docs/runtime.md`](docs/runtime.md)                      | Runtime contract and supported-variant table     |
| [`docs/api-methods.md`](docs/api-methods.md)              | Vendor-neutral catalogue of API patterns         |
| [`docs/usage.md`](docs/usage.md)                          | Embed the runner in your own Go program          |
| [`docs/stores.md`](docs/stores.md)                        | Plug in a custom state store (SQLite, BoltDB, …) |
| [`Go API reference`](https://pkg.go.dev/github.com/p1llus/skopos) | Go API reference                         |

## License

Skopos is licensed under the Apache License, Version 2.0. See
[`LICENSE`](LICENSE) for the full text.

```
SPDX-License-Identifier: Apache-2.0
```
