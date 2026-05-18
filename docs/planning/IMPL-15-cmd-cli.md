# IMPL-15 — `cmd/skopos/*`

## Scope

Audit the CLI surface against the post-redesign client + schema and the
`docs/usage.md` §1 contract; the four subcommands (`validate`, `run`,
`init`, `template list|show`) and their flag set were already laid out
in the post-redesign shape by earlier work, so this slice's footprint is
a hygiene pass and one stale-comment fix in `cmd_init.go`.

## Old → new map

| Legacy surface / mechanism                                                                | New mechanism                                                                                                                                                                                                                            |
|-------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `cmd_init.go` config-template comment "persisting the cursor between runs"                | "persisting state.* between runs". The on-disk shape is `{"state": {...}}` per Slice 8 / Slice 14; the legacy `cursor.*` namespace root has no representative in the persisted snapshot or in any operator-facing message.               |
| `runRun` builds a `client.Runner` from `*schema.Doc` and the resolved `Config`            | Unchanged. `Runner.Doc / Store / Sink / Client / Logger / Tracer / MaxPages` is the post-redesign struct (Slice 12); no field was renamed or added that the CLI needs to plumb through.                                                  |
| Continuous-mode error policy "always retry, even error.mode: fail"                        | Owned by the `--interval` for-loop in `cmd_run.go` (line ~184). `Runner.Drain` returns whatever `error.mode` produces; the CLI swallows it, logs via `client.RedactURLError`, sleeps `cfg.Interval`, retries. Only ctx cancellation breaks the loop. |
| `--trace path` opens an `O_APPEND` file and wraps it in `client.NewJSONLTracer`           | Unchanged. The cache-HIT tombstone shape (`{"iteration":N,"step_id":"X","cache_hit":true}`) introduced in Slice 14 falls out of the Tracer transparently — the CLI does no post-processing.                                              |
| `--state path` wires `client.NewFileStore(path)`                                          | Unchanged. The on-disk JSON shape is now `{"state": {...}}` (Slice 14). `preflightStatePath` still write-probes the parent dir so a typo surfaces at flag-parse time, not in the deferred `Save`.                                        |
| `skopos validate` invokes `schema.Validate(doc)` and prints diagnostics to stdout         | Unchanged. The validator was fully rewritten in Slice 7; its `Diagnostic{Severity, Path, Message, Line, Column}` shape is what `formatDiag` renders. Exits 1 via `errValidationFailed` on any error-severity diagnostic.                 |
| `skopos init [-o path] [--force]` emits a commented YAML config                           | Unchanged. The config keys mirror the flag set: `input`, `state`, `out`, `trace`, `once`, `interval`, `http_timeout`, `max_pages`. No keys without a matching flag (per `HANDOFF.md` watch-out).                                         |
| `skopos template list` / `skopos template show <name>`                                    | Unchanged. Enumerates `templates.Names()` (sorted, sans `.yml`). `show` reads `templates.Read(name)` and writes to stdout (default) or `-o path`. Templates themselves stay in the legacy shape until Slice 16; the CLI just plumbs them through. |
| `-c <config>` resolution                                                                  | Unchanged. `loadConfig` is strict-decoded YAML (`KnownFields(true)`); explicit CLI flags override config values via `fs.Visit`. Empty/comments-only config files (the fresh `skopos init` output) decode to a zero `Config` without an EOF error. |

## Removed content

Comments / docstrings:

- `cmd_init.go:24` comment "persisting the cursor between runs" — the
  only remaining `cursor.*` framing in `cmd/skopos/*` non-test code.
  The audit (`grep -nE '(cursor|async_job|placeholder_event|TokenCache|RequestCache|BaseURL|formerly|backwards|legacy|deprecated)' cmd/skopos/*.go` excluding `_test.go`) returned this line and nothing else.

Files, functions, flags, config keys: none. The CLI surface and its
wiring were already in the post-redesign shape (`docs/usage.md` §1)
prior to this slice. `cmd/skopos/*_test.go` carry stale shape per
global rule #6; Slice 17 owns the rewrite.

## File-by-file walk

### `cmd/skopos/main.go`

No changes. Hand-rolled subcommand dispatcher with the four subcommands
(`validate`, `run`, `init`, `template`) plus `-h` / `--help` / `help`.
The `usage()` text matches `docs/usage.md` §1 flag-for-flag. The
`errValidationFailed` sentinel is matched at the `validate` arm so the
diagnostic output is not duplicated by a "skopos: validation failed"
suffix.

### `cmd/skopos/cmd_run.go`

No source-level changes. Re-read confirms:

- The precedence ladder is right (defaults → `-c` config file →
  explicit flags), and `fs.Visit` is used to detect which flags the
  user explicitly set (zero-value-vs-unset disambiguation).
- The two preflight rejections (`-i` required; `--once` + `--interval`
  mutually exclusive) match the doc.
- The deferred Tracer `Flush()` runs before `closeTrace` so the file
  contains every line the Runner emitted before close.
- The continuous-mode loop owns the "always retry" policy. Cosmetic:
  the `client.RedactURLError(err)` call still applies before the log
  format so a transport-level `*url.Error` returned by a future
  `Drain` rev cannot leak credentials.
- The implicit one-shot path (`!cfg.Once && cfg.Interval == 0`)
  intentionally swallows `ctx.Err()` so SIGINT mid-drain exits `0`
  instead of `1`, mirroring the continuous-mode unwind.

### `cmd/skopos/cmd_validate.go`

No changes. The diagnostic format
(`<path>: <severity>: <message> (<file>:<line>:<col>)`) matches the
`docs/usage.md` §11 operator-facing example. Parse errors are wrapped
into a single `error`-severity diagnostic with a line number extracted
from `yaml.v3`'s `yaml: line N:` prefix; non-YAML parse errors fall
back to line `0` (no file:line suffix on the diagnostic). The exit
code is `1` only when at least one error-severity diagnostic appears;
warnings do not flip the exit code.

### `cmd/skopos/cmd_init.go`

One comment fix: the `state:` template line lost its "cursor" framing.
The rest of the template stays as-is — every key in the generated YAML
corresponds 1:1 with a flag on `skopos run`.

### `cmd/skopos/cmd_template.go`

No changes. `list` writes one name per line to stdout. `show -o path
<name>` writes the bundled template's bytes to `path` (or stdout when
`-o` is omitted / `-`). The `.yml` extension is optional on the
positional arg and the missing-name error path includes the available
list (the `templates.Read` error message carries it). Slice 16 will
rewrite every bundled template; the CLI plumbing is unchanged.

### `cmd/skopos/config.go`

No changes. The `Config` struct mirrors the flag set; `loadConfig` is
strict-decoded with `dec.KnownFields(true)` so a typo in the YAML
config surfaces as a decoder error rather than silently dropping the
field. Empty / comments-only files (the `skopos init` default output)
decode to a zero `Config` without an `io.EOF` from `yaml.v3`.

### `cmd/skopos/io.go`

No changes. The `readBytes` helper accepts `""` or `"-"` as stdin
sentinels; everything else is a file path. Used by both `runValidate`
and `runRun`'s `loadDoc`.

### `cmd/skopos/doc.go`

No changes. The package preamble lists the four subcommands and
describes each one in two sentences. No legacy refs (`cursor.*`,
`async_job`, `Defaults.BaseURL`, etc.) appear. The preamble does not
duplicate the flag table from `docs/usage.md` §1; operators looking
for flag-level reference are pointed at the doc.

## Build state

```
go build ./cmd/...   → no errors, no warnings
go build ./...       → no errors, no warnings (schema + client + cmd + tools all green)
```

The audit grep for legacy vocabulary (`cursor`, `async_job`,
`placeholder_event`, `TokenCache`, `RequestCache`, `BaseURL`,
`formerly`, `backwards`, `legacy`, `deprecated`, `requests[].path`)
returns nothing in `cmd/skopos/*.go` (non-test) after the
`cmd_init.go:24` fix. Test files carry stale shape per global rule #6
and are owned by Slice 17.

## Walkthrough verification

### Continuous run with `error.mode: fail` from the spec

`Runner.Drain` returns the `error.mode: fail` error verbatim through
the runner's `Drain(ctx)` return. The CLI loop at `cmd_run.go:184`
swallows it:

```go
for {
    if err := runner.Drain(ctx); err != nil {
        if ctx.Err() != nil { return nil }
        logger.Printf("drain error: %v (retrying after %s)", client.RedactURLError(err), cfg.Interval)
    }
    if ctx.Err() != nil { return nil }
    select {
    case <-ctx.Done(): return nil
    case <-time.After(cfg.Interval):
    }
}
```

The drain-error log line is produced; the loop sleeps `cfg.Interval`;
the next iteration calls `Drain` again. Only SIGINT / SIGTERM
(propagated via `signal.NotifyContext`'s ctx) ends the loop. This
matches `docs/usage.md` §1 ("Continuous-mode error policy is 'always
retry': even `error.mode: fail` from the spec does not stop the
loop").

### `--trace` cache-HIT tombstone

When the runner's Tracer fires for a step served from `cache.*`
(populated and fresh slot), `buildExchange` in `client/trace.go`
detects the cache-HIT path (`t.method == "" && runErr == nil`) and
returns the minimal `{Iteration, StepID, CacheHit:true}` record. The
JSONL line the CLI's tracer file receives is:

```json
{"iteration":7,"step_id":"login","cache_hit":true}
```

The CLI does no post-processing — `client.NewJSONLTracer(tf)` writes
the encoded record verbatim. A wire-call line on the same step would
populate `method`, `url`, `status`, `started_at`, `elapsed`,
`request`/`response` body classifications, etc.; the `omitempty` on
the wire-only fields keeps the tombstone clean.

### `validate` exit codes

- `skopos validate -i good.yml` → prints zero diagnostics, exits `0`.
- `skopos validate -i broken.yml` (one or more error-severity
  diagnostics) → prints each diagnostic on its own stdout line,
  returns `errValidationFailed` from `runValidate`, main.go maps the
  sentinel to exit `1` without prefixing the line with "skopos: ".
- `skopos validate -i warn.yml` (only warning-severity) → prints the
  warning, exits `0`. Severity is "error" or "warning"; anything else
  is treated as non-blocking.
- `skopos validate < stdin.yml` (no `-i`) → reads stdin via
  `readBytes("")`. The file label on the diagnostic suffix becomes
  `(stdin:N[:C])`.

### `template show` to stdout-not-a-tty

`skopos template show oauth2_client_credentials` writes the embedded
template bytes to `os.Stdout`. The bytes already include a trailing
newline (every `.yml` in `templates/` ends with one); `runTemplateShow`
appends one only if the embedded file is missing the terminator (a
historical hygiene check). Redirecting to a file
(`skopos template show oauth2_client_credentials > spec.yml`) produces
an exact byte-for-byte copy of the embedded template plus its trailing
newline — no header, no footer.

### `skopos init` on an existing path without `--force`

The `O_EXCL` open flag fails with `os.ErrExist`; the CLI returns the
explicit "%s already exists (use --force to overwrite)" error rather
than the raw `os.PathError`, so the operator sees the actionable hint.

## Notes for downstream slices

### Slice 16 — `templates/*.yml`

- `template list` enumerates whatever `templates/templates.go` exports;
  if the slice renames or removes a template, the CLI surface
  follows transparently.
- `template show <name>` looks up `name.yml` (or `name.yaml`)
  in the embedded FS. Slice 16 should keep the existing 27 names so
  operator-facing copy-paste paths (e.g. `skopos template show
  bearer_simple`) continue to work — but renames are not blocking, the
  CLI just reports "template %q not found (available: ...)" if a name
  goes away.
- `skopos validate -i <template>` and `skopos run -i <template>` are
  the two CLI paths Slice 16 should run end-to-end against every
  rewritten template before slice close.

### Slice 17 — test rewrite

- `cmd/skopos/cmd_run_test.go`, `cmd_validate_test.go`,
  `cmd_template_test.go`, `cmd_init_test.go`, `config_test.go`,
  `integration_test.go` all use the legacy spec shape
  (`state.fields`, `defaults.base_url`, `requests[].path`, the old
  pagination/progress variants). Each must be re-templated against
  the post-redesign IR — the easiest path is to rebase each fixture
  YAML onto the rewritten templates from Slice 16.
- The `cmd_run_test.go` integration test that boots an `httptest`
  server should also re-verify `--trace`'s cache-HIT tombstone on a
  spec carrying a `requests[].cache` block.
- The validate-exit-code assertions on the `cmd_validate_test.go`
  golden fixtures should pin both the diagnostic line format and the
  `errValidationFailed` sentinel mapping.

### Slice 18 — schema-reference regeneration

- The CLI doc (`docs/usage.md` §1) is the source of truth for the
  flag table; the regenerated `docs/schema-reference.md` should not
  duplicate it.
- Nothing the CLI exposes feeds back into the schema reference — the
  schema-reference doc is the parsed IR walked, not the CLI surface.
