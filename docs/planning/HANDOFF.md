# Handoff — Phase 2 Slice 13 (`client/{http,auth,cache,fanout}.go`)

Slice 12 closed (`client/runner.go` rewritten against the new IR; the
async-job phase machine, the `placeholder_event` two-pass logic, the
`progressPlan` constructor, and the `pagination.seed` / `progress.seed`
/ `progress.advance` call sites all gone; the drain loop now follows
`docs/runtime.md` §2 — load → per-drain wipe → pagination loop
(request chain with `requests[].terminate_when:` per step → bind
producer body/headers/events → emit to sink → `applyProgress(s,
doc.Progress)` → `pagination.advance(s)`) → deferred Save; the
`on_status:` verb table dispatches `skip` / `fail` / `empty_events` /
`invalidate_cache` and `error.mode` falls back through `standard` /
`warn` / `fail`; the MaxPages cap bounds runaway pagination). See
[`IMPL-12-client-runner.md`](IMPL-12-client-runner.md). `schema/` plus
`client/{state,value,predicate,extract,bodypath,pagination,progress,
runner,sink,trace,redact,filestore,doc,fanout}.go` build green; Slice
12 introduced **zero** new errors in the file it owns. The remaining
21 client/* errors live in `auth.go`, `oauth2.go`, `requestcache.go`,
and `http.go` until this slice closes.

The next slice is **Slice 13 — `client/http.go` + `auth.go` +
`cache.go` (merging `oauth2.go` + `requestcache.go`) + `fanout.go`**
(rewrite the HTTP execution layer, the auth layer, the unified `Cache`
runtime, and the fan-out per-item dispatcher against the post-redesign
surface).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2
   slice list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 13"](PHASE-2-PLAN.md#slice-13--clienthttpgo--authgo--cachego--fanoutgo).
   Your slice's detailed scope.
4. [`docs/runtime.md`](../runtime.md) §6 (error semantics — `on_status:`
   verbs + `error.mode`), §7 (secret redaction), §8 (cache namespace),
   §10 (HTTP transport defaults), §12 (fan-out). These are the
   operational reference for everything you're rewriting.
5. [`docs/schema.md` §auth + §requests + §cache](../schema.md). The
   post-redesign shapes for `Auth`, `Request`, the unified `Cache`
   struct, and the `cache.<name>` namespace.
6. [`IMPL-08-client-state.md`](IMPL-08-client-state.md). The
   `scope.cache` map is the new home for cache slots; the per-drain
   wipe / snapshot filter never touches it.
7. [`IMPL-12-client-runner.md` §"Notes for downstream slices" / Slice
   13 sub-section](IMPL-12-client-runner.md). The new runner's
   contract with the HTTP / auth / cache / fan-out layers — what it
   calls, what it reads, what it expects each helper to return. In
   particular: `dropReachableCaches(s, doc)` lives in `runner.go`; the
   HTTP/auth layer owns the cache MISS / WRITE path; fan-out's
   internal `invalidate_cache` arm needs the same helper.

---

## Your slice

**Branch.** Develop on `claude/slice-13-client-http-cache-auth-<token>`.

**Files you may touch.**

- `client/http.go`
- `client/auth.go`
- `client/oauth2.go` → merge contents into a new `client/cache.go`
  alongside `requestcache.go`'s contents, then **delete** `oauth2.go`
  and `requestcache.go`. The unified `Cache` runtime lives in one
  file.
- `client/fanout.go`

**Files you must NOT touch.**

- `client/state.go`, `client/value.go`, `client/predicate.go`,
  `client/extract.go`, `client/bodypath.go`, `client/pagination.go`,
  `client/progress.go`, `client/runner.go` (frozen at Slices 8-12's
  close).
- `client/sink.go`, `client/filestore.go`, `client/redact.go`,
  `client/trace.go`, `client/doc.go` (Slice 14 owns those).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).

**Deliverables.**

1. **`client/http.go` rewrite.**
   - `(*scope).buildURL(req schema.Request)` becomes `s.evalValue(req.URL)`
     directly — there is no `Defaults.BaseURL` and there is no
     `req.Path`. Every request carries an absolute URL Value (often a
     `${...}`-interpolated string).
   - `(*scope).executeRequest(ctx, client, req, trace)` consults the
     unified `Cache` block on `req.Cache` BEFORE firing: if the slot
     `cache.<name>` is fresh, the cache-hit body is returned as the
     `stepResult` without an HTTP round-trip. The cache-miss path
     fires the request, captures the body, and writes
     `cache.<name>` + the slot's resolved `expires_at` instant.
   - Trace records continue to live behind a non-nil `Runner.Tracer`;
     the runner passes a `*httpTrace` scratchpad and reads back
     metadata after the exchange.
2. **`client/auth.go` rewrite.**
   - `auth.MultiMode.Default` is now a bare `schema.Auth`, not the
     wrapped `{auth: ...}` form. The dispatcher reads it as-is.
   - `auth.OAuth2.<grant>.cache` is now a `*schema.Cache`, not a
     `*schema.TokenCache`. The cache writes `cache.<name>` (in
     `scope.cache`), NOT `state.<store_in>` + `state.<store_in>_expires_at`.
3. **`client/cache.go` (new file, replacing `oauth2.go` and
   `requestcache.go`).**
   - One struct + helper handles both call sites (auth grant cache,
     request-level cache). Cache slots live in `scope.cache` (process
     memory, never persisted).
   - The cache-hit decision: `now() + buffer >= expires_at` →
     re-fetch. The captured value lands at `cache.<name>` AND the
     resolved expiry instant lives alongside (internal record
     keeping; the schema declares only one `cache.<name>` Path).
   - The OAuth2 token-fetch path (POST to `token_url`, parse
     `access_token` + `expires_in`) merges with the request-level
     cache write path. Both end at the same "write to scope.cache"
     primitive.
4. **`client/fanout.go` rewrite.**
   - The runner already passes `""` for the legacy `phase` parameter.
     Slice 13 can drop the parameter from the signature entirely.
   - Per-item `on_status: invalidate_cache` needs to use the new
     `dropReachableCaches(s, doc)` from `runner.go` (or duplicate the
     walk inside fanout.go — but reuse is preferred).
   - `fan_out.as` validation is already enforced at validate time
     (Slice 7); the runtime doesn't re-check.
5. **Cross-file invariants.**
   - The `cache.<name>` namespace is process-memory only — never read
     from or written to `Snapshot.State`. `(*scope).snapshot()` in
     `state.go` already excludes the cache map; this slice must not
     stuff cache slots into state.
   - `state.<store_in>_expires_at` is GONE. The validator no longer
     auto-registers that slot; this slice must not look for it
     either.
   - `safeURL` (in `redact.go`, Slice 14's territory) is still the
     canonical URL renderer for log / error / trace surfaces.
   - `redactURLError` is still the canonical transport-error redaction
     helper.
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
7. **New artefact `docs/planning/IMPL-13-client-http-cache-auth.md`**
   carrying:
   - Scope (one sentence).
   - Old → new map (per legacy call → new mechanism — `TokenCache` →
     `Cache`, `RequestCache` → `Cache`, `state.<store_in>` →
     `cache.<name>`, `BaseURL` → absolute `url:` Value).
   - Removed-content list (every deleted helper, field, call site).
   - Build-state enumeration (Slice 13 should bring the
     auth/oauth2/requestcache/http red-error count down to zero;
     the full `go build ./client/...` should be green after this
     slice).
   - Walkthrough verification for the trickier cases (OAuth2 cache
     hit, OAuth2 cache miss + token fetch, request-level cache hit
     when an event-fetch step is preceded by a login step, fan-out's
     per-item `invalidate_cache`, `multi_mode.Default` dispatch).
   - Notes for downstream slices (Slice 14 on the trace / redact
     surface, Slice 15 on the CLI's continuous-mode interaction with
     the cache namespace, Slice 17 on the test rewrite).
8. Slice-table row 13 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-13-client-http-cache-auth.md` link.
9. `HANDOFF.md` rewritten to point at Slice 14
   (`client/{sink,redact,trace,filestore,doc}.go`).

**Smoke tests.** Throwaway and optional. The trickier areas are:

- An OAuth2 client_credentials grant with a cache block. Walk a
  three-iteration drain: iter 1 misses the cache (fetches token,
  writes `cache.access_token`); iter 2 + 3 hit the cache (no token
  fetch); after `iter 3 + cache.buffer`, iter 4 misses again.
- A request-level cache (a custom JSON login step) preceding an
  event-fetch step. Same cache-hit / cache-miss walk as the OAuth2
  case.
- `on_status: invalidate_cache` on a 401 — verify the cache slot is
  removed from `scope.cache` and the next iteration's HTTP layer
  re-fetches the token.
- `multi_mode` auth with two `bearer` branches and a `none` default.
  Walk a drain where the predicate fires the first branch, then
  fires the default arm on a different iteration.
- A fan_out step with `merge: flatten` on a list of three items. One
  item returns a non-list body → expect a clear error at
  merge time, not a silent loss of events.

End-to-end correctness is not checkable until Slices 16-17 land. The
build is the binding verification target.

**Out of scope.** No schema changes (Slice 7 closed). No state /
scope / value-runtime / pagination / progress / runner changes
(Slices 8-12 closed). No sink / trace / redact / filestore changes
(Slice 14). No CLI changes (Slice 15). No templates (Slice 16). No
tests (Slice 17).

---

## Watch out for

- **The package becomes fully green after this slice.** Slices 8-12
  bought breathing room by leaving auth/cache/http red; Slice 13's
  closing condition is `go build ./client/...` returning zero errors
  on the whole package. If you discover a stray reference in some
  earlier-slice file that should have been caught upstream, fix it in
  the same slice — but flag it via `AskUserQuestion` first if the fix
  touches a frozen file.

- **`cache.<name>` lives in `scope.cache`, not `scope.state`.** The
  validator auto-registered `state.<store_in>` slots in the old shape;
  the new shape declares `cache.<name>` Paths in the schema and the
  runtime writes them into a separate map. Persistence (`Snapshot`)
  never touches cache; a runner restart re-fetches.

- **The unified `Cache` struct replaces `TokenCache` and
  `RequestCache`.** One helper, two call sites. The same expiry-
  resolution logic, the same buffer semantics, the same miss → fetch
  → write loop. Authors write the same YAML shape under both
  `auth.oauth2.<grant>.cache:` and `requests[].cache:`.

- **`MultiModeAuth.Default` is a bare `Auth`, not `{auth: ...}`.**
  The struct has fields like `Bearer *BearerAuth`, `Basic *BasicAuth`,
  etc. — the dispatcher reads the variant directly off the bare value.
  Don't re-introduce the wrapping shape.

- **`req.URL` is a `schema.Value`, not `*schema.Value`.** Every
  request has a URL. There is no `req.Path` and no `Defaults.BaseURL`.
  `s.evalValue(req.URL)` returns the resolved URL string; parse it
  with `url.Parse`. The Value layer handles secret-tainting; the URL
  renderer (`safeURL`) handles output redaction.

- **OAuth2 interactive grants are out of scope.** Authorization code,
  device code, PKCE — none of those land in this slice (or any
  slice). The pull-loop has no browser round-trip surface.

- **Tests stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `client/auth_test.go`, `client/oauth2_test.go` (or whatever it
  becomes), `client/requestcache_test.go`, `client/fanout_test.go`.
  Expect `go test ./client/...` to stay red.

- **The runner's `dropReachableCaches` helper is the canonical
  invalidate path.** Fanout's per-item `on_status:
  invalidate_cache` arm should call it directly (export it if
  needed). Two parallel cache-walk implementations is a divergence
  risk worth avoiding.

---

## When you finish

`git add` the rewritten files, the new `IMPL-13-client-http-cache-auth.md`,
the updated `RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit
with a message like:

```
feat(client): slice 13 — http, auth, cache, fan-out against the new IR

HTTP execution layer rewrite per docs/runtime.md §10: req.URL is a
Value (no Defaults.BaseURL, no req.Path); req.Cache pre-check lives
inside executeRequest. Auth layer rewrite: MultiModeAuth.Default is a
bare Auth; OAuth2 grants use the unified Cache struct. cache.go
absorbs oauth2.go + requestcache.go: one struct + helper for both
sites, slots live in scope.cache (process memory only). Fan-out
rewrite drops the legacy phase parameter and routes per-item
invalidate_cache through dropReachableCaches.

schema/ + client/... build green at slice close; tests stay red
until Slice 17.
```

Push to `claude/slice-13-client-http-cache-auth-<token>` and open a
PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
