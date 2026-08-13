package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// APFS implements Provider using macOS copy-on-write file clones.
//
// Teaching notes:
//   APFS can clone a file with clonefile(2) so two directory entries share
//   the same physical extents until one is modified — same idea as ZFS CoW,
//   at file granularity.
//
//   We emulate "snapshot + clone" as:
//     1. Snapshot = CoW-copy PGDATA → <root>/snapshots/<name>  (frozen)
//     2. Clone    = CoW-copy snapshot → <root>/branches/<name> (writable)
//
//   Unlike ZFS, the snapshot step copies metadata for every file (still fast,
//   but not quite "one metadata op for the whole dataset"). For Phase 1 on a
//   Mac this is the right teaching substitute.
type APFS struct {
	root string // e.g. ./data
}

func NewAPFS(root string) *APFS {
	return &APFS{root: root}
}

func (a *APFS) Name() string { return "apfs" }

func (a *APFS) EnsureVolume(path string) error {
	return os.MkdirAll(path, 0o700)
}

func apfsLikely() bool {
	return runtime.GOOS == "darwin"
}

func (a *APFS) snapshotPath(name string) string {
	return filepath.Join(a.root, "snapshots", name)
}

func (a *APFS) Snapshot(sourceDir, snapshotName string) (string, error) {
	dest := a.snapshotPath(snapshotName)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("snapshot already exists: %s", snapshotName)
	}

	start := time.Now()
	if err := cowCopyTree(sourceDir, dest); err != nil {
		_ = os.RemoveAll(dest)
		return "", fmt.Errorf("apfs snapshot (cow copy): %w", err)
	}

	// Mark snapshot intuitively read-only at the top level.
	// (Individual files may still be writable; Phase 1 does not start Postgres here.)
	_ = os.Chmod(dest, 0o555)

	fmt.Fprintf(os.Stderr, "  [storage/apfs] snapshot %q in %s\n", snapshotName, time.Since(start).Round(time.Millisecond))
	return dest, nil
}

func (a *APFS) Clone(snapshotRef, destDir string) error {
	if _, err := os.Stat(snapshotRef); err != nil {
		return fmt.Errorf("snapshot missing: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("clone destination already exists: %s", destDir)
	}

	start := time.Now()
	// Snapshot dir may be 0555; clone must be writable for Postgres.
	if err := cowCopyTree(snapshotRef, destDir); err != nil {
		_ = os.RemoveAll(destDir)
		return fmt.Errorf("apfs clone: %w", err)
	}
	if err := chmodTree(destDir, 0o700, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  [storage/apfs] clone → %s in %s\n", destDir, time.Since(start).Round(time.Millisecond))
	return nil
}

func (a *APFS) Destroy(ref string) error {
	return os.RemoveAll(ref)
}

func (a *APFS) Exists(ref string) bool {
	_, err := os.Stat(ref)
	return err == nil
}

// cowCopyTree uses `cp -aRc` so APFS can share extents (clonefile) per file.
// -a  archive (preserve mode/ownership/symlinks as allowed)
// -R  recursive
// -c  clonefile when possible (CoW), else fall back to copy
func cowCopyTree(src, dst string) error {
	cmd := exec.Command("cp", "-aRc", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func chmodTree(root string, dirMode, fileMode os.FileMode) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, dirMode)
		}
		return os.Chmod(path, fileMode)
	})
}
