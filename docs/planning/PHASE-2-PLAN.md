# Phase 2 plan — code, templates, fixtures, tests

This document is the cross-slice reference for Phase 2. Every slice
agent reads it once before starting their slice, then follows the
slice-specific scope captured in their own `IMPL-NN-*.md` artefact.

The plan covers:

- the slice list, in execution order ([§1](#1-slice-list));
- per-slice scope, file ownership, and downstream-impacting decisions
  ([§2](#2-per-slice-detail));
- cross-cutting rules specific to Phase 2 ([§3](#3-cross-cutting-rules));
- the verification posture (when each piece becomes testable
  end-to-end) ([§4](#4-verification-posture)).

The locked design is in [`../DESIGN_SUGGESTIONS.md`](../DESIGN_SUGGESTIONS.md).
The post-Phase-1 prose docs in `docs/` (`schema.md`, `api-methods.md`,
`runtime.md`, `stores.md`, `usage.md`) are the **operational reference
for Phase 2 agents** — they describe the post-redesign shape Phase 2
implements. Global rules in [`RESEARCH_PLAN.md`](RESEARCH_PLAN.md) apply
to every slice.

---

## 1. Slice list

Four domains, executed in strict dependency order. Each domain may be
multiple slices; the order within a domain is also strict.

| #   | Status | Domain     | Slice                                                                          | Impl plan file                          |
|-----|--------|------------|--------------------------------------------------------------------------------|-----------------------------------------|
| 4   | [ ]    | schema     | `schema/schema.go` + `read.go` + `doc.go` — struct definitions                 | `IMPL-04-schema-structs.md`             |
| 5   | [ ]    | schema     | `schema/value.go` — Value language (reducers, interpolation, add/subtract)     | `IMPL-05-schema-value.md`               |
| 6   | [ ]    | schema     | `schema/path.go` + `predicate.go` — namespace roots, path & predicate rules    | `IMPL-06-schema-path-predicate.md`      |
| 7   | [ ]    | schema     | `schema/validate.go` — full validator rewrite                                  | `IMPL-07-schema-validator.md`           |
| 8   | [ ]    | client     | `client/state.go` — Snapshot shape, scope, per-drain wipe, new namespace roots | `IMPL-08-client-state.md`               |
| 9   | [ ]    | client     | `client/value.go` + `predicate.go` + `extract.go` + `bodypath.go` — value runtime | `IMPL-09-client-value.md`             |
| 10  | [ ]    | client     | `client/pagination.go` — collapse 7 variants → 4 + `custom`                    | `IMPL-10-client-pagination.md`          |
| 11  | [ ]    | client     | `client/progress.go` — variants → flat list of writes, per-page firing         | `IMPL-11-client-progress.md`            |
| 12  | [ ]    | client     | `client/runner.go` — drain loop, request loop, on_status, error.mode           | `IMPL-12-client-runner.md`              |
| 13  | [ ]    | client     | `client/http.go` + `auth.go` + `cache.go` (merging `oauth2.go` + `requestcache.go`) + `fanout.go` | `IMPL-13-client-http-cache-auth.md` |
| 14  | [ ]    | client     | `client/sink.go` + `filestore.go` + `redact.go` + `trace.go` + `doc.go` — secret propagation, trace records, last cleanup | `IMPL-14-client-sink-trace.md` |
| 15  | [ ]    | cmd        | `cmd/skopos/*` — `validate` / `run` / `init` / `template list|show`            | `IMPL-15-cmd-cli.md`                    |
| 16  | [ ]    | templates  | `templates/*.yml` + `templates/templates.go` + `schema/testdata/*.yml`         | `IMPL-16-templates.md`                  |
| 17  | [ ]    | tests      | All `*_test.go` files in `schema/`, `client/`, `cmd/`, `internal/testserver/`  | `IMPL-17-tests.md`                      |
| 18  | [ ]    | docs       | Regenerate `docs/schema-reference.md`                                          | `IMPL-18-schema-reference.md`           |

**Order rationale.** Dependencies flow downward:

- Schema package is rewritten first because every other domain consumes
  `*schema.Doc`. Within schema, structs come before the value language
  (the parser owns Value), which comes before paths/predicates (which
  use Value), which come before the validator (which uses all three).
- Client package consumes `*schema.Doc`. Within client, the
  `Snapshot`/scope shape is rewritten first because every subsequent
  client slice writes to or reads from scope. Value resolution comes
  next (the pagination/progress/runner slices call into it). Pagination,
  progress, runner are independent of each other but all three feed off
  the new scope; we sequence them by line count (smallest → largest)
  for review ergonomics.
- CLI is small and consumes the finished client/schema surface; it
  comes after client is done.
- Templates and `schema/testdata/` fixtures are rewritten only once
  the schema validator accepts the new shape (Slice 7+).
- Tests rewrite is the back-stop — see [§4](#4-verification-posture).
- `schema-reference.md` is generated; it is the final step (Slice 18).

---

## 2. Per-slice detail

### Slice 4 — `schema/schema.go` + `read.go` + `doc.go`

**Scope.** Rewrite the struct definitions for the IR document. Make
`schema.Parse` accept the new shape; reject the old shape at the
parser. No validator changes (that's Slice 7); the validator will be
broken at the end of this slice and stays broken until Slice 7.

**Files touched (only).** `schema/schema.go`, `schema/read.go`, `schema/doc.go`.

**Structural changes.**

1. `Doc`: drop `Defaults`. Add no new fields.
2. `State`: flatten — `State.Fields map[string]FieldDecl` becomes
   `State map[string]FieldDecl` at the top level (the `state:` block
   IS the map; no `.fields` indirection). `FieldDecl` loses
   `Mutability` (lifetime is inferred). Add `Format` field for
   `timestamp` / `duration` wire-format hint.
3. `Auth`: unchanged variant set (`none|bearer|basic|api_key|custom|oauth2|multi_mode`).
   Replace `TokenCache` with a unified `Cache` struct. `MultiModeAuth.Default`
   becomes a bare `Auth` (not the wrapped `{auth: ...}` form).
4. `OAuth2Auth`: nest a `Cache` (not `TokenCache`) under each grant.
5. `Request`: drop `Path`. Add `TerminateWhen Predicate`. Replace
   `RequestCache` with the same unified `Cache`. `Cache` and `FanOut`
   stay mutually exclusive.
6. `Body`, `FanOut`, `ExtractVar`: unchanged in shape (extract drops
   `Target` since `to:` carries the namespace).
7. `Response`: unchanged. Remove the `PlaceholderEvent` field.
8. `Pagination`: collapse to a 5-key union — `None`, `CursorToken`,
   `NextURL`, `Counter`, `Custom`. Delete the old `CursorTokenPagination`,
   `PageNumberPagination`, `OffsetPagination`, `LinkHeaderPagination`,
   `NextURLInBodyPagination`, `ScrollIDPagination`, `GraphQLRelayPagination`
   types and their `*_raw` mirrors.
9. `Progress`: replace the discriminated union with a flat
   `[]ProgressWrite` where `ProgressWrite{To Path, From Value, Coerce, Regex}`.
   Delete `TimestampProgress`, `UseNowProgress`, `EventTime`, `Initial`,
   `TimeWindowProgress`, `AsyncJobProgress`, `AsyncSubmitStep`,
   `AsyncPollStep`, `AsyncFetchStep`, `AsyncExtract`, `AsyncOnComplete`,
   `CursorUpdateDirective`, `cursorUpdateRaw`.
10. `ErrorBlock`: unchanged. `on_status` verb set is just data here;
    the verb table is enforced in Slice 7.
11. `read.go`: adjust `Parse` if the new shape changes round-trip
    behaviour. `doc.go`: rewrite the package preamble against the new
    shape.

**Downstream impact.** Every other slice consumes these types. The
validator (Slice 7) needs the new types in place. The client package
(Slices 8-14) reads field paths off `*schema.Doc`. Templates (Slice 16)
and tests (Slice 17) depend on `Parse` accepting the new YAML.

**Verification.** Build the package (`go build ./schema/...`). Tests
will be red — that's fine, see global rule #6.

---

### Slice 5 — `schema/value.go`

**Scope.** Implement the Value language additions: `Add`, `Subtract`,
the reducers (`Max`, `Min`, `First`, `Last`, `Count`), and string
interpolation parsing (`${state.x}` desugars to `{concat: [...]}`).
Remove `{now: true, offset: ...}` sibling-key handling.

**Files touched.** `schema/value.go`.

**Structural changes.**

1. Add `Add Subtract *struct{ Operands [2]Value }` (or equivalent) to
   `Value`.
2. Add `Max Min First Last Count *Value` to `Value` — each carrying a
   single `Value` operand that resolves to a list or a list-shaped
   projection (e.g. `{ref: events.*.timestamp}`).
3. Add `Regex *RegexExpr` where `RegexExpr{Pattern, From Value, Capture int, Default *Value}`.
4. Rewrite the string-scalar unmarshaller: any YAML string in a Value
   position is scanned for `${...}` segments. Each segment may carry a
   `|<default>` sigil. The string desugars to a `{concat: [...]}`
   Value. Escape rules per `DESIGN_SUGGESTIONS.md` §4.1.
5. Reject `{now: true, offset: ...}` at parse time. Plain `{now: true}`
   is unchanged.
6. The `format:` verb set accepts a Go layout string as an open
   fallback (the validator distinguishes closed-set verbs from layouts).

**Downstream impact.** Client value runtime (Slice 9) implements these.
The validator (Slice 7) type-checks them. Templates and tests use
interpolation pervasively.

**Verification.** Build the package. Slice 5 owns its own parse-time
spot tests — small, throwaway smoke tests live in `schema/value.go`'s
test file during the slice and **are deleted at slice close** (the
Slice 17 test rewrite is the authoritative test pass).

---

### Slice 6 — `schema/path.go` + `schema/predicate.go`

**Scope.** Update the namespace-root vocabulary and any predicate-form
adjustments. Predicate forms (`eq`, `gt`, `lt`, `gte`, `lte`, `present`,
`not`, `and`, `or`, `literal_bool`) stay as-is; absent-tolerance is
already the policy. The path layer needs the new namespace roots in
its closed set.

**Files touched.** `schema/path.go`, `schema/predicate.go`.

**Structural changes.**

1. Namespace roots in the closed set: `state`, `cache`, `events`,
   `extract`, `steps`, `response`, plus any author-chosen `fan_out.as`
   name (validated against the reserved list at Slice 7). Drop
   `cursor`. Drop `body` (the old body-relative root deleted in the
   previous Phase 1 doc pass).
2. `events.*` paths accept the new shortcuts: `events.first`,
   `events.last`, `events.<int>`, `events.count`, `events.*`.
3. `steps.<id>` accepts `body[.<path>]` or `header.<name>` subpaths
   (unchanged from current).
4. `response.<...>` accepts `body[.<path>]` or `header.<name>`.

**Downstream impact.** The validator (Slice 7) reads the closed-root
set off path.go. The client scope resolver (Slice 8) mirrors the same
set in `resolveNamespaceRef`.

**Verification.** Build the package.

---

### Slice 7 — `schema/validate.go`

**Scope.** Full validator rewrite. The current `validate.go` is 1487
lines and weaves old-shape checks throughout — `cursor.*` namespace,
`mutability:` field, `state.fields`, the 7 pagination variants, the 5
progress variants, `async_job` phase machine, `Defaults`, `path:`,
`placeholder_event:`, etc. None of those survive.

**Files touched.** `schema/validate.go`.

**Structural changes (high level).**

1. Replace the cursor-schema inference (`cursorSchema(d *Doc)`) with
   the lifetime-inference rule from `docs/schema.md` §state:
   - operator-config: has `default:`, never written to;
   - per-drain scratch: target of any `pagination.*.to` write;
   - persistent: target of any `progress:` write or
     `requests[].extract` with `to: state.*`.
2. Reject conflicts (same field written by pagination and progress)
   with a clear diagnostic.
3. Validate the new `Pagination` union (exactly one of
   `none|cursor_token|next_url|counter|custom`). Per-variant required
   fields per `docs/schema.md` §3.6.
4. Validate `Progress` as a flat list of writes; each entry's `to:`
   must be `state.<name>` and that field must be declared under
   `state:`.
5. Validate `requests[].terminate_when:` as a Predicate; reject when a
   request is missing `id:` but referenced from
   `steps.<id>.body.<path>` or `fan_out.over: {ref: steps.<id>...}`.
6. Validate the closed `on_status:` verb set
   (`skip|fail|empty_events|invalidate_cache`). Reject anything else.
   Reject `invalidate_cache` when no reachable cache block exists in
   the spec (degrades to `empty_events` at runtime per
   `docs/runtime.md` §6, but the validator can warn).
7. Validate the unified `Cache` block (auth + request). Reject
   `requests[].fan_out` + `requests[].cache` co-occurrence.
8. Validate the reducer arguments (`Max`/`Min`/`First`/`Last`/`Count`):
   either a `{list: [...]}` literal or a list-shaped Value (typically
   `{ref: events.*.<field>}`).
9. Validate string interpolation segments resolve to valid paths.
10. Validate the fan-out reserved-root set
    (`state|cache|events|extract|steps|response`).
11. Reject `Defaults`, `state.fields`, `mutability:`, `cursor.*`,
    `placeholder_event:`, `flow:`, `async_job:`, `complete_when:`,
    `target:` on extract, `path:` on request, `{now: true, offset: ...}`.

**Downstream impact.** Templates (Slice 16) and test fixtures rely on
the validator accepting the new shape. The CLI's `skopos validate`
(Slice 15) exposes the diagnostic format.

**Verification.** Build the package. Add a handful of throwaway smoke
tests inline for the trickiest checks (lifetime inference conflict,
reducer arg shapes, on_status verbs) — **delete them before slice
close**.

---

### Slice 8 — `client/state.go`

**Scope.** Reshape `Snapshot` and the per-drain `scope`. Implement the
new per-drain wipe. Add the `cache.*` and `events.*` namespaces to
`resolveNamespaceRef`. Remove `cursor.*` entirely.

**Files touched.** `client/state.go`.

**Structural changes.**

1. `Snapshot` becomes `struct{ State map[string]any }` — no `Cursor`
   field. Per `docs/stores.md` §2.
2. `scope` drops the `cursor` map. Adds:
   - `events any` (the decoded events list, exposed as `events.*` /
     `events.first` / `events.last` / `events.<int>` / `events.count`);
   - `cache map[string]any` (process-memory cache slots);
   - retains `state`, `extract`, `steps`, `stepHeaders`, `body`,
     `responseHeaders`, `item`, `itemBinding`, `nowFn`, `logger`.
3. `newScope` seeds `state` from defaults under the snapshot (per
   `docs/runtime.md` §2 step 1). The per-drain wipe runs in `Runner.Drain`,
   not in `newScope`, so that scope can be reused across redrains in
   continuous mode without re-loading the snapshot.
4. `snapshot()` writes only operator-config + persistent state fields,
   omitting per-drain scratch (per `docs/stores.md` §2). `cache.*` is
   never persisted.
5. `resolveNamespaceRef`: drop the `cursor` arm. Add `cache` and
   `events` arms; the `events` arm understands `first`, `last`,
   `count`, `<int>` index, and `*` projection.
6. Helper exposed (or methods on `scope`) to classify a state field as
   `operator-config | scratch | persistent` from the parsed doc — the
   classification is read off the IR, not stored on the scope; it
   drives the per-drain wipe and the `snapshot()` filter.

**Downstream impact.** Every later client slice reads from / writes to
the new scope. The runner slice owns the wipe call site; the
pagination/progress slices own the writes.

**Verification.** Build `./client/...`. Tests red — fine.

---

### Slice 9 — `client/value.go` + `predicate.go` + `extract.go` + `bodypath.go`

**Scope.** The value-resolution runtime. Implement the new Value forms
introduced in Slice 5 (Add/Subtract/Max/Min/First/Last/Count/Regex,
plus the desugared `{concat: [...]}` form for interpolated strings).
Implement the `events.*` projection semantics. Re-implement extract
to write to `state.<name>` or `extract.<name>` (no more `target:
cursor`).

**Files touched.** `client/value.go`, `client/predicate.go`,
`client/extract.go`, `client/bodypath.go`.

**Structural changes.**

1. `evalValue` adds branches for `Add`, `Subtract`, `Max`, `Min`,
   `First`, `Last`, `Count`, `Regex`.
2. Reducer inputs: a list literal or a list-shaped Value. The
   list-shaped case typically resolves through `events.*.<field>` —
   defer to the scope's events-projection resolver from Slice 8.
3. Predicate eval is largely unchanged. Confirm absent-tolerance
   semantics: `present` returns false; `eq`/`gt`/`lt` return false;
   no exceptions thrown.
4. `extract.go`: each extract writes to its `to:` destination's
   namespace. `state.<name>` → `scope.state`; `extract.<name>` →
   `scope.extract`. No `target:` switch; no `cursor.*`.
5. `bodypath.go`: unchanged except for any references to the removed
   `body.<path>` root.

**Downstream impact.** Pagination, progress, runner, fan-out all call
into `evalValue`. The trace and redaction layers (Slice 14) inherit
the secret-taint propagation through the new Value forms.

**Verification.** Build `./client/...`. Spot-check throwaway tests
fine; remove before close.

---

### Slice 10 — `client/pagination.go`

**Scope.** Collapse the 7-strategy pagination plan into 4 named
variants + `custom`. Implement the per-page execution order from
`docs/runtime.md` §3 (request → terminate_when → advance → loop).
Remove `paginationPlan.seed` — the per-drain wipe handles
bootstrapping; there is no per-iteration seed step.

**Files touched.** `client/pagination.go`.

**Structural changes.**

1. `paginationPlan` interface shrinks to a single
   `advance(scope, body, headers, events) (terminate bool, err error)`
   or similar — variant logic moves into the per-variant struct
   `advance` method. The per-drain wipe is the runner's responsibility.
2. Five concrete plans: `nonePagination`, `cursorTokenPagination`,
   `nextURLPagination`, `counterPagination`, `customPagination`.
3. `cursorTokenPagination` covers what was `cursor_token` + `scroll_id` +
   `graphql_relay` + page-number-from-body.
4. `nextURLPagination` covers what was `link_header` + `next_url_in_body`.
   Owns `regex` + `capture` (only useful for Link header but applies
   uniformly).
5. `counterPagination` covers what was `page_number` + `offset`. Owns
   `start` + `step`; default `terminate_when:` is `events.count < step`
   (short-page).
6. `customPagination` exposes the primitive: a list of `{to, from,
   regex?, coerce?}` writes plus an author-supplied `terminate_when:`
   (required for custom). No default predicate.
7. Each variant's `terminate_when:` defaults match the table in
   `docs/runtime.md` §3.

**Downstream impact.** Runner slice (Slice 12) drives the new pagination
plan from inside the drain loop. Templates (Slice 16) are validated
against the new variant set.

**Verification.** Build `./client/...`. Smoke tests fine; remove before
close.

---

### Slice 11 — `client/progress.go`

**Scope.** Replace the 5-variant `progressPlan` (statelessProgress,
latestTimestampProgress, maxEventFieldProgress, useNowProgress,
timeWindowProgress, asyncJobProgress) with a single
"evaluate-write-list-per-page" pass. Progress now fires once per
accepted page-response, including empty pages — not once per drain.

**Files touched.** `client/progress.go`.

**Structural changes.**

1. Delete the `progressPlan` interface and every variant struct.
2. Provide one function: `applyProgress(scope, doc.Progress) error`.
   It stages every `from:` resolution first (against the same pre-write
   snapshot of `state.*`), then writes every `to:` destination. Batch
   semantics per `docs/runtime.md` §5.
3. The runner calls this function once per accepted page-response —
   placed in the drain loop, not at end of drain.
4. Drop every async_job hook (handled by `requests[].terminate_when:`
   in the runner slice now).
5. Drop the `seed` half (no per-drain pinning step; first-run seeding
   is the `default:` on the destination state field).
6. Cumulative high-water marks are written explicitly by the author;
   the runner doesn't merge.

**Downstream impact.** Runner slice (Slice 12) places the call site.

**Verification.** Build `./client/...`.

---

### Slice 12 — `client/runner.go`

**Scope.** Rewrite the drain loop to match `docs/runtime.md` §2. Add
the request-level loop primitive (`requests[].terminate_when:`).
Implement the new `on_status` verb table and the unchanged `error.mode`
fallback.

**Files touched.** `client/runner.go`.

**Structural changes.**

1. Drain sequence (named-step rewrite):
   1. Load (`store.Load` + scope seed).
   2. Per-drain wipe (reset every per-drain-scratch state field to
      its `default:` or unset).
   3. Pagination loop. Each iteration:
      a. Run the requests chain end-to-end, honouring `if:` and the
         per-request `terminate_when:` loop on each step.
      b. Decode the producer step's body, resolve `events_at`, bind
         to `scope.events`.
      c. Emit events to `Sink` (one call per event).
      d. Apply `progress:` writes (once per accepted page-response,
         including empty pages).
      e. Advance pagination plan: terminate? → exit. Else `advance:`
         writes → loop.
   4. Commit (`store.Save` via deferred call — runs on normal exit,
      `error.mode: warn`, AND `error.mode: fail`).
2. Request loop: a step with `terminate_when:` re-fires until the
   predicate evaluates true; without it, the step runs exactly once.
3. `on_status` verbs: `skip` / `fail` / `empty_events` / `invalidate_cache`
   per `docs/runtime.md` §6. `retry` is absent. The
   `invalidate_cache` degrade-to-`empty_events` path requires a log
   line when no reachable cache slot exists.
4. `error.mode` fallback: `standard` (loop ends, per-drain state
   wipes on next start, deferred Save runs); `warn` (continue,
   progress does NOT fire for this iteration); `fail` (Drain returns
   non-nil, deferred Save still runs).
5. Remove `progress.seed` / `progress.advance` call sites; the new
   `applyProgress` is called per-page.
6. Remove `pagination.seed` call sites; the per-drain wipe replaces it.
7. Remove the `placeholder_event` two-pass logic entirely. Empty
   pages just trigger the next page; no synthetic event.
8. Remove the `async_job` non-producer-phase branch.

**Downstream impact.** This is the central orchestrator; CLI (Slice 15)
plus tests (Slice 17) hang off the new behaviour.

**Verification.** Build `./client/...`. End-to-end correctness is only
checkable once templates + tests land (Slices 16-17).

---

### Slice 13 — `client/http.go` + `auth.go` + `cache.go` + `fanout.go`

**Scope.** Merge `oauth2.go` + `requestcache.go` into one `cache.go`
holding the unified `Cache` struct's runtime. Update the HTTP execution
layer for the new request shape (no `Defaults.BaseURL`; every request
has `url:`). Update the auth layer for the new `MultiModeAuth.Default`
shape (bare `Auth`, not wrapped). Update fan-out for the new reserved-root
list.

**Files touched.** `client/http.go`, `client/auth.go`, `client/oauth2.go`
→ `client/cache.go` (rename + merge), `client/requestcache.go` (deleted,
contents absorbed into `cache.go`), `client/fanout.go`.

**Structural changes.**

1. `cache.go`: one struct + helper that handles both call sites (auth
   grant cache, request-level cache). Slots live in `scope.cache`
   (process memory, never persisted). `Cache.ExpiresAt` is a Value
   resolving to `time.Time`. Re-fetch when `now() + buffer >= expires_at`.
2. Remove the `state.<store_in>_expires_at` framework-internal slot
   convention — `cache.<name>` carries the value AND its expiry
   internally; the `expires_at:` Value is evaluated at write time and
   stored alongside.
3. `http.go`: drop `BaseURL` resolution. Every request resolves `url:`
   directly (now an absolute URL Value, often a string interpolation).
4. `auth.go`: `MultiModeAuth.Default` is a bare `Auth`. Dispatcher
   reads it as-is, no wrapping.
5. `fanout.go`: validate the `fan_out.as` name doesn't collide with
   the reserved roots (`state|cache|events|extract|steps|response`);
   matches Slice 7's validator. Per-item errors go through the same
   `on_status` / `error.mode` dispatcher.

**Downstream impact.** Templates (Slice 16) drop `defaults.base_url`
universally; OAuth2 templates write `cache:` blocks pointing at
`cache.<name>`.

**Verification.** Build `./client/...`.

---

### Slice 14 — `client/sink.go` + `filestore.go` + `redact.go` + `trace.go` + `doc.go`

**Scope.** Last client cleanup. `Sink` and `FileStore` interfaces don't
change in signature, but their docstrings need rewriting against the
new `Snapshot` shape. `redact.go` + `trace.go` carry secret-taint
propagation through every new Value form (Add/Subtract/Max/Min/First/
Last/Count/Regex/interpolated-strings). Per `docs/runtime.md` §7.

**Files touched.** `client/sink.go`, `client/filestore.go`,
`client/redact.go`, `client/trace.go`, `client/doc.go`.

**Structural changes.**

1. `Snapshot` JSON shape change ripples into `filestore.go`'s
   serialiser — no `cursor:` key in the on-disk JSON; only `state:`.
2. `redact.go`: every new Value form propagates secret-taint. The
   redacted-headers list still includes `Authorization`, `Cookie`,
   `Proxy-Authorization`, plus the runtime credential surfaces named
   by `auth.api_key.header` / `auth.custom.header`.
3. `trace.go`: `Exchange` fields documented in `docs/runtime.md` §9.
   Confirm no field references `cursor.*` or any old vocabulary.
4. `doc.go`: rewrite the package preamble against the new shape.

**Downstream impact.** Last code-touch slice before the CLI.

**Verification.** Build `./client/...`. Tests still red overall.

---

### Slice 15 — `cmd/skopos/*`

**Scope.** Rewrite the CLI surface to match `docs/usage.md` §1. Four
subcommands: `validate`, `run`, `init`, `template list|show`. The flag
set is documented in the usage doc's flag table.

**Files touched.** `cmd/skopos/cmd_run.go`, `cmd_validate.go`,
`cmd_init.go`, `cmd_template.go`, `config.go`, `main.go`, `io.go`, `doc.go`.

**Structural changes.**

1. `run`: builds a `client.Runner` from the parsed `*schema.Doc` and
   the flags (`--state` / `--once` / `--interval` / `--out` / `--trace`
   / `--http-timeout` / `--max-pages`).
2. `validate`: emits diagnostics from `schema.Validate` to stdout;
   exits 1 when any error-severity diagnostic is present.
3. `init`: writes the fully-commented default config.
4. `template list` / `template show`: enumerate the bundled templates;
   `show` writes one to stdout or `-o`.
5. `config.go`: the config struct mirrors the flag set. Explicit flags
   override config values.
6. The continuous-mode error policy ("always retry, even
   `error.mode: fail`") sits in the CLI's loop, not in the runner.

**Downstream impact.** Templates slice (Slice 16) is what gives
`template list|show` meaningful output. Tests slice (Slice 17) rewrites
the integration tests.

**Verification.** Build `./cmd/...`. Run `skopos --help` smoke check;
remove any throwaway smoke tests before close.

---

### Slice 16 — `templates/*.yml` + `templates/templates.go` + `schema/testdata/*.yml`

**Scope.** Rewrite every bundled template and every schema test
fixture against the new shape. Each template is a self-contained
example; the rewrites are mechanical once the schema validator accepts
the new shape.

**Files touched.** Every `*.yml` under `templates/`. `templates/templates.go`.
Every `*.yml` under `schema/testdata/`.

**Structural changes (per template).**

1. `state:` flattens (drop `.fields`).
2. Remove `state.initial_interval`. First-run seeding becomes
   `default: {subtract: [{now: true}, "720h"]}` on the destination
   state field.
3. Remove `defaults:` block. Inline the base URL via interpolation
   (`url: "${state.url}/path"`).
4. Rename `cursor.<x>` → `state.<x>` everywhere; declare every such
   field under `state:`.
5. Collapse pagination variants per the variant-mapping table in
   `docs/schema.md` §3.6:
   - `cursor_token`, `scroll_id`, `graphql_relay`, page-number-from-body
     → `cursor_token` with `from:` + `to:` + optional `terminate_when:`.
   - `link_header`, `next_url_in_body` → `next_url` with `from:` + `to:`
     + optional `regex:`/`capture:`.
   - `page_number`, `offset` → `counter`.
6. Rewrite `progress:` as a flat list of `{to, from}` writes.
7. Replace `async_job:` progress with the three-request submit/poll/fetch
   chain in `requests:`, using `requests[].terminate_when:` on the poll
   step.
8. Token caching (OAuth2 + custom-login) writes to `cache.<name>` via
   the unified `Cache` block.
9. ETag conditional templates write the etag into `state.<name>` via
   `extract:`; no `cursor.*`.
10. Drop `placeholder_event:` from `response:` blocks.

**Templates in scope (26 + variants).** Every file currently in
`templates/`. List:

```
api_key_auth, async_poll, async_poll_latest_ts, async_poll_stateless,
basic_auth, bearer_simple, cursor_token, custom_auth, etag_conditional,
etag_conditional_middle, fanout, link_header, multi_mode_auth,
ndjson_response, next_url_in_body, oauth2_client_credentials,
oauth2_password_grant, oauth2_relay, offset_pagination, page_number,
post_form_body, post_json_body, post_raw_body, scroll_id,
session_cookie, session_login_cached, simple_get_object
```

**Test fixtures in scope.** Every file currently in `schema/testdata/`.

**Downstream impact.** Tests slice (Slice 17) reads these as golden
inputs.

**Verification.** Run `skopos validate -i <each template>` end-to-end.
Add a throwaway "validate every bundled template" smoke test during
the slice; **delete it at slice close** (the test slice owns the
authoritative golden-file pass).

---

### Slice 17 — All tests

**Scope.** Rewrite every `*_test.go` file in the project. Drop tests
that the rewritten code makes redundant; consolidate around golden
files where possible (matches the project's existing posture per
README and merged commits like `f8db414 chore(tests): Replace large
amount of redundant tests with simple golden files`).

**Files touched.** Every `*_test.go` in `schema/`, `client/`, `cmd/`,
`internal/testserver/`.

**Structural changes.**

1. `schema/fixtures_test.go` (3128 lines) consolidates into golden
   files where possible. Per-form unit tests stay only when the form
   has a non-trivial parse / validate path.
2. `client/pagination_test.go` + `progress_test.go` (~2400 lines)
   collapse into per-variant goldens driven by `internal/testserver/`
   fakes.
3. `client/runner_test.go`, `client/oauth2_test.go` (auth_test.go),
   `client/fanout_test.go`, `client/redact_test.go`,
   `client/trace_test.go`, `client/requests_test.go`: each rewritten
   against the new shape.
4. `cmd/skopos/*_test.go`: smaller; reshape against the new CLI surface.
5. `internal/testserver/*.go`: update each per-template fake to match
   any URL shape change in Slice 16.
6. The full `go test ./...` is green at the end of this slice.

**Downstream impact.** The verification gate for the entire Phase 2.
The schema-reference regeneration (Slice 18) is mechanical once tests
pass.

**Verification.** `go test ./...`.

---

### Slice 18 — Regenerate `docs/schema-reference.md`

**Scope.** Run whatever generator produces `schema-reference.md` and
commit the regenerated output. The current generator wiring lives in
`tools/` (TBD by the slice agent — read the existing file to learn the
build step).

**Files touched.** `docs/schema-reference.md` (regenerated). Possibly
`tools/...` (the generator itself) if the new struct shape needs the
generator updated to walk the new types.

**Downstream impact.** None — this is the last slice.

**Verification.** Diff the regenerated file against the post-Phase-1
prose in `docs/schema.md`. Each declared type should appear in both
with matching field tables.

---

## 3. Cross-cutting rules

Every Phase 2 slice agent must internalise these in addition to the
global rules in [`RESEARCH_PLAN.md`](RESEARCH_PLAN.md) §"Global rules".

1. **The Phase 1 prose docs are the operational reference.** Slice
   agents read `docs/schema.md`, `docs/runtime.md`, `docs/stores.md`,
   `docs/usage.md`, and `docs/api-methods.md` for the post-redesign
   shape. They consult `docs/DESIGN_SUGGESTIONS.md` for the rationale
   only when the prose is ambiguous. They do NOT consult
   `docs/schema-reference.md` (still stale).
2. **No backward compatibility, no migration code** (already global
   rule #1). Delete the old shape in the same commit.
3. **Spot-check smoke tests are encouraged inside a slice, deleted
   before slice close.** Slice 17 is the authoritative test rewrite.
   Inline smoke tests that the author of a slice uses to verify their
   own change are throwaway. Mark them `// SMOKE - DELETE BEFORE SLICE
   CLOSE` if it helps the slice agent track them.
4. **Tests and CI need not pass per slice** (already global rule #6).
   The pre-existing test suite is written against the old shape; every
   slice between 4 and 16 leaves more of it red. Slice 17 brings it
   back green.
5. **Each slice cleans up stale comments in any file it touches**
   (already global rule #5). This includes the package-level `doc.go`
   for each domain.
6. **Per-slice impl plan file is the slice's executable artefact.**
   Each slice agent creates `IMPL-NN-<short-tag>.md` matching the
   filename in the §1 slice table, populates it (scope / old → new
   map / removed list / downstream notes), checks the box in
   `RESEARCH_PLAN.md`, and rewrites `HANDOFF.md` to point at the next
   slice.
7. **One PR per slice; merge in slice order.** Each slice's PR title
   names the slice number and short tag (e.g. `feat(schema): slice 4 —
   struct definitions`).

---

## 4. Verification posture

The project's testing posture is golden-file-heavy: the bulk of
behaviour-shape verification lives in golden inputs/outputs under
`internal/testserver/` + `schema/testdata/` rather than in dense unit
tests (matches commit `f8db414`). This means **end-to-end verification
is only possible once both Slice 16 and Slice 17 land**: templates
provide the inputs, the testserver provides the wire-protocol fakes,
and the test slice activates the golden infrastructure.

Per slice:

| Slice | What builds | What passes                                       |
|-------|-------------|---------------------------------------------------|
| 4-7   | schema      | nothing (existing tests are old-shape)           |
| 8-14  | schema + client | nothing                                        |
| 15    | + cmd       | nothing                                          |
| 16    | all         | every bundled template parses + validates clean  |
| 17    | all         | `go test ./...` green                            |
| 18    | all         | regenerated `schema-reference.md` matches prose  |

A slice agent does not slow themselves down trying to keep stale tests
green between slices 4 and 16. They DO verify the package they own
builds (`go build ./<domain>/...`) and they MAY write throwaway smoke
tests for the hardest paths in their slice, deleted at slice close.

---

## 5. Open questions

None. Every shape decision is locked in `DESIGN_SUGGESTIONS.md`;
every operational decision is locked in the Phase 1 prose docs. A
slice agent who encounters genuine ambiguity flags it in their slice's
`IMPL-NN-*.md` and escalates to the operator rather than guessing.
