# Handoff — Phase 2 Slice 8 (`client/state.go`)

Slice 7 closed (schema package is the gate on shape; full validator
rewrite landed, lifetime inference enforces per-drain ∩ persistent ∅,
the unified `Cache` block + closed `on_status` verb set are checked,
the five-variant pagination union is checked, the flat `Progress` list
is checked, `events.*` projection vocabulary binds at validate time,
the `invalidate_cache`-without-cache degrade emits a warning). See
[`IMPL-07-schema-validator.md`](IMPL-07-schema-validator.md). The
schema package now builds **green**; tests stay red until Slice 17.

The next slice is **Slice 8 — `client/state.go`**.

---

## Read first

In this order:

1. [`RESEARCH_PLAN.md` §"Global rules"](RESEARCH_PLAN.md#global-rules---apply-to-every-slice).
   Eight global rules apply to every slice. Internalise them.
2. [`PHASE-2-PLAN.md` §1 + §3 + §4](PHASE-2-PLAN.md). The Phase 2 slice
   list, the cross-cutting rules, the verification posture.
3. [`PHASE-2-PLAN.md` §"Slice 8"](PHASE-2-PLAN.md#slice-8--clientstatego).
   Your slice's detailed scope — six structural-change bullets.
4. [`docs/runtime.md` §2 (drain lifecycle)](../runtime.md). The
   `Runner.Drain` sequence: Load → per-drain wipe → pagination loop →
   sink → progress → deferred Save. Slice 8 owns Snapshot, scope, and
   the helpers that drive the wipe + the snapshot filter.
5. [`docs/stores.md` §2 (Snapshot shape)](../stores.md). The on-disk
   shape — only `state:` lives in the snapshot; `cache.*` is process
   memory only.
6. [`docs/schema.md` §Namespaces](../schema.md#namespaces). The closed
   root set the scope's `resolveNamespaceRef` mirrors.
7. [`IMPL-04-schema-structs.md`](IMPL-04-schema-structs.md),
   [`IMPL-05-schema-value.md`](IMPL-05-schema-value.md),
   [`IMPL-06-schema-path-predicate.md`](IMPL-06-schema-path-predicate.md),
   [`IMPL-07-schema-validator.md`](IMPL-07-schema-validator.md). The
   four predecessor artefacts; IMPL-07 documents the lifetime
   classification rule the scope mirrors.

The current `client/state.go` is pre-redesign and references the
removed `cursor` namespace, the old `Snapshot{State, Cursor}` shape,
and per-drain wipe logic that pivots off `Mutability`. Plan to rewrite
the file end-to-end against the new namespaces.

---

## Your slice

**Branch.** Develop on `claude/slice-08-client-state-<token>`.

**Files you may touch.**

- `client/state.go`

**Files you must NOT touch.**

- Any other file under `client/` (Slices 9–14 own those).
- Any file under `schema/` (frozen at Slice 7's close).
- Any test file (Slice 17 owns the test rewrite).
- Any file outside `client/`.

**Deliverables.**

1. `client/state.go` rewritten against the six bullets in
   [`PHASE-2-PLAN.md` §Slice 8](PHASE-2-PLAN.md#slice-8--clientstatego).
   The bullets are:
   1. `Snapshot` becomes `struct{ State map[string]any }` — no
      `Cursor` field. Per `docs/stores.md` §2.
   2. `scope` drops the `cursor` map. Adds:
      - `events any` (the decoded events list, exposed as `events.*` /
        `events.first` / `events.last` / `events.<int>` /
        `events.count`);
      - `cache map[string]any` (process-memory cache slots);
      - retains `state`, `extract`, `steps`, `stepHeaders`, `body`,
        `responseHeaders`, `item`, `itemBinding`, `nowFn`, `logger`.
   3. `newScope` seeds `state` from defaults under the snapshot (per
      `docs/runtime.md` §2 step 1). The per-drain wipe runs in
      `Runner.Drain`, not in `newScope`, so that scope can be reused
      across redrains in continuous mode without re-loading the
      snapshot.
   4. `snapshot()` writes only operator-config + persistent state
      fields, omitting per-drain scratch (per `docs/stores.md` §2).
      `cache.*` is never persisted.
   5. `resolveNamespaceRef`: drop the `cursor` arm. Add `cache` and
      `events` arms; the `events` arm understands `first`, `last`,
      `count`, `<int>` index, and `*` projection.
   6. Helper exposed (or methods on `scope`) to classify a state field
      as `operator-config | scratch | persistent` from the parsed
      doc — the classification is read off the IR, not stored on the
      scope; it drives the per-drain wipe and the `snapshot()` filter.

2. **Hygiene pass** per global rule #5. Top-of-file comments,
   doc-strings on every helper, error-message strings: no `cursor.*`,
   no `body.<path>`, no `async_job.*`, no `mutability`, no slice
   numbers, no design-doc references. Error messages should read as
   if the post-redesign shape had always existed.

3. **`events.*` projection binding.** Mirror Slice 7's semantics in
   the runtime resolver:
   - `events.count` returns the cardinality of the active page.
   - `events.<int>` returns the event at position `<int>` (0-based);
     non-integer or out-of-bounds resolves to absent (predicates
     remain absent-tolerant).
   - `events.first` and `events.last` resolve to the corresponding
     event (or absent on an empty page).
   - `events.*.field` projects the field across every event; the `*`
     segment is only legal at position 1 of an events ref.

4. **Lifetime classifier.** Read off the parsed `*schema.Doc`:
   - per-drain scratch: target of any `pagination.*.to` write
     (cursor_token, next_url, counter, custom.advance[].to);
   - persistent: target of any `progress[].to` write or
     `requests[].extract` with `to: state.<name>`;
   - operator config: declared in `state:` with a `default:` and never
     written.
   This classifier is the source for both the per-drain wipe and the
   `snapshot()` filter. Slice 7 verifies the classification has no
   conflicts; Slice 8 trusts the doc-shape is clean.

5. New artefact `docs/planning/IMPL-08-client-state.md` carrying:
   - Scope (one sentence).
   - Old → new map for `Snapshot`, `scope`, `newScope`,
     `resolveNamespaceRef`, the `snapshot()` filter.
   - Removed-content list (helper functions, struct fields, constants
     gone).
   - Notes for downstream slices: Slice 9 (value runtime calls
     `resolveNamespaceRef`), Slice 12 (runner owns the per-drain wipe
     call site), Slice 13 (cache.go writes to scope.cache).

6. Slice-table row 8 in `RESEARCH_PLAN.md` flipped to `[x]` with the
   `IMPL-08-client-state.md` link.

7. `HANDOFF.md` rewritten to point at Slice 9
   (`client/value.go` + `predicate.go` + `extract.go` + `bodypath.go`).

**Smoke tests.** Throwaway and optional. The trickier areas are the
events-projection resolver (especially `events.*.field` against an
empty list) and the snapshot filter (per-drain fields must NOT leak
into the persisted JSON). Mark smoke tests `// SMOKE - DELETE BEFORE
SLICE CLOSE`. The existing `client/*_test.go` files are stale and
off-limits per Slice 17.

**Out of scope.** No schema changes (Slice 7 closed). No value /
predicate runtime changes (Slice 9). No pagination / progress / runner
changes (Slices 10–12). No cache / auth / http changes (Slice 13).
No sink / trace changes (Slice 14).

---

## Watch out for

- **`pathClosedRoots` is internal to `schema/path.go`.** The client
  package cannot reference it. Copy the six closed-root names inline
  into `resolveNamespaceRef` and note the mirror in the doc-string.

- **`Snapshot` JSON shape change is wire-visible.** The on-disk format
  loses the `cursor:` key. Any existing on-disk state files become
  incompatible — that is the project's stated posture (no migration,
  no installed user base, see global rule #1). The serialiser that
  Slice 14's `FileStore` will use reads the same `Snapshot` shape.

- **`events` may be a list whose elements are arbitrary
  `interface{}`.** The `events.*.field` projection walks each element
  and extracts the named field; treat absent / wrong-typed elements
  as zero-valued (the predicate layer is already absent-tolerant).

- **`cache.*` is process-memory only.** `snapshot()` MUST omit it.
  The `scope.cache` map is freshly allocated per `newScope`; the
  `Cache` block runtime (Slice 13) writes to it; nothing else reads
  off-process.

- **Per-drain wipe is the runner's call site, NOT `newScope`'s.**
  Continuous-mode operation reuses one scope across N drains; wiping
  in `newScope` would re-load the snapshot every drain, which is the
  wrong contract. Provide the helper; let Slice 12 wire the call.

- **The build is GREEN at the start of this slice.** Don't regress
  it. `go build ./schema/... ./client/...` should pass after your
  rewrite (modulo the rest of `client/` that still consumes the old
  shape — if Slice 8's exported surface changes break callers in
  other client files, fix the call sites minimally and document in
  IMPL-08 so the downstream slice owners see the breakage).

- **Test files stay stale.** Per global rule #6 and PHASE-2-PLAN §4,
  Slice 17 owns the test rewrite. You may NOT touch `client/*_test.go`.
  Expect a sea of red on `go test ./client/...` and ignore it.

---

## When you finish

`git add` the rewritten `client/state.go`, the new
`IMPL-08-client-state.md`, the updated `RESEARCH_PLAN.md`, and the
updated `HANDOFF.md`. Commit with a message like:

```
feat(client): slice 8 — Snapshot shape, scope, per-drain wipe, new namespace roots

Replaces the pre-redesign Snapshot/scope shape. Snapshot drops Cursor;
scope drops cursor and adds events + cache. resolveNamespaceRef mirrors
the schema package's closed root set (state | cache | events | extract
| steps | response) plus author-chosen fan_out.as aliases. The
events.* projection resolver binds first/last/<int>/count/* sub-segments
at evaluation time. The lifetime classifier reads the parsed doc and
drives the per-drain wipe + the snapshot() filter.

schema/ + client/state.go build green at slice close; tests stay red
until Slice 17.
```

Push to `claude/slice-08-client-state-<token>` and open a PR.

If you discover the slice is wider than the plan (e.g. a struct shape
change forces a Slice 9 file to touch state.go's surface), **stop and
flag it via `AskUserQuestion`** rather than widening scope silently.
