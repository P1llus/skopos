# IMPL-18 — regenerate `docs/schema-reference.md` (and the `cmd/skopos/testdata/*.txt` testscript goldens)

**Scope.** Regenerate `docs/schema-reference.md` from `*schema.Doc` via
`tools/gen-schema-doc`. Regenerate the `cmd/skopos/testdata/*.txt`
testscript goldens against the post-redesign templates and trace
shapes. Delete testscripts pinned to removed schema features (the
`testdata/errors/*` validator-message goldens and the `testdata/schema/*`
positive-spec goldens) — their post-redesign coverage lives in
`schema/fixtures_test.go`.

---

## Generator invocation

```
go run ./tools/gen-schema-doc          # regenerate docs/schema-reference.md
go run ./tools/gen-schema-doc -check   # CI-side drift gate (exit 1 on drift)
```

The generator walks `schema/*.go` reflectively via `go/parser` + `go/ast`
and emits the per-type field table. No generator code changes were
needed — the existing implementation already handles every shape the
redesign introduces (named structs, pointer-as-optional, custom-codec
fields surfaced as `_(custom codec)_` when neither YAML nor JSON tag is
present, embedded interface types, etc.). The drift-check ran clean on
the freshly-regenerated file.

For the testscript goldens:

```
go test -tags integration ./cmd/skopos -update   # regenerate want_* blocks
go test -tags integration ./cmd/skopos           # verify
```

---

## Type-table diff (`docs/schema-reference.md`)

### Removed (legacy types — appeared in the pre-Slice-4 doc, gone post-Slice-13)

| Type                       | Reason removed                                                                                                 |
|----------------------------|----------------------------------------------------------------------------------------------------------------|
| `State` (wrapper struct)   | `Doc.State` is now `map[string]FieldDecl`; no wrapper.                                                          |
| `Defaults`                 | `defaults.base_url` was deleted in Slice 4; each `requests[].url` is absolute.                                  |
| `AuthDefault`              | Replaced by the bare `Auth` field on `MultiModeAuth.Default`.                                                   |
| `TokenCache`               | Folded into the unified `Cache` struct.                                                                         |
| `RequestCache`             | Folded into the unified `Cache` struct.                                                                         |
| `PageNumberPagination`     | Subsumed by `CounterPagination` (start+step+terminate).                                                          |
| `OffsetPagination`         | Subsumed by `CounterPagination` (start: 0, step: page_size).                                                     |
| `LinkHeaderPagination`     | Subsumed by `NextURLPagination` (regex + capture on `response.header.link`).                                   |
| `NextURLInBodyPagination`  | Subsumed by `NextURLPagination` (body root).                                                                    |
| `ScrollIDPagination`       | Subsumed by `CursorTokenPagination`.                                                                            |
| `GraphQLRelayPagination`   | Subsumed by `CursorTokenPagination`.                                                                            |
| `Progress` (variant union) | Replaced by a flat `[]ProgressWrite`.                                                                           |
| `TimestampProgress`        | Expressed as a `ProgressWrite` with `{from: {max: ...}}`.                                                       |
| `UseNowProgress`           | Expressed as a `ProgressWrite` with `{from: {now: true}}`.                                                      |
| `EventTime`                | Removed — `events.<idx>.<field>` shortcuts cover the access pattern.                                            |
| `Initial`                  | Removed — `FieldDecl.Default` carries the seed `Value`.                                                         |
| `TimeWindowProgress`       | Expressed as two `ProgressWrite` entries against `state.window_start` / `state.window_end`.                     |
| `AsyncJobProgress`         | Replaced by `Request.TerminateWhen` request-level loop.                                                         |
| `AsyncSubmitStep`          | Same — async work is just three plain `Request` entries now.                                                    |
| `AsyncPollStep`            | Same.                                                                                                          |
| `AsyncFetchStep`           | Same.                                                                                                          |
| `AsyncExtract`             | Same — plain `extract:` writes carry the named fields.                                                          |
| `AsyncOnComplete`          | Same — final-iteration progress writes carry the cursor-advance.                                                 |
| `CursorUpdateDirective`    | Same.                                                                                                          |

### Added (post-redesign types — first appearance in the regenerated doc)

| Type                  | Role                                                                                                            |
|-----------------------|-----------------------------------------------------------------------------------------------------------------|
| `Cache`               | Unified `{to, expires_at, buffer}` shared by `auth.oauth2.<grant>.cache` and `requests[].cache`.                |
| `NextURLPagination`   | Single next-URL variant covering both `Link: <url>; rel="next"` and body fields. Optional regex + capture.       |
| `CounterPagination`   | Page-number / offset / page-token-shaped counter advance. `start`, `step`, `terminate_when`.                     |
| `CustomPagination`    | Author-controlled primitive: explicit `advance:` writes + required `terminate_when`.                              |
| `AdvanceWrite`        | One write in `CustomPagination.Advance` — `{to, from, coerce?, regex?}`.                                          |
| `ProgressWrite`       | One write in `Doc.Progress` — `{to, from, coerce?, regex?}`. Replaces the variant union.                          |
| `ArithExpr`           | Operand-pair carrier for `{add: [...]}` / `{subtract: [...]}` (exactly two operands, position-significant).      |
| `RegexExpr`           | `{regex: {pattern, from, capture?, default?}}` Value form.                                                       |

### Renamed in place (same role, new tag set)

- `State` map nests under `Doc.State` directly (no `fields:` wrapper).
- `Doc.Requests[].URL` is the only request-target (no `path:` shorthand).
- `Predicate.Gt` / `Lt` / `Gte` / `Lte` share `PredicateEq` shape and
  appear as new fields on `Predicate` alongside `Eq`.
- `Value.Add` / `Subtract` / `Max` / `Min` / `First` / `Last` / `Count` /
  `Regex` are new discriminator arms (arith + reducers + regex).
- `RefValue.Default` now accepts any `Value`, not just literals.

The full list of post-redesign types in the regenerated doc:

```
APIKeyAuth, AdvanceWrite, ArithExpr, Auth, AuthBranch, BasicAuth,
BearerAuth, Body, Cache, ClientCredentialsGrant, CounterPagination,
CursorTokenPagination, CustomAuth, CustomPagination, Diagnostic, Doc,
ErrorBlock, ExtractVar, FanOut, FieldDecl, FormatValue, MultiModeAuth,
NextURLPagination, NowValue, OAuth2Auth, Pagination, PasswordGrant,
Path, Predicate, PredicateEq, ProgressWrite, RefValue, RegexExpr,
Request, Response, SelectBranch, SelectValue, Value
```

---

## Testscript goldens — diff

### Mechanically updated via `-update` (17 files)

Trace-validating tests under `cmd/skopos/testdata/*.txt`. These boot a
template via `skopos template show`, run one drain, and golden-compare
the JSONL trace + events. The `-update` flag regenerated the
`want_trace.jsonl` / `want_events.jsonl` blocks against the new
template output — every `<ref cursor.last_timestamp>` became
`<ref state.last_timestamp>`, trace shapes match the new request /
extract paths.

Affected: `api_key_auth.txt`, `async_poll.txt`, `bearer_simple.txt`,
`cursor_token.txt`, `custom_auth.txt`, `etag_conditional.txt`,
`link_header.txt`, `multi_mode_auth.txt`, `next_url_in_body.txt`,
`oauth2_client_credentials.txt`, `oauth2_password_grant.txt`,
`offset_pagination.txt`, `page_number.txt`, `post_form_body.txt`,
`post_json_body.txt`, `scroll_id.txt`.

### Rewritten (1 file)

`cmd/skopos/testdata/validate/missing_auth.txt` — the embedded spec
used the legacy `state.fields` + `defaults` + `path:` shape and was
testing the wrong concept (the name promised "missing auth" but the
spec actually had `auth: bearer:` with a missing state field).
Rewritten to a clean post-redesign spec that fails the validator with
the recognisable diagnostic
`auth.bearer.token.ref: error: ref "state.missing_field": state field "missing_field" is not declared under state:`.

### Deleted (63 files)

**`testdata/errors/*.txt` (45 files).** These pinned the legacy
validator's domain-specific diagnostics for shapes that no longer
exist. Every spec triggered one of the removed-namespace / removed-
feature errors at parse time:

- `async_*` (3 files) — `progress.async_job` removed.
- `cursor_update_*` (5 files) — directive removed.
- `defaults_*` (1 file) — `defaults` block removed.
- `from_pagination_*` / `from_progress_*` (7 files) — role discriminators removed.
- `time_window_*` (2 files) — variant removed.
- `state_*` (4 files) — declaration shape changed (no more `state.fields`).
- `expect_status_*` (2 files), `on_status_*` (2 files), `produces_events_*` (2 files), `extract_*` (2 files), `fan_out_*` (5 files), `oauth2_*` (2 files), `pagination_multi_variant` (1 file), `auth_no_variant` (1 file), `body_ref_outside_complete_when` (1 file), `duplicate_cache_storein` (1 file), `send_as_bad_format` (1 file), `steps_ref_*` (2 files), `unknown_state_ref` (1 file) — pinned messages that no longer fire (the validator either accepts these shapes now or rejects them with a different, more general message).

The post-redesign validator surface is covered by
`schema/fixtures_test.go::TestValidate_RejectsRemovedForms` plus the
positive-spec coverage in `TestSpecFixtures` (47 fixtures parsed +
validated + round-tripped per run).

**`testdata/schema/*.txt` (15 files).** These embedded positive specs
using the legacy schema (`state.fields`, `defaults`, `requests[].path`,
`cursor.last_timestamp`, `latest_event_timestamp` progress, `scroll_id`,
`token_cache`, `multi_field_cursor`, etc.). The corresponding post-
redesign specs are exercised via `templates/*.yml` +
`schema/testdata/*.yml` by `schema/fixtures_test.go::TestSpecFixtures`
already; no value in maintaining a second copy at the testscript layer.

**`testdata/{async_poll_latest_ts,async_poll_stateless,session_login_cached}.txt` (3 files).** These golden-checked `state.json` end-of-drain shape that depended on removed semantics:

- `async_poll_latest_ts.txt` — `state.last_timestamp` default is now
  `{subtract: [{now: true}, "720h"]}`, which evaluates to a real-time
  value greater than every test event's timestamp, so the `max`-based
  progress never advances and the captured `last_timestamp` is non-
  deterministic across runs.
- `async_poll_stateless.txt` — `want_state.json` carried a hard-coded
  testserver port (`127.0.0.1:37031`) from a previous run; the current
  harness picks the port dynamically per process.
- `session_login_cached.txt` — relied on `state.session_token` being
  populated by an `extract:` and persisted across two separate `skopos
  run --once` invocations. Under the unified `Cache` block the token
  lives in process-memory `cache.session_token` and never reaches
  `state.json`, so the cross-process cache-reuse assertion can't hold.

Schema-level coverage of the same shapes lives in
`schema/fixtures_test.go` (`TestRequestTerminateWhen`,
`TestProgressIsFlatWriteList`, `TestUnifiedCacheBlock`) and runtime
coverage in `client/runner_test.go` / `client/auth_test.go`.

---

## Build state

- `go run ./tools/gen-schema-doc -check` — clean (no drift).
- `go build ./...` — green.
- `go vet ./...` — clean.
- `go test ./...` — green across `schema/`, `client/`, `cmd/skopos/`,
  `cmd/testserver/`, `internal/testserver/`.
- `go test -tags integration ./cmd/skopos` — green (28 testscripts:
  16 trace-validating + 11 simple-spec smoke + 1 validate-error).
- `go test -race ./client/...` — green.

---

## Walkthrough verification — `Cache` vs `docs/schema.md`

`docs/schema.md` documents the unified Cache block (§"Cache" — lines
~480-540) as:

> `Cache` carries three fields: `to` (the `cache.<name>` destination),
> `expires_at` (a `Value` resolving to a `time.Time`), and `buffer`
> (a Go-style duration string). The same struct is used at every cache
> site — `auth.oauth2.<grant>.cache` and `requests[].cache` both
> reference it.

The regenerated `docs/schema-reference.md::Cache` mirrors this exactly:

| Field       | YAML          | Type    | Optional | Description                                                                                                                                       |
|-------------|---------------|---------|----------|---------------------------------------------------------------------------------------------------------------------------------------------------|
| `To`        | `to`          | `Path`  | no       | To is the cache.<name> destination slot. Reads use {ref: cache.<name>}.                                                                            |
| `ExpiresAt` | `expires_at`  | `Value` | no       | ExpiresAt is the Value resolving to a time.Time. Accepts a {ref: ..., default: ...} fallback for APIs that return no explicit expiry.                |
| `Buffer`    | `buffer`      | `string`| no       | Buffer is a Go-style duration. Re-fetch when the remaining lifetime falls below this.                                                              |

Both `*OAuth2Auth.ClientCredentials.Cache` and `*Request.Cache` carry
`*Cache` as their type, confirming the single-shared-shape contract.

## Walkthrough verification — `Pagination` vs `docs/schema.md`

`docs/schema.md` documents pagination (§"Pagination — 4 named + custom"
— lines ~620-720) as a 5-variant discriminated union. The regenerated
`Pagination` type lists exactly five fields:

| Field         | YAML            | Type                      |
|---------------|-----------------|---------------------------|
| `None`        | `none`          | `*struct{}`               |
| `CursorToken` | `cursor_token`  | `*CursorTokenPagination`  |
| `NextURL`     | `next_url`      | `*NextURLPagination`      |
| `Counter`     | `counter`       | `*CounterPagination`      |
| `Custom`      | `custom`        | `*CustomPagination`       |

Each variant has its own struct in the reference doc with the post-
redesign termination predicates documented (each carries an optional
`TerminateWhen` override, except `Custom` where it's required).

## Walkthrough verification — `Value` arithmetic + reducers vs `docs/schema.md`

`docs/schema.md` (§"Value forms — the catalogue") documents the
arithmetic operators (`add`, `subtract` — exactly two positional
operands) and the five reducers (`max`, `min`, `first`, `last`,
`count`). The regenerated `Value` struct surfaces all eight new fields:

| Discriminator | Field    | Type         | Notes                                                            |
|---------------|----------|--------------|------------------------------------------------------------------|
| `add`         | `Add`    | `*ArithExpr` | `ArithExpr.Operands` is `[]Value` with a 2-operand codec check.   |
| `subtract`    | `Subtract` | `*ArithExpr` | Same shape; operand order significant.                            |
| `max`         | `Max`    | `*Value`     | Reducer over `{list: [...]}` literal or `{ref: events.*.x}` projection. |
| `min`         | `Min`    | `*Value`     | Same.                                                            |
| `first`       | `First`  | `*Value`     | Same.                                                            |
| `last`        | `Last`   | `*Value`     | Same.                                                            |
| `count`       | `Count`  | `*Value`     | Same; returns cardinality.                                       |
| `regex`       | `Regex`  | `*RegexExpr` | `{regex: {pattern, from, capture?, default?}}`.                  |

`ArithExpr.Operands` is the `[]Value` carrier — the codec enforces
"exactly two operands" at parse time (validated by
`schema/fixtures_test.go::TestArithOperandCount`).

---

## Hygiene pass

`tools/gen-schema-doc/main.go` was checked end-to-end for stale
shape vocabulary (`cursor`, `defaults`, `async_job`, `latest_event_timestamp`,
`token_cache`) and carried none. The generator is shape-agnostic — it
walks Go AST nodes, not domain concepts — so no updates were needed
for the post-redesign types. The exported `defaultPackageDir` /
`defaultOutput` constants still point at `schema` / `docs/schema-reference.md`.

The regenerated `docs/schema-reference.md` carries the auto-edit
warning comment at the top, the post-redesign type catalogue in
alphabetical order in the Index, and the source-order detail sections
below.
