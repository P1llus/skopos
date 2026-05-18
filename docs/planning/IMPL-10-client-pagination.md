# IMPL-10 — client/pagination.go

## Scope

Rewrite `client/pagination.go` against the post-redesign IR: collapse
the seven legacy pagination strategies into four named variants
(`none`, `cursor_token`, `next_url`, `counter`) plus the
author-controlled `custom` primitive. Drop the `paginationPlan.seed`
half — the per-drain wipe in `(*scope).resetPerDrainScratch` owns
bootstrapping, so the interface now carries a single `advance` method
that takes the post-page-response scope and returns `(terminate bool,
err error)`. Each variant evaluates its `terminate_when:` predicate
(author-supplied or variant default) before applying any writes,
matching the per-page execution order documented in
[`docs/runtime.md` §3](../runtime.md#3-pagination-loop).

## Old → new map

### Per legacy variant

| Legacy variant                | New variant         | Notes |
|-------------------------------|---------------------|-------|
| `cursor_token`                | `cursor_token`      | `from:` is a Path (response/header/steps); `to:` writes `state.<name>`. Default terminate: source resolves to absent / empty. |
| `scroll_id`                   | `cursor_token`      | Same shape — the scroll id is just a server-typed cursor token. Author overrides `terminate_when:` when the server signals end-of-session via a body flag rather than a missing id. |
| `graphql_relay`               | `cursor_token`      | `from:` points at `endCursor`; `terminate_when:` is `{not: {present: response.body.<...>.pageInfo.hasNextPage}}` or `{eq: ...}` on the flag — author-supplied. |
| `page_number` (from-body)     | `cursor_token`      | When the server returns the next page number rather than has_more, the value is just an opaque cursor; `cursor_token` carries it. |
| `link_header`                 | `next_url`          | `from: response.header.link` plus the `<(.*?)>;\s*rel="next"` regex with `capture: 1`. Default terminate fires when the post-regex value resolves to absent / empty. |
| `next_url_in_body`            | `next_url`          | `from:` points at the body field; no regex needed in the common case. |
| `page_number` (client-driven) | `counter`           | `start: 1`, `step: 1`. Default terminate: `{lt: {path: events.count, value: <step>}}` (short-page). |
| `offset`                      | `counter`           | `start: 0`, `step: {ref: state.page_size}` (or a literal). Same default terminate. |
| (no legacy equivalent)        | `custom`            | Primitive form for shapes that don't fit the named variants: a list of `{to, from, regex?, coerce?}` writes plus an author-supplied `terminate_when:` (required). |

### Interface shape

| Before                                                                                                              | After                                                                                                                                            |
|---------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------|
| `paginationPlan { seed(s *scope); advance(s, body, headers, events) (bool, error) }`                                | `paginationPlan { advance(s *scope) (terminate bool, err error) }`                                                                               |
| `seed` ran before each iteration's request bodies / queries were evaluated, populating bootstrap cursor names.       | No `seed`. The per-drain wipe in `(*scope).resetPerDrainScratch` resets every pagination `to:` field at drain start; the runner owns the call.   |
| `advance` returned `(want_more bool, err error)` — true means "fetch another page".                                  | `advance` returns `(terminate bool, err error)` — true means "stop the loop". The polarity flip matches `terminate_when:`'s sense.               |
| Producer body, producer headers, and events list were passed as positional arguments.                                | Producer body, headers, and events list live on the scope (`scope.body`, `scope.responseHeaders`, `scope.events`); the runner binds them.        |

### Per-variant logic

| Variant         | Default `terminate_when:`                                       | Advance write(s)                                                                              |
|-----------------|-----------------------------------------------------------------|-----------------------------------------------------------------------------------------------|
| `none`          | (always terminates after iteration 1).                           | None.                                                                                          |
| `cursor_token`  | `{not: {present: <from>}}` — source resolves to absent/empty.    | Write the resolved source value to `state.<name>`.                                              |
| `next_url`      | `{not: {present: <from>}}` — post-regex value absent/empty.      | Optionally apply `regex` + `capture`; write the resolved value to `state.<name>`.               |
| `counter`       | `{lt: {path: events.count, value: <step>}}` — short-page.        | Write `(state.<name> if set, else start) + step` to `state.<name>`.                             |
| `custom`        | None (required from author).                                     | Resolve every `advance[].from` first (snapshot), then write each result to its `to:` slot.       |

## Removed content

- `paginationPlan.seed(s *scope)` method.
- `cursorTokenPagination.advance`'s manual `s.cursor["token"]` writes.
- `pageNumberPagination` struct, its `has_more_at` evaluation, and its
  `BatchSize`-aware short-page logic.
- `offsetPagination` struct, its `BatchSize` short-page logic, and the
  `evalOffsetBatchSize` helper.
- `linkHeaderPagination` struct, the `parseNextLink` RFC 5988 parser,
  and the `regexp.Regexp`-cached pattern. Regex compilation now routes
  through `(*scope).applyRegex` (compiled per call, RE2).
- `nextURLInBodyPagination` struct and its `next_url_at` body-walk arm.
- `scrollIDPagination` struct, its `complete_when` body/headers
  save-restore dance, and its `scroll_id_at` zero-id default.
- `graphQLRelayPagination` struct and its `has_next_page_at` /
  `end_cursor_at` flow.
- Every `s.cursor[...]` mutation. The `cursor` map was deleted from
  `scope` at Slice 8 close; per-drain scratch lives in `s.state`.
- The file-level note about implicit `send_as` auto-injection (the
  pre-redesign comment had already documented its own removal).
- Every reference to the seven legacy strategy names in doc-strings and
  error messages.
- Imports of `net/http`, `regexp`, and `strings` — all dropped.

## Added content

- `customPagination` struct and its snapshot-then-write `advance`
  implementation.
- `paginationStateField(p schema.Path) (string, error)` helper —
  unpacks a `state.<name>` `To` Path into its field name with a
  defensive shape check.
- Per-variant `terminate` helpers that route the author-supplied
  `terminate_when:` through `(*scope).evalPredicate` and fall back to
  the variant's default termination check.
- File-level doc covering the four named variants, the per-page
  execution order, the per-drain scratch contract, and the MaxPages
  cap delegation.

## Reuse

- `(*scope).resolveNamespaceRef` resolves `from:` Paths for
  `cursor_token`, `next_url`, and the `events.count` path in counter's
  default predicate.
- `(*scope).applyRegex` (Slice 9) handles `next_url.regex` +
  `next_url.capture` and `custom.advance[].regex` (capture group 0,
  no default).
- `applyFormat` handles `custom.advance[].coerce`.
- `(*scope).evalPredicate` evaluates every author-supplied
  `terminate_when:` and the synthesised `events.count < step`
  resolution for counter's default.
- `(*scope).evalValue` resolves `counter.start`, `counter.step`, and
  `custom.advance[].from`.
- `toInt` / `asInt64` / `toString` / `isZeroRuntime` from `value.go`
  handle the small coercions.

## Per-page execution order

The runner binds `scope.events`, `scope.body`, and
`scope.responseHeaders` against the producer step's response before
calling `plan.advance(s)`. Inside `advance`, each named variant
follows the same fixed shape:

1. Read the variant's source (where applicable):
   - `cursor_token` / `next_url`: resolve `from:` via
     `resolveNamespaceRef`.
   - `next_url`: also apply `regex` + `capture` when set.
   - `counter`: resolve `step` (needed to construct the default
     predicate).
2. Evaluate `terminate_when:` (author-supplied or variant default):
   - Author-supplied: call `s.evalPredicate(*cfg.TerminateWhen)`.
   - `cursor_token` default: `!fromOK || from == nil || isZeroRuntime(from)`.
   - `next_url` default: `!fromOK || from == nil || isZeroRuntime(from)`
     (after regex).
   - `counter` default: `resolveNamespaceRef("events.count") < step`.
   If true, return `(true, nil)` — no writes fire.
3. Apply the variant's advance writes:
   - `cursor_token`: `state.<name> = got`.
   - `next_url`: `state.<name> = got` (post-regex value).
   - `counter`: `state.<name> = (state.<name> if set, else start) + step`.
4. Return `(false, nil)` — the runner issues the next request.

`custom` differs slightly: terminate is evaluated first (no source to
read), then every `advance[].from` is staged into a slice before any
`to:` writes apply. This snapshot-then-write order lets entries
reference each other without seeing a half-applied page.

`none` is the degenerate case: `advance` always returns `(true, nil)`.

## Counter's bootstrap

`counter` has no `seed` step; the per-drain wipe leaves `state.<name>`
unset at drain start. The author bootstraps the first iteration via
one of two equivalent shapes:

- **Field-default.** Declare `state.<name>: {default: <start>}`. The
  per-drain wipe seeds `state.<name>` to `<start>`. The request reads
  `{ref: state.<name>}` directly.
- **Ref-default.** Read with `{ref: state.<name>, default: <start>}`.
  The first request falls through to the default; subsequent requests
  read the advanced value.

`counter.advance` reads `state.<name>` (treating unset as `start`),
adds `step`, and writes the result. Both bootstrapping shapes converge
on the same sequence:

```
Field-default ({default: 1}, start: 1, step: 1):
  iter 1: state.page=1 (from wipe) → req page=1 → advance: state.page = 1+1 = 2
  iter 2: state.page=2 → req page=2 → advance: state.page = 3
  ...

Ref-default (no wipe seed, {ref: state.page, default: 1}, start: 1, step: 1):
  iter 1: state.page=unset → req page=1 (from ref default) → advance:
         state.page = (unset → start) + step = 1+1 = 2
  iter 2: state.page=2 → req page=2 → advance: state.page = 3
  ...
```

`counter.start` is therefore the value the runtime assumes a
not-yet-set field would hold; it does NOT replace the request's read
default.

## MaxPages

The runner (Slice 12) enforces the document-level MaxPages ceiling
(default `10_000`). Pagination plans here do not count iterations;
they just stay clean enough that the runner's "iteration cap exceeded"
diagnostic identifies the active variant by its error-message prefix
(`pagination.cursor_token.*`, `pagination.next_url.*`, etc.).

## Build state

`go build ./schema/...` — green. `go build -gcflags="-e" ./client/...`
— red, with **zero** errors in `pagination.go`. Errors stop with
`too many errors` by default; the full `-gcflags="-e"` count is 69
errors across 6 files:

- `client/auth.go` (1) — Slice 13.
- `client/http.go` (6) — Slice 13.
- `client/oauth2.go` (7) — Slice 13.
- `client/progress.go` (41) — Slice 11.
- `client/requestcache.go` (7) — Slice 13.
- `client/runner.go` (7) — Slice 12.

The two runner errors new in this slice (`pagination.seed undefined`,
`too many arguments in call to pagination.advance`) are deliberate —
Slice 12 rewires the call site against the new interface.

Test files stay red until Slice 17 owns the rewrite.

## Verification

`go build ./schema/...` is green. `go build -gcflags="-e" ./client/...`
lists 69 compile errors across 6 files; none live in `pagination.go`.

End-to-end behaviour-shape coverage (against real `*schema.Doc`
fixtures through the full client package) lands with Slice 17, once
the rest of `client/` compiles.

### Walk-through verification

Walked the trickier cases against the code:

- `cursor_token` with `from: response.header.x-next-token`:
  `resolveNamespaceRef("response.header.x-next-token")` reaches
  `headerLookup(s.responseHeaders, "x-next-token")`; default
  terminate fires when the header is absent or its value is `""`;
  otherwise `state.<name> = got` (the header string).
- `cursor_token` with author-supplied `terminate_when:
    {eq: {path: response.body.has_next, value: false}}`:
  the predicate evaluates against the bound `scope.body`; on a `true`
  result `advance` returns `(true, nil)` without writing.
- `next_url` with `from: response.header.link`, regex
  `<(.*?)>;\s*rel="next"`, capture 1: `resolveNamespaceRef` returns
  the joined Link header string; `s.applyRegex` returns the captured
  URL; default terminate fires only when the regex doesn't match
  (because `got` becomes nil) or when no Link header is set.
- `next_url` with `from: response.body.links.next`, no regex: the
  body walk returns the URL string directly; default terminate fires
  when the field is absent / `""`.
- `counter` with `start: 1, step: 1`, no terminate override, on a
  page that returned 10 events: `resolveStep` → `1`; `events.count`
  → `10`; `10 < 1` is false → don't terminate; `resolveStart` → `1`;
  current state.page=1 (wipe-seeded) → write `1+1=2`.
- `counter` with `start: 0, step: {ref: state.page_size}` (offset):
  step resolves to `state.page_size` (declared with a default);
  advance writes `offset + page_size`; default terminate fires when
  the page returned fewer than `step` events.
- `counter` first iteration with no `state.<name>.default` set:
  per-drain wipe leaves `state.page=unset`; on advance `cur` is nil
  → treat as `start`; write `start + step`. The request that fired
  before this iteration must have used `{ref: state.page, default:
  <start>}` to bootstrap, but `advance` is unaffected.
- `custom` with two entries — entry 0 reads `{ref: state.next}` and
  entry 1 writes `state.next`: `advance` evaluates entry 0's `from`
  first against the pre-write `state.next`, then entry 1's `from`,
  then applies both writes. The cross-entry read sees the OLD value.
- `custom` without `terminate_when:` is rejected at validate time
  (Slice 7); the runtime arm here treats `cfg.TerminateWhen` as
  always populated and evaluates it first.
- `none`: `advance` returns `(true, nil)` regardless of scope.

## Notes for downstream slices

### Slice 11 — `client/progress.go`

- Progress fires once per accepted page-response, including empty
  pages. The runner calls `applyProgress(s, doc.Progress)` inside
  the page loop, between event sink delivery and the
  `plan.advance(s)` call.
- Progress writes touch `state.<name>` directly; the staging pattern
  (resolve every `from:` first, then apply every `to:`) mirrors the
  one `customPagination.advance` uses here. Both functions read the
  pre-write `state.*` snapshot for every operand.

### Slice 12 — `client/runner.go`

- Drop every `pagination.seed(s)` call site. The per-drain wipe
  (`s.resetPerDrainScratch()`) runs at the top of every drain
  instead.
- The new advance signature is `pagination.advance(s) (terminate
  bool, err error)`. The runner binds `scope.events`, `scope.body`,
  and `scope.responseHeaders` against the producer step's response
  before the call.
- Polarity flip: the old `advance` returned `want_more=true` to mean
  "loop again"; the new one returns `terminate=true` to mean "stop".
- The runner is responsible for the MaxPages safety cap. Increment
  the page counter after each successful `advance` (terminate=false)
  and exit with an "iteration cap exceeded" diagnostic if the cap
  trips; the active plan's error wraps identify the variant in the
  failure path.
- Progress / sink delivery / advance order per `docs/runtime.md` §3:
  bind events → emit to sink → applyProgress → advance.

### Slice 13 — `client/http.go`, `auth.go`, `cache.go`

- Unaffected by pagination. The unified `Cache` plumbing and the
  `cache.*` namespace are read by `evalValue` through scope, which
  this slice does not touch.

### Slice 14 — `client/sink.go`, `redact.go`, `trace.go`

- `schema.IsSecret` already recurses through every Value form used
  inside `pagination.custom.advance[].from` and the named-variant
  `start` / `step` Values. The redact layer's secret-taint
  propagation is inherited; no additional plumbing required here.

### Slice 17 — Tests

- `client/pagination_test.go` (~1800 lines, mostly per-legacy-variant
  tables) is fully red. The rewrite should anchor on per-variant
  golden inputs driven by `internal/testserver/` fakes and exercise
  the four named variants plus `custom` against the small list of
  termination shapes (`default`, author-supplied, default-with-regex).
- The default-terminate paths read `events.count` through
  `resolveNamespaceRef` — the test fixtures should cover the
  "no page bound yet" arm (returns `int64(0)`) and the
  "post-empty-page" arm (returns `int64(0)`, terminates).
