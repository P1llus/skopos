# Handoff — Phase 2 complete

Phase 2 is closed. All eighteen slices in the cross-cutting schema-
redesign rollout (slices 1–3 in Phase 1, slices 4–18 in Phase 2) have
landed. There is no successor slice queued from this plan.

The exit state of the project is:

- **Schema package** (Slice 4 → Slice 7) — rewritten end-to-end around
  the post-redesign IR: flat `Doc.State` map, unified `Cache` block,
  4-named-plus-custom `Pagination`, flat `[]ProgressWrite`,
  request-level `terminate_when:` loop, multi-mode auth, predicate
  comparison verbs (gt / lt / gte / lte), Value arithmetic (add /
  subtract) and reducers (max / min / first / last / count).
- **Client runtime** (Slice 8 → Slice 14) — rewritten against the
  same IR: per-drain scope wipe, lifetime inference for state fields,
  reducer-aware Value evaluator, request-level loop, in-process cache,
  fan-out flatten/wrap merge semantics, redaction over the new Value
  graph, JSONL trace + sink.
- **CLI** (Slice 15) — `validate` / `run` / `init` / `template
  list|show` carry the new flag set; the `--state-file` round-trip
  preserves only persistent state fields (scratch is wiped per drain).
- **Bundled templates** (Slice 16) — 27 templates in
  `templates/*.yml` plus 20 fixtures in `schema/testdata/*.yml`, every
  one parses + validates + round-trips through
  `schema/fixtures_test.go::TestSpecFixtures`.
- **Test suite** (Slice 17) — every `*_test.go` rewritten; `go test
  ./...`, `go vet ./...`, `go test -race ./client/...` are all green.
- **Schema reference** (Slice 18) — `docs/schema-reference.md`
  regenerated from `*schema.Doc` via `tools/gen-schema-doc`; the
  `cmd/skopos/testdata/*.txt` testscript goldens regenerated against
  the post-redesign templates; obsolete legacy-shape testscripts
  (`testdata/errors/*`, `testdata/schema/*`, and the three
  state-dependent scenarios `async_poll_latest_ts.txt`,
  `async_poll_stateless.txt`, `session_login_cached.txt`) deleted.
  Schema-level coverage of the deleted scenarios lives in
  `schema/fixtures_test.go`.

See [`IMPL-18-schema-reference.md`](IMPL-18-schema-reference.md) for
the per-file diff log, the generator invocation, the type-table
old → new map, the three walkthrough verifications (Cache,
Pagination, Value arithmetic + reducers), and the testscript-deletion
rationale.

---

## Verification commands

```
go run ./tools/gen-schema-doc -check       # schema-reference drift gate
go build ./...                              # green
go vet ./...                                # clean
go test ./...                               # green
go test -tags integration ./cmd/skopos      # green (testscript goldens)
go test -race ./client/...                  # green
```

All commands above are green at HEAD on this branch.

---

## Next phase

There is no Phase 3 in this plan. Future work (operator UI,
observability roll-up, scheduler, multi-tenant config storage,
etc.) is not scoped here. If a new phase is opened, plan a fresh
`PHASE-3-PLAN.md` alongside `PHASE-2-PLAN.md` and reset the slice
table in `RESEARCH_PLAN.md`.

The existing implementation plan files
([`IMPL-01-docs-schema.md`](IMPL-01-docs-schema.md) through
[`IMPL-18-schema-reference.md`](IMPL-18-schema-reference.md)) are
preserved as the executed record of the redesign rollout.
