# AGENTS.md

Guidance for agents (and humans) working in this repository.

Skopos is a pre-1.0 Go module (`github.com/p1llus/skopos`) that turns a
YAML spec into a working HTTP-pull agent. The spec *is* the program —
[schema](schema/) parses and validates it into `*schema.Doc`,
[client](client/) interprets that IR in process.

Read [README.md](README.md) first if you are new to the project.

---

## House rules

These rules are absolute. They apply to every file you touch, every PR
description, every code comment, every doc, and every issue you create.

### 1. Documentation describes the *current* state, nothing else

`README.md`, `docs/*.md`, `doc.go`, package comments, type/function
docstrings, and normal GitHub issues describe what exists today. They
do not contain:

- "deferred", "out of scope", "future work", "TODO", "not yet
  implemented", "open question", "next phase", "next slice"
- Pointers to features that are not in the code at HEAD
- Roadmap framing of any kind

Forward-looking material belongs in one of these places — *only* these:

- A `PLAN.md` (or similar) created for that planning effort
- A GitHub issue explicitly tagged as a feature/planning tracker

Parts of the existing tree may still violate this rule. That is not
permission to copy the pattern — fix it if you are already editing the
file; otherwise leave it.

### 2. Comments and docstrings describe the code, not the project history

Code comments, `doc.go`, docstrings, and any text the Go toolchain
surfaces on pkg.go.dev must:

- Describe what the code *is and does* — never what it used to do, why
  the slice exists, which plan it came from, or what is deferred.
- Not link to `PLAN.md`, `docs/planning/IMPL-XX`, work-slice numbers,
  or any document that will go stale within a week.
- Follow the pkg.go.dev contract enforced by `.golangci.yml`
  (`staticcheck` ST1000/ST1020/ST1021, `revive` `exported`): every
  package has a `// Package <name>` comment in `doc.go`; every
  exported identifier's doc starts with the identifier name.
- Be added where a future reader needs the *why* — a non-obvious
  invariant, a subtle constraint, a deliberate deviation. Not on
  every function.

Linking to `docs/runtime.md §N` from inside a long-form `doc.go`
narrative is acceptable when the doc is the authoritative behavioural
spec — but never link to planning docs, slice files, or PR
descriptions.

### 3. Tests: golden integration tests first, unit tests sparingly

Integration coverage lives in [cmd/skopos/testdata/](cmd/skopos/testdata/)
as [go-internal/testscript](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript)
fixtures driven by [cmd/skopos/integration_test.go](cmd/skopos/integration_test.go)
(behind `//go:build integration`). They drive the real `skopos` CLI
against a real `httptest` server backed by
[internal/testserver](internal/testserver/), so one fixture exercises
cmd → runner → schema → HTTP end-to-end.

Before adding a unit test, check whether an existing golden already
covers the path. If it does, no new test is needed. When you do add
tests:

- Prefer extending or adding a golden in
  [cmd/skopos/testdata/](cmd/skopos/testdata/) or a schema fixture in
  [schema/testdata/](schema/testdata/).
- Keep `*_test.go` files lean. Cover the behaviour that matters,
  not every permutation. Quality over quantity.
- Regenerate goldens deliberately: `make integration-tests-update`
  (only after you have verified the new output by eye).

---

## Layout

| Path                                                       | What lives here                                                                          |
| ---------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| [schema/](schema/)                                         | Data-only IR types, `Load` / `Parse` / `Validate`. No runtime imports.                   |
| [client/](client/)                                         | In-process interpreter for `*schema.Doc`. `Runner`, `Sink`, `Store`, trace, redaction.   |
| [cmd/skopos/](cmd/skopos/)                                 | CLI binary: `validate`, `run`, `init`, `template`. Integration tests live alongside.     |
| [cmd/testserver/](cmd/testserver/)                         | Stand-alone dev stub server (`go run ./cmd/testserver`).                                 |
| [internal/testserver/](internal/testserver/)               | Shared HTTP scenarios used by both the dev server and the integration tests.             |
| [templates/](templates/)                                   | Bundled YAML spec templates, embedded via `embed.FS`.                                    |
| [tools/gen-schema-doc/](tools/gen-schema-doc/)             | Generator that produces [docs/schema-reference.md](docs/schema-reference.md) from types. |
| [docs/](docs/)                                             | Human-facing docs (see the doc map below).                                               |
| [docs/planning/](docs/planning/)                           | Historical implementation plans. **Not** referenced from code or other docs.             |

### Doc map (when you need to look something up)

- Spec field reference → [docs/schema.md](docs/schema.md)
- Auto-generated per-field reference → [docs/schema-reference.md](docs/schema-reference.md)
- Runtime contract (drain lifecycle, redaction, errors) → [docs/runtime.md](docs/runtime.md)
- API patterns catalogue → [docs/api-methods.md](docs/api-methods.md)
- Embedding guide → [docs/usage.md](docs/usage.md)
- State stores → [docs/stores.md](docs/stores.md)

---

## Workflow

### Build / lint / test

```sh
make check                     # vet + build + test + lint (fast local loop)
make ci                        # same with -race -count=1
make integration-tests         # testscript goldens (build tag: integration)
make integration-tests-update  # regenerate goldens — review the diff
make schema-doc                # regenerate docs/schema-reference.md
make schema-doc-check          # drift gate; CI runs this
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs
`make schema-doc-check` and `make ci`. The lint config is
[.golangci.yml](.golangci.yml).

### When you change schema types

1. Update the types in [schema/](schema/).
2. Update [schema/validate.go](schema/validate.go) if validation rules change.
3. Run `make schema-doc` and commit the regenerated
   [docs/schema-reference.md](docs/schema-reference.md). CI will fail
   on drift if you skip this.
4. If user-facing field semantics changed, also update
   [docs/schema.md](docs/schema.md) by hand.
5. Run `make integration-tests` — touch goldens only with
   `make integration-tests-update` after eyeballing the diff.

### When you change a template

1. Edit the file in [templates/](templates/).
2. The matching golden in [cmd/skopos/testdata/](cmd/skopos/testdata/)
   loads the template live via `skopos template show`, so it picks up
   your edit automatically. Run `make integration-tests`; update with
   `make integration-tests-update` if the new output is correct.

### When you change runtime behaviour

1. Edit [client/](client/).
2. Re-read [docs/runtime.md](docs/runtime.md) and update it if the
   behavioural contract moved. (This is the one doc code may reference
   from `doc.go` narratives.)
3. Run `make integration-tests` and review every golden that shifted.

### File headers

Every Go file starts with `// SPDX-License-Identifier: Apache-2.0`
followed by a blank line and the package clause (or `//go:build` line
where applicable). Keep this consistent in new files.
