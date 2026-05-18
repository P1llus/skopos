# Handoff — Phase 2 Slice 10 (`client/pagination.go`)

Slice 9 closed (`client/value.go` rewritten with arms for `Add`,
`Subtract`, `Max`, `Min`, `First`, `Last`, `Count`, `Regex`; the
`{now: true, offset: ...}` sibling form is gone — offsets desugar to
`{add: [{now: true}, "<dur>"]}`; reducer operands resolve through the
`events.*.<field>` projection in `scope`; `client/extract.go` writes to
`state.<name>` or `extract.<name>` directly off `ExtractVar.To`; the
cursor arm is gone; `client/bodypath.go` doc-strings are aligned to the
`response.body` / `steps.<id>.body` namespace roots only).
See [`IMPL-09-client-value.md`](IMPL-09-client-value.md). The schema
package still builds **green**; the client package stays red until the
remaining slices land — Slice 9 introduced **zero** new errors in the
files it owns.

The next slice is **Slice 10 — `client/pagination.go`** (collapse the
seven legacy pagination strategies into four named variants plus
`custom`).

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 10"](PHASE-2-PLAN.md#slice-10--clientpaginationgo).
   Your slice's detailed scope.
4. [`docs/schema.md` §pagination](../schema.md). The four named variants
   (`none`, `cursor_token`, `next_url`, `counter`) plus `custom`, the
   per-variant fields, and each variant's default `terminate_when:`.
5. [`docs/runtime.md` §3 (pagination loop)](../runtime.md). The per-page
   execution order: request → `terminate_when` → advance → loop. The
   MaxPages safety cap. The interaction with `on_status` and
   `error.mode`.
6. [`IMPL-08-client-state.md`](IMPL-08-client-state.md). Especially the
   §"Notes for downstream slices" / Slice 10 sub-section — the per-drain
   wipe is already exposed as `(*scope).resetPerDrainScratch`; the
   pagination plan owns no seed step.
7. [`IMPL-09-client-value.md`](IMPL-09-client-value.md). The reducer +
   regex helpers your variants will call into. The `events.count`
   resolution arm. The `applyRegex` helper (reuse, don't duplicate).

---

## Your slice

**Branch.** Develop on `claude/slice-10-client-pagination-<token>`.

**Files you may touch.**

- `client/pagination.go`

**Files you must NOT touch.**

- `client/state.go`, `client/value.go`, `client/predicate.go`,
  `client/extract.go`, `client/bodypath.go` (frozen at Slices 8–9's
  close).
- Any other file under `client/` (Slices 11–14 own those).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).

**Deliverables.**

1. **`paginationPlan` interface.** Reduce to a single `advance` (or
   similarly named) method that takes the post-page-response scope and
   returns `(terminate bool, err error)`. The per-drain wipe is the
   runner's responsibility (Slice 12). There is no `seed` step.

2. **Five concrete plans.**

   - `nonePagination` — exactly one page per drain.
   - `cursorTokenPagination` — reads `from:` (body Path or header
     name), writes `to: state.<name>`. Covers what was
     `cursor_token` + `scroll_id` + `graphql_relay` + page-number-from-body.
     Owns `regex` + `capture` + `coerce`.
   - `nextURLPagination` — reads `from:` (body Path or header name),
     writes `to: state.<name>`. Covers what was `link_header` +
     `next_url_in_body`. Owns `regex` + `capture`.
   - `counterPagination` — increments a `state.<name>` counter by
     `step` each page. Owns `start` + `step`; default
     `terminate_when:` is `{lt: {path: events.count, value: <step>}}`.
   - `customPagination` — list of `{to, from, regex?, coerce?}` writes
     plus an author-supplied `terminate_when:` (required for `custom`).
     No default predicate.

3. **Per-page execution order** per [`docs/runtime.md` §3](../runtime.md):

   1. Issue the request.
   2. Evaluate `terminate_when` against the post-page-response scope
      (events bound, body bound, headers bound). If true, stop the
      pagination loop.
   3. Apply the variant's advance writes (`pagination.*.to`) — these
      write to per-drain-scratch state fields. The next iteration's
      request reads from the updated state.
   4. Loop.

4. **Default `terminate_when:` per variant.** Match the table in
   `docs/runtime.md` §3:

   - `none` — terminates after the first page.
   - `cursor_token` — terminates when the cursor source resolves to
     absent / empty.
   - `next_url` — terminates when the next-URL source resolves to
     absent / empty.
   - `counter` — terminates when `events.count < step` (short-page).
   - `custom` — no default; the author must supply one.

5. **MaxPages safety cap.** Honour the document-level `pagination.max_pages`
   ceiling (if any). The runner enforces it, but pagination should
   surface a clear diagnostic when the cap fires (Slice 12 owns the
   call site; Slice 10 owns the variant-level cleanliness).

6. **Reuse, don't duplicate.** `regex` + `capture` route through
   `(*scope).applyRegex` from Slice 9. `coerce` routes through
   `applyFormat`. The events list and `events.count` are read off
   `scope` via `resolveNamespaceRef` — don't reach into `scope.events`
   directly.

7. **Hygiene pass** per global rule #5. Top-of-file comments,
   doc-strings on every helper, error-message strings: no
   `cursor.<name>` as a namespace root, no `body.<path>` as a top-level
   root, no slice numbers, no design-doc references. Error messages
   should read as if the post-redesign shape had always existed. Remove
   every reference to the seven legacy strategies.

8. New artefact `docs/planning/IMPL-10-client-pagination.md` carrying:
   - Scope (one sentence).
   - Old → new map (per legacy variant → which new variant owns its
     coverage).
   - Removed-content list.
   - Build-state enumeration.
   - Notes for downstream slices (especially Slice 12 — the runner
     drives the loop).

9. Slice-table row 10 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-10-client-pagination.md` link.

10. `HANDOFF.md` rewritten to point at Slice 11 (`client/progress.go`).

**Smoke tests.** Throwaway and optional. The trickier areas are:

- `counterPagination`'s default `events.count < step` predicate — verify
  it routes through `resolveNamespaceRef("events.count")` and reads
  `int64(0)` when no page is bound yet.
- `nextURLPagination` with a Link-header regex like
  `'<(.*?)>;\\s*rel="next"'` and `capture: 1` — verify
  `(*scope).applyRegex` returns the captured URL.
- `cursorTokenPagination` with `from: response.header.<name>` — verify
  the header-lookup arm in `resolveNamespaceRef` reaches the right
  scope slot.
- `customPagination` with two writes that depend on each other — verify
  the runner doesn't see a half-applied state mid-write (snapshot-then-write
  semantics, same as `applyProgress` once Slice 11 lands).

The client package will still NOT compile end-to-end at slice close
(Slices 11–14 own the files that still reference removed shapes), so a
real smoke `_test.go` won't build inside `package client`. A
walkthrough verification is acceptable; document it in
`IMPL-10-client-pagination.md`.

**Out of scope.** No schema changes (Slice 7 closed). No state /
scope / value-runtime changes (Slices 8–9 closed). No progress / runner
changes (Slices 11–12). No cache / auth / http changes (Slice 13). No
sink / trace changes (Slice 14).

---

## Watch out for

- **The package will not compile end-to-end.** `progress.go`,
  `runner.go`, `oauth2.go`, `requestcache.go`, `http.go`, `auth.go`
  still reference removed schema types. Your slice owns one file only;
  the rest stays red until the slices that own them land.

- **No `seed` step.** The seven-variant plan had a per-iteration seed
  step. The new four-variant plan does not. The per-drain wipe lives
  in `(*scope).resetPerDrainScratch`; the runner calls it at the top
  of every drain. Your variants only `advance`.

- **Pagination writes go to `state.<name>` directly.** There is no
  `cursor` namespace any more. A `pagination.cursor_token.to` Path is
  `state.<name>` and the validator guarantees the field is declared
  under `state:` with no `default:` (per-drain scratch lifetime is
  inferred from the write site).

- **`events.count` reads route through `scope`.** `events.count`
  resolves to `int64(0)` when no page is bound yet (so the
  `counter` variant's first-page predicate doesn't crash). Don't
  open-code `len(scope.events.([]any))`.

- **Tests stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch
  `client/pagination_test.go`. Expect `go test ./client/...` to stay
  red.

---

## When you finish

`git add` the rewritten file, the new `IMPL-10-client-pagination.md`,
the updated `RESEARCH_PLAN.md`, and the updated `HANDOFF.md`. Commit
with a message like:

```
feat(client): slice 10 — pagination variants collapsed to 4 + custom

paginationPlan loses its seed step; the per-drain wipe in
(*scope).resetPerDrainScratch handles bootstrapping. Five concrete
plans: nonePagination, cursorTokenPagination, nextURLPagination,
counterPagination, customPagination. Each variant advances by writing
to state.<name> per its To Path; regex + capture route through
(*scope).applyRegex; default terminate_when predicates match
docs/runtime.md §3.

schema/ + client/{state,value,predicate,extract,bodypath,pagination}.go
build at slice close (modulo the rest of client/ which other slices
still own); tests stay red until Slice 17.
```

Push to `claude/slice-10-client-pagination-<token>` and open a PR.

If you discover the slice is wider than the plan, **stop and flag it
via `AskUserQuestion`** rather than widening scope silently.
