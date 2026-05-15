# Schema redesign — unified reference grammar

Status: **planning** (slice 0).
Owner of the plan: original author + agent loop. Each slice is a discrete
task that an agent picks up via `HANDOFF.md`.

This document is the master plan for collapsing the six parallel
"reference a piece of data" sub-grammars in the IR down to **one
rooted-path grammar**, plus the follow-on tidying that the unification
enables. There is **no backwards compatibility goal**. The wire-format
identifier (`ir_version`) bumps can stay at `1` for all slices.

---

## 0. Why

Today the IR has six different syntactic shapes for "point at a piece of
data" and the cost shows up everywhere a template author asks _"which
dialect does this field speak?"_. The inventory:

1. **Namespace-rooted refs**: `{ref: state.x}`, `{ref: cursor.x}`,
   `{ref: steps.id.body.x}`, `{ref: extract.x}`. Root segment is a typed
   namespace.
2. **Body-relative paths** (bare dotted): `events_at: data.events`,
   `token_at: next_cursor`, `extract[].path: meta.last_id`. Same syntax
   as (1) but resolves against the producer body, not the namespace
   table. The validator emits a "namespace shadow" warning whenever the
   first segment collides with a namespace root — the warning is the
   schema apologising for its own ambiguity.
3. **Source + header pair**: `extract[]` reads with
   `source: body | header` and `header: <name>`. A third way to say
   "look in the response".
4. **`send_as` slots**: `query.<param>` / `header.<name>`. A
   mini-grammar for the write direction.
5. **Semantic-role values**: `{from_pagination: token}`,
   `{from_progress: window_start}`. Alias names that live in parallel
   to `cursor.<name>`, with an "explicit wins" tiebreaker rule.
6. **`body.<path>` namespace**: only legal inside `complete_when`
   predicates. Nowhere else.

Two of these (1 and 2) are visually identical at the call site. The
others each introduce a new vocabulary the author has to learn.

The redesign collapses everything to **one** path grammar rooted at a
declared namespace. The "is this body-relative or namespace-rooted?"
question disappears because the answer is always "namespace-rooted".
The `source: header` / `header: <name>` pair becomes a path with the
header namespace. `from_pagination` / `from_progress` disappear in
favor of stable `cursor.<role>` names. `send_as` auto-injection
disappears in favor of explicit `{ref: cursor.<role>}` in the request's
own query / header / body map. The `complete_when` `body.<path>`
namespace becomes `response.body.<path>`, which is the same word the
template already uses everywhere else.

---

## 1. Target design

### 1.1 The single path grammar

A `Path` is a namespace-rooted dotted string (or `{parts: [...]}` for
segments that contain literal dots — unchanged). The legal roots are:

| Root                       | Resolves to                                                          | Valid in                                                                                                  |
|----------------------------|----------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `state.<name>`             | Declared / auto-registered state field.                              | Anywhere a `Path` appears.                                                                                |
| `cursor.<name>`            | Inferred cursor field (pagination / progress / async_job / extract). | Anywhere a `Path` appears.                                                                                |
| `extract.<name>`           | Per-iteration extract binding from an earlier step.                  | Requests that follow the producing step.                                                                  |
| `steps.<id>.body.<path>`   | Earlier step's decoded response body.                                | Requests that follow step `<id>`.                                                                         |
| `steps.<id>.header.<name>` | Earlier step's response header (case-insensitive).                   | Requests that follow step `<id>`.                                                                         |
| `response.body.<path>`     | _Contextual_: the active step's response body.                       | Producer / events-bearing slots and `complete_when` predicates (see §1.4 for the full call-site list).    |
| `response.header.<name>`   | _Contextual_: the active step's response headers.                    | Same call sites as `response.body`.                                                                       |
| `item.<path>`              | `fan_out.as` binding (deferred — unchanged).                         | Inside a step with `fan_out:`.                                                                            |

`response.*` is the only contextual root: its meaning depends on the
call site (it's the current step's response in extract / cache slots,
the predicate-step's body inside a `complete_when`, the producer step's
body inside `pagination.*` / `response.events_at` / `progress.*`). The
validator pins which step is "current" per call site. The old
`body.<path>` root (legal only inside `complete_when`) is **deleted**;
authors write `response.body.<path>` in the same place.

### 1.2 Read sites that change shape

Every position that today carries a bare body-relative dotted string
becomes a `Path` requiring a namespace root. The full list:

| Field                                                  | Before                                | After                                              |
|--------------------------------------------------------|---------------------------------------|----------------------------------------------------|
| `response.events_at`                                   | `events_at: data.events`              | `events_at: response.body.data.events`             |
| `pagination.cursor_token.token_at`                     | `token_at: next_cursor`               | `token_at: response.body.next_cursor`              |
| `pagination.scroll_id.scroll_id_at`                    | `scroll_id_at: meta.scroll`           | `scroll_id_at: response.body.meta.scroll`          |
| `pagination.next_url_in_body.next_url_at`              | `next_url_at: meta.next_page`         | `next_url_at: response.body.meta.next_page`        |
| `pagination.graphql_relay.has_next_page_at`            | `has_next_page_at: data.x.hasNextPage`| `has_next_page_at: response.body.data.x.hasNextPage` |
| `pagination.graphql_relay.end_cursor_at`               | `end_cursor_at: data.x.endCursor`     | `end_cursor_at: response.body.data.x.endCursor`    |
| `pagination.page_number.has_more_at`                   | `has_more_at: meta.has_next`          | `has_more_at: response.body.meta.has_next`         |
| `auth.oauth2.<grant>.cache.expiry_field`               | `expiry_field: expires_in`            | `expiry_field: response.body.expires_in`           |
| `requests[].cache.expiry_field`                        | `expiry_field: expires_in`            | `expiry_field: response.body.expires_in`           |
| `progress.latest_event_timestamp.event_time.path`      | `path: created_at`                    | `path: response.body.created_at`* (see §1.5)       |
| `progress.max_event_field.event_time.path`             | `path: id`                            | `path: response.body.id`*                          |
| `progress.async_job.<role>.extract.<name>.path`        | `path: export_id`                     | `from: response.body.export_id` (renamed; see §1.3)|
| `requests[].extract[].path` (body source)              | `path: meta.last_id`                  | `from: response.body.meta.last_id` (renamed)       |
| `requests[].extract[]` header source                   | `source: header, header: ETag`        | `from: response.header.ETag`                       |
| `progress.async_job.poll.complete_when` `body.<path>`  | `path: body.status`                   | `path: response.body.status`                       |
| `pagination.scroll_id.complete_when` `body.<path>`     | `path: body.complete`                 | `path: response.body.complete`                     |

*\* `event_time.path` — the wrapper struct stays for now; only the path
itself changes shape. A future slice could collapse `event_time:
{path: <p>}` to `event_time: <Path>` directly, but it's an
ergonomics-only change with its own breakage so it stays separate.*

### 1.3 ExtractVar collapses

```yaml
# before
extract:
  - name: etag
    source: header
    header: ETag
    target: cursor
  - name: last_id
    path: meta.last_id
    target: cursor
```

```yaml
# after
extract:
  - name: etag
    from: response.header.ETag
    target: cursor
  - name: last_id
    from: response.body.meta.last_id
    target: cursor
```

The struct fields `source`, `header`, `path` collapse into one
namespace-rooted `from: Path`. Allowed roots for `from`:
`response.body.*`, `response.header.*` (and, by symmetry,
`steps.<id>.body.*` / `steps.<id>.header.*` — but no concrete template
needs the cross-step form today; the validator can allow it without
extra runtime work because the namespace already resolves).

`coerce` and `target` stay unchanged.

### 1.4 `response.*` call-site table

`response.*` is contextual. The validator pins "which step is
`response` rooted at" per call site:

| Call site                                                              | `response.*` rooted at                          |
|------------------------------------------------------------------------|-------------------------------------------------|
| `response.events_at`                                                   | The producer step (events-bearing step).        |
| `pagination.*.*_at` (token, scroll, next_url, has_next_page, end_cursor, has_more) | The producer step.                              |
| `progress.latest_event_timestamp.event_time.path` (and `max_event_field` / `async_job.on_complete.event_time.path`) | A per-event object inside the producer body (not the body root; see §1.7). |
| `requests[].extract[].from`                                            | The current step (the one carrying the extract).|
| `auth.oauth2.<grant>.cache.expiry_field` and `requests[].cache.expiry_field` | The cached step's response.                     |
| `progress.async_job.<role>.extract.<name>.from`                        | The named role step's response.                 |
| `pagination.scroll_id.complete_when` predicate paths                   | The producer step (where the scroll id lives).  |
| `progress.async_job.poll.complete_when` predicate paths                | The poll step.                                  |

Outside these call sites `response.*` is **rejected**. Templates that
need to reach a labelled prior step's body still use
`steps.<id>.body.<path>` (and the new `steps.<id>.header.<name>`).

### 1.5 `event_time.path` is per-event, not body-rooted

The current `event_time.path: created_at` is a per-event field path —
the runner walks `events_at`'s list and reads `created_at` off each
element. We keep the per-event semantics but rename the call-site to
make this obvious:

- Either: leave `event_time: {path: <body-relative-segment>}` as a
  per-event sub-path (bare dotted string; no namespace root). The
  validator documents it as "per-event field, not a namespace path".
- Or: spell it `response.body.events[*].created_at` (a wildcard form
  that today's `Path` does not support).

The first option keeps the surface tiny and is what the design picks.
Per-event paths are the **only** remaining bare dotted segment in the
IR; the schema docs document this exception explicitly. (`event_time`
applies to elements inside a list located by another `Path`; rooting
it doesn't help the reader.)

### 1.6 Semantic-role Values die

`{from_pagination: <role>}` and `{from_progress: <role>}` are deleted.
Every template that read them rewrites to `{ref: cursor.<role>}`. The
cursor namespace already declares all the names:

| Old form                              | New form                                            |
|---------------------------------------|-----------------------------------------------------|
| `{from_pagination: token}`            | `{ref: cursor.token}`                               |
| `{from_pagination: page}`             | `{ref: cursor.page}`                                |
| `{from_pagination: offset}`           | `{ref: cursor.offset}`                              |
| `{from_pagination: offset_end}`       | `{ref: cursor.offset_end}`                          |
| `{from_pagination: scroll_id}`        | `{ref: cursor.scroll_id}`                           |
| `{from_pagination: relay_cursor}`     | `{ref: cursor.<cursor_var>}` (the author's var name)|
| `{from_pagination: next_link}`        | `{ref: cursor.next_link}`                           |
| `{from_pagination: next_url}`         | `{ref: cursor.next_url}`                            |
| `{from_progress: latest_timestamp}`   | `{ref: cursor.last_timestamp}`                      |
| `{from_progress: window_start}`       | `{ref: cursor.window_start}`                        |
| `{from_progress: window_end}`         | `{ref: cursor.window_end}`                          |

This kills the "explicit wins / role vs cursor.<name>" tiebreaker rule
and the per-strategy role-membership validator check. The cursor
namespace's existing "field must be provided by the active strategies"
check carries all the same coverage.

### 1.7 Auto-injection (`send_as`) dies

`pagination.cursor_token.send_as` and `pagination.scroll_id.send_as`
both go away. The runner no longer auto-injects the cursor into the
producer step; templates wire it explicitly:

```yaml
# before
requests:
  - method: GET
    path: /findings
    query:
      limit: {format: string, value: {ref: state.page_size}}
pagination:
  cursor_token:
    token_at: next_cursor
    send_as: query.cursor      # auto-injects into ?cursor=<token>

# after
requests:
  - method: GET
    path: /findings
    query:
      cursor: {ref: cursor.token, default: ""}
      limit: {format: string, value: {ref: state.page_size}}
pagination:
  cursor_token:
    token_at: response.body.next_cursor
```

Trade-off: three extra lines per template versus one fewer concept and
one fewer "explicit wins" tiebreaker rule. The latter is worth the
former.

### 1.8 `Predicate.<verb>.equal` renames to `value`

The right-hand-side of `{eq | gt | lt | gte | lte: {path, equal}}` is
named `equal` for historical reasons — `eq` was the first verb. For
the comparison verbs (`gt | lt | gte | lte`) reading
`equal: <Value>` is actively misleading. Rename to `value`:

```yaml
# before
eq:
  path: state.region
  equal: "gov"

# after
eq:
  path: state.region
  value: "gov"
```

This is cosmetic-only on the Go side (the struct field stays
`PredicateEq.Value`, type unchanged) but every template, every test,
every doc example flips.

### 1.9 Expiry slots leave `cursor.__…`

The framework writes `cursor.__oauth2_<store_in>_expires_at` and
`cursor.__step_<store_in>_expires_at` today. Two underscores
screaming "private, don't touch" is the tell that they're conflated
with the namespace they live in. They move to **state**, paired with
the token they describe:

| Before                                                 | After                                  |
|--------------------------------------------------------|----------------------------------------|
| `state.<store_in>` (token)                             | `state.<store_in>` (token, unchanged)  |
| `cursor.__oauth2_<store_in>_expires_at`                | `state.<store_in>_expires_at`          |
| `cursor.__step_<store_in>_expires_at`                  | `state.<store_in>_expires_at`          |

The validator auto-registers both names as `runtime` `string` state
fields. The expiry pair is therefore single-snapshot-block (only
`Snapshot.State` is touched); `Snapshot.Cursor` stops carrying any
framework-private keys.

This is the minimal step toward state/cursor unification without
actually deleting the cursor namespace. Full unification (option 1 in
the friend's advice) is deferred to a later round — see §3.

### 1.10 Summary: what an author writes in the new world

A complete `cursor_token` template after the redesign:

```yaml
ir_version: "1"

state:
  fields:
    url:         {type: url,    default: "http://localhost:9999"}
    page_size:   {type: int,    default: 5}
    api_key:     {type: secret, default: "test-bearer-token"}

defaults:
  base_url: {ref: state.url}

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    path: /cursor_token/findings
    query:
      cursor: {ref: cursor.token, default: ""}
      limit:  {format: string, value: {ref: state.page_size}}

response:
  decode: json
  events_at: response.body.findings

pagination:
  cursor_token:
    token_at: response.body.next_cursor

progress:
  latest_event_timestamp:
    event_time:
      path: created_at         # per-event sub-path; see §1.5

error:
  mode: standard
```

A snippet that wires an extracted ETag back as a conditional GET:

```yaml
requests:
  - id: data
    method: GET
    path: /events
    headers:
      If-None-Match: {ref: cursor.etag, default: ""}
    extract:
      - name: etag
        from: response.header.ETag
        target: cursor
    on_status:
      304: skip
```

And an `async_job` poll predicate, now reading from `response.body`:

```yaml
progress:
  async_job:
    poll:
      step: poll
      complete_when:
        eq:
          path: response.body.status
          value: complete
      extract:
        result_url:
          from: response.body.result_url
```

Every read in the document is `{ref: <namespace>.<...>}`. Every body
reference says `response.body.<...>` or `steps.<id>.body.<...>`. Every
header reference says `response.header.<...>` or
`steps.<id>.header.<...>`. There is one grammar.

---

## 2. Slices

The redesign breaks into discrete slices that are landable
independently. Slice numbering is execution order; each slice's
"completes when" gate is reviewable in isolation.

The header before each slice lists:
- **Effort**: rough size (S / M / L / XL).
- **Scope**: the surfaces it touches.
- **Depends on**: prior slices that must land first.
- **Breaks**: which templates / docs / tests need updating in the
  same slice.

### Slice 0 — Planning (this document) — **complete**

Effort: S. Scope: docs. Depends on: nothing.

Output: `SCHEMA_DESIGN.md` (this file) and `HANDOFF.md` (rolling
spec). No code changes.

### Slice 1 — `Path` codec accepts the new roots

Effort: M. Scope: `schema/path.go`, `schema/validate.go`,
`client/state.go::resolveNamespaceRef`, new tests in
`schema/fixtures_test.go` and `client/value_test.go`. Depends on: 0.
Breaks: none yet — additive.

Goal: teach the codec and the namespace resolver about three new
roots, **without yet making any existing field reject the old form**:

- `response.body.<path>` and `response.header.<name>` — contextual
  roots. The validator threads a per-call-site flag indicating which
  step's body / headers `response.*` resolves against. Until slice 2
  this flag is only set inside `complete_when` predicates (where the
  old `body.<path>` already paired one-to-one with the new
  `response.body.<path>`).
- `steps.<id>.header.<name>` — extends the existing
  `steps.<id>.body.<path>` so prior-step headers are addressable.

The runner (`client/state.go::resolveNamespaceRef`) gains a
`response` arm + a `steps.<id>.header.*` arm. A scope-side field
`responseBody` / `responseHeaders` is set per call site by the caller
(initial wiring: complete_when predicate evaluation).

Completes when:
- `schema.Validate` accepts `response.body.*`, `response.header.*`,
  `steps.<id>.header.*` in every position a `Path` is legal (paired
  with the per-site allow-list — same shape as the existing
  `allowBody` flag).
- `client` resolves all three roots correctly when reached.
- Existing tests pass; new tests in `schema/fixtures_test.go` cover
  positive (accepted) and negative (rejected outside the allowed call
  sites) cases.
- All `ir_version` strings stay at `"1"` for this slice — wire shape
  is purely additive.

### Slice 2 — Body-relative path positions become namespace-rooted

Effort: L. Scope: `schema/schema.go` (field type changes),
`schema/validate.go`, every `client/*.go` that reads one of these
fields, every `templates/*.yml`, `schema/testdata/*`,
`cmd/skopos/testdata/*` golden files, `schema/fixtures_test.go`,
`client/*_test.go`. Depends on: 1. Breaks: every template + every
fixture.

Goal: change the runtime contract so that every body-relative field
(see §1.2 table) requires a namespace root. Reject bare dotted
strings at codec time.

Implementation order inside the slice:

1. Wire `ir_version: "1"` as the only accepted version.
2. Change field types where today they're `Path` (already typed)
   but validated as body-relative. Tighten the validator to require
   `response.body.<...>` (or `steps.<id>.body.<...>`) for each field
   in §1.2's table.
3. Update the runtime resolvers in `client/bodypath.go`,
   `client/pagination.go`, `client/progress.go`, `client/extract.go`,
   `client/oauth2.go`, `client/requestcache.go` to walk the namespace
   path (the leading `response.body.` / `steps.<id>.body.` segments
   become a no-op for the runner because it already had the producer
   body in scope; it just strips the root and walks the rest).
4. Delete the "namespace shadow warning" code path — it's obsolete.
5. Rewrite every template under `templates/*.yml` to use the new
   form. Regenerate `cmd/skopos/testdata/*.txt` golden files.
6. Rewrite every fixture under `schema/testdata/*` and every inline
   YAML in `schema/fixtures_test.go` to match.

Completes when:
- The validator rejects bare body-relative paths everywhere with a
  precise error pointing at the new form.
- Every template loads + validates + runs end-to-end against the
  testserver.
- `go test ./...` is green.

### Slice 3 — `ExtractVar` collapses to a single `from` field

Effort: M. Scope: `schema/schema.go::ExtractVar`,
`schema/validate.go`, `client/extract.go`, every template that uses
`extract:`, `schema/fixtures_test.go`,
`client/extract_test.go` (or wherever extract tests live).
Depends on: 1, 2. Breaks: templates using `extract:`.

Goal: replace `path` + `source` + `header` with a single `from: Path`
that points at `response.body.<...>` or `response.header.<name>`.

Implementation:

1. Add a `From Path` field to `ExtractVar`. Remove `Path`, `Source`,
   `Header`.
2. Update the codec — old fields are not accepted (yes, that means
   every YAML test fixture changes).
3. Update the validator to enforce the legal roots (`response.body`,
   `response.header`, and by symmetry `steps.<id>.body` /
   `steps.<id>.header`).
4. Update the runtime to walk `from` (strip the root segment, then
   dispatch body-walk vs header-lookup based on which root matched).
5. Update every template + golden file.

Completes when:
- `ExtractVar.From` is the only read-side field on the struct;
  `coerce` / `target` / `name` unchanged.
- All templates use the new form; goldens regenerated.
- Tests green.

### Slice 4 — Delete `from_pagination` / `from_progress`

Effort: M. Scope: `schema/value.go` (delete two Value variants),
`schema/validate.go` (delete `validPaginationRole` /
`validProgressRole` / `rolesForStrategy` / `rolesForProgressStrategy`
helpers), `client/value.go` (delete two evaluator arms),
`client/pagination.go` + `client/progress.go` (delete
`from_pagination` / `from_progress` seeding), every template,
`cmd/skopos/testdata/*`, `schema/fixtures_test.go`. Depends on: 1, 2.
Breaks: every template.

Goal: `{from_pagination: <role>}` and `{from_progress: <role>}` are
**removed** from the Value union. Every template author writes
`{ref: cursor.<role>}` instead. The role-membership validator check
goes away (the cursor namespace's "field must be inferred" check
already covers it).

Implementation:

1. Delete the two discriminator keys from `valueDiscriminatorKeys`
   and `valueVariantAllowedKeys`.
2. Delete `FromPagination` / `FromProgress` fields from `Value`.
3. Delete the codec branches (YAML + JSON, marshal + unmarshal).
4. Delete the validator arms.
5. Delete the runtime evaluator arms — but the pagination / progress
   drivers MUST still seed `scope.cursor` with the same names that
   were previously routed through `s.fromPagination` /
   `s.fromProgress`. The seeding moves from "per-iteration
   `fromPagination` map" to "per-iteration writes into
   `scope.cursor`". (Important: this requires care for
   `cursor.scroll_id` first-iteration behavior — today it's unset
   until the server returns one. Keep that semantics; the runtime
   writes the cursor only after the server returns a value.)
6. Rewrite every template.
7. Update the schema doc tables that catalogue the role set.

Completes when:
- `schema.Value` has 11 variants (literal_string, literal_int,
  literal_bool, ref, now, concat, select, format, base64, list,
  object).
- Every template uses `{ref: cursor.<name>}` for pagination /
  progress signals.
- Tests green.

### Slice 5 — Delete `send_as` auto-injection

Effort: M. Scope: `schema/schema.go::CursorTokenPagination` /
`ScrollIDPagination`, `schema/validate.go::checkSendAs` (delete),
`client/pagination.go` (delete the `paginationAutoInjector` interface
and the `autoInjectSlot` plumbing), every template using
`cursor_token` or `scroll_id`, `client/runner.go` (drop the inject
wiring), `cmd/skopos/testdata/*`. Depends on: 1–4. Breaks: every
`cursor_token` / `scroll_id` template.

Goal: the runner no longer auto-injects the cursor into the producer
request. The template explicitly writes
`{ref: cursor.token, default: ""}` (or `cursor.scroll_id`, with
appropriate first-iteration handling) in its own `query` / `headers`
/ `body` map.

Implementation:

1. Delete `SendAs` from both `CursorTokenPagination` and
   `ScrollIDPagination`.
2. Delete the `paginationAutoInjector` interface, `autoInjectSlot`
   type, and the `reqInject` wiring in `client/runner.go` /
   `client/pagination.go` / `client/http.go::queryDeclared` /
   `headerDeclared`.
3. Update every `cursor_token` and `scroll_id` template to add the
   explicit ref in its `query:` / `headers:` map.
4. Refresh goldens.
5. Update validator error messages — the "explicit wins" rule is
   gone.

Completes when:
- `CursorTokenPagination` carries only `TokenAt Path`.
- `ScrollIDPagination` carries only `ScrollIDAt Path` and the
  optional `CompleteWhen *Predicate`.
- Every template explicitly wires the cursor in its request.
- Tests green.

### Slice 6 — Predicate `equal` → `value`

Effort: S. Scope: `schema/predicate.go` (codec field rename),
`schema/validate.go` (no struct field change, only the YAML tag and
JSON tag), every template / fixture / doc using `equal:`. Depends
on: 1. Breaks: every doc + every template that uses a Predicate verb.

Goal: cosmetic-only rename so `gt: {path: X, value: Y}` reads as
"X greater than Y" rather than "X greater-than-equal Y".

Implementation:

1. Change `PredicateEq`'s YAML/JSON tag from `equal` to `value`. The
   Go field name `Equal` either renames to `Value` (cleaner) or stays
   (less churn). Pick `Value` for the rename to keep readers happy.
2. Update every template and fixture.
3. Update every doc that shows a Predicate.

Completes when:
- `equal:` is no longer accepted by the codec.
- Every doc / template / fixture is on `value:`.
- Tests green.

### Slice 7 — Move expiry slots out of `cursor`

Effort: M. Scope: `client/oauth2.go`, `client/requestcache.go`,
`client/state.go`, `schema/validate.go` (cache `store_in` collision
check needs to also reject `<store_in>_expires_at`), `docs/stores.md`,
`docs/runtime.md`, tests. Depends on: 1.

Goal: framework-internal expiry-tracking slots stop polluting
`cursor.*`. Both caches write to `state.<store_in>_expires_at` (a
runtime-mutability string field auto-registered alongside the token
slot). `Snapshot.Cursor` carries only author-facing cursor data.

Implementation:

1. Rename the slot in both OAuth2 and request-cache code paths.
2. Auto-register `<store_in>_expires_at` as `runtime` `string` state.
3. Update the validator collision-check to also reject author state
   declarations of `<store_in>_expires_at` whenever a cache block
   declares `<store_in>`.
4. Migrate templates — no template references the old key by name
   (it was underscore-prefixed for a reason), so this is purely an
   internal slot rename. State snapshot files in `cmd/skopos/testdata`
   need regeneration.
5. Document the new slot in the runtime / store docs.

Completes when:
- No `cursor.__*` keys remain in the runtime code, fixtures, or
  golden files.
- Tests green.

### Slice 8 — Docs and `schema-reference.md`

Effort: L. Scope: every file in `docs/`, `README.md`, the
auto-generated `docs/schema-reference.md` (via
`tools/gen-schema-doc`). Depends on: 1–7 (or whatever subset has
landed when this runs). Breaks: nothing (docs only).

Goal: every example, every namespace table, every authoring rule
reflects the new design. The schema-reference is regenerated from the
new Go types.

Implementation:

1. Rewrite `docs/schema.md`:
   - New namespace table (§1.1).
   - New body-relative path policy (deleted — everything is namespace-rooted).
   - New `ExtractVar.from` shape.
   - Delete `from_pagination` / `from_progress` sections.
   - Delete `send_as` references.
   - Update Predicate examples to `value:`.
   - Update the cursor-namespace table to reflect that `cursor.token`
     / `cursor.scroll_id` are the only pagination signals (no more
     parallel role names).
   - Delete the namespace-shadow warning section.
2. Rewrite `docs/runtime.md`:
   - Update the scope-lifetime table — `fromPagination` /
     `fromProgress` are gone; their contents merged into `cursor`.
   - Update §8.4–§8.6 supported-variant tables.
3. Rewrite `docs/api-methods.md`:
   - Every YAML snippet in §1–§6 uses the new refs.
   - Update §2.4 (`cursor_token`), §2.7 (`scroll_id`) to remove
     `send_as`.
   - Update §3.9 / §1.10 (session cookie via POST login) to use
     `from: response.header.Set-Cookie`.
   - Update §5 (progress) examples.
4. Update `docs/usage.md` (snippets reference the new field names).
5. Update `docs/stores.md` — note that `Snapshot.Cursor` is now
   author-facing only (slice 7 moved expiry slots into `Snapshot.State`).
6. Update `README.md` Quickstart and Templates table to match any
   renamed fields.
7. Regenerate `docs/schema-reference.md` via
   `tools/gen-schema-doc`; CI gate stays the same.

Completes when:
- Every doc loads cleanly.
- `go run ./tools/gen-schema-doc -check` is green.
- Every doc example is copy-pasteable into a template and validates.

### Slice 9 (optional, deferred) — Merge `state` and `cursor`

Effort: XL. Scope: everything. Depends on: 1–8 landed and baked.

Goal: kill the cursor namespace as an author-facing concept. Every
pagination / progress / async_job field auto-registers a
`mutability: runtime` state field; `Snapshot.Cursor` is deleted;
`Store.Save` / `Store.Load` only round-trip `Snapshot.State`.

This is option 1 in the friend's advice. It is **not part of the
current redesign loop**; it's recorded here so the next planning
round can pick it up without re-discovery. The reason to defer:
slices 1–8 already deliver ~70% of the readability win and the
runtime persistence layer is large enough that a separate planning
cycle is appropriate.

---

## 3. Out-of-scope / explicitly deferred

The following changes were considered and are **not** in this
redesign:

- **Merging `state` and `cursor`** — see slice 9 above. Recorded as
  a possible follow-on; not in the loop.
- **Collapsing `event_time: {path: <p>}` to `event_time: <Path>`** —
  trivial ergonomics; can land any time.
- **Wildcard / list-iteration paths** (`response.body.events[*].id`).
  Per-event `event_time.path` already handles the only template
  motivating this; adding wildcard syntax is a larger Value-grammar
  change.
- **Renaming `Predicate.Eq` Go type** (it serves as the shared
  `{path, value}` shape for eq / gt / lt / gte / lte and the name
  `PredicateEq` is misleading). Cosmetic-only Go rename; can land
  alongside slice 6 or separately.
- **Killing `pagination.scroll_id.complete_when`'s contextual
  `body.<path>`** — already deleted in slice 2 because every
  `complete_when` predicate now uses `response.body.<path>`.
- **`fan_out` runner implementation** — already deferred per the
  current schema, untouched by this redesign.

---

## 4. Rules of engagement

These are the constants that every slice respects.

1. **No backwards compatibility.** Each slice's first commit bumps
   anything needed to compile + pass tests under the new shape.
   Bigger templates / goldens regenerate in the same commit as the
   code change.
2. **`ir_version` lives at `"1"` from slice 1 onwards.** Slice 1 is
   purely additive and stays on `"1"`. No release has been done yet so we can still use `"1"` for the wire-format identifier.
3. **Every slice is self-contained.** `go test ./...` is green at
   the end of every slice. CI never sees a half-renamed schema.
4. **Per-slice diff hygiene.**
   - One slice = one PR (or one logical commit set).
   - Templates and goldens regenerate in the same diff as the code
     they exercise.
   - `tools/gen-schema-doc` runs in slice 8 (or alongside any slice
     that touches a struct field a doc references).
5. **Validation messages name the new form.** Every error / warning
   in `schema.Validate` points at the new namespace-rooted form, not
   the deleted shape.
6. **Codec round-trip stays byte-stable.** The existing
   `Value`-and-`Path` round-trip guarantee (load → marshal returns
   byte-identical YAML / JSON) holds under the new shape. Tests in
   `schema/fixtures_test.go` already cover this; new fixtures land
   beside renamed ones.
7. **Secret propagation is untouched.** The `IsSecret(d, v)` walk
   already covers every container in `Value`; deleting two variants
   (`FromPagination`, `FromProgress`) removes two arms but adds
   nothing. The expiry-slot move (slice 7) doesn't change secret
   semantics because expiry timestamps are not secret.

---

## 5. Progress log

Updated after each slice lands. Each entry: slice number, date,
brief outcome, link to PR / commit.

| Slice | Date       | Outcome                                                                                              |
|-------|------------|------------------------------------------------------------------------------------------------------|
| 0     | 2026-05-15 | Plan drafted. SCHEMA_DESIGN.md + HANDOFF.md committed.                                               |
| 1     | 2026-05-15 | Additive groundwork landed. `response.body.<path>`, `response.header.<name>`, and `steps.<id>.header.<name>` accepted by the validator at `complete_when` sites and rejected elsewhere; runtime resolver + scope fields (`responseHeaders`, `stepHeaders`) wired; `response.body.<path>` interchangeable with legacy `body.<path>` (proven by predicate-level test). No templates / docs / goldens touched. |
| 2     | 2026-05-15 | Body-relative path positions converted to namespace-rooted paths. `events_at`, every `pagination.*_at`, `complete_when.path`, `auth.oauth2.cache.expiry_field`, `requests[].cache.expiry_field`, and the new `progress.async_job.*.extract.<name>.from` slot all reject bare dotted strings and require `response.body.<path>` (or `steps.<id>.body.<path>` for a labelled prior step). `AsyncExtract.Path` renamed to `AsyncExtract.From`. Runtime resolver lost `case "body":`; `stripBodyRoot` / `scope.resolveBodyPath` helpers in `client/bodypath.go` trim the leading two segments. Per-event sites (`progress.{latest_event_timestamp,max_event_field}.event_time.path` and `async_job.on_complete.cursor_update.event_time.path`) keep bare per-event paths and now reject namespace roots. All 26 templates, all `schema/testdata/*.yml` fixtures, and the embedded YAML in `cmd/skopos/testdata/*.txt` regenerated. `extract.path` / `requests[].extract[].path` deliberately stay body-relative — that move lands in slice 3 alongside the `from:` rename. |
| 3     | 2026-05-15 | `requests[].extract[]` collapsed to a single `from: Path`. `ExtractVar.Path / Source / Header` are gone; `ExtractVar.From` is the lone read-side field. Custom YAML/JSON unmarshalers reject the deleted `path:` / `source:` / `header:` keys at parse time with hints pointing at the new shape. Validator helper `checkExtractFromPath` enforces the four legal roots (`response.body.<path>`, `response.header.<name>`, `steps.<id>.body.<path>`, `steps.<id>.header.<name>`) and the slice-2-deferred extract-shadow warning + `isNamespaceRoot` warning arm are deleted (the new `from` is fully namespace-rooted). Runtime `runExtracts` dispatches body-walk vs header-lookup off the path root via a new `scope.resolveExtractFrom` helper that mirrors the four validator arms. `templates/session_cookie.yml`, `schema/testdata/multi_field_cursor.yml`, and the embedded YAML in `cmd/skopos/testdata/{errors,schema}/*.txt` rewritten. `client/{requests,runner}_test.go` flipped to `From`. New `TestSliceThreeExtractFromCollapse` covers parse-time rejections, the new namespace-rooted requirement, every accepted root, and bare-segment errors. Doc-gen reflects the slimmer `ExtractVar`. |
| 4     | _pending_  |                                                                                                      |
| 5     | _pending_  |                                                                                                      |
| 6     | _pending_  |                                                                                                      |
| 7     | _pending_  |                                                                                                      |
| 8     | _pending_  |                                                                                                      |
| 9     | _deferred_ | Optional follow-on round.                                                                            |
