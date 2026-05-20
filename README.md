# Skopos

[![Go Reference](https://pkg.go.dev/badge/github.com/p1llus/skopos.svg)](https://pkg.go.dev/github.com/p1llus/skopos)
[![Release](https://img.shields.io/github/v/release/p1llus/skopos)](https://github.com/p1llus/skopos/releases)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**Declarative HTTP polling, as code.**

> [!WARNING]
> Skopos is experimental and pre-1.0. The spec, CLI flags, and Go API are
> expected to change in breaking ways between releases. Pin a version if you
> depend on it.

Skopos turns a YAML document describing an HTTP API — its authentication,
requests, and pagination — into a working pull-based ingest agent. The
spec *is* the program; the runner interprets it.

It's aimed at two kinds of users:

- **Operators and platform teams** who want HTTP polling defined
  declaratively alongside the rest of their infrastructure-as-code, in a
  format that's easy to read, review, and diff.
- **Application authors** who want to expose a simple, schema-validated
  interface that lets their end users configure polling against arbitrary
  third-party APIs — without writing per-vendor code.

Skopos ships as both a single static binary and a Go library, so you can
reach for whichever fits the use case — a CLI for operations, or an
embedded runner inside your own service.

## Overview

- **One spec, many API shapes.** Authentication modes include bearer,
  basic, API key, OAuth2 (client credentials and password grant), AWS
  SigV4, session cookies, and dispatched multi-mode auth. Pagination
  modes include
  cursor token, page number, offset, `Link` header, next-URL-in-body,
  scroll ID, and async submit/poll/fetch.
- **Stateful between runs.** Cursors and bookkeeping live in a pluggable
  store (in-memory, file, or your own implementation) and survive
  restarts.
- **CLI or library, same behaviour.** Use the `skopos` binary for
  operations, or import the runner as a Go package — both interpret the
  same spec.
- **Built to be operated.** Optional per-request tracing with automatic
  secret redaction, structured diagnostics from `skopos validate`, and a
  configurable cap on pages per drain.
- **Pure Go, no plugins.** A single binary or a single import path. No
  sidecars, no external services required.

## Install

<details>
<summary><b>CLI binary via <code>go install</code></b></summary>

Builds the latest released `skopos` into `$GOBIN`:

```sh
go install github.com/p1llus/skopos/cmd/skopos@latest
```

</details>

<details>
<summary><b>Go library</b></summary>

Add Skopos to your module and import the `client` and `schema` packages:

```sh
go get github.com/p1llus/skopos@latest
```

```go
import (
    "github.com/p1llus/skopos/client"
    "github.com/p1llus/skopos/schema"
)
```

See [Creating your own client](#creating-your-own-client) below for a
minimal embedding example.

</details>

<details>
<summary><b>Prebuilt archive + signature verification</b></summary>

Each release publishes per-OS archives, a `checksums.txt`, and a keyless
[cosign](https://github.com/sigstore/cosign) Sigstore bundle
(`checksums.txt.sigstore.json`) on
[GitHub Releases](https://github.com/p1llus/skopos/releases).

Archive names follow `skopos_<semver>_<os>_<arch>.<ext>` (semver has no
leading `v` in filenames; the Git tag does).

```sh
TAG=v0.1.0
VERS=${TAG#v}
OS_ARCH=linux_amd64   # or darwin_amd64, darwin_arm64, linux_arm64, windows_amd64

curl -fsSLO "https://github.com/p1llus/skopos/releases/download/${TAG}/checksums.txt"
curl -fsSLO "https://github.com/p1llus/skopos/releases/download/${TAG}/checksums.txt.sigstore.json"
curl -fsSLO "https://github.com/p1llus/skopos/releases/download/${TAG}/skopos_${VERS}_${OS_ARCH}.tar.gz"

cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/p1llus/skopos/.github/workflows/release.yml@refs/tags/${TAG}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt

sha256sum --check --ignore-missing checksums.txt
```

</details>

## Quickstart

The `skopos` binary comes with prebuilt templates to help get started

```sh
# 1. (Optional) Start the testserver in another terminal. It hosts a stub endpoint
#    for every bundled template and exits on Ctrl-C.
go run github.com/p1llus/skopos/cmd/testserver@latest

# 2. Browse the templates embedded in the binary, then print one out
#    as a starting spec. Default values point to the correct testserver endpoint
skopos template list
skopos template show bearer_simple > spec.yml

# 3. Validate the spec.
skopos validate -i spec.yml

# 4. Run once. Events are emitted as JSONL on stdout.
skopos run -i spec.yml --once
```

<details>
<summary>Example event output</summary>

```json
{"id":"evt-000001","seq_num":1,"timestamp":"2026-05-15T06:28:29.552249238Z"}
{"id":"evt-000002","seq_num":2,"timestamp":"2026-05-15T06:28:30.552249238Z"}
{"id":"evt-000003","seq_num":3,"timestamp":"2026-05-15T06:28:31.552249238Z"}
{"id":"evt-000004","seq_num":4,"timestamp":"2026-05-15T06:28:32.552249238Z"}
{"id":"evt-000005","seq_num":5,"timestamp":"2026-05-15T06:28:33.552249238Z"}
```

</details>

You can define a output file if you don't want events to appear in stdout, below example also enables tracing:

```sh
skopos run -i spec.yml --once --out events.jsonl --trace trace.jsonl
```

<details>
<summary>Override settings with configuration file</summary>

If you either want to quickly swap between different configurations or do not want to provide them in the CLI each time you run, you can run `skopos init -o config.yml`

```sh
skopos init -o config.yml
```

```yaml
# skopos run configuration
# All fields are optional. CLI flags take precedence over values set here.
# Omit a field to use the built-in default.

# input: path to the spec file to run. Equivalent to -i / --input.
# input: spec.yml

# state: path to the JSON state file for persisting the cursor between runs.
# Equivalent to --state. Omit to use an in-memory store (state lost on exit).
# state: state.json

# out: path to write JSONL events to. Equivalent to --out.
# Defaults to stdout when omitted.
# out: events.jsonl

# trace: path to write per-exchange JSONL trace records to.
# Equivalent to --trace. Trace is disabled when omitted.
# trace: trace.jsonl

# once: run a single drain and exit. Equivalent to --once.
# once: false

# interval: sleep this duration between drains in continuous mode.
# Equivalent to --interval. Omit (or set to 0) for one-shot mode.
# interval: 30s

# http_timeout: per-request HTTP timeout. Defaults to 30s when omitted.
# http_timeout: 30s

# max_pages: cap on the number of pagination iterations per drain.
# Defaults to 10000 when omitted.
# max_pages: 10000

```

</details>

<details>
<summary>Example trace output</summary>

`trace.jsonl` carries one record per HTTP exchange, with secrets
redacted:

```json
{"iteration":1,"method":"GET","url":"http://localhost:9999/bearer_simple/events","query":{"limit":"<format:string>","since":"<ref state.last_timestamp>"},"request_headers":{"Accept":"application/json, application/x-ndjson;q=0.9, */*;q=0.1","Authorization":"<redacted>"},"status":200,"response_body":"body 403 bytes, object-like","started_at":"2026-05-15T06:28:47.777479114Z","elapsed":16011226}
```

</details>

Swap `bearer_simple` for any other template name to try a different API
shape against the same testserver. Run `skopos --help` (or
`skopos <command> --help`) for the full list of commands and flags.

## Templates

Bundled templates cover every authentication and pagination pattern the
runner supports today. List them with `skopos template list`, then print
one to a file as a starting point:

```sh
skopos template show bearer_simple > spec.yml
```

The full template source lives in [`templates/`](templates) — that's the
easiest place to browse the patterns side-by-side.

## Creating your own client

To embed Skopos in your own Go program — for example to route events
into a database or message queue, or to persist cursors somewhere other
than a local file — the runner is exposed as a library. Bring your own
implementations of `Sink` (where events go) and `Store` (where cursors
live):

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
        Store: client.NewFileStore("state.json"), // or your own Store
        Sink:  client.NewJSONLSink(os.Stdout),    // or your own Sink
    }
    if err := runner.Drain(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

For scheduling, custom HTTP clients, sharing state across goroutines,
and writing your own sinks or stores, see
[`docs/usage.md`](docs/usage.md).

## Documentation

| Doc                                                              | What's in it                                                  |
| ---------------------------------------------------------------- | ------------------------------------------------------------- |
| [Spec reference](docs/schema.md)                                 | Every field in the YAML spec, with examples                   |
| [Runtime contract](docs/runtime.md)                              | How the runner interprets a spec, end to end                  |
| [API patterns](docs/api-methods.md)                              | Catalogue of API shapes Skopos can speak to                   |
| [Embedding guide](docs/usage.md)                                 | Using Skopos as a Go library inside your own service          |
| [State stores](docs/stores.md)                                   | Plugging in a custom cursor store (SQLite, BoltDB, …)         |
| [More examples](cmd/skopos/testdata)                             | Golden-file fixtures, one per bundled template                |
| [Go API reference](https://pkg.go.dev/github.com/p1llus/skopos)  | Generated package docs on pkg.go.dev                          |

## License

Skopos is licensed under the Apache License, Version 2.0. See
[`LICENSE`](LICENSE) for the full text.

```
SPDX-License-Identifier: Apache-2.0
```
