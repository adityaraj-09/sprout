// Package storage is Layer 3 of Sprout-like branching.
//
// Why this exists:
//   Branching a TB-sized Postgres must NOT copy bytes (when CoW is available).
//   We snapshot + clone the filesystem that holds PGDATA.
//   Postgres never knows — it just starts against a new data directory.
//
// Providers:
//   - APFS  (macOS): clonefile-based CoW directory clones
//   - ZFS   (Linux): real zfs snapshot + zfs clone (needs SPROUT_ZFS_DATASET)
//   - Copy  (fallback): full cp -a — works on Azure without CoW
package storage

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Provider abstracts copy-on-write snapshot/clone operations.
// BranchManager depends on this interface — not on ZFS or APFS specifically.
type Provider interface {
	Name() string

	// Snapshot freezes sourceDir into a named point-in-time image.
	Snapshot(sourceDir, snapshotName string) (snapshotRef string, err error)

	// Clone creates a writable data directory from a snapshot.
	Clone(snapshotRef, destDir string) error

	// Destroy removes a snapshot or a clone directory/dataset.
	Destroy(ref string) error

	// Exists reports whether a snapshot or dataset/dir exists.
	Exists(ref string) bool
}

// Detect picks the best provider for this machine.
//
// Order:
//  1. SPROUT_STORAGE=copy|apfs|zfs forces a backend
//  2. ZFS only if SPROUT_ZFS_DATASET is set (e.g. sprout/data)
//  3. APFS on macOS
//  4. Plain copy (Azure / any Linux without configured ZFS datasets)
func Detect(root string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SPROUT_STORAGE"))) {
	case "copy", "plain", "cp":
		fmt.Fprintln(os.Stderr, "  [storage] using full-copy backend (SPROUT_STORAGE=copy)")
		return NewCopy(root), nil
	case "apfs":
		if !apfsLikely() {
			return nil, fmt.Errorf("SPROUT_STORAGE=apfs but not on macOS")
		}
		return NewAPFS(root), nil
	case "zfs":
		ds := strings.TrimSpace(os.Getenv("SPROUT_ZFS_DATASET"))
		if ds == "" {
			return nil, fmt.Errorf("SPROUT_STORAGE=zfs requires SPROUT_ZFS_DATASET (e.g. sprout/data)")
		}
		return NewZFS(ds)
	}

	if ds := strings.TrimSpace(os.Getenv("SPROUT_ZFS_DATASET")); ds != "" && zfsAvailable() {
		return NewZFS(ds)
	}
	if apfsLikely() {
		return NewAPFS(root), nil
	}
	fmt.Fprintln(os.Stderr, "  [storage] using full-copy backend (set SPROUT_ZFS_DATASET for real ZFS CoW)")
	return NewCopy(root), nil
}

func zfsDatasetForPath(path string) string {
	cmd := exec.Command("zfs", "list", "-H", "-o", "name,mountpoint")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	bestName, bestLen := "", -1
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, mp := fields[0], fields[1]
		if mp == "-" {
			continue
		}
		if path == mp || strings.HasPrefix(path, strings.TrimRight(mp, "/")+"/") {
			if len(mp) > bestLen {
				bestName, bestLen = name, len(mp)
			}
		}
	}
	return bestName
}
