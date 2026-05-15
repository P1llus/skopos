# Custom state stores

The runner persists state between drains through a small two-method
interface. This guide is for operators implementing a backing store the
project does not ship — SQLite, BoltDB, etcd, Redis, Postgres, etc.

For the high-level "what is state" picture see
[`usage.md`](usage.md#stores-persisting-state); for the field-by-field
schema see [`schema.md`](schema.md#state).

## The contract

```go
type Store interface {
    Load() (Snapshot, error)
    Save(Snapshot) error
}

type Snapshot struct {
    State  map[string]any `json:"state,omitempty"`
    Cursor map[string]any `json:"cursor,omitempty"`
}
```

What lives where:

- **`State`** carries runtime-mutable state fields — the only state
  declared in `state.fields` that changes between iterations. The OAuth2
  token cache slot (`auth.oauth2.<grant>.cache.store_in`) is the canonical
  example; custom JSON logins are similar. Each cache slot is paired with
  a `<store_in>_expires_at` slot (also a runtime-mutable state field) that
  holds the cached value's expiry timestamp as an RFC 3339 string — both
  slots auto-register from the cache block, so authors never declare them
  in `state.fields`. Config-mutability fields (the default,
  operator-supplied) are NOT persisted: they come from
  `state.fields[<name>].default` and re-seed every Drain.
- **`Cursor`** carries every cursor field inferred from the active
  pagination + progress + async_job strategies. After slice 7 it is
  author-facing only — the framework-internal expiry slots that used to
  live at `cursor.__*_expires_at` moved into `State` (see above). The
  catalogue in `client/state.go` lists the cursor key per strategy.

Both maps hold values that round-trip through JSON cleanly: strings,
numbers, booleans, nested maps. The bundled `FileStore` uses
`encoding/json`; a custom store can serialise however it likes as long as
`Save(snap)` followed by `Load()` returns an equal `Snapshot`.

## Lifecycle

One `Drain` does:

1. `store.Load()` once at the top — seeds `scope.state` from
   `Snapshot.State` merged with `schema.State.Fields[].Default`, and
   `scope.cursor` from `Snapshot.Cursor`.
2. The pull loop mutates state and cursor as it runs.
3. `store.Save(snapshot)` is called via `defer` — it runs on the normal
   exit, on `error.mode: warn`, AND on `error.mode: fail`. A partial
   drain (e.g. an `async_job` that hit `phase=poll` halfway) still
   persists its progress so the next Drain resumes from the high-water
   mark.

`Load` returning the zero `Snapshot` signals first-run state. A custom
store SHOULD return `(Snapshot{}, nil)` for "no record yet" rather than
an error.

## Concurrency contract

The runner does not gate concurrent access to a shared `Store`. A `Store`
shared between Runners (or accessed concurrently for any other reason)
MUST be safe for concurrent `Load` and `Save`. A few common shapes:

| Pattern                                       | What you implement                                            |
|-----------------------------------------------|---------------------------------------------------------------|
| One Runner, one process                       | Nothing extra; calls are serial within a Drain.               |
| Multiple Runners, one process, distinct paths | Nothing extra (each Runner has its own Store).                |
| Multiple Runners, one process, shared store   | Internal mutex around the read/write.                         |
| Multiple processes against the same backing   | Single-writer arbitration (lockfile, SELECT FOR UPDATE, …).   |

The bundled `FileStore` covers shape 1–3 with an internal mutex; it is
**not** safe across processes pointing at the same file (the contract is
spelled out in `filestore.go`).

## Worked example: SQLite

> This example is documentation-only. The runner has no SQLite dependency
> — you supply the driver in your own module (e.g. `modernc.org/sqlite`
> or `github.com/mattn/go-sqlite3`).

Schema: one row per spec, keyed by a stable identifier the operator
chooses. State and cursor are stored as JSON BLOBs so the schema doesn't
have to track the IR's structural changes.

```sql
CREATE TABLE IF NOT EXISTS skopos_state (
    id      TEXT PRIMARY KEY,
    state   BLOB NOT NULL DEFAULT '{}',
    cursor  BLOB NOT NULL DEFAULT '{}',
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
            cursor  BLOB NOT NULL DEFAULT '{}',
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

    var stateJSON, cursorJSON []byte
    err := s.db.QueryRow(
        `SELECT state, cursor FROM skopos_state WHERE id = ?`, s.id,
    ).Scan(&stateJSON, &cursorJSON)
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
    if len(cursorJSON) > 0 {
        if err := json.Unmarshal(cursorJSON, &snap.Cursor); err != nil {
            return client.Snapshot{}, fmt.Errorf("sqlitestore: decode cursor: %w", err)
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
    cursorJSON, err := json.Marshal(snap.Cursor)
    if err != nil {
        return fmt.Errorf("sqlitestore: encode cursor: %w", err)
    }

    _, err = s.db.Exec(`
        INSERT INTO skopos_state (id, state, cursor, updated)
        VALUES (?, ?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(id) DO UPDATE SET
            state = excluded.state,
            cursor = excluded.cursor,
            updated = excluded.updated`,
        s.id, stateJSON, cursorJSON,
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

Single-process, multiple specs: open `db` once, construct one `Store` per
spec with distinct `id`s. The embedded mutex serialises Load/Save within
a process; SQLite's own locking handles concurrent processes (use
`PRAGMA journal_mode=WAL` for better concurrency).

## What "correct" means

A `Store` is correct when:

1. `Load` returns `(Snapshot{}, nil)` for first-run, never an error.
2. After `Save(snap)`, the next `Load` returns a snapshot deeply equal to
   `snap` (specifically: `snap.State` and `snap.Cursor` round-trip through
   whatever serialisation the store uses).
3. Concurrent `Save` calls from N goroutines do not interleave; the last
   `Save` wins and the result is one of the inputs (not a mix).
4. A `Save` that returns non-nil leaves the on-disk state unchanged (the
   `FileStore` achieves this with temp-file + rename; a SQL store gets it
   for free from transactional semantics).

Property 3 matters because the runner's deferred `Save` fires from the
same goroutine as `Drain`, but a process can host multiple Runners that
share a `Store`. Property 4 matters because the next Drain re-reads
whatever Save left behind — a half-written cursor would silently corrupt
progress.

## Testing your Store

The bundled `MemoryStore` is the simplest reference implementation. A
quick conformance test for a custom store:

```go
func TestStoreRoundTrip(t *testing.T, newStore func() client.Store) {
    s := newStore()

    got, err := s.Load()
    if err != nil { t.Fatalf("Load on empty: %v", err) }
    if got.State != nil || got.Cursor != nil {
        t.Fatalf("Load on empty: got %+v, want zero", got)
    }

    want := client.Snapshot{
        State:  map[string]any{"token": "abc"},
        Cursor: map[string]any{"last_timestamp": "2026-01-01T00:00:00Z"},
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

## Why so minimal?

The two-method interface is intentional. Anything richer (transactions,
batching, optimistic concurrency tokens) would force every implementer to
participate, and the runner does not need any of it: one Drain reads
once, mutates in-memory, writes once. The complexity that DOES exist —
single-writer-per-path, runtime vs config mutability, cursor inference —
lives in the runner, not the contract.
