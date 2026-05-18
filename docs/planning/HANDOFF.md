# Handoff — Phase 2 Slice 15 (`cmd/skopos/*`)

Slice 14 closed (`client/{sink,filestore,redact,trace,doc}.go`
rewritten against the new IR). The five-file sweep:

- `client/doc.go` package preamble rewritten: drain lifecycle per
  `runner.go`'s top comment, namespace lifetimes (state / cache /
  events / extract / steps / response), concurrency contract,
  redaction pointer. No `async_job phase machine`, no `want_more`.
- `client/filestore.go` documents the on-disk shape (`{"state": {...}}`
  only) and the silent unknown-key drop on Load (global rule #1 — no
  migration code, Go's `json.Unmarshal` defaults handle a stale
  `"cursor":` key).
- `client/redact.go::valueShape` extended to recognise every Slice-5
  Value variant — `Add`, `Subtract`, `Max`, `Min`, `First`, `Last`,
  `Count`, `Regex`. `safeURL(nil)` now returns `""` (was the literal
  `"<nil-url>"`) so the new `omitempty` on `Exchange.URL` drops the
  field cleanly.
- `client/trace.go::Exchange` lost `Phase` (zero contributors); gained
  `CacheHit bool json:"cache_hit,omitempty"`. `Method` / `URL` /
  `StartedAt` / `Elapsed` carry `omitempty`. `buildExchange` detects
  the cache-HIT case (`t.method == "" && runErr == nil`) and returns
  a minimal tombstone `{iteration, step_id, cache_hit:true}`; the
  trailing `phase` param is named `_` to keep the frozen `runner.go`
  + `fanout.go` call sites compiling.
- `client/sink.go` was already post-redesign; no edits needed.

See [`IMPL-14-client-sink-trace.md`](IMPL-14-client-sink-trace.md).
`go build ./...` is green at slice close. Tests stay red until
Slice 17.

The next slice is **Slice 15 — `cmd/skopos/*`** (the CLI: `validate`,
`run`, `init`, `template list|show`).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2
   slice list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 15"](PHASE-2-PLAN.md#slice-15--cmdskopos).
   Your slice's detailed scope.
4. [`docs/usage.md`](../usage.md) §1 (CLI surface) — the source of
   truth for the four subcommands and the flag table. The CLI MUST
   match this doc.
5. [`docs/runtime.md`](../runtime.md) §2 (drain lifecycle) and §10
   (HTTP transport defaults) — the runtime knobs the `run` command
   exposes via flags (timeout, max-pages, interval).
6. [`client/runner.go`](../../client/runner.go) — the `Runner` struct
   the `run` command builds. Fields: `Doc`, `Store`, `Sink`, `Client`,
   `Now`, `Logger`, `MaxPages`, `Tracer`. Don't widen the surface;
   the CLI is the wiring layer, not a feature site.
7. [`IMPL-14-client-sink-trace.md` §"Notes for downstream
   slices" / Slice 15 sub-section](IMPL-14-client-sink-trace.md#slice-15--cmdskoposcmd_rungo).
   The cache-HIT JSONL line shape (`{iteration, step_id, cache_hit:true}`)
   may want a `--help` paragraph in the trace-file doc.

---

## Your slice

**Branch.** Develop on `claude/slice-15-cmd-cli-<token>`.

**Files you may touch.**

- `cmd/skopos/cmd_run.go`
- `cmd/skopos/cmd_validate.go`
- `cmd/skopos/cmd_init.go`
- `cmd/skopos/cmd_template.go`
- `cmd/skopos/config.go`
- `cmd/skopos/main.go`
- `cmd/skopos/io.go`
- `cmd/skopos/doc.go`

**Files you must NOT touch.**

- Any file under `client/` (frozen at Slices 8-14's close).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).
- Any file under `templates/` (Slice 16 owns the template rewrite).

**Deliverables.**

1. **`cmd_run.go`** — builds a `client.Runner` from the parsed
   `*schema.Doc` and the flags (`--state`, `--once`, `--interval`,
   `--out`, `--trace`, `--http-timeout`, `--max-pages`). Reads the
   config file via `config.go`; explicit flags override config
   values. The continuous-mode error policy ("always retry, even
   `error.mode: fail`") sits in the CLI's loop, not in the Runner.
   Uses `client.RedactURLError` at the log site for drain errors.
2. **`cmd_validate.go`** — invokes `schema.Validate` and prints
   diagnostics to stdout; exits 1 when any error-severity
   diagnostic is present.
3. **`cmd_init.go`** — writes a fully-commented default config to
   stdout (or `-o <path>`).
4. **`cmd_template.go`** — `template list` enumerates bundled
   templates from `templates/templates.go`; `template show <name>`
   writes the template body to stdout (or `-o <path>`).
5. **`config.go`** — config struct mirrors the flag set. YAML or
   JSON file format, the choice is yours (the existing code reads
   YAML; stick with it unless there's a reason to switch).
6. **`main.go`** — the cobra (or equivalent) root command. Sets up
   the four subcommands; no surface beyond what `usage.md` §1
   describes.
7. **`doc.go`** — package preamble for `cmd/skopos`. Describe the
   CLI surface, point at `docs/usage.md`. No legacy refs.
8. **`io.go`** — shared input/output helpers (file vs stdin/stdout).
9. **Hygiene pass** per global rule #5. No `cursor.<name>` framing,
   no `async_job` phase machine references, no `placeholder_event`,
   no `Defaults.BaseURL` / `requests[].path` mentions, no slice
   numbers in production strings, no "formerly" / "backwards
   compatibility" framing. Help-text and error messages should read
   as if the post-redesign shape had always existed.
10. **New artefact `docs/planning/IMPL-15-cmd-cli.md`** carrying:
    - Scope (one sentence).
    - Old → new map (per legacy flag / behaviour → new wiring).
    - Removed-content list (every dropped flag, command, helper).
    - Build-state enumeration (`go build ./cmd/...` stays green;
      Slice 15 introduces zero new errors in production code).
    - Walkthrough verification for the trickier cases (continuous
      run with `error.mode: fail`, `--trace` and the cache-HIT
      tombstone JSONL, `validate` exit codes, `template show` for
      a stdin-not-tty case).
    - Notes for downstream slices (Slice 16 on the templates the
      `template list|show` command will enumerate, Slice 17 on the
      CLI integration tests, Slice 18 on the schema-reference
      regeneration).
11. Slice-table row 15 in `RESEARCH_PLAN.md` flipped to `[x]` with
    the `IMPL-15-cmd-cli.md` link.
12. `HANDOFF.md` rewritten to point at Slice 16
    (`templates/*.yml` + `templates/templates.go` +
    `schema/testdata/*.yml`).

**Smoke tests.** Throwaway and optional. The trickier areas are:

- `skopos run --once -c <config> -i <spec>` with a real bundled
  template (once Slice 16 lands) — should drain one page and exit
  cleanly.
- `skopos run --interval 5s` under SIGINT — should commit the
  deferred Save and Flush before exiting.
- `skopos validate -i <broken-spec>` — should exit 1 and print the
  diagnostics from `schema.Validate`.
- `skopos template show oauth2_client_credentials` — should write
  the template YAML to stdout.

End-to-end correctness against the bundled templates is not
checkable until Slice 16 lands (the templates Slice 15's
`template list|show` enumerates are still in the legacy shape until
then). The build is the binding verification target.

**Out of scope.** No schema changes (Slice 7 closed). No client
changes (Slices 8-14 closed). No template changes (Slice 16). No
tests (Slice 17). No docs regeneration (Slice 18).

---

## Watch out for

- **The continuous-mode error policy is the CLI's, not the
  Runner's.** `Runner.Drain` returns whatever `error.mode` produces;
  the `run --interval N` loop in `cmd_run.go` swallows ALL errors
  (logging them with `client.RedactURLError` at the log site) and
  sleeps for the next iteration. Only ctx cancellation (SIGINT /
  SIGTERM) breaks the loop. Bug-for-bug, this matches the existing
  shape — the runtime semantics are unchanged from Slice 12's close.

- **`--trace` writes JSONL.** Each line is one `Exchange` record;
  cache HITs emit a minimal tombstone (`{"iteration":N,
  "step_id":"X","cache_hit":true}`). The CLI should NOT post-process
  the trace lines; `client.NewJSONLTracer(w)` writes directly to
  whatever file `--trace` names.

- **`template list` enumerates `templates/*.yml`.** The list comes
  from `templates/templates.go`'s generated map. Slice 16 owns the
  template rewrite; Slice 15 just plumbs `template list|show`
  through whatever shape `templates.go` exports today. If
  `templates.go` carries a stale list of files (some renamed away),
  `template list` will surface that — Slice 16 will fix it.

- **`schema.Validate` returns `[]Diagnostic`.** The validate command
  prints them per the existing format (severity, path, message) and
  exits 1 on the first error-severity diagnostic. No JSON output by
  default; the existing `--format json` flag (if it exists) stays
  as is.

- **`config.go` should mirror the flag set.** Explicit CLI flags
  override config values. Do NOT introduce config keys that don't
  have a corresponding flag — the project's posture is
  "config = persisted flag values", not "config = strictly more
  than flags".

- **Tests stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `cmd/skopos/cmd_*_test.go`, `cmd/skopos/config_test.go`,
  `cmd/skopos/integration_test.go`. Expect `go test ./cmd/...` to
  stay red. Slice 15's verification target is `go build ./cmd/...`
  zero errors.

- **`doc.go` is the package preamble.** The current preamble lives
  on the `cmd/skopos` package; rewrite to match what the CLI does
  today (per `docs/usage.md` §1). Point at the usage doc rather
  than duplicating the flag table.

---

## When you finish

`git add` the rewritten files, the new `IMPL-15-cmd-cli.md`, the
updated `RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit
with a message like:

```
feat(cmd): slice 15 — CLI against the new IR

run / validate / init / template list|show rewritten against the
post-redesign client + schema. The continuous-mode error policy
("always retry, even on error.mode: fail") sits in the CLI loop,
not in Runner. --trace writes JSONL; cache HITs emit a minimal
tombstone. --state persists the new Snapshot shape.

schema/ + client/ + cmd/ stays green at slice close; tests stay
red until Slice 17.
```

Push to `claude/slice-15-cmd-cli-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
