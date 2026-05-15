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

**Verifying a release** — the release workflow signs `checksums.txt` with keyless [cosign](https://github.com/sigstore/cosign) (Sigstore bundle: `checksums.txt.sigstore.json`). GoReleaser publishes the archives, `checksums.txt`, and that bundle together on GitHub Releases.

Use the Git tag exactly as tagged (including the leading `v`). Archive names follow `skopos_<semver>_<os>_<arch>.<ext>` plus `checksums.txt` and `checksums.txt.sigstore.json` (semver has no leading `v` in the filenames).

```sh
TAG=v0.1.0
VERS=${TAG#v}
OS_ARCH=linux_amd64   # or darwin_amd64, darwin_arm64, linux_arm64 (see Assets on the release)

curl -fsSLO "https://github.com/p1llus/skopos/releases/download/${TAG}/checksums.txt"
curl -fsSLO "https://github.com/p1llus/skopos/releases/download/${TAG}/checksums.txt.sigstore.json"
curl -fsSLO "https://github.com/p1llus/skopos/releases/download/${TAG}/skopos_${VERS}_${OS_ARCH}.tar.gz"
# Windows: skopos_${VERS}_${OS_ARCH}.zip (no arm64 zip)

cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/p1llus/skopos/.github/workflows/release.yml@refs/tags/${TAG}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt

sha256sum --check --ignore-missing checksums.txt
```

## Quickstart

No repo clone needed. The `skopos` binary embeds every template, and the
testserver runs straight from the module path. Every template's URL
defaults already point at `http://localhost:9999`, so no flags or edits
are required to drive any of them against the testserver.

```sh
# 1. (Optional) generate a fully commented config file. Skip this if you
#    don't need to override any defaults — every flag has a sensible one.
skopos init -o config.yml

# 2. Start the testserver in another terminal. It hosts a stub endpoint
#    for every bundled template on :9999 and exits on Ctrl-C, so there's
#    nothing to install permanently.
go run github.com/p1llus/skopos/cmd/testserver@latest

# 3. Grab a starter spec (bearer token, single GET) and validate it.
skopos template show bearer_simple > spec.yml
skopos validate -i spec.yml

# 4. Run once. Events are emitted as JSONL on stdout.
skopos run -i spec.yml --once
```

```json
{"id":"evt-000001","seq_num":1,"timestamp":"2026-05-15T06:28:29.552249238Z"}
{"id":"evt-000002","seq_num":2,"timestamp":"2026-05-15T06:28:30.552249238Z"}
{"id":"evt-000003","seq_num":3,"timestamp":"2026-05-15T06:28:31.552249238Z"}
{"id":"evt-000004","seq_num":4,"timestamp":"2026-05-15T06:28:32.552249238Z"}
{"id":"evt-000005","seq_num":5,"timestamp":"2026-05-15T06:28:33.552249238Z"}
```

```sh
# 5. Run again, this time tee-ing events to a file and recording each
#    HTTP exchange to a redacted trace.
skopos run -i spec.yml --once --out events.jsonl --trace trace.jsonl
```

`events.jsonl` is the same JSONL stream as above. `trace.jsonl` carries
one record per HTTP exchange with secrets redacted:

```json
{"iteration":1,"method":"GET","url":"http://localhost:9999/bearer_simple/events","query":{"limit":"<format:string>","since":"<from_progress:latest_timestamp>"},"request_headers":{"Accept":"application/json, application/x-ndjson;q=0.9, */*;q=0.1","Authorization":"<redacted>"},"status":200,"response_body":"body 403 bytes, object-like","started_at":"2026-05-15T06:28:47.777479114Z","elapsed":16011226}
```

Swap `bearer_simple` for any other template name to try a different API
shape against the same testserver. Common flags:

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

Every template points at `http://localhost:9999` with paths that match
[`cmd/testserver`](cmd/testserver). Run the testserver and any template
runs end-to-end with no further configuration.

| API shape                                               | Template                                                                          |
| ------------------------------------------------------- | --------------------------------------------------------------------------------- |
| Bearer token, no pagination                             | [`bearer_simple.yml`](templates/bearer_simple.yml)                              |
| HTTP Basic auth                                         | [`basic_auth.yml`](templates/basic_auth.yml)                                    |
| API key in header, time-window cursor                   | [`api_key_auth.yml`](templates/api_key_auth.yml)                                |
| Operator-defined auth headers                           | [`custom_auth.yml`](templates/custom_auth.yml)                                  |
| Multi-mode auth dispatched on a state flag              | [`multi_mode_auth.yml`](templates/multi_mode_auth.yml)                          |
| OAuth2 client-credentials with cached access token      | [`oauth2_client_credentials.yml`](templates/oauth2_client_credentials.yml)      |
| OAuth2 password grant with cached access token          | [`oauth2_password_grant.yml`](templates/oauth2_password_grant.yml)              |
| OAuth2 + GraphQL Relay-style cursor pagination          | [`oauth2_relay.yml`](templates/oauth2_relay.yml)                                |
| Session cookie via POST login                           | [`session_cookie.yml`](templates/session_cookie.yml)                            |
| Cached JSON login with a per-step token cache           | [`session_login_cached.yml`](templates/session_login_cached.yml)                |
| Minimal GET, no auth, no pagination                     | [`simple_get_object.yml`](templates/simple_get_object.yml)                      |
| `cursor_token` pagination                               | [`cursor_token.yml`](templates/cursor_token.yml)                                |
| `page_number` pagination + `has_more_at`                | [`page_number.yml`](templates/page_number.yml)                                  |
| `offset` pagination                                     | [`offset_pagination.yml`](templates/offset_pagination.yml)                      |
| Link-header pagination (RFC 5988)                       | [`link_header.yml`](templates/link_header.yml)                                  |
| Next-URL-in-body pagination                             | [`next_url_in_body.yml`](templates/next_url_in_body.yml)                        |
| `scroll_id` session                                     | [`scroll_id.yml`](templates/scroll_id.yml)                                      |
| Async submit / poll / fetch (clock-driven cursor)       | [`async_poll.yml`](templates/async_poll.yml)                                    |
| Async submit / poll / fetch (timestamp-driven cursor)   | [`async_poll_latest_ts.yml`](templates/async_poll_latest_ts.yml)                |
| Async submit / poll / fetch (no cursor advance)         | [`async_poll_stateless.yml`](templates/async_poll_stateless.yml)                |
| ETag-driven conditional GET (304 skip)                  | [`etag_conditional.yml`](templates/etag_conditional.yml)                        |
| ETag-driven conditional middle step in a 3-step chain   | [`etag_conditional_middle.yml`](templates/etag_conditional_middle.yml)          |
| NDJSON response decode                                  | [`ndjson_response.yml`](templates/ndjson_response.yml)                          |
| POST with JSON body                                     | [`post_json_body.yml`](templates/post_json_body.yml)                            |
| POST with form-urlencoded body                          | [`post_form_body.yml`](templates/post_form_body.yml)                            |
| POST with a raw (non-JSON) body                         | [`post_raw_body.yml`](templates/post_raw_body.yml)                              |

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


## More examples

More examples can be found by the generated golden files for each template in the [`cmd/skopos/testdata`](cmd/skopos/testdata) directory.

## License

Skopos is licensed under the Apache License, Version 2.0. See
[`LICENSE`](LICENSE) for the full text.

```
SPDX-License-Identifier: Apache-2.0
```
