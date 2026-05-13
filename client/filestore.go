// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileStore persists a Snapshot to a single JSON file. Writes are atomic
// (write to a temp file in the same directory + rename).
//
// # Concurrency contract: single-writer per path
//
// In-process Save/Load calls are serialised by the embedded mutex, so a
// single *FileStore shared between Runners in the same process does not
// silently lose snapshots.
//
// FileStore is single-writer-only by contract: at most one *FileStore
// (in any process) may write a given path at a time. No lockfile is
// acquired and no cross-process arbitration is attempted; two writers
// on the same path will race on the temp-file → rename sequence and the
// second writer silently clobbers the first's snapshot.
//
// Operators running multiple skopos processes against the same state
// MUST give each process its own path, or plug in a Store backed by
// something with multi-writer semantics (BoltDB, SQLite, etc.).
type FileStore struct {
	mu   sync.Mutex
	path string
}

// NewFileStore returns a FileStore backed by path. The file is created
// lazily on the first Save.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Load reads the snapshot from disk. A missing file is not an error —
// Load returns the zero Snapshot, signalling a first-run state.
func (f *FileStore) Load() (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, nil
		}
		return Snapshot{}, fmt.Errorf("read %s: %w", f.path, err)
	}
	if len(data) == 0 {
		return Snapshot{}, nil
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("parse %s: %w", f.path, err)
	}
	return snap, nil
}

// Save writes the snapshot atomically: marshal → write to a temp file in
// the same directory → rename. This guarantees a partial write can't leave
// the operator with a corrupt state file.
func (f *FileStore) Save(s Snapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, f.path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
