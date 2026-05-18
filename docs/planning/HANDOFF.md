# Handoff — Phase 2 Slice 14 (`client/{sink,redact,trace,filestore,doc}.go`)

Slice 13 closed (`client/{http,auth,cache,fanout}.go` rewritten
against the new IR; `oauth2.go` + `requestcache.go` merged into a
single `cache.go` carrying the unified `Cache` runtime — one
`cacheGet` / `cacheStore` pair backs both `auth.oauth2.<grant>.cache`
and `requests[].cache`; cache slots live in `scope.cache` (process
memory only, never persisted); `http.go` no longer composes
`Defaults.BaseURL + req.Path` — every request evaluates its absolute
`url:` Value directly; `executeRequest` consults `req.Cache` itself,
returning a synthetic `stepResult` on a HIT and writing the slot on a
successful MISS; `auth.go`'s `applyMultiMode` reads
`MultiModeAuth.Default` as a bare `Auth`; fan-out's per-item
`on_status: invalidate_cache` routes through the runner's
`dropReachableCaches(s, doc)`. See
[`IMPL-13-client-http-cache-auth.md`](IMPL-13-client-http-cache-auth.md).
`go build ./...` is green at slice close — the full client package
compiles cleanly. Tests stay red until Slice 17.

The next slice is **Slice 14 — `client/sink.go` + `filestore.go` +
`redact.go` + `trace.go` + `doc.go`** (sink + filestore docstring
rewrites against the new `Snapshot` shape, redaction taint
propagation through the new Value forms, trace-record cleanup, and
the package preamble rewrite).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2
   slice list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 14"](PHASE-2-PLAN.md#slice-14--clientsinkgo--filestorego--redactgo--tracego--docgo).
   Your slice's detailed scope.
4. [`docs/runtime.md`](../runtime.md) §7 (secret redaction), §9
   (`Tracer` surface), §13 (observability). These are the
   operational reference for the redaction and trace surfaces you're
   rewriting.
5. [`docs/stores.md`](../stores.md) — the persisted `Snapshot`
   shape (`{"state": {...}}` only; no `cursor:` key, no `cache:`
   key). `filestore.go`'s on-disk JSON format is whatever the
   `Snapshot` JSON tags produce.
6. [`docs/schema.md`](../schema.md) §Values + §Paths +
   §State. The Value forms (`Add`, `Subtract`, `Max`, `Min`,
   `First`, `Last`, `Count`, `Regex`, plus the desugared
   `{concat: [...]}` for interpolated strings) that the new redact
   layer must propagate secret-taint through.
7. [`IMPL-13-client-http-cache-auth.md` §"Notes for downstream
   slices" / Slice 14 sub-section](IMPL-13-client-http-cache-auth.md).
   The cache HIT path returns a synthetic `stepResult` without
   firing the trace — Slice 14 decides whether `buildExchange`
   should skip empty-trace records (`Exchange.Phase` is also dead
   surface now).

---

## Your slice

**Branch.** Develop on `claude/slice-14-client-sink-trace-<token>`.

**Files you may touch.**

- `client/sink.go`
- `client/filestore.go`
- `client/redact.go`
- `client/trace.go`
- `client/doc.go`

**Files you must NOT touch.**

- `client/state.go`, `client/value.go`, `client/predicate.go`,
  `client/extract.go`, `client/bodypath.go`, `client/pagination.go`,
  `client/progress.go`, `client/runner.go`, `client/http.go`,
  `client/auth.go`, `client/cache.go`, `client/fanout.go` (frozen at
  Slices 8-13's close).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).

**Deliverables.**

1. **`client/sink.go` docstring rewrite.** The `Sink` interface is
   unchanged in signature, but every reference to `cursor.*` /
   `async_job` / `placeholder_event` in docstrings (if any) must go.
   The package-level invariant — `Sink.Emit` is called once per
   accepted event, `Sink.Flush` from the deferred unwind — stays.
2. **`client/filestore.go` Snapshot-shape rewrite.** The on-disk
   JSON shape is `{"state": {...}}` (Slice 8 already moved the
   in-memory `Snapshot` struct; this slice closes any docstring or
   error-message references to `cursor:` on disk). A loader faced
   with a stale file with a `"cursor"` key MUST silently ignore the
   unknown key (Go's `json.Unmarshal` already does this for unknown
   fields on `Snapshot`); global rule #1 forbids a migration path.
3. **`client/redact.go` secret-taint propagation rewrite.** The
   `schema.IsSecret` walk must recognise every new Value form:
   `Add`, `Subtract`, `Max`, `Min`, `First`, `Last`, `Count`,
   `Regex`, and the desugared `{concat: [...]}`. Any operand that
   reaches into a `secret`-typed state field taints the composite
   Value. The redact targets (`Authorization`, `Cookie`,
   `Proxy-Authorization`, `Set-Cookie`, `auth.api_key.header`,
   `auth.custom.header`) are unchanged in policy but may need
   docstring touch-ups.
4. **`client/trace.go` Exchange-record cleanup.** `Exchange.Phase`
   has no contributors (the runner passes `""` everywhere and Slice
   13's fan-out does too). Either drop the field or leave it
   `omitempty` with no contributors — both produce identical wire
   output. Decide and document. The same applies to `httpTrace`'s
   `phase` if any vestige remains. Cache HIT paths in
   `executeRequest` return without populating `httpTrace`; Slice 14
   decides whether `buildExchange` should detect the no-wire case
   (e.g. `t.method == ""`) and skip the OnExchange call rather than
   emitting an empty record.
5. **`client/doc.go` package preamble rewrite.** The current preamble
   still references `async_job phase machine` and outdated Drain
   semantics. Rewrite against the new shape: drain lifecycle per
   `docs/runtime.md` §2, no async-job phase machine, no
   `placeholder_event`, the unified `Cache` block, the per-drain
   wipe, the per-page progress evaluation.
6. **Hygiene pass** per global rule #5. Top-of-file comments, doc
   strings on every helper, error-message strings: no `cursor.<name>`
   as a namespace root, no `state.<store_in>` framework-internal slot
   convention, no slice numbers, no design-doc references, no
   "formerly", no "for backwards compatibility", no references to
   the legacy `Defaults.BaseURL` / `requests[].path` shape, the old
   `TokenCache` / `RequestCache` types, the `state.fields` /
   `mutability:` vocabulary, the async-job phase machine, or the
   `placeholder_event` mechanism. Error messages should read as if
   the post-redesign shape had always existed.
7. **New artefact `docs/planning/IMPL-14-client-sink-trace.md`**
   carrying:
   - Scope (one sentence).
   - Old → new map (per legacy surface → new mechanism — `Exchange.Phase`
     decision, `Snapshot.Cursor` removal, `IsSecret` walk over new Value
     forms, etc.).
   - Removed-content list (every deleted helper, field, log string).
   - Build-state enumeration (`go build ./client/...` stays green;
     Slice 14 introduces zero new errors).
   - Walkthrough verification for the trickier cases (cache HIT trace
     behaviour, secret-taint propagation through `{concat: [...]}`
     and `{add: [...]}`, `Snapshot` JSON round-trip without a
     `cursor:` key, `Exchange.Phase` decision rationale).
   - Notes for downstream slices (Slice 15 on the CLI's tracer wiring,
     Slice 17 on the test rewrite, Slice 18 on the schema-reference
     regeneration).
8. Slice-table row 14 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-14-client-sink-trace.md` link.
9. `HANDOFF.md` rewritten to point at Slice 15
   (`cmd/skopos/*` — the CLI).

**Smoke tests.** Throwaway and optional. The trickier areas are:

- Secret-taint propagation through a `{concat: [...]}` interpolation:
  `"${state.token}-${state.suffix}"` where `state.token` is `secret`-typed
  should mark the composite header value as redacted in the trace.
- Secret-taint propagation through reducers: `{max: {ref: events.*.token}}`
  where `events.*.token` walks a list of secret values.
- `Snapshot` round trip through `FileStore.Save` → `Load` with a
  pre-redesign on-disk JSON file carrying a `cursor:` key. The
  `cursor:` should be silently dropped on load; the next `Save`
  writes only `state:`.
- An `executeRequest` cache HIT: confirm the trace surface emits the
  decision Slice 14 picks (skip vs empty record).

End-to-end correctness is not checkable until Slices 16-17 land. The
build is the binding verification target.

**Out of scope.** No schema changes (Slice 7 closed). No state /
scope / value-runtime / pagination / progress / runner / http /
auth / cache / fanout changes (Slices 8-13 closed). No CLI changes
(Slice 15). No templates (Slice 16). No tests (Slice 17).

---

## Watch out for

- **The cache HIT path returns without a wire exchange.** Slice 13's
  `executeRequest` opens with a `req.Cache != nil` pre-check; a HIT
  returns a synthesised `stepResult{statusCode: 200, body: <cached>}`
  and never populates `httpTrace`. The runner still calls
  `r.Tracer.OnExchange(buildExchange(...))` afterwards (it doesn't
  know about the HIT). Slice 14 decides whether an empty-trace
  record is a feature ("we tried this step, here's a tombstone") or
  a noise problem ("zero method, zero URL, zero status — skip it").
  Pick one, document it in `IMPL-14-*.md`.

- **`Exchange.Phase` has zero contributors.** The async-job phase
  machine is gone; the runner passes `""` everywhere. Either drop
  the field outright (clean, mildly breaking for any external
  consumer parsing the JSONL) or leave it as `omitempty` with no
  contributors (defensive, zero behaviour change). Same call for
  `httpTrace.phase` if any vestige remains.

- **`Snapshot.Cursor` is gone — but the on-disk format must be
  forward-compatible with stale files.** Per global rule #1, no
  migration code: but Go's `json.Unmarshal` already drops unknown
  keys on a struct, so a stale `{"cursor": {...}, "state": {...}}`
  file simply has its `cursor:` key dropped on `Load`. The next
  `Save` writes only `{"state": {...}}`. No special handling
  required; document the silent drop in `filestore.go`'s docstring.

- **Secret-taint propagation MUST cover every new Value form.**
  Slice 5 added `Add` / `Subtract` / `Max` / `Min` / `First` /
  `Last` / `Count` / `Regex` plus the desugared `{concat: [...]}`
  for interpolated strings. `schema.IsSecret` lives in the schema
  package; if it doesn't already recurse into every variant, that's
  a Slice 14 fix (in `client/redact.go`'s caller path or via a
  bug fix in `schema/value.go`, depending on where `IsSecret`'s
  walk lives — check before touching).

- **Tests stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `client/sink_test.go`, `client/filestore_test.go`,
  `client/trace_test.go`, `client/redact_test.go`. Expect
  `go test ./client/...` to stay red. Slice 14's verification
  target is `go build ./client/...` zero errors.

- **`doc.go` is the package preamble — it sets reader expectations.**
  The current preamble still talks about an `async_job phase machine`
  and `want_more`. Rewrite to match what the runner actually does
  today (per `runner.go`'s top-of-file comment block — the
  authoritative description lives there, Slice 14 just propagates it
  to the package-level doc).

---

## When you finish

`git add` the rewritten files, the new `IMPL-14-client-sink-trace.md`,
the updated `RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit
with a message like:

```
feat(client): slice 14 — sink, redact, trace, filestore, doc against the new IR

Sink / FileStore docstring rewrites: Snapshot is {"state": {...}};
the cursor key was Slice 8's removal, this slice closes the prose.
redact.go: secret-taint propagation through every new Value form
(Add/Subtract/Max/Min/First/Last/Count/Regex/{concat: [...]}).
trace.go: Exchange.Phase is dead surface — drop or omitempty; cache
HIT path is documented. doc.go: package preamble rewrite against the
new drain shape.

schema/ + client/... stays green at slice close; tests stay red
until Slice 17.
```

Push to `claude/slice-14-client-sink-trace-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
