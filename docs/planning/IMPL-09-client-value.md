# IMPL-09 — client/value.go + predicate.go + extract.go + bodypath.go

## Scope

Re-implement the value-resolution runtime against the post-redesign IR:
`evalValue` gains arms for the eight new Value forms (`Add`, `Subtract`,
`Max`, `Min`, `First`, `Last`, `Count`, `Regex`); the `{now: true, offset: ...}`
sibling form is gone (offsets desugar to `{add: [{now: true}, "<dur>"]}`);
extract destinations are encoded in `to:` directly (`state.<name>` or
`extract.<name>`, no `target:` switch); body-path helpers drop the
deleted `body.<path>` root from their doc strings.

## Old → new map

### `client/value.go`

| Before                                                                            | After                                                                                                                                                                |
|-----------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `case v.Now`: returned `s.now()` plus `now.offset` adjustment.                    | `case v.Now`: returns `s.now()` only — `NowValue` is now an empty struct; offsets compose via `{add: [{now: true}, "<dur>"]}`.                                       |
| File-level doc described the runtime representation set and named `cursor.<name>` as a legal path root. | File-level doc lists the new return-type set (adds `time.Duration`), enumerates the eight new discriminators, and names the closed root set (`state | cache | events | extract | steps | response`) plus the fan-out alias arm. |
| (no `Add` arm)                                                                    | `case v.Add`: dispatches to `evalArith("add", ...)`. Type pairs: `time + duration → time`, `duration + duration → duration`, `int + int → int64`.                    |
| (no `Subtract` arm)                                                               | `case v.Subtract`: dispatches to `evalArith("subtract", ...)`. Type pairs: `time - duration → time`, `time - time → duration`, `duration - duration → duration`, `int - int → int64`. |
| (no reducer arms)                                                                 | `case v.Max | v.Min`: comparator reducer via `reduceExtreme` (skip nil entries, use `orderedCompare`).                                                               |
| (no reducer arms)                                                                 | `case v.First | v.Last`: positional reducer via `firstNonNil` / `lastNonNil` (skip nil entries).                                                                     |
| (no reducer arms)                                                                 | `case v.Count`: cardinality reducer, returns `int64(len(list))` including nil positions.                                                                             |
| (no regex arm)                                                                    | `case v.Regex`: compiles the pattern, applies `FindStringSubmatch` to the resolved string of `From`, returns `m[Capture]` (default 0 = full match); falls back to `Default` on no match, or nil when neither match nor `Default` applies. |

### `client/predicate.go`

No structural change. Doc strings updated to drop the slice-number
reference and the implication that bare `body.<path>` ever short-circuited
through this file:

| Before                                                                                                | After                                                                                                                                                                                          |
|-------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `compare`'s doc named the legacy bare-body short-circuit and `complete_when` predicates.              | `compare`'s doc states every predicate path is namespace-rooted; lists the resolvable roots (state, events, extract, steps, response, cache, fan_out alias) and reiterates absent-tolerance.   |
| `equal`'s doc referenced a "scroll_id fixture" justifying the loose-equality rule.                    | `equal`'s doc retains the rule but cites a generic `response.body.flag` example instead of a template-specific fixture.                                                                        |

### `client/extract.go`

Full rewrite:

| Before                                                                                                                                       | After                                                                                                                                                                  |
|----------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `runExtracts` used `ev.Name` and `ev.Target` to decide between `scope.extract` and `scope.cursor`.                                           | `runExtracts` reads `ev.To` (a Path), splits via `extractDest` into namespace + name, writes to `scope.state` or `scope.extract` based on the namespace prefix.        |
| Only `Coerce` was applied before write.                                                                                                       | `Regex` is applied first (full-match capture, no fallback), then `Coerce`. Both run only when `raw != nil`.                                                            |
| No `cursor` arm replacement — the runtime had a `target: cursor` switch.                                                                      | No cursor arm. `state.<name>` and `extract.<name>` are the only destinations.                                                                                          |
| Error messages used `extract.<name>` for context labelling.                                                                                   | Error messages use `extract <dest>.<name>` where `<dest>` is `state` or `extract`, mirroring the destination shape.                                                    |

The `resolveExtractFrom` body-walk logic is unchanged (`response.body /
response.header / steps.<id>.body / steps.<id>.header`). The file-level
doc-string is rewritten to drop the cursor-target paragraph and to
describe the new destination contract.

### `client/bodypath.go`

Doc-string hygiene only:

| Before                                                                                                                                                            | After                                                                                                  |
|-------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------|
| `pathParts`'s doc named `progress.{latest_event_timestamp, max_event_field}.event_time.path` as the example call site for per-event body-relative walks.          | `pathParts`'s doc keeps the per-event body-relative walk explanation without naming the deleted progress variants. |

`stripBodyRoot`, `resolveBodyPath`, `lookupBodyPath`, and `locateEvents`
are unchanged. They already accept only `response.body.<path>` and
`steps.<id>.body.<path>` and reject everything else with a clear
diagnostic, matching the closed root set from Slice 6.

## Removed content

- `NowValue.Offset` handling in `evalValue` — the field was removed from
  the schema struct in Slice 5; the runtime arm that read it is gone.
- The cursor-namespace paragraph from the `evalValue` file-level
  doc-string.
- `ExtractVar.Name` and `ExtractVar.Target` references — both fields
  were removed from the schema struct in Slice 4. The runtime no longer
  reads them; the destination is the `To` Path's namespace prefix.
- The `case "", "extract" / case "cursor"` switch in `runExtracts`.
- The legacy `complete_when` predicate citation in
  `(*scope).compare`'s doc-string.
- The scroll_id fixture citation in `equal`'s doc-string.
- The `progress.{latest_event_timestamp, max_event_field}` citation in
  `pathParts`'s doc-string.

## Added content

- `(*scope).evalArith(verb, operands)` — common operand resolution and
  nil short-circuit for `add` / `subtract`.
- `arithAdd(a, b)`, `arithSubtract(a, b)` — pure functions implementing
  the documented type pairs.
- `(*scope).evalReducerInput(verb, operand)` — resolves the reducer's
  operand to a `[]any` (the codec normalises `{max: [...]}` to
  `{max: {list: [...]}}`, so `evalValue` returns a list directly).
- `reduceExtreme(list, greatest)` — best-effort min/max over the
  comparable subset of `list`, skipping nil entries.
- `firstNonNil(list)`, `lastNonNil(list)` — first/last reducer helpers.
- `(*scope).applyRegex(pattern, in, capture, def)` — shared regex
  helper used by both the Value-form arm in `evalValue` and the
  extract-time `Regex` field in `runExtracts`.
- `extractDest(p)` — unpacks an `ExtractVar.To` Path into namespace
  (`state` | `extract`) + field name.

The new imports introduced are `regexp` (in `value.go`); nothing else
changes.

## Type pairs and runtime dispatch

`evalValue` returns one of:

  string | int64 | bool | time.Time | time.Duration | []any | map[string]any | nil

`time.Duration` is the only new entry compared to Slice 8's set; it
arises only as the result of `subtract(time, time)` or
`add(duration, duration)` / `subtract(duration, duration)`. Down-stream
consumers route durations through `format: duration` (string),
`format: parse_duration` (int64 nanoseconds), or another arithmetic
operation.

Arithmetic dispatch in `arithAdd` / `arithSubtract`:

1. **Time path.** `asTime(operand)` checks for `time.Time` and for
   strings that parse as RFC 3339 (with or without nanoseconds). If one
   operand is a time, the other is coerced via `toDuration` (strings go
   through `time.ParseDuration`, ints become nanoseconds).
2. **Int path.** Both operands must coerce via `asInt64` (accepts `int`,
   `int64`, `float64`; rejects strings to avoid silent duration→int
   confusion).
3. **Duration path.** Both operands coerce via `toDuration`. This is the
   fall-through case — only reached when neither operand is a time and
   at least one is not an int.

`{add: [{now: true}, "1h"]}` flows through the time path; `{add: [1, 2]}`
flows through the int path; `{add: ["1h", "30m"]}` flows through the
duration path. The validator (Slice 7) is responsible for rejecting
illegal pairs (e.g. `time + time`, `int + duration`) at lowering time;
the runtime catches the residue with a clear error.

## Reducer semantics

| Reducer  | Empty list | All-nil list | Mixed nil/value list                         |
|----------|------------|--------------|----------------------------------------------|
| `max`    | `nil`      | `nil`        | Skip nils, return the largest comparable.    |
| `min`    | `nil`      | `nil`        | Skip nils, return the smallest comparable.   |
| `first`  | `nil`      | `nil`        | Return the first non-nil element.            |
| `last`   | `nil`      | `nil`        | Return the last non-nil element.             |
| `count`  | `0`        | `len(list)`  | `len(list)` — every position counts, including nils. The semantics match `events.count`. |

`reduceExtreme` uses `orderedCompare` from `predicate.go`, so the same
ordering rule applies to reducers and to ordered-comparison predicates:
times via RFC 3339 parsing, then ints, then lexicographic strings. Two
elements that cannot be compared under any of those rules are skipped
silently (the reducer is best-effort over the comparable subset). This
makes `{max: {ref: events.*.timestamp}}` Just Work when one event's
timestamp is `nil` (absent field) but is still defensive when a
template projects a heterogeneous field.

## Regex semantics

`(*scope).applyRegex(pattern, in, capture, def)`:

- Compiles `pattern` via `regexp.Compile` (Go's RE2 engine). Compile
  errors surface as a clear `regex: compile <pattern>: <reason>`.
- Runs `FindStringSubmatch(in)`.
- On match: returns `m[capture]` (0 = full match, n = n-th submatch,
  1-based). Out-of-range `capture` is an error.
- On no match: evaluates `def` if set, else returns `nil`.

`ExtractVar.Regex` calls this helper with `capture=0` and `def=nil`
(the extract field is just a pattern string — there is no per-extract
default).

## Build state

`go build ./schema/...` — green. `go build ./client/...` — red, with
**zero** errors in `value.go`, `predicate.go`, `extract.go`, or
`bodypath.go`. Errors stop with `too many errors`; the full set (via
`-gcflags="-e"`) lives in:

- `client/auth.go` (Slice 13)
- `client/http.go` (Slice 12/13)
- `client/oauth2.go` (Slice 13)
- `client/pagination.go` (Slice 10)
- `client/progress.go` (Slice 11)
- `client/requestcache.go` (Slice 13)
- `client/runner.go` (Slice 12)

Test files stay red until Slice 17 owns the rewrite.

## Verification

`go build ./schema/...` is green. `go build -gcflags="-e" ./client/...`
lists 120 compile errors across 7 files; none live in the four files
this slice owns.

End-to-end value-runtime coverage (against real `*schema.Doc` fixtures
through the full client package) lands with Slice 17, once the rest of
`client/` compiles.

### Walk-through verification

Walked the trickier cases against the code:

- `{subtract: [{now: true}, "720h"]}` — operand[0] resolves to `time.Time`;
  operand[1] resolves to `string("720h")`. `arithSubtract` sees `isTimeA`,
  not `isTimeB`; falls into the "time - duration" arm, calls
  `toDuration("720h")` → `720h`, returns `now.Add(-720h)`. Result type:
  `time.Time`.
- `{add: [{now: true}, "1h"]}` — symmetric. `arithAdd` time path,
  `toDuration("1h")`, returns `time.Time` shifted forward.
- `{add: [1, 2]}` — neither operand is a time; both pass `asInt64`;
  returns `int64(3)`.
- `{add: ["1h", "30m"]}` — neither operand is a time; `asInt64` fails;
  both `toDuration` parse cleanly; returns `time.Duration` of `1h30m`.
- `{max: [3, 1, 5, 2]}` — list literal evaluates to `[]any{3,1,5,2}`;
  `reduceExtreme(_, true)` walks via `orderedCompare`; returns `5`.
- `{max: {ref: events.*.timestamp}}` over a list with a nil entry —
  `projectEvents` returns `[]any{"2024-...", nil, "2024-..."}`;
  `reduceExtreme` skips nil; returns the later timestamp string (compared
  via `asTime` inside `orderedCompare`).
- `{count: {ref: events.*.timestamp}}` — `evalReducerInput` returns the
  projected list (with nils preserved); `int64(len(list))` matches the
  events.count semantics from Slice 8.
- `{first: [nil, nil, "a"]}` — `firstNonNil` returns `"a"`.
- `{regex: {pattern: "(\\d+)", from: "hello 42", capture: 1}}` —
  `FindStringSubmatch` returns `["42", "42"]`; `m[1]` = `"42"`.
- `{regex: {pattern: "abc", from: "xyz", default: "fallback"}}` — no
  match; `default` evaluates to `"fallback"`.
- `{regex: {pattern: "abc", from: "xyz"}}` — no match, no default;
  returns `nil`.
- Extract with `to: state.foo`, regex first then coerce: `extractDest`
  returns `("state", "foo")`; `applyRegex` reduces `"Page-42"` to
  `"42"`; `applyFormat("int", "42")` returns `int64(42)`;
  `s.state["foo"] = int64(42)`.
- Extract with `to: extract.bar`, no regex / no coerce: raw flows
  through unchanged; `s.extract["bar"] = raw`.

## Notes for downstream slices

### Slice 10 — `client/pagination.go`

- The new pagination variants (`cursor_token`, `next_url`, `counter`,
  `custom`) write into `scope.state` via `evalValue` + `state.<name>`
  destinations. The per-drain wipe (already in place via
  `(*scope).resetPerDrainScratch`) handles bootstrapping; no new
  per-iteration seed step.
- `pagination.next_url`'s `regex:` + `capture:` reach the runtime
  through the same `regexp.Compile` + `FindStringSubmatch` path as the
  Value-form `Regex` arm. Re-use `(*scope).applyRegex` rather than
  duplicating the compile/match logic.
- `pagination.counter`'s default terminate predicate
  (`events.count < step`) reads `events.count` directly off the
  `events.count` arm in `resolveNamespaceRef`; no new helper needed.

### Slice 11 — `client/progress.go`

- Progress `from:` expressions are arbitrary Values. The runner stages
  every `from:` evaluation against the pre-write snapshot of `state.*`
  (per `docs/runtime.md` §5) and only then writes every `to:`. The
  staging pattern needs access to `evalValue`; nothing new required
  here.
- Cumulative high-water marks compose via the new reducers:
  `from: {max: [{ref: state.last_ts}, {max: {ref: events.*.timestamp}}]}`.
  The inner `max` resolves the projection; the outer `max` picks the
  greater of the prior state and the page max.
- Each progress `regex:` field uses `(*scope).applyRegex` with
  `capture=0` and no default (same shape as `ExtractVar.Regex`).

### Slice 12 — `client/runner.go`

- Bind `scope.events` once per accepted page-response before
  `applyProgress` and sink delivery. The events list comes from
  `locateEvents(body, eventsAt, ndjson)` (already in `bodypath.go`).
- The drain loop drives the new pagination plan: per-page
  `terminate_when` predicate evaluation reads `events.count` and
  cursor state through `resolveNamespaceRef` — all of which routes
  through `evalValue` and the helpers in this slice.

### Slice 13 — `client/http.go`, `auth.go`, `cache.go`

- OAuth2 token cache reads `expiry_field` as a body-rooted Path
  (`response.body.<path>` or `steps.<id>.body.<path>`). The walk goes
  through `stripBodyRoot` + `lookupBodyPath`, unchanged.
- Cache expiry composition uses `{add: [{now: true}, "<dur>"]}` —
  caches that fall back when no `expires_in` is supplied evaluate that
  expression via `evalValue` and get a `time.Time` back.

### Slice 14 — `client/sink.go`, `redact.go`, `trace.go`

- `schema.IsSecret` (Slice 5) already recurses through every new Value
  form (Add/Subtract/reducers/Regex). The redact layer's secret-taint
  propagation is inherited; no additional plumbing in this slice.
