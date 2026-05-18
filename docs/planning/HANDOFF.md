# Handoff — Phase 2 Slice 17 (`*_test.go` rewrites across `schema/`, `client/`, `cmd/`, `internal/testserver/`)

Slice 16 closed (every bundled template and every schema test fixture
rewritten against the post-redesign IR). All 27 `templates/*.yml` and all
20 `schema/testdata/*.yml` parse + validate clean; YAML/JSON round-trips
are byte-stable. `go build ./...` is green; `*_test.go` files remain red
against the old shape — those are this slice's job.

`templates/templates.go` was left untouched (no shape coupling; the
`//go:embed *.yml` directive picked up the rewrites transparently).

See [`IMPL-16-templates.md`](IMPL-16-templates.md) for the per-file
rewrite log, removed-content list, walkthroughs of the trickier reshapes
(`async_poll` request-level loop, `oauth2_client_credentials` unified
Cache, `fanout` absolute URLs), and downstream notes for Slices 17 / 18.

The next slice is **Slice 17 — `*_test.go` rewrites across the
project**. This is the verification gate for every preceding code slice
— the package builds have been green since Slice 7, but the test suite
has been frozen against the pre-redesign shape and is the back-stop
that proves the post-redesign code (Slices 4 – 15) and templates
(Slice 16) actually behave end-to-end.

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice — especially #1
   (no-backwards-compat), #4/#5 (clean stale comments), and #6 (tests
   are this slice's purpose, not a side-effect).
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 17"](PHASE-2-PLAN.md#slice-17--all-tests).
   This slice's scope, the file inventory, and the consolidation
   strategy (golden files where the legacy suite had dense per-form
   unit tests).
4. [`IMPL-16-templates.md`](IMPL-16-templates.md) for the rewritten
   template + fixture shapes — your golden inputs.
5. [`docs/schema.md`](../schema.md), [`docs/runtime.md`](../runtime.md),
   [`docs/stores.md`](../stores.md), [`docs/usage.md`](../usage.md),
   [`docs/api-methods.md`](../api-methods.md) — the operational spec
   the tests must verify. The validator's diagnostic surface is in
   [`schema/validate.go`](../../schema/validate.go); every runtime
   contract is documented under one of the post-redesign prose docs.
6. The earlier `IMPL-04` through `IMPL-15` impl plans for the per-file
   shape that each test must verify (the design rationale lives in
   `docs/DESIGN_SUGGESTIONS.md`; consult it only when the prose docs
   are ambiguous).

---

## Your slice

**Branch.** Develop on `claude/slice-17-tests-<token>`.

**Files you may touch (and only these — plus the new
`IMPL-17-tests.md`).**

- Every `*_test.go` in `schema/`. The matrix-coverage fixture suite at
  `schema/fixtures_test.go` (3128 lines today) consolidates around
  goldens; per-form unit tests stay only where the parse / validate
  path is non-trivial.
- Every `*_test.go` in `client/`. Two pairs are very large:
  `client/pagination_test.go` + `progress_test.go` (~2400 lines
  combined) collapse into per-variant golden goldens driven by the
  `internal/testserver/` fakes. `client/runner_test.go`,
  `client/oauth2_test.go` (rename to `auth_test.go` to match
  `auth.go` + the merged `cache.go`), `client/fanout_test.go`,
  `client/redact_test.go`, `client/trace_test.go`,
  `client/requests_test.go` each rewrite against the new shape.
- Every `*_test.go` in `cmd/skopos/`. Smaller. Reshape against the new
  CLI surface (`validate` / `run` / `init` / `template list|show`).
- Every `*.go` file (production code AND tests) under
  `internal/testserver/`. The per-template fakes need URL-shape updates
  for every template that changed endpoint path. Most kept the
  original paths; the etag templates pulled the
  `/etag_conditional/data` and `/etag_conditional_middle/data`
  endpoints. The OAuth2 fakes still serve `/oauth2/token`.

**Files you must NOT touch.**

- Any `*.yml` under `templates/` or `schema/testdata/` (frozen at
  Slice 16's close).
- Any `.go` file outside the `*_test.go` set and `internal/testserver/`
  (schema, client, cmd are frozen at their respective slice closes).
- `docs/schema-reference.md` (Slice 18).

**Deliverables.**

1. **`go test ./...` green** at slice close. This is the slice's
   verification gate.
2. **Consolidation around goldens** where the legacy suite was dense
   per-form unit tests — matches commit `f8db414`'s posture (the
   project's stated testing model). The `schema/testdata/*.yml`
   fixtures and the `templates/*.yml` templates are the natural
   inputs; runtime goldens (events.jsonl + state.json + exchange
   records) live under a sibling directory the slice agent names. Per-
   form unit tests stay where the form has a non-trivial parse /
   validate / lower path.
3. **`internal/testserver/` fakes match the rewritten template URLs.**
   The fakes drive the runner end-to-end goldens; their handler maps
   stay 1:1 with the templates that exercise them.
4. **Hygiene pass.** Per global rules #4/#5: no `// formerly`,
   `// see slice N`, `// for backwards compat` lines anywhere. Every
   test that touches a file with stale top-of-file vocabulary fixes the
   vocabulary as part of the test rewrite.
5. **New artefact `docs/planning/IMPL-17-tests.md`** carrying:
   - Scope (one sentence).
   - Per-package rewrite log (old → new test count; what consolidated
     into goldens; what per-form tests remained; cross-package shared
     fixtures).
   - The runtime-golden directory layout (path + naming convention +
     how the fakes feed the goldens).
   - Build-state enumeration (`go test ./...` green; `go vet ./...`
     clean).
   - Walkthrough verification (one or two of the trickier rewrites —
     `client/runner_test.go`'s drain lifecycle, `client/oauth2_test.go`
     (renamed `auth_test.go`)'s cache invalidation path,
     `schema/fixtures_test.go`'s consolidation strategy).
   - Notes for Slice 18 on what to expect from the schema-reference
     regenerator now that the type set is fully exercised by tests.
6. Slice-table row 17 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-17-tests.md` link.
7. `HANDOFF.md` rewritten to point at Slice 18 (`docs/schema-reference.md`
   regeneration).

**Verification.**

- `go test ./...` is green.
- `go vet ./...` is clean (zero warnings, including in test files).
- `go test -race ./client/...` is green for the runner / sink-concurrency
  tests (the runner is single-goroutine but the sink contract is mutex-
  guarded — see `docs/runtime.md` §11).

---

## Watch out for

- **Many existing tests reference removed shapes.** `schema.TokenCache`,
  `schema.RequestCache`, `schema.State` (the old wrapper type), the
  `Defaults` block, `cursor.*`, `progress.async_job`, the seven legacy
  pagination variants, `placeholder_event` — none of these survive.
  When a test references a removed type or field, delete the test (the
  shape it pinned no longer exists); when a test references a shape
  whose new spelling differs, rewrite the test.

- **Empty-page progress is the new behaviour, not an opt-in.** Tests
  that previously asserted "progress fires only on non-empty pages"
  are obsolete — the new contract (`docs/runtime.md` §5) is "progress
  fires once per accepted page-response, including empty pages." The
  rewritten tests should pin that contract directly.

- **`error.mode: standard` keeps progress writes that already fired.**
  The deferred `Save` runs on every exit (normal, warn, fail). Tests
  that previously asserted "no state survives a standard error" should
  be replaced by tests asserting "state writes from completed pages
  survive a mid-drain error."

- **`cache.*` is process memory only.** Tests must verify that
  `Store.Save` does NOT persist `cache.*` (and that a runner restart
  forces a fresh OAuth2 / session-login fetch). The legacy
  `state.<store_in>_expires_at` slot convention is gone — `cache.<name>`
  carries the value AND its expiry internally.

- **`requests[].terminate_when:` is the async-job loop primitive.** Tests
  that previously walked the `progress.async_job` phase machine should
  exercise the three-request chain pattern with a `terminate_when:` on
  the poll step instead.

- **String interpolation is everywhere.** Tests that previously asserted
  on `Concat`-shaped Values should accept the desugared form (a string
  literal in a Value position desugars to `{concat: [...]}` when it
  contains `${...}` segments). The codec round-trips the desugared form
  byte-stably; tests should compare the parsed-and-marshalled YAML, not
  the source string.

- **`cursor_token_placeholder.yml` was repurposed.** Slice 16 turned
  it into an empty-page progress fixture. Any legacy test that
  reached for it as a placeholder-event fixture needs to be deleted
  or retargeted at the new role.

- **`internal/testserver/` is shared across packages.** Tests in
  `client/` and `cmd/skopos/` import the same handlers. Coordinate
  URL-shape changes so neither package breaks the other — make the
  testserver match the rewritten templates, then update every consumer
  in one pass.

- **`go test -race` is the bar for the runner.** A bug here would
  surface only under race, not under plain `go test`. Run it before
  declaring slice-close.

---

## When you finish

`git add` every rewritten `*_test.go`, every modified file under
`internal/testserver/`, the new `IMPL-17-tests.md`, the updated
`RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit with a message
like:

```
test: slice 17 — *_test.go rewrites across schema/, client/, cmd/,
internal/testserver/ against the new IR

Every *_test.go rewritten against the post-redesign IR. The matrix-
coverage suites in schema/fixtures_test.go and client/{pagination,
progress}_test.go consolidate around golden inputs from templates/
and schema/testdata/ plus per-template fakes in internal/testserver/.
oauth2_test.go renames to auth_test.go to match auth.go + the merged
cache.go. Empty-page progress, request-level terminate_when async
loops, cache.* process-memory semantics, and the unified Cache block
are pinned by new goldens.

go test ./... and go test -race ./client/... are green. The full
Phase 2 surface is verified end-to-end.
```

Push to `claude/slice-17-tests-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
