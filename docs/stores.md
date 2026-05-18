# Custom state stores

The runner persists `state.*` between drains through a small two-method
interface. This guide is for operators implementing a backing store the
project does not ship — SQLite, BoltDB, etcd, Redis, Postgres, etc.

For the field-by-field schema see [`schema.md`](schema.md#state); for
the drain lifecycle and the runtime contract see
[`runtime.md`](runtime.md); for the embedding API see
[`usage.md`](usage.md#6-stores-persisting-state).

---

## 1. What the runner persists

The persistence contract is intentionally narrow. The runner persists
exactly one thing: state fields whose lifetime infers to **persistent**,
plus state fields whose lifetime infers to **operator config**. Every
other runtime namespace is ephemeral.

A state field's lifetime is derived from where it appears as the `to:`
of a write — see [`schema.md` §state](schema.md#state) for the full
inference rule. The three sub-flavours:

| Lifetime         | Inference rule                                                                          | Persisted? |
|------------------|-----------------------------------------------------------------------------------------|------------|
| Operator config  | Has a `default:` (or is operator-supplied) and is never the `to:` of any write.         | Yes.       |
| Per-drain scratch| Appears as the `to:` of any `pagination:` write.                                        | No.        |
| Persistent       | Appears as the `to:` of any `progress:` write, or `requests[].extract` with `to: state.*`. | Yes.       |

The runner persists operator-config + persistent. Per-drain scratch is
wiped at the start of every drain (see [`runtime.md`
§2](runtime.md#2-drain-lifecycle)).

### What is NOT persisted

Every namespace other than `state.*` lives in process memory only:

| Namespace                    | Why not persisted                                                              |
|------------------------------|--------------------------------------------------------------------------------|
| `cache.<name>`               | Process memory only — cleared on runner restart. See [`runtime.md` §8](runtime.md#8-cache-namespace). |
| `events.<...>`               | Per-iteration. Reset when the next page-response arrives.                      |
| `extract.<name>`             | Per-iteration. Reset at the top of every iteration.                            |
| `steps.<id>.body.<path>`     | Per-iteration. The decoded body lives only while the chain is mid-iteration.   |
| `steps.<id>.header.<name>`   | Per-iteration. Same scope as `steps.<id>.body.*`.                              |
| `response.body.<path>`       | Per-evaluation. Bound only while a predicate or extract is being resolved.     |
| `response.header.<name>`     | Per-evaluation. Same scope as `response.body.*`.                               |
| `<fan_out.as>.<path>`        | Per-fan-out-iteration. Bound only while iterating per-item inside one step.    |

The same applies to `state.<name>` slots whose lifetime infers to
**per-drain scratch** (every pagination variant's `to:` destination):
those slots are restored from their declared `default:` (or unset, if
none) on the next drain start. A drain that fails mid-page
re-bootstraps pagination from the variant's defaults.

---

## 2. The `Store` interface

```go
type Store interface {
    Load() (Snapshot, error)
    Save(Snapshot) error
}

type Snapshot struct {
    State map[string]any `json:"state,omitempty"`
}
```

`Snapshot.State` is a flat map of `<field-name> → <value>`. The keys
are exactly the names declared under `state:` in the spec, with no
namespace prefix. Values round-trip through JSON cleanly: strings,
numbers, booleans, nested maps, lists.

The bundled `FileStore` uses `encoding/json`; a custom store can
serialise however it likes as long as `Save(snap)` followed by
`Load()` returns an equal `Snapshot`.

`Load` returning the zero `Snapshot` (`Snapshot{}`) signals first-run
state. A custom store SHOULD return `(Snapshot{}, nil)` for "no record
yet" rather than an error.

### Lifecycle

One `Drain` does:

1. `store.Load()` once at the top — the runner seeds the in-memory
   `state.*` map by layering each declared field's `default:` Value
   under the loaded snapshot. Any persistent field present in the
   snapshot wins; any field absent from the snapshot falls back to its
   `default:`.
2. Per-drain scratch fields are wiped (reset to their declared
   `default:`, or unset).
3. The pagination loop runs, mutating `state.*` as `pagination.*.to`,
   `requests[].extract` with `to: state.*`, and `progress:` writes
   fire.
4. `store.Save(snapshot)` is called via `defer` — it runs on the normal
   exit, on `error.mode: warn`, AND on `error.mode: fail`. A partial
   drain (e.g. a poll loop that hit `MaxPages`) still persists its
   progress so the next `Drain` resumes from the high-water mark.

What's in `snapshot.State` at `Save` time:

- Every operator-config field (so the persisted file is
  self-contained and a re-load reproduces the same starting state
  without needing to re-supply defaults).
- Every persistent field's most-recent value.
- Per-drain scratch fields are **omitted**: the next drain
  re-bootstraps them from `default:` regardless of what was in memory
  at `Save` time.

The `cache.*` namespace is excluded entirely — see
[`runtime.md` §8](runtime.md#8-cache-namespace).

---

## 3. Concurrency contract

The runner does not gate concurrent access to a shared `Store`. A
`Store` shared between `Runner`s (or accessed concurrently for any
other reason) MUST be safe for concurrent `Load` and `Save`. A few
common shapes:

| Pattern                                       | What you implement                                            |
|-----------------------------------------------|---------------------------------------------------------------|
| One Runner, one process                       | Nothing extra; calls are serial within a Drain.               |
| Multiple Runners, one process, distinct paths | Nothing extra (each Runner has its own Store).                |
| Multiple Runners, one process, shared store   | Internal mutex around `Load` / `Save`.                        |
| Multiple processes against the same backing   | Single-writer arbitration (lockfile, SELECT FOR UPDATE, etc.).|

The bundled `FileStore` covers shapes 1–3 with an internal mutex; it
is **not** safe across processes pointing at the same file. The
contract is spelled out in `client/filestore.go`: two skopos processes
writing the same path race on the temp-file → rename sequence, and the
second writer silently clobbers the first.

---

## 4. Bundled implementations

| Implementation | Construction                          | Notes                                                   |
|----------------|---------------------------------------|---------------------------------------------------------|
| `MemoryStore`  | `&MemoryStore{}` (or omit `Runner.Store`) | Process-local; lost on exit. Default when `Runner.Store` is nil. Useful for tests and one-shot runs. |
| `FileStore`    | `client.NewFileStore("state.json")`   | Single JSON file. Atomic writes (temp file + rename). Single-writer per path. |

Both implement the `Store` interface above. The CLI's `--state` flag
wires a `FileStore` at the given path; omitting `--state` leaves the
runner with the default `MemoryStore`.

---

## 5. Worked example: SQLite

> This example is documentation-only. The runner has no SQLite dependency
> — you supply the driver in your own module (e.g. `modernc.org/sqlite`
> or `github.com/mattn/go-sqlite3`).

Schema: one row per spec, keyed by a stable identifier the operator
chooses. The state map is stored as a JSON BLOB so the schema does not
have to track the spec's `state:` declarations.

```sql
CREATE TABLE IF NOT EXISTS skopos_state (
    id      TEXT PRIMARY KEY,
    state   BLOB NOT NULL DEFAULT '{}',
    updated DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

Implementation:

```go
package sqlitestore

import (
    "database/sql"
    "encoding/json"
    "errors"
    "fmt"
    "sync"

    "github.com/p1llus/skopos/client"
)

type Store struct {
    mu sync.Mutex
    db *sql.DB
    id string
}

// New returns a Store backed by db, scoped to id. Multiple Stores against
// the same db with different ids do not interfere. The caller owns db.Close.
func New(db *sql.DB, id string) (*Store, error) {
    _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS skopos_state (
            id      TEXT PRIMARY KEY,
            state   BLOB NOT NULL DEFAULT '{}',
            updated DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
        )`)
    if err != nil {
        return nil, fmt.Errorf("sqlitestore: init schema: %w", err)
    }
    return &Store{db: db, id: id}, nil
}

func (s *Store) Load() (client.Snapshot, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    var stateJSON []byte
    err := s.db.QueryRow(
        `SELECT state FROM skopos_state WHERE id = ?`, s.id,
    ).Scan(&stateJSON)
    if errors.Is(err, sql.ErrNoRows) {
        return client.Snapshot{}, nil // first run
    }
    if err != nil {
        return client.Snapshot{}, fmt.Errorf("sqlitestore load: %w", err)
    }

    var snap client.Snapshot
    if len(stateJSON) > 0 {
        if err := json.Unmarshal(stateJSON, &snap.State); err != nil {
            return client.Snapshot{}, fmt.Errorf("sqlitestore: decode state: %w", err)
        }
    }
    return snap, nil
}

func (s *Store) Save(snap client.Snapshot) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    stateJSON, err := json.Marshal(snap.State)
    if err != nil {
        return fmt.Errorf("sqlitestore: encode state: %w", err)
    }

    _, err = s.db.Exec(`
        INSERT INTO skopos_state (id, state, updated)
        VALUES (?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(id) DO UPDATE SET
            state = excluded.state,
            updated = excluded.updated`,
        s.id, stateJSON,
    )
    if err != nil {
        return fmt.Errorf("sqlitestore: upsert: %w", err)
    }
    return nil
}
```

Wiring it in:

```go
import (
    "database/sql"

    _ "modernc.org/sqlite" // or your preferred driver

    "github.com/p1llus/skopos/client"
    "yourmodule/sqlitestore"
)

db, _ := sql.Open("sqlite", "skopos.db")
defer db.Close()

store, _ := sqlitestore.New(db, "okta-system-log")

r := &client.Runner{
    Doc:   doc,
    Store: store,
    Sink:  client.NewJSONLSink(os.Stdout),
}
```

Single-process, multiple specs: open `db` once, construct one `Store`
per spec with distinct `id`s. The embedded mutex serialises `Load` /
`Save` within a process; SQLite's own locking handles concurrent
processes (use `PRAGMA journal_mode=WAL` for better concurrency).

---

## 6. What "correct" means

A `Store` is correct when:

1. `Load` returns `(Snapshot{}, nil)` for first-run, never an error.
2. After `Save(snap)`, the next `Load` returns a snapshot deeply equal
   to `snap` (specifically: `snap.State` round-trips through whatever
   serialisation the store uses).
3. Concurrent `Save` calls from N goroutines do not interleave; the
   last `Save` wins and the result is one of the inputs (not a mix).
4. A `Save` that returns non-nil leaves the on-disk state unchanged
   (the bundled `FileStore` achieves this with temp-file + rename; a
   SQL store gets it for free from transactional semantics).

Property 3 matters because the runner's deferred `Save` fires from the
same goroutine as `Drain`, but a process can host multiple Runners
that share a `Store`. Property 4 matters because the next `Drain`
re-reads whatever `Save` left behind — a half-written snapshot would
silently corrupt the persistent state.

---

## 7. Testing your `Store`

The bundled `MemoryStore` is the simplest reference implementation. A
quick conformance test for a custom store:

```go
func TestStoreRoundTrip(t *testing.T, newStore func() client.Store) {
    s := newStore()

    got, err := s.Load()
    if err != nil { t.Fatalf("Load on empty: %v", err) }
    if got.State != nil {
        t.Fatalf("Load on empty: got %+v, want zero", got)
    }

    want := client.Snapshot{
        State: map[string]any{
            "last_timestamp": "2026-01-01T00:00:00Z",
            "api_key":        "abc",
        },
    }
    if err := s.Save(want); err != nil { t.Fatalf("Save: %v", err) }

    got, err = s.Load()
    if err != nil { t.Fatalf("Load after Save: %v", err) }
    if !reflect.DeepEqual(got, want) {
        t.Fatalf("round trip: got %+v, want %+v", got, want)
    }
}
```

For a multi-writer store also exercise concurrent `Save` calls under
`-race`; the implementation must serialise writes so the final read is
one of the writers' inputs, not a torn mix.

---

## 8. Why so minimal?

The two-method interface is intentional. Anything richer
(transactions, batching, optimistic concurrency tokens) would force
every implementer to participate, and the runner does not need any of
it: one `Drain` reads once, mutates in memory, writes once. The
complexity that DOES exist — single-writer-per-path, lifetime
inference, the per-drain wipe — lives in the runner, not the contract.
