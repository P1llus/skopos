# IMPL-05 — schema/value.go

## Scope

Rewrote the Value language layer in `schema/value.go`: added the arithmetic
(`add`, `subtract`), reducer (`max`, `min`, `first`, `last`, `count`), and
`regex` forms; implemented `${...}` string interpolation in the YAML and
JSON scalar paths; removed the `{now: true, offset: ...}` sibling-key form;
deleted the migration-hint discriminator table; extended `IsSecret` to walk
every new form; and absorbed the one-line `isSecretStatePath` fix Slice 4
left behind. No other file was touched.

## Old → new map

### Value variants

| Old shape                                                  | New shape                                                                 |
|------------------------------------------------------------|---------------------------------------------------------------------------|
| `{now: true, offset: <dur>}` (sibling `offset:` accepted)  | `{add: [{now: true}, <dur>]}` or `{subtract: [{now: true}, <dur>]}`. Plain `{now: true}` stays as-is; the `offset:` sibling is now rejected at parse time. |
| `{ref: state.token}` (no interpolation)                    | unchanged for the explicit form; the new scalar path desugars `"${state.token}"` to the same `{ref: state.token}` Value. |
| `"plain string"` → `LiteralString`                         | unchanged on the fast path; strings containing `$` flow through the interpolation scanner. |
| (no arithmetic forms)                                      | `Add`, `Subtract` carry `*ArithExpr{Operands []Value}` (length 2 enforced at parse time). |
| (no reducer forms)                                         | `Max`, `Min`, `First`, `Last`, `Count` each carry a single `*Value`. A bare list (`{max: [v1, v2]}`) is sugar that desugars to the inner `Value{List: [...]}`. |
| (no regex form)                                            | `Regex *RegexExpr{Pattern, From, Capture, Default}`. Pattern is required.|

### Discriminator key set

Old (9 keys):
`literal_string | ref | now | concat | select | format | base64 | list | object`

New (17 keys):
`literal_string | ref | now | concat | select | format | base64 | list | object | add | subtract | max | min | first | last | count | regex`

Each new variant's `valueVariantAllowedKeys` entry is single-key (no
permitted siblings); `now` loses `offset` from its allowed-sibling set so
the rejected form surfaces a clear "unknown key 'offset' in now form"
diagnostic.

### Removed content

- `NowValue.Offset *Value` — the field is gone; `NowValue` is now an empty
  marker struct. The codec rejects any `offset:` sibling on `{now: ...}`
  via the unchanged closed-allowed-sibling check.
- `removedValueDiscriminatorKeys` map (the `from_pagination` /
  `from_progress` migration-hint table) — both entries pointed at the
  deleted `cursor.*` namespace, so the entire table is gone per global
  rules #1 (no migration code) and #4 (no references to removed
  concepts).
- The migration-hint paragraph in `pickValueDiscriminator`'s 0-match arm —
  replaced by the canonical "no recognised discriminator key" error that
  enumerates the closed set.

### IsSecret walker

| Form        | What IsSecret now walks                                  |
|-------------|-----------------------------------------------------------|
| `Add`       | every element of `Add.Operands` (length 2)               |
| `Subtract`  | every element of `Subtract.Operands` (length 2)          |
| `Max`/`Min`/`First`/`Last`/`Count` | the wrapped `*Value` operand              |
| `Regex`     | `Regex.From`; and `Regex.Default` when set               |

The pre-existing recursion (Ref/Concat/Select/Format/Base64/List/Object)
is unchanged. The dead `NowValue.Offset` walker arm is gone with the
field.

### String interpolation

`desugarInterpolation(s string)` scans for `${<path>[|<default>]}` segments
and returns:

- A plain `LiteralString` on the fast path when `s` contains no `$`.
- The inner Ref directly when there is exactly one segment and no
  surrounding literal text (`"${state.url}"` → `{ref: state.url}`).
- A `Concat` over interleaved literal and Ref segments otherwise
  (`"${state.url}/path"` → `{concat: [{ref: state.url}, "/path"]}`).

Escape handling: `\$` outside a segment is a literal `$`; `\{` inside a
segment is a literal `{`; outside an interpolation segment, `$` not
followed by `{` is treated as a literal `$`. Both `Value.UnmarshalYAML`'s
scalar branch and `Value.UnmarshalJSON`'s `"` arm route through the same
desugarer.

Defaults parse through `parseInterpDefault`, which YAML-decodes the text
after `|` so `"${state.page|1}"` produces an integer-typed default and
`"${state.token|}"` produces an explicit empty-string default.

### Reducer dual-input handling

The schema requires reducers to accept both a list literal and a
list-shaped Value. The codec normalises the literal form into the
`Value{List: [...]}` shape at parse time so the validator (Slice 7) and
runtime (Slice 9) see only one input pattern:

- YAML: `decodeReducerOperandYAML` peeks the operand node and decodes a
  SequenceNode as a `Value{List: ...}`; any other node decodes as a
  regular Value.
- JSON: `decodeReducerOperandJSON` switches on the leading `[` to do the
  same normalisation.

### Arithmetic operand enforcement

`decodeArithOperandsYAML` and the JSON map switch both require exactly
two elements under the `add` / `subtract` key and reject any other
length with a precise count diagnostic.

### IsSecret one-line fix

`isSecretStatePath` previously called `d.State.Fields[p.Parts[1]]`. Slice
4 flattened `Doc.State` to `map[string]FieldDecl`, so the call is now
`d.State[p.Parts[1]]`. Comment block updated to drop the deleted
`Now.Offset` arm from the documented traversal contract.

## Build state

`go build ./schema/...` is **red** at slice close — same posture as at
slice start. The remaining errors are all in `schema/validate.go` (Slice
7's rewrite). `schema/value.go`, `schema/schema.go`, `schema/read.go`,
`schema/doc.go`, `schema/path.go`, and `schema/predicate.go` build
cleanly against each other.

Smoke verification: a temporary `value_smoke_test.go` file (under build
tag `smoke`, with the validate.go and stale tests moved aside) ran 14
asserts covering interpolation fast-path, concat desugaring, default
parsing (empty + int), now-rejects-offset, plain now, add/subtract
operand decoding, max list-literal sugar, max projection, regex shape,
and IsSecret propagation through both interpolation and reducers — all
passed. The smoke file was deleted before slice close per the
PHASE-2-PLAN convention.

## Notes for downstream slices

### Slice 6 (`schema/path.go` + `schema/predicate.go`)

- The interpolation parser uses `ParsePath` directly; Slice 6's closed
  namespace-root vocabulary (`state | cache | events | extract | steps |
  response | <fan_out.as>`) is what authors will use inside `${...}`. The
  scanner makes no assumption beyond "non-empty dotted string".
- The `events.first` / `events.last` / `events.<int>` / `events.count`
  shortcuts pass through `ParsePath` unchanged (they are normal path
  segments).
- The reducer projection shape `{ref: events.*.<field>}` relies on `*`
  being a legal path segment — Slice 6 owns enforcing what `*` means at
  validation time.

### Slice 7 (`schema/validate.go`)

- Reducer operand type-checking: after parse, `Max/Min/First/Last/Count`
  always carry a single `*Value`; the list-literal sugar has been
  normalised into `Value{List: ...}`. The validator only needs to
  type-check the operand as "resolves to a list".
- Arithmetic type-pairs: the codec guarantees exactly two operands. The
  validator walks the type-pair table in `docs/schema.md` §Arithmetic
  (`time + duration`, `duration + duration`, `int + int`, `time - time`,
  etc.) and rejects unmatched pairs at lowering time.
- String interpolation surface area: every `LiteralString` reaching the
  validator is post-desugar (`$`-free). The validator type-checks
  refs/defaults inside the produced `Concat` exactly like a hand-authored
  one.
- `{now: true, offset: ...}` is already rejected at parse time; the
  validator can drop any leftover handling. Same for the migration-hint
  table — the codec no longer issues those messages.
- `Regex` validation: `Pattern` is required (already enforced at parse
  time); `Capture` must be ≥ 0 (codec accepts any int; the validator
  bounds it). `From` is a Value; `Default` is optional.

### Slice 9 (`client/value.go`)

- `evalValue` gains arms for `Add`, `Subtract`, `Max`, `Min`, `First`,
  `Last`, `Count`, `Regex`. Reducer operands always resolve to a list at
  evaluation time (the codec normalises the literal-form sugar). The
  `Regex` arm compiles the pattern, runs `FindStringSubmatch` against
  the resolved string of `From`, and returns the chosen capture (or the
  optional `Default`, or a zero Value when both miss).
- `NowValue` carries no payload — the runtime's clock is applied at the
  Now arm directly; the arithmetic forms compose Now with a duration to
  recreate the old `{now: true, offset: ...}` behaviour.
- Concat propagation already exists; the new interpolation path emits
  only Concats and Refs, so no new evaluator hook is needed.
- Secret-taint: `IsSecret` now flags arithmetic/reducer/regex compositions
  reaching state.<secret> via any operand. Slice 14's redact layer
  inherits this for free.
