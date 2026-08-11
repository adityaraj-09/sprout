// Package storage is Layer 3 of Ardent-like branching.
//
// Why this exists:
//   Branching a TB-sized Postgres must NOT copy bytes.
//   We snapshot + clone the filesystem that holds PGDATA.
//   Postgres never knows — it just starts against a new data directory.
//
// Phase 1 providers:
//   - APFS  (macOS): clonefile-based CoW directory clones
//   - ZFS   (Linux): real zfs snapshot + zfs clone
package storage

import "fmt"

// Provider abstracts copy-on-write snapshot/clone operations.
// BranchManager depends on this interface — not on ZFS or APFS specifically.
type Provider interface {
	Name() string

	// Snapshot freezes sourceDir into a named point-in-time image.
	// On ZFS this is metadata-only. On APFS we CoW-clone the directory tree once.
	Snapshot(sourceDir, snapshotName string) (snapshotRef string, err error)

	// Clone creates a writable data directory from a snapshot.
	Clone(snapshotRef, destDir string) error

	// Destroy removes a snapshot or a clone directory/dataset.
	Destroy(ref string) error

	// Exists reports whether a snapshot or dataset/dir exists.
	Exists(ref string) bool
}

// Detect picks the best provider for this machine.
func Detect(root string) (Provider, error) {
	if zfsAvailable() {
		return NewZFS(root)
	}
	if apfsLikely() {
		return NewAPFS(root), nil
	}
	return nil, fmt.Errorf("no CoW storage backend available (need ZFS or APFS)")
}
