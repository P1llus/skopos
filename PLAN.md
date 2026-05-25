# PLAN: unify pagination + fan_out into a single `iterate:` loop primitive

Status: design proposal for review. Not yet implemented. Pre-1.0, so
breaking changes are acceptable.

This plan replaces two separate constructs —

- `pagination:` (document-level discriminated union, wraps the whole
  `requests:` chain), and
- `requests[].fan_out:` (per-request iteration over a materialized list)

— with one recursive loop primitive, `iterate:`, that can wrap either a
single request or a nested group of requests, and whose *driver* selects
how the iteration sequence is produced.

---

## 1. Why

Today's `pagination:` has the same structural problem the top-level
`response:` block had before #63: it is declared once at document scope
but the document runs *many* requests. With one global loop you can only
paginate by re-running the entire chain, and there is no syntax to say
"drain request A to completion, then run B over everything A produced."

Separately, `fan_out` and `pagination` are the same idea wearing two
costumes:

| Aspect              | `pagination` (today)                         | `fan_out` (today)                       |
|---------------------|-----------------------------------------------|------------------------------------------|
| Iteration set       | discovered per response (cursor/url/counter)  | a materialized list, known up front      |
| Per-iter binding    | writes `state.<scratch>`, read by next build  | binds an `as:` item namespace            |
| Scope               | the whole `requests:` chain (document-level)  | a single request                         |
| Output              | events **emitted** per page (streamed)        | bodies **merged** (flatten/wrap)         |
| Termination         | `terminate_when` (or variant default)         | list exhausted                           |
| Count               | unbounded                                     | bounded = `len(list)`                    |

Both are "repeat this body, binding something per iteration, until done."
The only genuinely distinct concept between them is the **output policy**
(emit-as-you-go vs. merge-the-bodies). Everything else is a shared loop
with a pluggable driver.

Unifying them lets the schema express, with one mental model:

- plain pagination (cursor/url/counter),
- plain fan-out over a list,
- **paginate A to completion, accumulate, then fan B over the union**,
- nested loops (paginate a listing, fan out detail calls per page),
- group loops (a fetch+enrich *pair* repeated per page).

---

## 2. The model

### 2.1 A pipeline is a tree of steps

`Doc.Requests` stops being `[]Request` and becomes an ordered list of
**steps**, where a step is either:

- a **leaf request** (one HTTP request), or
- a **loop**: an `iterate:` driver plus a nested sub-pipeline (`do:`).

A leaf request may carry an inline `iterate:` as sugar for "wrap *me* in
a one-request loop." So there are two surface forms but one runtime
shape; the inline form desugars to a loop whose body is the single
request.

```yaml
# leaf request, no loop  (was: pagination.none)
- {id: x, method: GET, url: ..., events_at: response.body.data}

# leaf request that loops itself  (was: pagination on a one-request doc,
# or fan_out on one request)
- id: x
  method: GET
  url: ...
  events_at: response.body.data
  iterate: {cursor_token: {from: response.body.next, to: state.cursor}}

# group loop: iterate wraps a nested sub-pipeline
- iterate: {counter: {to: state.page, step: 100}}
  do:
    - {id: list, method: GET, url: ..., events_at: response.body.repos}
    - {id: enrich, method: GET, url: ..., ...}
```

Disambiguation rule: a step with `do:` is a loop; a step with `method:`
is a leaf request; setting both is a validation error.

### 2.2 Drivers (discriminated union)

The driver is the existing pagination union **plus** `over` (the old
fan_out list driver):

| Driver         | Fields                                   | Iteration sequence                    |
|----------------|-------------------------------------------|----------------------------------------|
| `cursor_token` | `from`, `to`, `terminate_when?`           | next-cursor token from the response    |
| `next_url`     | `from`, `to`, `regex?`, `capture?`, `tw?` | next-page URL from the response        |
| `counter`      | `to`, `start?`, `step?`, `terminate_when?`| client-incremented counter             |
| `custom`       | `advance[]`, `terminate_when` (required)  | author-supplied advance writes         |
| `over`         | `over` (list Value), `as`                 | one iteration per list element         |

These map 1:1 onto today's `schema.CursorTokenPagination`,
`NextURLPagination`, `CounterPagination`, `CustomPagination`, and
`FanOut` — no field churn inside the drivers themselves.

### 2.3 Output policy: `collect:`

A loop either **emits** events as it goes or **merges** the per-iteration
producer bodies into one combined body:

| `collect:` | Meaning                                                              |
|------------|----------------------------------------------------------------------|
| `emit`     | Each iteration's accepted page emits its events immediately (stream).|
| `flatten`  | Each iteration's producer body must be a list; merged by concat.     |
| `wrap`     | Each iteration's producer body is appended as-is to a list.          |

Default by driver (override allowed):

- `over` ⇒ default `flatten` (today's fan_out default).
- `cursor_token` / `next_url` / `counter` / `custom` ⇒ default `emit`.

The override is genuinely expressive, not just compatibility glue:
`over` + `collect: emit` fans out and streams each item's events as it
goes; `cursor_token` + `collect: flatten` drains all pages into one
combined body for an outer consumer.

### 2.4 Producers and events under nesting

`events_at` keeps its meaning — "where the events list lives in a decoded
body" — but the "exactly one producer in the document" rule generalizes
to **exactly one producer per emit-scope**:

- An **emit-scope** is the top-level pipeline or the body of a `collect:
  emit` loop. Within one emit-scope, exactly one leaf request (not
  descending into nested loops) must carry `events_at`.
- A **merge loop** (`collect: flatten|wrap`) is *not* an emit-scope. Its
  body has exactly one producer too, but that producer's body is the unit
  that gets merged. The merged result becomes the loop node's output
  body, addressable as `steps.<loop-id>.body`, so an outer `events_at`
  (or an outer loop) can consume it.

Concretely: a single leaf request that is both wrapped in a `merge`
`iterate` *and* carries `events_at` is exactly today's `fan_out` — "merge
my per-item bodies, then treat the merged list as my events." A leaf with
an `emit` `iterate` and `events_at` is exactly today's pagination.

### 2.5 Binding and scratch lifetime

- `over.as` binds an item namespace for the duration of one iteration,
  exactly like today. Nested `over` loops nest their bindings; each `as:`
  is a namespace root valid inside that loop's body. Collisions across
  nested `as:` (and with reserved roots / state names / step ids) are
  rejected — generalizing the current `fan_out.as` check.
- Driver `to:` slots (`cursor_token.to`, `counter.to`, …) remain
  per-loop scratch, but the lifetime sharpens from "per-drain" to **reset
  on loop entry**. A nested pagination loop inside an outer fan-out
  restarts its cursor for each outer item. For top-level loops, "loop
  entry" == "drain start", so this is backward compatible with today's
  per-drain wipe.
- Accumulators ("collect all of A's pages into a list") are **not** driver
  `to:` slots — they are ordinary persistent `state` fields written by an
  `extract`/`progress` append (`{concat: [{ref: state.acc}, {ref:
  events...}]}`), so the per-loop-entry reset does not touch them.

### 2.6 Termination and the safety cap

- Each driver keeps its default `terminate_when` (cursor/url: source
  absent; counter: short page; custom: required explicit; `over`:
  list exhausted). `terminate_when:` on the `iterate:` block overrides.
- `Runner.MaxPages` becomes a **per-loop** iteration cap: every loop
  counts its own iterations against the cap, so an infinite *inner* loop
  is bounded too. (Today's single global counter is the degenerate
  one-loop case.)

### 2.7 Progress timing

`progress:` stays a single document-level list and fires **once per
accepted emitted page, at any depth** — i.e. every time some emit-scope
produces an accepted page-response. This generalizes today's "once per
accepted page" rule (today there is only one emit-scope). Merge-loop
iterations do not fire progress; progress fires when the merged result is
later consumed as an emitted page. Per-loop `progress:` blocks are a
possible later refinement, noted in §8.

The persistence contract is unchanged: events flush before
`Store.Save`, `Save` runs once in the deferred teardown, at-least-once
holds.

---

## 3. Worked examples

### 3.1 Plain pagination (was `pagination.cursor_token`)

```yaml
requests:
  - id: page
    method: GET
    url: "${state.url}/events"
    query: {cursor: "${state.cursor|}"}
    events_at: response.body.data
    iterate:
      cursor_token: {from: response.body.next, to: state.cursor}
```

### 3.2 Plain fan-out (was `fan_out`)

```yaml
requests:
  - id: list
    method: GET
    url: "${state.url}/incidents"
  - id: detail
    method: GET
    url: "${state.url}/incidents/${incident.id}/details"
    events_at: ""
    iterate:
      over: {ref: steps.list.body.incidents}
      as: incident
      # collect: flatten   (default for `over`)
```

### 3.3 Paginate A to completion, then fan B over the union

The capability that the global loop cannot express today. The listing
loop drains all pages into a persistent accumulator; the detail loop then
fans out over the union.

```yaml
state:
  repo_ids: {type: list}            # accumulator (persistent state)

requests:
  - id: list                        # drain ALL pages, emit nothing
    method: GET
    url: "${state.url}/repos"
    query: {page: "${state.page|1}"}
    iterate: {counter: {to: state.page, step: 1}, collect: emit}
    extract:
      - to: state.repo_ids
        from: {concat: [{ref: state.repo_ids}, {ref: response.body.repos.*.id}]}

  - id: issues                      # then fan out over the union
    method: GET
    url: "${state.url}/repos/${repo.id}/issues"
    events_at: ""
    iterate: {over: {ref: state.repo_ids}, as: repo}
```

(The `list` loop has no `events_at`, so its emit-scope has zero producers
— allowed for a side-effecting loop. `issues` is the document producer.)

### 3.4 Nested: paginate a listing, fan out detail per page

```yaml
requests:
  - iterate: {cursor_token: {from: response.body.next, to: state.cursor}}
    do:
      - id: list
        method: GET
        url: "${state.url}/incidents"
        query: {cursor: "${state.cursor|}"}
      - id: detail
        method: GET
        url: "${state.url}/incidents/${incident.id}"
        events_at: ""
        iterate: {over: {ref: steps.list.body.incidents}, as: incident}
```

Outer loop paginates the listing; per page, the inner `over` loop fans
detail calls and (because `detail` carries `events_at`) emits each page's
merged events. The outer loop's `to: state.cursor` resets on drain entry;
the inner loop's binding resets per outer iteration.

### 3.5 Group loop: fetch + enrich repeated per page

```yaml
requests:
  - iterate: {counter: {to: state.page, step: 100}}
    do:
      - id: list
        method: GET
        url: "${state.url}/orders?page=${state.page}"
      - id: enrich
        method: GET
        url: "${state.url}/orders/${steps.list.body.batch_id}/lines"
        events_at: response.body.lines
```

Two requests form the loop body; both re-run each page.

---

## 4. Go type sketch (illustrative)

```go
// Doc.Requests becomes []Step.
type Step struct {
    *Request `yaml:",inline"`           // leaf form (nil for a group loop)
    Iterate  *Iterate `yaml:"iterate,omitempty"`
    Do       []Step   `yaml:"do,omitempty"` // group form: nested sub-pipeline
}

type Iterate struct {
    // exactly one driver:
    CursorToken *CursorTokenDriver `yaml:"cursor_token,omitempty"`
    NextURL     *NextURLDriver     `yaml:"next_url,omitempty"`
    Counter     *CounterDriver     `yaml:"counter,omitempty"`
    Custom      *CustomDriver      `yaml:"custom,omitempty"`
    Over        *OverDriver        `yaml:"over,omitempty"`
    // shared:
    TerminateWhen *Predicate `yaml:"terminate_when,omitempty"`
    Collect       string     `yaml:"collect,omitempty"` // emit|flatten|wrap
}

type OverDriver struct {
    Over Value  `yaml:"over"`
    As   string `yaml:"as"`
}
// CursorTokenDriver / NextURLDriver / CounterDriver / CustomDriver are the
// existing *Pagination structs, renamed.
```

`Iterate` gets `Variant()` / `VariantNames()` helpers exactly like the
current `Pagination` and `DecodeStage` unions. `Step` needs a custom
`UnmarshalYAML`/`UnmarshalJSON` (mirroring `DecodeChain`) only if the
inline `*Request` embedding plus `do:` cannot be disambiguated by the
default decoder; the validator enforces "not both `method:` and `do:`"
regardless.

Recursion (`Do []Step`) decodes natively in yaml.v3 / encoding/json.

---

## 5. Parsing & validation changes

- New union helpers for `Iterate` (Variant/VariantNames), reusing the
  `paginationVariants` pattern.
- `checkPagination` → `checkIterate`, recursing through the step tree.
  Per-loop checks: exactly one driver; `over` requires `as`; `custom`
  requires `terminate_when`; `collect` in {emit, flatten, wrap}; `to:`
  paths are `state.<name>`; `from:` paths are response/header/steps-rooted.
- Producer check generalizes: walk each emit-scope and require exactly one
  `events_at`-bearing leaf in it. Merge loops require exactly one producer
  in their body.
- `as:` collision check generalizes to the full nest of active bindings.
- Step-shape check: reject `method:` + `do:` together; reject `do:`
  without `iterate:`.
- Lifetime inference (`schema/validate.go`): driver `to:` slots are
  per-loop scratch; reject a slot used as both a driver target and a
  persistent `progress`/`extract` target (today's per-drain vs persistent
  conflict check, scoped per loop).

---

## 6. Runtime changes (`client/`)

- `runner.go`: replace the single flat pagination loop with a **recursive
  step executor**. Executing a pipeline walks the `[]Step`; a leaf runs as
  today (`runRequest`, `terminate_when`, `on_status`, `error.mode`); a
  loop runs its driver's `advance` around its body sub-pipeline.
- `pagination.go` + `fanout.go` collapse into one driver layer: the
  `paginationPlan.advance` interface absorbs the `over` driver (which
  walks a materialized list instead of reading a response signal).
- Output policy lives in the loop executor: `emit` streams events through
  the existing emitter per accepted page; `flatten`/`wrap` reuse
  `mergeFanOutBodies` to build the loop's output body.
- Per-loop scratch reset on loop entry (replaces the single
  `resetPerDrainScratch` call; top-level loops still reset at drain
  start).
- `MaxPages` enforced per loop.
- Scope binding (`item`/`itemBinding`) generalizes to a stack of active
  `as:` bindings for nested `over` loops (the stash/restore in `runFanOut`
  already prototypes this for one level).
- `on_status` / `error.mode` dispatch is unchanged per leaf; the
  `iterInvalidate` / `iterBreak` / `iterWarn` verdicts propagate up
  through the loop executor (a break ends the *enclosing* loop, matching
  today's "pagination loop ends").

---

## 7. Breaking changes & migration

| Old                                   | New                                                            |
|---------------------------------------|----------------------------------------------------------------|
| `pagination: {none: {}}`              | omit `iterate:` (a request with no loop runs once)             |
| `pagination: {cursor_token: {...}}`   | `iterate: {cursor_token: {...}}` on the producer/chain         |
| `pagination: {next_url \| counter \| custom}` | same fields under `iterate:`                            |
| `requests[].fan_out: {over, as, merge: flatten}` | `iterate: {over: {over, as}, collect: flatten}`     |
| `requests[].fan_out: {..., merge: wrap}` | `iterate: {over: {...}, collect: wrap}`                     |
| `Doc.Pagination` field                | removed                                                        |
| `Request.FanOut` field                | removed (folded into `Step.Iterate.Over`)                     |

`fan_out` + `cache` mutual exclusion becomes `over`-driver + `cache`
mutual exclusion. The async submit/poll/fetch pattern
(`requests[].terminate_when`) is untouched — it is a *request* loop, not
an `iterate` loop, and the two still compose.

Fixtures and goldens to migrate: every `schema/testdata/*.yml` and
`cmd/skopos/testdata/*` that sets `pagination:` or `fan_out:`, plus
`docs/schema/v1/skopos.schema.json` (JSON Schema), the generated
`docs/schema-reference.md` (`make schema-doc`), and `templates/`.

---

## 8. Open questions

- **Per-loop `progress:`.** §2.7 keeps progress document-level. Should an
  inner loop be able to declare its own progress writes (e.g. checkpoint
  after each page of a sub-loop)? Deferring until a template needs it.
- **`collect:` on the top-level pipeline.** The top-level pipeline is an
  implicit emit-scope; does an explicit top-level `collect: flatten` ever
  make sense, or is merge only meaningful on a named loop another step
  consumes? Leaning: merge only on named loops.
- **`Step` decoding.** Confirm whether `*Request` inline-embedding plus
  `do:`/`iterate:` siblings decode unambiguously in yaml.v3, or whether a
  custom `UnmarshalYAML` (DecodeChain-style) is required.
- **Worklist pattern interaction.** The `state.queue` worklist
  (docs/runtime.md §5) is built on `pagination` advancing the whole chain
  per page; re-express it as an `iterate` loop and confirm the
  per-loop-entry scratch lifetime does not disturb the persistent queue.

---

## 9. Suggested implementation slices

1. **IR + parsing.** Introduce `Step`, `Iterate`, the driver union, the
   `Over` driver; remove `Doc.Pagination` and `Request.FanOut`. Union
   helpers + custom unmarshaler if needed. No runtime yet.
2. **Validation.** Recursive `checkIterate`, generalized producer and
   `as:`-collision checks, step-shape and lifetime checks.
3. **Runtime — single-request loops.** Recursive executor for the
   leaf+`iterate` (non-`do`) case covering all five drivers and all three
   `collect:` modes. Prove pagination and fan-out parity against migrated
   goldens.
4. **Runtime — group loops (`do:`) and nesting.** Per-loop scratch reset,
   binding stack, per-loop `MaxPages`, propagation of break/invalidate
   verdicts.
5. **Docs + schema-doc + templates + JSON Schema.** Regenerate
   `docs/schema-reference.md`; rewrite the `pagination` / `fan_out`
   sections of `docs/schema.md` and the runtime §3/§12 of
   `docs/runtime.md`; migrate `templates/` and `api-methods.md`.
