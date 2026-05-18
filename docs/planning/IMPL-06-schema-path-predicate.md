# IMPL-06 — schema/path.go + schema/predicate.go

## Scope

Closed `schema/path.go`'s namespace-root vocabulary on the six post-redesign
roots; explicitly rejected the three removed roots (`cursor`, `body`,
`item`) at parse time; documented the `events.*` shortcut vocabulary in
the package docstring. Deleted the `equal:` → `value:` migration codec
from `schema/predicate.go` and rewrote the docstrings around the live
call-site list and the absent-tolerance policy. No other file was touched.

## path.go — old → new root-set map

| Form              | Old behaviour                                 | New behaviour                                                                                       |
|-------------------|-----------------------------------------------|-----------------------------------------------------------------------------------------------------|
| `state.<name>`    | accepted (validator-only check)               | accepted at parse time (closed-set root).                                                           |
| `cache.<name>`    | accepted; validator did the work              | accepted at parse time (closed-set root).                                                           |
| `events.<...>`    | accepted; validator did the work              | accepted at parse time (closed-set root). Docstring lists the `first`/`last`/`<int>`/`count`/`*` shortcuts. |
| `extract.<name>`  | accepted                                      | accepted at parse time (closed-set root).                                                           |
| `steps.<id>.<…>`  | accepted                                      | accepted at parse time (closed-set root).                                                           |
| `response.<…>`    | accepted                                      | accepted at parse time (closed-set root).                                                           |
| `<fan_out.as>.<…>`| accepted                                      | accepted at parse time as a bare-identifier root; validator (Slice 7) binds the alias.              |
| `cursor.<name>`   | accepted at parse; validator rejected         | **rejected at parse time** with hint pointing at `state.<name>`.                                    |
| `body.<path>`     | accepted at parse; validator rejected         | **rejected at parse time** with hint pointing at `response.body.<path>` / `steps.<id>.body.<path>`. |
| `item.<path>`     | accepted at parse; validator rejected         | **rejected at parse time** with hint pointing at the author-chosen `fan_out.as` alias.              |
| non-identifier root (e.g. `123foo`, `foo-bar`) | accepted; validator decided      | **rejected at parse time** as malformed root.                                                       |

The closed-root set is captured in `pathClosedRoots`, the removed set in
`pathRemovedRoots` (with hint strings), and the bare-identifier shape in
`isIdentifier(s)`. The validator (Slice 7) reads `pathClosedRoots`
directly when enforcing the fan-out alias reservation (`fan_out.as` must
not shadow a closed-set name).

`*` and bare integer segments (`events.0`, `events.42`) remain valid path
segments — the parser checks segment emptiness and root membership, not
segment content beyond the root.

## predicate.go — old → new call-site map

| Doc-comment line              | Old                                                            | New                                                                              |
|-------------------------------|----------------------------------------------------------------|----------------------------------------------------------------------------------|
| Call-site list                | `requests[].if`, `auth.multi_mode.branches[].when`, `Value.select.branches[].when`, `async_job.poll.complete_when`, `pagination.scroll_id.complete_when` | `requests[].if`, `requests[].terminate_when`, `auth.multi_mode.branches[].when`, `Value.select.branches[].when`, `pagination.*.terminate_when` |
| Example block                 | `{eq: {path: cursor.phase, value: "submit"}}` etc.             | `{eq: {path: state.phase, value: "submit"}}` etc.                                |
| `PredicateEq` doc             | "right-hand-side field is named Value (renamed from Equal in slice 6) so the shape reads naturally under the ordered verbs too" | Plain: "Path is the left-hand-side namespace-rooted locator; Value is the right-hand-side compared against it." |
| Absent-tolerance note         | absent                                                         | One paragraph: every verb returns false on either-side absent, never throws.     |

## Removed content

- `predicateEqRaw` shadow type (`schema/predicate.go`). After deletion,
  `PredicateEq` uses default `gopkg.in/yaml.v3` / `encoding/json`
  decoding via its existing field tags (`yaml:"path" json:"path"`,
  `yaml:"value" json:"value"`).
- `predicateEqEqualRenamedHint` constant (`schema/predicate.go`). The
  string described a slice-6 rename event that is now history; per
  global rule #1, no migration code remains.
- `PredicateEq.UnmarshalYAML` and `PredicateEq.UnmarshalJSON`
  (`schema/predicate.go`). Both existed only to surface the
  `equal:` → `value:` migration hint. Gone.
- The `bytes` import (`schema/predicate.go`). Only the deleted
  `UnmarshalJSON` used it.
- Stale namespace-root references in `path.go`'s package docstring
  (`cursor`, `item` were named as legal roots; the canonical example was
  `ref: cursor.last_timestamp`).
- Stale predicate examples that used `cursor.*` paths.

## Build state

`go build ./schema/...` is **red** at slice close — same posture as at
slice start. The remaining failures all live in `schema/validate.go`
(Slice 7's rewrite). `schema/path.go` and `schema/predicate.go` build
cleanly against `schema/schema.go`, `schema/read.go`, `schema/value.go`,
and `schema/doc.go`.

Smoke verification: a temporary `slice6_smoke_test.go` (build tag `smoke`,
validate.go + stale tests moved aside) ran 11 asserts covering closed-set
acceptance, fan-out alias acceptance, removed-root rejection with hint
content, `*` and integer segment acceptance, the `events.*` shortcut
vocabulary, map-form root checking, dotted/JSON round-trips, predicate
parsing on the new `state.*` paths, the dropped `equal:` migration
codec, and predicate-side rejection of a removed root. All 11 passed.
The smoke file was deleted before slice close per the PHASE-2-PLAN
convention.

## Notes for downstream slices

### Slice 7 — `schema/validate.go`

- The validator can read `pathClosedRoots` directly (same package) when
  enforcing `fan_out.as` reservation: the alias must not be one of the
  six closed roots. The three deleted-reserved roots (`cursor`, `body`,
  `item`) are already rejected at parse time, so the validator never
  sees them; it does not need to re-check.
- `pathRemovedRoots` is internal to the parser. The validator does not
  reference it.
- The `events.*` projection vocabulary (`first` / `last` / `<int>` /
  `count` / `*`) is parsed as ordinary segments. The validator binds
  the semantics at use-site — `events.count` must resolve to an int,
  `events.<int>` must be a non-negative integer literal, etc. — and
  cross-checks with `Response.events_at` having been declared.
- `PredicateEq` is now decoded by default yaml/json paths. Author errors
  like unknown sibling keys inside `{eq: {...}}` surface as standard
  decode errors; the validator does not need to special-case them.

### Slice 8 — `client/state.go`

- `resolveNamespaceRef` mirrors the same closed root set
  (`state | cache | events | extract | steps | response`). Copy the list
  inline — `pathClosedRoots` is an internal symbol of the `schema`
  package. The fan-out alias case (any other root) is dispatched off the
  scope's currently-bound alias map.
- Removed-root paths never reach the runtime; the parser blocks them.
  Slice 8 does not need fallback arms for `cursor.*` / `body.*` /
  `item.*`.

### Slice 9 — `client/value.go`

- The `events.*` shortcut vocabulary surfaces as ordinary `Path.Parts`
  for the value evaluator. `events.first` / `events.last` /
  `events.<int>` / `events.count` / `events.*` all reach the evaluator
  as `["events", "<segment>", ...]`. The evaluator dispatches based on
  the second segment; see `docs/schema.md` §events for the binding
  rules.
- Predicate evaluation reads `PredicateEq.Path` and `PredicateEq.Value`
  unchanged.
