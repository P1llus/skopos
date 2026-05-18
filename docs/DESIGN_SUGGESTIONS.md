# Schema redesign — design suggestions

This document proposes a reshaped schema for skopos. It covers every
top-level block, the cross-cutting primitives, and two side-by-side
template sketches to ground the proposal in concrete YAML.

Section 6 records the settled decisions from the design discussion and
the implementation-time work items that the code-change PR will pick
up. There are no open schema questions.

No code changes accompany this document. The next step is a separate
plan specifying the Go-side refactor (struct changes, validator
changes, runtime changes). The project has no installed user base, so
there is no on-disk snapshot to migrate — the old shape is deleted
outright in the same code change that introduces the new one.

---

## 1. Goals and non-goals

**Goals**

- End-user readability: a template author should be able to read a spec
  and understand the full iteration lifecycle without consulting a
  strategy-specific cursor catalogue.
- One vocabulary per concept. The same concept has the same field name
  everywhere. `to:` and `from:` are the one-word verbs for direction;
  named variants exist only where they save real ink over the primitive
  form.
- Refs are the language. Every dynamic value the runner produces should
  be readable via the same `{ref: <namespace>.<name>}` form authors
  already use.
- Explicit namespaces by lifetime. `state.*` is persisted across
  drains. `cache.*` is process-memory only. `events.*` is the decoded
  events list. There is no hidden `cursor.*` namespace — every name the
  runner writes belongs to a namespace the author can see.
- Sugar where it earns its keep; primitives everywhere else. Pagination
  keeps a small set of named variants for the common 80% (with a
  `custom:` escape hatch). Progress is just a list of state writes; no
  variants. The schema is small.
- Per-page progress checkpointing. The runner advances `state.*`
  destinations after each accepted page-response, regardless of event
  count, so authors can persist server-provided cursors and timestamps
  even when an individual page returned no events.
- Recovery via progress, not via per-drain state. Per-drain pagination
  state is always wiped at drain start. A drain that fails mid-page
  re-bootstraps pagination on retry; the high-water mark that prevents
  refetching the universe is whatever `progress:` already persisted to
  `state.*`. At-least-once delivery + sink dedup handles any overlap.

**Non-goals**

- Backward compatibility. The project has no released version. There
  is no installed user base, no existing on-disk `state.json` we need
  to migrate, and no "both old and new" support to plan for. The new
  schema is the source of truth; the old schema is deleted in the
  same code change that introduces the new one. No shims, no
  compatibility layers, no migration code. `ir_version` stays `"1"`.
- Rewriting all 26 templates in this document. Two side-by-side
  sketches (§5) are included to validate the proposal; the remaining
  templates are rewritten in the code-change PR.
- Specifying every runtime detail. Some implementation choices
  surface in §6 as notes for the implementation plan; none of them
  affect the schema shape.

---

## 2. Principles

1. **One vocabulary per concept.** Reading direction is always `from:`.
   Writing direction is always `to:`. Predicate hooks are always
   `terminate_when:` (loop stop) or `if:` / `when:` (conditional). No
   `read_from` / `write_to` / `target` / `store_in` / `*_at` /
   `complete_when` / `repeat_until` variants for the same idea.

2. **Lifetime is encoded by namespace.** `state.*` is persistent across
   drains. `cache.*` is in-memory only and cleared on runner restart.
   `events.*`, `steps.*`, `extract.*`, `response.*` are per-iteration
   and never persisted. Authors can tell at the use site whether a
   value survives a restart by reading the namespace prefix.

3. **No `mutability` annotations.** Whether a `state.*` field is
   operator-config-only, per-drain-scratch, or persistent high-water
   is *inferred* from where it is written:

   - Default-only, no writes → operator config (defaults applied every
     start; nothing persisted).
   - Target of `pagination.advance:` → per-drain scratch.
   - Target of `progress:` writes or `requests[].extract` to state →
     persistent across drains.

   The author may override with an explicit annotation in edge cases
   (e.g. a field both blocks want to write), but the validator catches
   conflicts so it's almost never needed.

4. **Sugar over primitives, in two places only.** `pagination:` and
   `progress:` are the *only* schema blocks where iteration-state logic
   lives. `pagination:` carries a small set of named variants plus a
   `custom:` escape hatch. `progress:` is a flat list of state writes;
   no named variants. Request-level primitives (`if:`, `on_status:`,
   `fan_out:`, `cache:`, `expect_status:`, `extract:`,
   `terminate_when:`) handle request-execution concerns only and never
   advance the iteration cursor.

5. **`default:` accepts the full Value language.** A `state` field's
   `default:` may be any Value, including `{subtract: [{now: true},
   "720h"]}`. First-run seeding is just a default; there is no
   `initial.lookback` / `initial_offset` family.

6. **Predicates and refs are absent-tolerant.** `{ref: response.body.x}`
   resolves to a zero Value when `x` is missing. `{present:
   response.body.x}` returns `false`. `{eq: {path: response.body.x,
   value: ...}}` returns `false`. No path-resolution exception is ever
   thrown; the CEL/httpjson pattern of "force a failure to terminate"
   has no place in this schema.

7. **Per-page progress checkpoints, irrespective of event count.**
   Progress writes happen once per accepted page-response (i.e. any
   response whose status passes `expect_status:` and `on_status:`),
   not at drain end. Empty event lists still trigger progress so that
   authors can persist server-provided cursors, ingestion timestamps,
   or other body fields that live outside the events array. The runner
   does not buffer events across pages; reducers (`max`, `min`,
   `first`, `last`, `count`) are streaming operations.

8. **Per-drain state always resets at drain start.** Fields written by
   `pagination:` are not preserved across drain boundaries. A drain
   that fails mid-page re-bootstraps pagination on the next start.
   Recovery from partial failure is the author's responsibility via
   what they choose to write in `progress:` — typically a high-water
   timestamp or server-provided cursor that lives in `state.*` and
   survives.

---

## 3. Block-by-block proposal

The top-level keys, in canonical order:

| Key            | Required | Section                            |
|----------------|----------|------------------------------------|
| `ir_version`   | yes      | [§3.1](#31-ir_version)             |
| `state`        | no       | [§3.2](#32-state)                  |
| `auth`         | yes      | [§3.3](#33-auth)                   |
| `requests`     | yes      | [§3.4](#34-requests)               |
| `response`     | yes      | [§3.5](#35-response)               |
| `pagination`   | yes      | [§3.6](#36-pagination)             |
| `progress`     | yes      | [§3.7](#37-progress)               |
| `error`        | no       | [§3.8](#38-error)                  |

`defaults:` is removed. `flow:` / `progress.async_job` are removed.
`cache.*` is a namespace, not a top-level block — cache *configuration*
lives on the auth grant or the request that needs it (§3.3, §3.4).

---

### 3.1 `ir_version`

Unchanged. Remains the literal string `"1"`. A single bump will ship
alongside the code change.

---

### 3.2 `state`

A flat map of typed field declarations. The `.fields` indirection from
the current schema is removed.

```yaml
state:
  url:
    type: url
    default: "http://localhost:9999"
  api_key:
    type: secret
  page_size:
    type: int
    default: 5
  last_timestamp:
    type: timestamp
    format: rfc3339
    default: {subtract: [{now: true}, "720h"]}
  next_token:
    type: string
```

**Field declaration table**

| Field      | Required | Description |
|------------|----------|-------------|
| `type`     | yes      | One of `string`, `int`, `bool`, `secret`, `duration`, `timestamp`, `url`, `enum`. See [§4.4 type table](#44-type-table). |
| `default`  | no       | Any **Value** (not just a literal). `{subtract: [{now: true}, "720h"]}` seeds a timestamp field. |
| `values`   | no       | Closed enumeration; valid only with `type: enum`. |
| `format`   | no       | Wire-format hint for `type: timestamp` and `type: duration`. Closed-set verb (`rfc3339`, `rfc3339nano`, `unix_seconds`, `unix_millis`) or a Go layout string (e.g. `"2006-01-02T15:04:05.000-0700"`). Default for `timestamp`: `rfc3339`. Ignored on other types. |

**Inferred lifetime**

Whether a field is operator-config / per-drain / persistent is *not*
declared on the field. It is derived from where the field appears as
the `to:` of a write:

| Lifetime                | Inference rule                                                          | Examples                                  |
|-------------------------|-------------------------------------------------------------------------|-------------------------------------------|
| Operator config         | Has `default:` (or is operator-supplied) and is never written to.       | `state.url`, `state.api_key`, `state.page_size` |
| Per-drain scratch       | Appears as the `to:` of any `pagination.*.<...>.to` write.              | `state.next_token`, `state.page`          |
| Persistent              | Appears as the `to:` of any `progress:` write or any extract `to: state.*`. | `state.last_timestamp`, `state.window_start` |

Conflicts (same field written from both pagination and progress) are
rejected at validate time. Authors can disambiguate with an explicit
annotation in the rare case it matters; this is not part of the common
path.

**Notes**

- `cache.*` slots are NOT in `state.*`. They live in their own
  namespace (§4.3) and are never persisted.
- The current `extract[].target: cursor` and the current implicit
  `cursor.*` namespace are removed. Extracts that previously wrote to
  `cursor.<name>` now write `to: state.<name>` (and the destination
  must be declared under `state:`).
- `mutability` as a field disappears. The implementation may keep a
  derived `mutability` on the parsed struct for runtime dispatch, but
  it is never authored.

---

### 3.3 `auth`

The auth variant set (`none`, `bearer`, `basic`, `api_key`, `custom`,
`oauth2`, `multi_mode`) is unchanged in shape. Two fixes:

**Cache unification.** `TokenCache` (OAuth2) and `RequestCache`
(requests[].cache) are now one struct: `Cache`. Same fields in both
places. `to:` points at a `cache.*` slot (the runner allocates the
slot if absent); `expires_at:` is a Value that resolves to a
`time.Time` and accepts a `default:` for APIs that don't return an
expiry.

```yaml
auth:
  oauth2:
    client_credentials:
      token_url: {ref: state.token_url}
      client_id: {ref: state.client_id}
      client_secret: {ref: state.client_secret}
      cache:
        to: cache.access_token
        expires_at: {ref: response.body.expires_in, default: "1h"}
        buffer: 60s
```

**`Cache` field table**

| Field         | Required | Description |
|---------------|----------|-------------|
| `to`          | yes      | `cache.<name>` slot. Read elsewhere via `{ref: cache.<name>}`. |
| `expires_at`  | yes      | Value resolving to a `time.Time` (use `{format: ...}` or `{add: [...]}` as needed). Accepts a `default:` for APIs without an expiry field. |
| `buffer`      | yes      | Duration. Re-fetch when the remaining lifetime falls below this. |

**Multi-mode `default` symmetry.** `multi_mode.default` is now a bare
`Auth` value (matching `branches[].auth`), not the wrapped
`{auth: ...}` form.

```yaml
auth:
  multi_mode:
    branches:
      - when: {eq: {path: state.region, value: "gov"}}
        auth: {bearer: {token: {ref: state.gov_token}}}
    default: {bearer: {token: {ref: state.commercial_token}}}
```

---

### 3.4 `requests`

Ordered list of HTTP requests. The `Request` struct gains one new
field (`terminate_when:`) and otherwise stays the same in shape, but
inherits two cross-cutting changes.

**URL composition.** `defaults.base_url` is gone. Every request
declares an absolute `url:` (or, for a fan-out child, a path
constructed via `concat`):

```yaml
requests:
  - method: GET
    url: {concat: [{ref: state.url}, "/events"]}
    query:
      since: {ref: state.last_timestamp}
      limit: {format: string, value: {ref: state.page_size}}
```

The `path:` field is removed; every request uses `url:`. (String
interpolation is parked in §6.)

**Per-entry fields**

| Field              | Required | Description |
|--------------------|----------|-------------|
| `id`               | when referenced | Step identifier. Required for `steps.<id>.body.*` refs and fan_out. |
| `method`           | yes      | HTTP verb. |
| `url`              | yes      | Absolute URL Value. |
| `query`            | no       | Map of name → Value. |
| `headers`          | no       | Map of name → Value. |
| `body`             | no       | Discriminated union: `json:`, `form:`, or `raw:`. Exactly one. |
| `extract`          | no       | List of [ExtractVar](#extractvar). |
| `fan_out`          | no       | Per-item iteration. Mutually exclusive with `cache`. |
| `expect_status`    | no       | List of acceptable status codes. Default `[200]`. |
| `if`               | no       | Predicate — skip the step when false. |
| `terminate_when`   | no       | Predicate — when true, stop looping this step. Pairs with `extract:` for async-poll-style patterns. |
| `on_status`        | no       | Map of status code → verb (`skip`, `fail`, `empty_events`, `invalidate_cache`). |
| `produces_events`  | no       | Marks this step as the events producer. |
| `cache`            | no       | [Cache](#cache) block for token / login caching. |

**`terminate_when:` on a request** is the request-level loop primitive
that replaces `pagination.scroll_id.complete_when`,
`progress.async_job.poll.complete_when`, and the implicit phase machine
in `async_job`. Its semantics: after the response is received, evaluate
the predicate against `response.*` and `state.*`. If true, exit the
loop and proceed to the next request. If false, re-fire the same
request.

**Async-job pattern (replaces `progress.async_job` and the proposed
`flow:` block)**

```yaml
requests:
  - id: submit
    method: POST
    url: {concat: [{ref: state.url}, "/async_poll/exports"]}
    expect_status: [202]
    extract:
      - {to: state.export_id, from: response.body.export_id}

  - id: poll
    method: GET
    url: {concat: [{ref: state.url}, "/async_poll/exports/", {ref: state.export_id}, "/status"]}
    expect_status: [200, 404]
    terminate_when:
      and:
        - {present: response.body.status}
        - {eq: {path: response.body.status, value: complete}}
    extract:
      - {to: state.result_url, from: response.body.result_url}

  - id: fetch
    method: GET
    url: {ref: state.result_url}
    produces_events: true
```

No `flow:` block. No `async_job` variant. No `cursor.phase` state
machine. The phase information lives in `state.export_id` /
`state.result_url` as plain extracts.

**ExtractVar**

| Field    | Required | Description |
|----------|----------|-------------|
| `to`     | yes      | `state.<name>` or `extract.<name>`. When `state.*`, the destination must be declared under `state:`. |
| `from`   | yes      | Namespace-rooted Path. Accepts `response.body.<path>`, `response.header.<name>`, `steps.<id>.body.<path>`, `steps.<id>.header.<name>`. |
| `coerce` | no       | Type coercion verb. |
| `regex`  | no       | Optional regex transform applied to the resolved value before writing. See [§4.1](#41-value-language). |

The `target:` field is removed; the `to:` destination's namespace
determines persistence.

---

### 3.5 `response`

How to decode the producer step's body and where to find the events
list.

| Field        | Required | Description |
|--------------|----------|-------------|
| `decode`     | yes      | `json` or `ndjson`. |
| `events_at`  | yes      | Path rooted at `response.body.<path>` or `steps.<id>.body.<path>` locating the events list. The zero Path means "the body root IS the events list." |

**`events.*` namespace.** Once `events_at` resolves, the events list is
exposed as the dedicated `events.*` namespace (§4.3). This prevents the
collision between "a response body field named `events`" and "the
decoded events list." References use `events.*.<field>` for a list
projection, `events.first.<field>` / `events.last.<field>` for the
first or last element in declared order, `events.<int>.<field>` for an
explicit positional index, and `events.count` for the cardinality.

`placeholder_event` is removed. It was a CEL/mito-era workaround for a
runtime that required a non-empty event list to keep the page loop
alive. In a native Go runtime the loop controls itself; an empty page
that passes `terminate_when: false` simply triggers the next page
fetch.

---

### 3.6 `pagination`

Discriminated union — exactly one variant key. Named variants cover
the common 80%; the `custom:` variant exposes the primitive form for
APIs that don't fit.

**Named variants**

```yaml
pagination:
  none: {}

# or — server returns next-cursor token (covers cursor_token, scroll_id, graphql_relay, page-number-from-body)
pagination:
  cursor_token:
    from: response.body.meta.next_token   # or response.header.<name>, or steps.<id>.body.<path>
    to:   state.next_token                # per-drain destination
    terminate_when:                       # optional; default: not {present: <from>}
      not: {present: response.body.meta.next_token}

# or — server returns a fully-formed next URL in body or Link header
pagination:
  next_url:
    from: response.body.meta.next_page    # or response.header.link with optional regex
    to:   state.next_url
    regex: '<(.*?)>;\s*rel="next"'        # optional; only useful for response.header.link
    capture: 1

# or — client-incremented counter
pagination:
  counter:
    to:    state.page                     # per-drain destination, type: int
    start: 1                              # optional; default 1
    step:  1                              # optional; default 1
    terminate_when:                       # optional; default: events.count < step (short-page)
      not: {present: response.body.meta.has_next}

# or — author-controlled primitives
pagination:
  custom:
    advance:
      - to: state.next_token
        from: {ref: response.body.cursor}
      - to: state.reset_at
        from: {ref: response.header.x-rate-limit-reset}
    terminate_when:
      or:
        - {not: {present: response.body.cursor}}
        - {lt: {path: response.body.remaining, value: 1}}
```

**Variant mapping from current schema**

| Current variant       | New variant      | Notes                                                                                       |
|-----------------------|------------------|---------------------------------------------------------------------------------------------|
| `none`                | `none`           | Unchanged.                                                                                  |
| `cursor_token`        | `cursor_token`   | `token_at` → `from`; token lives in `state.<name>`, not `cursor.token`.                     |
| `scroll_id`           | `cursor_token`   | `scroll_id_at` → `from`; `complete_when` → `terminate_when`.                                |
| `graphql_relay`       | `cursor_token`   | `end_cursor_at` → `from`; `cursor_var` → author-chosen `to:` name.                          |
| `link_header`         | `next_url`       | `pattern` → `regex` + `capture` on the same block.                                          |
| `next_url_in_body`    | `next_url`       | `next_url_at` → `from`.                                                                     |
| `page_number`         | `counter`        | `page_param` dropped (was diagnostic-only); `has_more_at` → `terminate_when`.               |
| `offset`              | `counter`        | `offset_param` dropped; `step` = `{ref: state.page_size}`.                                  |

**Execution order per page**

1. Request fires; response received.
2. `terminate_when` evaluated against `response.*` and pre-advance
   `state.*`. If true → loop ends (skip advance).
3. Otherwise, named-variant defaults or `custom.advance:` writes run.
4. Loop.

This ordering is important for `custom:` blocks: termination should
not depend on the side effect of `advance:`, so the predicate reads
`response.*` directly.

**Shared field table (named variants)**

| Field             | Required | Description |
|-------------------|----------|-------------|
| `from`            | yes (variants except `none`, `counter`, `custom`) | Namespace-rooted Path. |
| `to`              | yes (variants except `none`) | `state.<name>` destination. Per-drain (lifetime inferred). |
| `terminate_when`  | no       | Predicate. Variant-specific default if omitted. |
| `regex`, `capture`| no       | Optional regex transform on the read value (`next_url`). |
| `start`, `step`   | no       | Counter-specific. |
| `advance`         | yes (`custom` only) | List of `{to, from, regex?, coerce?}` writes. |

---

### 3.7 `progress`

A flat list of state writes evaluated after each successful page. No
named variants. No `custom:` wrapper (because the primitive form IS
the only form). The empty list (or an omitted `progress:` block) means
"no progress tracking" — what was previously `stateless`.

```yaml
progress: []                   # equivalent to omitting the block

# or — max-of-events high-water mark
progress:
  - to: state.last_timestamp
    from: {max: {ref: events.*.timestamp}}

# or — same, with a per-iteration lag
progress:
  - to: state.last_timestamp
    from: {subtract: [{max: {ref: events.*.timestamp}}, "30s"]}

# or — clock-driven (use_now)
progress:
  - to: state.last_timestamp
    from: {now: true}

# or — sliding window
progress:
  - to: state.window_start
    from: {ref: state.window_end, default: {subtract: [{now: true}, "30d"]}}
  - to: state.window_end
    from: {now: true}

# or — pin the first event's id for sort-order-resistant tracking
progress:
  - to: state.last_event_id
    from: {ref: events.first.id}
```

**Each entry has the same shape:**

| Field    | Required | Description |
|----------|----------|-------------|
| `to`     | yes      | `state.<name>`. Persistent across drains (lifetime inferred). The state field must be declared under `state:`. |
| `from`   | yes      | Any Value. Has access to every namespace the request scope has: `state.*`, `events.*`, `response.body.*`, `response.header.*`, `steps.<id>.body.*`, `steps.<id>.header.*`. Can use reducers (`max`/`min`/`first`/`last`/`count`) over `events.*`, arithmetic primitives, etc. |
| `coerce` | no       | Type coercion verb. |
| `regex`  | no       | Optional regex transform. |

**Evaluation semantics**

- **One firing per accepted page-response.** A page-response is
  accepted when its status passes `requests[].expect_status` and any
  `on_status:` action did not abort the drain. Progress fires even
  when `events.*` is empty — server-provided cursors, ingestion
  timestamps, and other response-body fields can be persisted
  independently of event production. Progress does NOT fire when a
  request is skipped by `if:`, aborted by `on_status: fail`, or
  errored out before a response was decoded.
- **Batch semantics across entries.** All `from:` expressions evaluate
  against the same snapshot of state. A later entry referencing
  `state.window_start` sees the *pre-write* value of that field,
  whatever order the entries appear in. This makes the time_window
  pattern (where the new `window_start` is read from the *old*
  `window_end`) correct regardless of declaration order.
- **Sink delivery before commit.** The runner emits the page's events
  to the sink, then evaluates progress writes, then commits state.
  Failure between events-sent and state-committed is acceptable
  (at-least-once); failure before events-sent leaves state unchanged.
- **No implicit accumulation.** Authors who want a cumulative
  high-water mark write the merge explicitly:
  `from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}`.
  This is verbose but precise; the runner never has to guess what
  "merging" means for a given field.
- **First-write-wins via `ref` default.** "Persist on the very first
  drain only" is just `from: {ref: state.x, default: <new-value>}` —
  if `state.x` is set, it writes itself back (no-op); if absent, the
  default kicks in and writes the new value. No new schema primitive
  needed.

**Variant mapping from current schema**

| Current variant                              | New form                                                                              |
|----------------------------------------------|---------------------------------------------------------------------------------------|
| `stateless`                                  | omit `progress:` or `progress: []`                                                    |
| `latest_event_timestamp` / `max_event_field` | `[{to: state.last_timestamp, from: {max: {ref: events.*.<field>}}}]`                  |
| `use_now`                                    | `[{to: state.last_timestamp, from: {now: true}}]`                                     |
| `time_window`                                | two entries — see example above                                                       |
| `async_job.on_complete.cursor_update`        | regular `progress:` writes after async_job dies (§3.4)                                |

**The `initial.lookback` / `initial_offset` family is gone.** First-run
seeding is the `default:` on the destination state field:

```yaml
state:
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}    # was initial.lookback
```

---

### 3.8 `error`

Unchanged. `mode: standard | warn | fail` and `include_body: bool`
stay as-is.

Per-step overrides go in `requests[].on_status` (unchanged), which
take precedence over `error.mode` for the named statuses.

---

## 4. Cross-cutting primitives

### 4.1 Value language

The existing Value forms are kept; the additions below make several
schema blocks expressible as primitives.

| Form | YAML shape | Produces |
|------|------------|----------|
| Literal string | `"foo"` | string |
| **Interpolated string** | `"${state.url}/api/v1"` | string with `${...}` segments resolved against any namespace |
| Literal int | `100` | int64 |
| Literal bool | `true` | bool |
| Ref | `{ref: state.url}` | resolved value |
| Ref with default | `{ref: state.token, default: ""}` | resolved value or fallback |
| Now | `{now: true}` | `time.Time` |
| Concat | `{concat: [<Value>, ...]}` | string |
| Select | `{select: {branches: [...], default: <Value>}}` | conditional |
| Format | `{format: <verb-or-layout>, value: <Value>}` | formatted string |
| Base64 | `{base64: <Value>}` | base64 string |
| List | `{list: [<Value>, ...]}` | list |
| Object | `{object: {<key>: <Value>}}` | map |
| **Add** | `{add: [<Value>, <Value>]}` | `time + duration → time`; `duration + duration → duration`; `int + int → int` |
| **Subtract** | `{subtract: [<Value>, <Value>]}` | `time - duration → time`; `time - time → duration`; `duration - duration → duration`; `int - int → int` |
| **Max** | `{max: <list-or-projection>}` | reducer — largest value |
| **Min** | `{min: <list-or-projection>}` | reducer — smallest value |
| **First** | `{first: <list-or-projection>}` | reducer — first element in declared order |
| **Last** | `{last: <list-or-projection>}` | reducer — last element in declared order |
| **Count** | `{count: <list-or-projection>}` | reducer — number of elements |
| **Regex** | `{regex: {pattern: <string>, from: <Value>, capture?: <int>, default?: <Value>}}` | extracted substring |

**String interpolation.** Any YAML string literal in a Value position
is scanned for `${<path>}` segments, which resolve against the same
namespaces as `{ref: <path>}`. The string desugars to a `{concat:
[...]}` Value with interleaved literal segments and refs. For example,
`"${state.url}/api/${state.endpoint}/events"` is exactly:

```yaml
{concat: [{ref: state.url}, "/api/", {ref: state.endpoint}, "/events"]}
```

Each `${...}` segment may use a default with the `|` sigil:
`"${state.next_token|}"` is `{ref: state.next_token, default: ""}`; the
text after the pipe is parsed as a YAML scalar so non-string defaults
work too (`"${state.page|1}"` defaults to integer `1`). Literal `$`
needs escaping as `\$`; once we see `$`, a literal `{` after it is
`\{`. Outside an `${...}` segment, `$` and `{` are plain text.

Interpolation produces a string. Refs inside `${...}` that resolve to
non-string values are coerced as if wrapped in `{format: string,
value: ...}`. Secret-tainted refs propagate their secret status to the
composed string (same rule as `{concat}` today).

**Reducer inputs.** A reducer accepts either:
- A list literal: `{max: [v1, v2, v3]}` — sugar for `{max: {list: [v1, v2, v3]}}`.
- A list-shaped Value: `{max: {ref: events.*.timestamp}}` — projects
  the `.timestamp` field across every event and returns the max.

**`{now: true, offset: ...}` is removed.** Replaced by `{subtract: [{now: true}, <duration>]}` and `{add: [{now: true}, <duration>]}`. The `offset:` sibling key on `{now}` is rejected at parse time.

**Format verbs.** Closed set, unchanged: `string`, `int`, `bool`,
`rfc3339`, `rfc3339nano`, `unix_seconds`, `unix_millis`, `duration`,
`url_encode`, `parse_duration`. **In addition,** `format:` now accepts
a Go date layout string (e.g. `"2006-01-02T15:04:05.000-0700"`). The
validator distinguishes by closed-set membership: a recognised verb is
the named parser; anything else is tried as a Go layout.

`format:` as a Value-time verb is most useful for ad-hoc coercions. For
state fields whose wire form is a non-default timestamp shape, prefer
declaring `format:` on the state field (§3.2) and never wrap the ref.

---

### 4.2 Predicate language

Unchanged. Forms: `eq`, `gt`, `lt`, `gte`, `lte`, `present`, `not`,
`and`, `or`, `literal_bool`.

`terminate_when:` (pagination, requests) and `if:` (requests, branches)
both use this language. All predicates are absent-tolerant — a Path
that resolves to absent makes `present` return false, makes `eq`/`gt`/
`lt`/etc. return false, and never throws.

---

### 4.3 Namespace table

| Namespace                  | Lifetime                          | Written by                                                                                   | Notes |
|----------------------------|-----------------------------------|----------------------------------------------------------------------------------------------|-------|
| `state.<name>`             | persisted across drains           | `progress:` writes, `requests[].extract` with `to: state.*`, `pagination.*.to` (per-drain).  | Lifetime sub-flavour (operator-config / per-drain / persistent) is inferred from where the field is written. See [§3.2](#32-state). |
| `cache.<name>`             | process memory only               | `Cache` blocks on auth grants or requests.                                                   | Cleared on runner restart. Never persisted to `state.json`. |
| `events.<...>`             | per-iteration (current page)      | The runner, after `response.events_at` resolves.                                             | `events.*.field` projects across all events. `events.first.field` / `events.last.field` are declared-order shortcuts. `events.<int>.field` is positional. `events.count` is cardinality. |
| `extract.<name>`           | per-iteration                     | `requests[].extract` with `to: extract.*`.                                                   | Reset at the top of every iteration. |
| `steps.<id>.body.<path>`   | per-iteration (after step runs)   | The decoded response body of a labelled prior step.                                          | |
| `steps.<id>.header.<name>` | per-iteration                     | The response headers of a labelled prior step.                                               | |
| `response.body.<path>`     | per-`terminate_when` / pagination evaluation | The active step's decoded response body.                                          | Valid in pagination `from:` / `terminate_when:` paths; in request-level `terminate_when:`; and in extract `from:`. |
| `response.header.<name>`   | same as above                     | The active step's response headers.                                                          | |
| `<fan_out.as>.<path>`      | per-fan-out-iteration             | The author-chosen `fan_out.as` name; bound to the current item.                              | |

**The `cursor.*` namespace is removed.** Everything previously written
to `cursor.<name>` is now `state.<name>` (with lifetime inferred) or
`scratch.<name>` if a future revision splits the per-drain flavour
into its own namespace — but for now `state.<name>` covers both.

---

### 4.4 Type table

| Type        | Wire shape          | Semantic |
|-------------|---------------------|----------|
| `string`    | string              | UTF-8; no parsing. |
| `int`       | integer             | 64-bit signed. |
| `bool`      | boolean             | true/false. |
| `secret`    | string              | Non-logging marker; otherwise like `string`. |
| `duration`  | string              | Go-style (`"720h"`, `"-30s"`). |
| `timestamp` | string              | Parsed per the declared `format:`. Refs always resolve to `time.Time` in-process. |
| `url`       | string              | URL string; no validation at load time. |
| `enum`      | string              | One of the declared `values`. |

`type: rfc3339` (proposed in an earlier draft) is dropped in favour of
`type: timestamp` + `format:`. The wire form is configurable; the
in-process representation is always `time.Time`.

---

## 5. Side-by-side template sketches

Two templates rewritten side-by-side to validate the proposal.
Current YAML on the left, proposed on the right.

### 5.1 `bearer_simple.yml` — simple GET with timestamp progress

**Current**

```yaml
ir_version: "1"

state:
  fields:
    url:
      type: url
      default: "http://localhost:9999"
    initial_interval:
      type: duration
      default: "720h"
    page_size:
      type: int
      default: 5
    api_key:
      type: secret
      default: "test-bearer-token-12345"

defaults:
  base_url: {ref: state.url}

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    path: /bearer_simple/events
    query:
      since: {ref: cursor.last_timestamp}
      limit: {format: string, value: {ref: state.page_size}}

response:
  decode: json
  events_at: response.body.events

pagination:
  none: {}

progress:
  latest_event_timestamp:
    event_time:
      path: timestamp
    initial:
      lookback: {ref: state.initial_interval}

error:
  mode: standard
```

**Proposed**

```yaml
ir_version: "1"

state:
  url:
    type: url
    default: "http://localhost:9999"
  page_size:
    type: int
    default: 5
  api_key:
    type: secret
    default: "test-bearer-token-12345"
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    url: "${state.url}/bearer_simple/events"
    query:
      since: {ref: state.last_timestamp}
      limit: "${state.page_size}"

response:
  decode: json
  events_at: response.body.events

pagination:
  none: {}

progress:
  - to: state.last_timestamp
    from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.timestamp}}]}

error:
  mode: standard
```

**What changed**

- `state.fields.*` → `state.*` (no `.fields` wrapper).
- `state.initial_interval` is gone. The first-run window is the
  `default:` on `state.last_timestamp`.
- `defaults.base_url` is gone. The request uses an interpolated `url:`
  string (`${state.url}/...`).
- `cursor.last_timestamp` → `state.last_timestamp`. No more `cursor.*`
  namespace.
- `progress.latest_event_timestamp` → a flat write list. The
  `event_time.path` sub-object is gone; the reducer over
  `events.*.timestamp` plus the explicit `max` against the prior
  state value says the same thing in primitives and is correct
  across partial-failure restarts.
- `{format: string, value: ...}` wrappers for query-string
  coercions collapse to `"${state.page_size}"` interpolation.

---

### 5.2 `next_url_in_body.yml` — URL-from-body pagination with timestamp progress

**Current**

```yaml
ir_version: "1"

state:
  fields:
    url:
      type: url
      default: "http://localhost:9999"
    initial_interval:
      type: duration
      default: "720h"
    page_size:
      type: int
      default: 5
    api_key:
      type: secret
      default: "test-bearer-token-12345"

defaults:
  base_url: {ref: state.url}

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    url:
      ref: cursor.next_url
      default:
        concat:
          - {ref: state.url}
          - /next_url_in_body/alerts
    query:
      since: {ref: cursor.last_timestamp}
      limit: {format: string, value: {ref: state.page_size}}

response:
  decode: json
  events_at: response.body.alerts

pagination:
  next_url_in_body:
    next_url_at: response.body.meta.next_page

progress:
  latest_event_timestamp:
    event_time:
      path: created_at

error:
  mode: standard
```

**Proposed**

```yaml
ir_version: "1"

state:
  url:
    type: url
    default: "http://localhost:9999"
  page_size:
    type: int
    default: 5
  api_key:
    type: secret
    default: "test-bearer-token-12345"
  next_url:
    type: url
  last_timestamp:
    type: timestamp
    default: {subtract: [{now: true}, "720h"]}

auth:
  bearer:
    token: {ref: state.api_key}

requests:
  - method: GET
    url: {ref: state.next_url, default: "${state.url}/next_url_in_body/alerts"}
    query:
      since: {ref: state.last_timestamp}
      limit: "${state.page_size}"

response:
  decode: json
  events_at: response.body.alerts

pagination:
  next_url:
    from: response.body.meta.next_page
    to:   state.next_url

progress:
  - to: state.last_timestamp
    from: {max: [{ref: state.last_timestamp}, {max: {ref: events.*.created_at}}]}

error:
  mode: standard
```

**What changed**

- `state.fields.*` → `state.*`; `state.initial_interval` gone.
- `defaults.base_url` gone; the request's bootstrap URL is the `ref`'s
  default, written with interpolation for brevity.
- `state.next_url` is declared explicitly. Pagination's `to:`
  destination makes the per-drain write visible at declaration time.
- `pagination.next_url_in_body.next_url_at` → `pagination.next_url`
  with explicit `from:` and `to:` (same primitive shape that
  `cursor_token`, `counter`, and `custom` use).
- `cursor.next_url` / `cursor.last_timestamp` → `state.next_url` /
  `state.last_timestamp`.
- `progress.latest_event_timestamp.event_time.path` → a flat write
  using a reducer over `events.*.created_at` plus an explicit `max`
  against prior state.

---

## 6. Settled decisions and implementation notes

There are no remaining open schema questions. The notes below record
choices that were considered and resolved during the design discussion,
plus implementation-time work items that the code-change PR will pick
up.

**A. Per-drain state lifetime.** Per-drain state (every `state.*` field
written by `pagination:`) wipes at the start of every drain. A drain
that fails mid-page re-bootstraps pagination on the next start. The
progress mechanism is the only path for recovery — authors who care
about resuming write a high-water mark (timestamp, server cursor,
etc.) to a persistent `state.*` field under `progress:` and rely on
at-least-once delivery + sink dedup to handle the overlap. This keeps
the runtime contract simple and removes the "did the previous drain
complete normally" branch from the snapshot loader.

**B. URL composition.** Adopted: string interpolation. `${state.url}`
inside any string Value desugars to `{concat: [...]}`. The change is
strictly additive at the parser; it is not URL-specific and works for
headers, body fields, raw bodies, etc. See §4.1 for full syntax. The
verbose `{concat: [...]}` form is still accepted; templates are free
to mix.

**C. Progress sugar.** Not adopted for now. The flat list of writes
in §3.7 covers every current template; we will revisit if a recurring
pattern emerges that authors find verbose. Adding a named variant
later is additive — no migration needed.

**D. State namespace split.** Not adopted. A single `state.*`
namespace with lifetime inferred from write sites is enough for today.
A future split into `state.*` (persistent) and `scratch.*` (per-drain)
remains possible if implicit lifetime inference confuses authors in
practice.

**E. Reducer set.** Adopted: `max`, `min`, `first`, `last`, `count`.
`sum` and `avg` are not added because no current template needs them.
Adding more is additive.

**F. Vocabulary final pass.** The current draft uses `to:` / `from:`
everywhere except `fan_out:` (which keeps `over:` and `as:` because
those words carry the right meaning for the per-item construct). The
code-change PR will run a final audit; any straggler will be renamed
at that point.

**G. No migration code.** There is no installed user base to migrate.
Existing `state.json` files from any development snapshot are
discarded — the code change deletes the old schema structs, the old
`cursor.*` namespace, and the old per-drain state handling outright.
No shims, no detection-and-translation logic, no "load v0 snapshot,
emit v1 snapshot" path. Anything not described in this document is
removed in the same change that introduces it.

---

## 7. What this document does not do

- Does not specify the Go struct changes in `schema/schema.go`.
- Does not specify the validator changes in `schema/validate.go`.
- Does not specify the runtime changes in `client/` (in particular,
  per-page progress checkpointing requires a refactor of the
  `Drain` loop and the `Store.Save` deferred call).
- Does not rewrite all 26 templates (the two side-by-side sketches in
  §5 validate the proposal; the remaining templates are a follow-up).

All of the above are deferred to a separate plan once this document is
accepted.
