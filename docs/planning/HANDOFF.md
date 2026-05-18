# Handoff — Phase 2 Slice 16 (`templates/*.yml` + `templates/templates.go` + `schema/testdata/*.yml`)

Slice 15 closed (`cmd/skopos/*` audited against the post-redesign
client + schema). The CLI surface and its wiring were already in the
new shape by the time this slice opened; the only source-level change
was a stale `cursor.*` framing comment in `cmd_init.go`. The build is
green:

- `go build ./cmd/...` → no errors.
- `go build ./...` → no errors (schema + client + cmd + tools all green).

The audit grep for legacy vocabulary returned only the one comment
line, which was fixed. The four subcommands — `validate`, `run`,
`init`, `template list|show` — match `docs/usage.md` §1 flag-for-flag.
The continuous-mode "always retry" policy sits in the CLI loop, not in
`Runner.Drain`; `--trace` writes raw JSONL (including the cache-HIT
`{iteration, step_id, cache_hit:true}` tombstone from Slice 14); the
`--state` `FileStore` writes the post-redesign `{"state": {...}}`
JSON shape from Slice 8 / Slice 14.

See [`IMPL-15-cmd-cli.md`](IMPL-15-cmd-cli.md). Tests stay red until
Slice 17 (`cmd/skopos/*_test.go` carries the legacy spec shape per
global rule #6).

The next slice is **Slice 16 — `templates/*.yml` +
`templates/templates.go` + `schema/testdata/*.yml`** (rewrite every
bundled template and every schema test fixture against the post-redesign
IR).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2
   slice list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 16"](PHASE-2-PLAN.md#slice-16--templatesyml--templatestemplatesgo--schematestdatayml).
   Your slice's detailed scope, including the per-template rewrite
   checklist and the variant-mapping table reference.
4. [`docs/schema.md`](../schema.md) end-to-end — the source of truth for
   the post-redesign IR shape. Pay special attention to:
   - §state (lifetime inference: operator-config / per-drain scratch /
     persistent);
   - §3.6 pagination variant mapping table;
   - §progress (flat list of writes);
   - §namespaces (the closed root set: `state | cache | events |
     extract | steps | response`).
5. [`docs/api-methods.md`](../api-methods.md) — the variant catalogue
   that maps vendor patterns onto schema fragments. Reach for it
   while rewriting each template.
6. [`docs/runtime.md`](../runtime.md) §2 (drain lifecycle), §3
   (pagination loop), §5 (progress evaluation timing), §6 (error
   semantics), §8 (cache namespace) — the runtime contract every
   template must be authored against.
7. [`docs/usage.md`](../usage.md) §3 (writing a template from scratch)
   — the step-by-step recipe vocabulary the rewritten templates must
   be self-consistent with.
8. [`IMPL-14-client-sink-trace.md`](IMPL-14-client-sink-trace.md) and
   [`IMPL-13-client-http-cache-auth.md`](IMPL-13-client-http-cache-auth.md)
   for the cache + secret-taint semantics OAuth2 / session-cookie /
   ETag templates depend on.
9. [`schema/validate.go`](../../schema/validate.go) — the validator
   you'll be running every rewritten template through. The diagnostic
   text is your fastest debugging surface.

---

## Your slice

**Branch.** Develop on `claude/slice-16-templates-<token>`.

**Files you may touch.**

- Every `templates/*.yml` (27 files):

  ```
  api_key_auth, async_poll, async_poll_latest_ts, async_poll_stateless,
  basic_auth, bearer_simple, cursor_token, custom_auth, etag_conditional,
  etag_conditional_middle, fanout, link_header, multi_mode_auth,
  ndjson_response, next_url_in_body, oauth2_client_credentials,
  oauth2_password_grant, oauth2_relay, offset_pagination, page_number,
  post_form_body, post_json_body, post_raw_body, scroll_id,
  session_cookie, session_login_cached, simple_get_object
  ```

- `templates/templates.go` (only if the embedded FS interface changes;
  most likely no edit needed — the `//go:embed *.yml` directive picks
  up rewrites transparently).
- Every `schema/testdata/*.yml`:

  ```
  api_key_query, async_poll_latest_ts, async_poll_stateless,
  cursor_token_placeholder, dual_mode_url_commercial,
  dual_mode_url_gov, etag_conditional_middle, fanout, format_verbs,
  lookback_progress, max_event_field, multi_field_cursor,
  oauth2_password_grant, oauth2_relay, on_status_dispatch,
  predicate_composition, token_cache, token_cache_multistep,
  use_now, value_forms
  ```

**Files you must NOT touch.**

- Any file under `client/` (frozen at Slices 8-14's close).
- Any file under `schema/` other than `schema/testdata/*.yml` (frozen
  at Slice 7's close).
- Any file under `cmd/skopos/` (frozen at Slice 15's close).
- Any `*_test.go` file (Slice 17 owns the test rewrite).

**Deliverables.**

1. **Each `templates/*.yml`** rewritten against the post-redesign IR.
   Per-template rewrite checklist (from PHASE-2-PLAN.md §2 Slice 16):
   - `state:` flattens — `state.fields.<name>:` becomes `state.<name>:`.
   - Remove `state.initial_interval`. First-run seeding moves to a
     `default: {subtract: [{now: true}, "720h"]}` on the destination
     state field (typically `state.last_timestamp`).
   - Remove `defaults:` block. Inline the base URL via interpolation
     (`url: "${state.url}/path"`).
   - Rename `cursor.<x>` → `state.<x>` everywhere; declare every such
     field under the top-level `state:` block.
   - Collapse pagination variants per `docs/schema.md` §3.6:
     - `cursor_token`, `scroll_id`, `graphql_relay`, page-number-from-body
       → `cursor_token` with `from:` / `to:` / optional `terminate_when:`.
     - `link_header`, `next_url_in_body` → `next_url` with `from:` /
       `to:` / optional `regex:` + `capture:`.
     - `page_number`, `offset` → `counter` with `start:` / `step:`.
   - Rewrite `progress:` as a flat list of `{to, from}` writes.
   - Replace `async_job:` progress with a three-request
     `submit / poll / fetch` chain in `requests:`; the poll step
     carries `terminate_when:` keyed on the job's completion signal.
   - Token caching (OAuth2 + custom-login) writes to `cache.<name>`
     via the unified `Cache` block (`cache: {to, expires_at, buffer}`).
   - ETag conditional templates write the etag into `state.<name>` via
     `extract:`; no `cursor.*`.
   - Drop `placeholder_event:` from `response:` blocks.
   - Drop `state.fields`-keyed `mutability:` markers (lifetime is now
     inferred from write sites).
   - `requests[].path` becomes `url:` (absolute URL Value, typically an
     interpolation).
   - Closed `on_status:` verb set (`skip | fail | empty_events |
     invalidate_cache`). No `retry` verb.
2. **Each `schema/testdata/*.yml`** rewritten the same way. These
   fixtures drive `schema.Validate` golden tests in Slice 17.
3. **`templates/templates.go`** — most likely unchanged. If the
   bundled name list rotates (some templates renamed away), update
   the package-level comment and any in-code name lists.
4. **Hygiene pass** per global rule #5. Each rewritten YAML reads as
   if the post-redesign shape had always existed — no comments
   referencing the old shape, no "formerly", no `cursor.*` mentions in
   in-line YAML comments.
5. **Smoke test (delete before close).** A throwaway test that
   iterates `templates.Names()` and runs `schema.Parse` + `schema.Validate`
   on each; fails the slice if any rewritten template emits an
   error-severity diagnostic. Same for `schema/testdata/*.yml`.
6. **New artefact `docs/planning/IMPL-16-templates.md`** carrying:
   - Scope (one sentence).
   - Per-template rewrite log (one row per template; old variant →
     new variant; notes on tricky reshape calls — async_job to
     submit/poll/fetch, OAuth2 cache wiring, etc.).
   - Removed-content list (every dropped variant per template).
   - Build-state enumeration (`go build ./...` green; every template
     parses + validates clean).
   - Walkthrough verification (one or two of the more interesting
     reshapes — async_poll, oauth2_client_credentials, fanout).
   - Notes for downstream slices (Slice 17 on the goldens that lock
     the new shape; Slice 18 on the schema-reference regeneration).
7. Slice-table row 16 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-16-templates.md` link.
8. `HANDOFF.md` rewritten to point at Slice 17 (`*_test.go` rewrites
   across `schema/`, `client/`, `cmd/`, `internal/testserver/`).

**Verification.**

- `go build ./...` stays green.
- `skopos validate -i <each template>` exits `0` for every template.
- `skopos validate -i <each schema/testdata fixture>` exits `0` for
  every fixture intended to be valid (some fixtures may be
  intentionally-broken negative cases; check the existing test
  fixtures' role before rewriting).

End-to-end correctness (drain → events → progress → snapshot) against
the bundled templates is checkable manually once each template is
rewritten — `skopos run -i <template> --once --out events.jsonl` with
a local fake (the `internal/testserver/` fixtures, but those are
themselves stale until Slice 17). The validate pass is the binding
verification target for this slice.

**Out of scope.** No schema changes (Slice 7 closed). No client
changes (Slices 8-14 closed). No CLI changes (Slice 15 closed). No
test rewrite (Slice 17). No docs regeneration (Slice 18).

---

## Watch out for

- **`docs/schema.md` §3.6 is the variant-mapping table.** Use it
  verbatim when collapsing pagination variants; don't reinvent the
  mapping per template.

- **First-run seeding moves into `default:`.** The old
  `progress: {initial: {lookback: "720h"}}` pattern doesn't survive.
  The destination state field carries
  `default: {subtract: [{now: true}, "720h"]}` and `progress:` is
  just the per-page write (typically a `{max: [...]}` over the prior
  value + the page's event timestamps).

- **`requests[].url:` is always absolute.** No `path:` field; no
  `defaults.base_url` to prefix. The interpolation `${state.url}/path`
  is how authors compose URLs.

- **`cache.<name>` is process memory only.** OAuth2 token caching
  writes to `cache.<name>`, not `state.<store_in>`. The slot expires
  per the `Cache.expires_at` Value (typically
  `{ref: response.body.expires_in, default: "1h"}`).

- **`async_job:` progress disappears.** The three-request chain
  (submit / poll / fetch) is the new shape; the poll step carries
  `terminate_when:` keyed on the job's done signal. See
  `docs/api-methods.md` §6 for the recipe.

- **`placeholder_event:` is gone.** Empty pages just trigger the next
  page; no synthetic event is injected.

- **Reserved fan-out root list:** `state | cache | events | extract |
  steps | response`. `fan_out.as` cannot collide with these (the
  validator enforces it; the rewritten fanout template should pick a
  name like `incident` or `item` that doesn't hit any root).

- **Tests stay stale.** Per global rule #6, Slice 17 owns the
  `*_test.go` rewrite. You may NOT touch any `_test.go` file.
  `go test ./...` stays red.

- **`schema/testdata/cursor_token_placeholder.yml`** — re-read this
  fixture's purpose before rewriting. If it was a deliberate negative
  fixture pinning the `placeholder_event:` rejection (and the
  validator already rejects the field at parse time), the fixture may
  need a different unhappy-path role under the new validator.

---

## When you finish

`git add` every rewritten `templates/*.yml` and `schema/testdata/*.yml`,
the new `IMPL-16-templates.md`, the updated `RESEARCH_PLAN.md`, and
the updated `HANDOFF.md`. Commit with a message like:

```
feat(templates): slice 16 — bundled templates against the new IR

Every templates/*.yml and schema/testdata/*.yml rewritten against the
post-redesign IR. state: flattens, defaults.base_url disappears in
favour of ${state.url} interpolation, cursor.* renames to state.*,
pagination collapses to 4 named variants + custom, progress is a flat
write list, async_job becomes a three-request submit/poll/fetch chain,
OAuth2 and session-login tokens write to cache.* via the unified
Cache block.

schema/ + client/ + cmd/ + every bundled template parses + validates
clean; tests stay red until Slice 17.
```

Push to `claude/slice-16-templates-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
