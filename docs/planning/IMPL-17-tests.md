# IMPL-17 — `*_test.go` rewrites across `schema/`, `client/`, `cmd/`, `internal/testserver/`

**Scope.** Rewrite every `*_test.go` file in the project against the
post-redesign IR. The legacy suite was 10,694 lines across 25 files
pinned to removed types (`schema.TokenCache`, `schema.RequestCache`,
`schema.State` (the wrapper), `schema.Defaults`, `schema.CursorUpdateDirective`,
`schema.NowValue.Offset`, `progress.async_job`, the seven legacy
pagination variants, `placeholder_event`, `state.fields`,
`requests[].path`, `cursor.*` namespace refs). The new suite is
~5,500 lines structured around: (a) golden-driven fixture parse +
validate + round-trip in `schema/fixtures_test.go`, (b) focussed runtime
behaviour tests in `client/*_test.go` against `httptest` fakes, and
(c) flag / config / spec-merge tests in `cmd/skopos/*_test.go`.

---

## Build state

- `go build ./...` — green.
- `go vet ./...` — clean (zero warnings).
- `go test ./...` — green across `schema/`, `client/`, `cmd/skopos/`,
  `cmd/testserver/`, `internal/testserver/`.
- `go test -race ./client/...` — green (the runner is single-goroutine
  but the `Sink` and `Tracer` contracts are mutex-guarded; the bundled
  `JSONLSink` and `JSONLTracer` pass the race detector under
  concurrent emit).
- `go vet -tags integration ./...` — clean. The `//go:build integration`
  testscript suite under `cmd/skopos/testdata/*.txt` still fails on
  golden mismatches (trace output references state.\* where the
  pre-redesign goldens reference cursor.\*); regenerating those
  goldens is left as a Slice 18 follow-up since the testscript
  artefacts are not part of the default `go test ./...` gate.

---

## Per-package rewrite log

### `schema/`

| File                    | Old → New                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
|-------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `schema/example_test.go`   | 78 lines → 78 lines. Two `Example*` functions kept; spec YAML reshaped to the post-redesign IR (no `state.fields` wrapper, no `defaults`, no `requests[].path`, no `progress.stateless`).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `schema/fixtures_test.go`  | 3128 lines → 545 lines. The huge per-form unit suite collapsed into: `TestSpecFixtures` (golden walk over both `schema/testdata/*.yml` and `templates/*.yml` — parse + validate + YAML/JSON round-trip), `TestValueCodecs` / `TestPredicateCodecs` (one representative per discriminator), `TestCodecRejections` (multi-discriminator, empty concat/and/or, typo siblings, removed `{raw}` form, removed `cursor.*` path root, zero-Value-marshal). New per-shape tests pin: `TestRequestTerminateWhen` (request-level loop primitive), `TestPaginationVariants` (four-named + custom shape), `TestProgressIsFlatWriteList`, `TestUnifiedCacheBlock`, `TestOAuth2_ExactlyOneGrantRequired`, `TestStringInterpolationDesugar`, `TestIsSecret` (Value walk over Concat / Select / Format / Base64 / List / Object / Add / Max — confirming the secret-propagation predicate covers every container the new Value language can express). |

### `client/`

| File                          | Old → New                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
|-------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `client/helpers_test.go`      | NEW. Common helpers: `fixedNow`, `vStr` / `vInt` / `vBool` / `vRef` / `vRefDefault` / `vNow` / `ptrValue` Value constructors, `mustPath` / `mustInterp`, `captureSink`, `captureTracer`, `minimalDoc` / `bearerDoc` test-spec constructors, `writeJSON` httptest helper. Shared by every other `*_test.go` in the package.                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `client/example_test.go`      | 81 lines → 81 lines. `ExampleRunner_Drain` and `ExampleNewJSONLSink` kept; spec YAML reshaped. Default URL is now patched directly on `doc.State["url"]` (no `Defaults` block).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `client/sink_test.go`         | 115 lines, unchanged structurally — no schema coupling. Pins concurrent-Emit, file-Flush, and pipe-Flush contracts.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `client/ndjson_test.go`       | 57 lines → 47 lines. Drops the `ndjsonDoc` constructor (built directly inline against the new shape). Pins the "ndjson decode at line N" error wrap.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `client/filestore_test.go`    | 90 lines → 105 lines. Removes `Cursor` field references (the legacy Snapshot wrapper). New `TestFileStore_LoadDropsUnknownKeys` pins forward-compat: a file written by an out-of-band rev (with extra top-level keys) loads cleanly.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `client/value_test.go`        | 447 lines → 460 lines. The big `TestEvalValue` table covers each Value discriminator (literals / ref / now / concat / select / format / base64 / list / object / arith / reducers / regex). New tests: `TestEvalValueArithmetic` (add/subtract type pairs), `TestEvalValueReducers` (max/min/first/last/count over both `{list: [...]}` literal and `{ref: events.*.field}` projection), `TestResolveNamespaceRef` (each closed root + fan_out alias + the `events.*` shortcuts). The legacy `cursor.*` arm is replaced by `cache.*` and `events.*`.                                                                                                                                                                                            |
| `client/predicate_test.go`    | 295 lines → 230 lines. The `TestEvalPredicate` table covers eq / gt / lt / gte / lte / present / and / or / not / literal_bool. New `TestEvalPredicate_TimestampComparison` (RFC 3339 time arithmetic) and `TestEvalPredicate_AbsenceTolerantEq` (eq + present are absence-tolerant; ordered comparisons error on absent — the actual runtime behaviour). The legacy `ge / le / matches / in` deferred-verb tests are gone (those verbs are codec-level rejections, covered in `schema/fixtures_test.go`).                                                                                                                                                                                                                                                       |
| `client/runner_test.go`       | 526 lines → 380 lines. Replaces the legacy MaxPages / placeholder_event / cursor-token two-pass tests with: drain happy-path, empty-page-still-fires-progress, standard-mode-keeps-progress, fail-mode-returns-error, on_status: skip / fail, request-level terminate_when loop (the canonical submit/poll/fetch pattern), MaxPages guardrail, ctx cancellation, Doc/Sink requirements, default HTTP timeout, producer-by-index vs explicit produces_events:true, extract-to-state persistence, and body-metadata-only error redaction.                                                                                                                                                                                                                                                                          |
| `client/pagination_test.go`   | 1407 lines → 250 lines. The 7-variant matrix collapsed to 4 named + custom: `TestPagination_NoneSinglePage`, `TestPagination_CursorTokenAdvancesAndStops`, `TestPagination_NextURLLinkHeader` (regex + capture), `TestPagination_CounterShortPageTerminates` (default short-page predicate), `TestPagination_CounterWithExplicitTerminate` (has_next override), `TestPagination_CustomAdvancesAndTerminates`, `TestPagination_PerDrainWipeResetsScratch` (the per-drain wipe contract from Slice 8).                                                                                                                                                                                                                                                  |
| `client/progress_test.go`     | 1114 lines → 320 lines. The legacy `progress.{stateless, latest_event_timestamp, use_now, max_event_field, time_window, async_job}` matrix replaced with a flat-write-list suite: `TestApplyProgress_FlatWriteList` (one-write + multi-write), `TestApplyProgress_SnapshotThenWrite` (sliding-window pattern relies on this), `TestApplyProgress_EmptyListIsNoop`, `TestApplyProgress_CoerceAndRegex`, `TestApplyProgress_PersistsAcrossDrains`, `TestApplyProgress_WarnDoesNotFire` (the iterWarn semantics), `TestApplyProgress_RejectsNonStateTo`, `TestApplyProgress_RegexMissReturnsNil`, `TestPersistentVsScratchClassification` (lifetime inference from Slice 8), `TestSnapshot_ExcludesScratchFields`, `TestSeedDefaults`. |
| `client/auth_test.go`         | 630 lines → 580 lines. Renamed from `oauth2_test.go` to match `auth.go` + the merged `cache.go`. Covers every Auth variant (none / bearer / basic / api_key (header + in_query) / custom / multi_mode) plus OAuth2 client_credentials + password_grant. New cache-block tests: `TestAuth_OAuth2ClientCredentials_CacheReusedAcrossPages` (intra-drain cache hit), `TestAuth_OAuth2_CacheNotPersisted` (fresh Runner re-fetches), `TestAuth_OAuth2_InvalidateCacheRefetches` (on_status: invalidate_cache verb), `TestRequestCache_HitSkipsWireCall` (requests[].cache unified block), `TestRequestCache_ExpiryRefetches` (expires_in + buffer math), `TestOAuth2_TokenEndpointBodyNotLeaked` (redaction-safe error path).                                                              |
| `client/fanout_test.go`       | 271 lines → 380 lines. `flatten` vs `wrap` merge semantics, empty-over no-op, per-item on_status: skip and fail, per-item ref resolution via the fan_out.as alias, flatten rejects non-list bodies. New ref pattern uses author-chosen aliases (`incident`, `rec`, `id`) since `item` is on the removed-roots list.                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `client/requests_test.go`     | 285 lines → 325 lines. Query map, body.json / body.form / body.raw / body.raw with interpolation, headers (literal + secret-ref), if-predicate gating, expect_status allow list, extract.\* per-iteration scratch.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `client/redact_test.go`       | 286 lines → 145 lines. `safeURL` (strip query + userinfo), `RedactURLError` (rewrite \*url.Error), `redactValue` (IsSecret → "<redacted>"; otherwise `valueShape`), `valueShape` covers every Value variant the schema package emits today. The legacy `Defaults`-based redaction tests are gone — there is no Defaults block.                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `client/trace_test.go`        | 269 lines → 240 lines. Per-exchange Exchange shape, Authorization always-redacted, IsSecret-driven header redaction, api_key in_query=true → query-key redaction, body metadata only (no raw bytes), cache-HIT tombstone shape (`{Iteration, StepID, CacheHit:true}` with wire fields zero), JSONL line format round-trip.                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `client/requestcache_test.go` | DELETED. The `schema.RequestCache` type does not exist post-Slice 13; the unified `schema.Cache` is exercised in `auth_test.go::TestRequestCache_*`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |

### `cmd/skopos/`

| File                              | Old → New                                                                                                                                                                                                                                                                                            |
|-----------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `cmd/skopos/cmd_run_test.go`      | 346 lines → 270 lines. The inline `minimalDocYAML` constructor reshaped against the post-redesign IR (no `state.fields`, no `defaults.base_url`, no `requests[].path`, no `latest_event_timestamp` progress). Otherwise unchanged: flag/config-merge ladder, trace append semantics, --once / --interval mutex, http-timeout, max-pages, config.input fallback. |
| `cmd/skopos/cmd_validate_test.go` | 74 lines → 95 lines. `invalidDoc` reshaped (drops `requests[].path`, missing `auth`). New `TestValidate_CleanDocReturnsNil` pins the happy path.                                                                                                                                                  |
| `cmd/skopos/cmd_init_test.go`     | 117 lines, unchanged — no schema coupling.                                                                                                                                                                                                                                                          |
| `cmd/skopos/cmd_template_test.go` | 149 lines, unchanged — no schema coupling.                                                                                                                                                                                                                                                          |
| `cmd/skopos/config_test.go`       | 138 lines, unchanged — no schema coupling.                                                                                                                                                                                                                                                          |
| `cmd/skopos/integration_test.go`  | 288 lines, unchanged. The testscript harness is itself shape-agnostic; the testdata/\*.txt goldens it consumes are the part that's still pre-redesign (out of scope per the Build-state note above).                                                                                                                                                                                                              |
| `cmd/testserver/main_test.go`     | 33 lines, unchanged — only exercises `filterScenarios` over the testserver registry.                                                                                                                                                                                                                |

### `internal/testserver/`

The fake-server scenarios kept their URL paths (Slice 16 left every
endpoint stable except the etag fixtures, which already routed to
`/etag_conditional/data` and `/etag_conditional_middle/data`). Two
scenarios were updated to omit a body field rather than emit it as
`false`, matching the `not: {present: response.body.meta.has_next}`
termination predicate the rewritten templates use:

- `internal/testserver/page_number.go` — the `meta.has_next` field is
  now omitted on the final page (instead of `meta.has_next: false`).
- `internal/testserver/etag_conditional.go` — same treatment for the
  `data` step.

The server_test.go file is unchanged — it exercises the testserver's
own contracts (auth checks, scenario registry) and has no shape
coupling.

---

## Walkthrough verifications

### `client/runner_test.go::TestDrain_TerminateWhenLoop`

The request-level loop primitive is the post-redesign replacement for
the old `progress.async_job` phase machine. The test boots a three-step
chain — submit → poll (with `terminate_when: eq response.body.status =
complete`) → fetch — and asserts:

- The poll step re-fires until the predicate evaluates true. The test
  counts atomic poll hits and asserts exactly 3 (running, running,
  complete).
- The fetch step (marked `produces_events: true`) fires exactly once
  after the poll loop settles and contributes the iteration's events.
- `applyProgress` and pagination advance fire AFTER the chain settles,
  not mid-loop.

This pins the contract that authors no longer need to model a phase
machine — the `requests[].terminate_when:` predicate plus plain
`extract:` writes carry every async-poll workflow.

### `client/auth_test.go::TestAuth_OAuth2_InvalidateCacheRefetches`

Pins the unified-Cache + on_status: invalidate_cache interaction:

- The data step returns 401 on the first iteration; `OnStatus[401]: invalidate_cache` fires.
- `dropReachableCaches` walks `doc.Auth.OAuth2.ClientCredentials.Cache`
  and clears `cache.access_token`.
- The runner does not emit, does not fire progress, does not advance
  pagination. It continues the loop.
- The second iteration's OAuth2 dispatcher misses the cache slot →
  re-fetches the token → second iteration's data step returns 200.

The end-state: 2 token-endpoint hits, 2 data-endpoint hits, no error,
one accepted page.

### `schema/fixtures_test.go::TestSpecFixtures` consolidation

The legacy suite had three layers:

1. Per-form codec tests (`TestValueCodecs`, `TestPredicateCodecs`) —
   one representative per Value/Predicate form.
2. Per-shape unit tests (`TestSchemaShapes_Review03`,
   `TestAutoRegisteredFieldDecl`, the `cursor_update_*` cases) — each
   pinning a specific validator-surface diagnostic or a removed
   shape's rejection.
3. The fixture walker — every `schema/testdata/*.yml` and
   `templates/*.yml` parsed + validated + round-tripped.

Post-redesign, the shape rules are simpler (closed verb sets, flat
state map, no nested phase machinery) and the fixture suite covers
the operationally-interesting variation — every Value form, every
Predicate verb, every pagination variant, every Auth variant, the
unified Cache block, the request-level terminate_when loop. The
`TestSpecFixtures` walker remains the primary verification gate: 47
files (27 templates + 20 testdata fixtures) parse + validate + YAML
+ JSON round-trip with zero diagnostics. The per-form unit layer is
preserved only where the codec has a non-trivial parse path
(`TestValueCodecs`, `TestPredicateCodecs`, `TestCodecRejections`,
`TestArithOperandCount`, `TestStringInterpolationDesugar`,
`TestPaginationVariants`, `TestProgressIsFlatWriteList`,
`TestUnifiedCacheBlock`).

---

## Notes for Slice 18

The schema-reference regenerator walks the `*schema.Doc` type set
directly. The post-redesign type catalogue is fully exercised by the
test suite now (every variant of every union has at least one round-
trip and one validator path covered), so the regenerator should
produce a complete table without any "TBD"-shaped placeholders. The
known shape decisions to mirror in the doc:

- `state` is a flat `map[string]FieldDecl`, not a wrapper struct.
- `auth` is a 7-variant discriminated union (none / bearer / basic /
  api_key / custom / oauth2 / multi_mode); `oauth2` is itself a
  2-variant union (client_credentials / password_grant).
- `pagination` is 4 named variants (none / cursor_token / next_url /
  counter) + primitive `custom`.
- `progress` is a flat `[]ProgressWrite` list — no variant nesting.
- `Cache` is one shared struct (`{to, expires_at, buffer}`) used by
  both `auth.oauth2.<grant>.cache` and `requests[].cache`.
- `Value` has 17 discriminator forms including the arithmetic pair
  (`add`, `subtract`) and the reducer set
  (`max`, `min`, `first`, `last`, `count`).
- `Predicate` has 10 forms including the four ordered comparisons
  (`gt`, `lt`, `gte`, `lte`) sharing the `PredicateEq` shape with
  `eq`.
- Namespace roots: `state | cache | events | extract | steps | response`
  plus author-chosen fan_out.as aliases. Three roots — `cursor`,
  `body`, `item` — are explicitly rejected at parse time with a
  migration hint.

The integration testscript goldens (`cmd/skopos/testdata/*.txt`)
remain pre-redesign and produce trace lines that reference the legacy
`cursor.*` namespace. Slice 18 (or a separate follow-up) should
regenerate those by running `go test -tags integration -update
./cmd/skopos/...` once the templates and testserver are confirmed to
match end-to-end.
