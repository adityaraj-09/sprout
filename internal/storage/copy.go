package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Copy is a non-CoW fallback (cp -a). Works on any Linux disk including Azure.
// Prefer ZFS/APFS when available — this uses full disk copies.
type Copy struct {
	root string
}

func NewCopy(root string) *Copy {
	return &Copy{root: root}
}

func (c *Copy) Name() string { return "copy" }

func (c *Copy) snapshotPath(name string) string {
	return filepath.Join(c.root, "snapshots", name)
}

func (c *Copy) Snapshot(sourceDir, snapshotName string) (string, error) {
	dest := c.snapshotPath(snapshotName)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("snapshot already exists: %s", snapshotName)
	}
	start := time.Now()
	if err := plainCopyTree(sourceDir, dest); err != nil {
		_ = os.RemoveAll(dest)
		return "", fmt.Errorf("copy snapshot: %w", err)
	}
	_ = os.Chmod(dest, 0o555)
	fmt.Fprintf(os.Stderr, "  [storage/copy] snapshot %q in %s (FULL copy — not CoW)\n", snapshotName, time.Since(start).Round(time.Millisecond))
	return dest, nil
}

func (c *Copy) Clone(snapshotRef, destDir string) error {
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
	if err := plainCopyTree(snapshotRef, destDir); err != nil {
		_ = os.RemoveAll(destDir)
		return fmt.Errorf("copy clone: %w", err)
	}
	if err := chmodTree(destDir, 0o700, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  [storage/copy] clone → %s in %s\n", destDir, time.Since(start).Round(time.Millisecond))
	return nil
}

func (c *Copy) Destroy(ref string) error { return os.RemoveAll(ref) }

func (c *Copy) Exists(ref string) bool {
	_, err := os.Stat(ref)
	return err == nil
}

func plainCopyTree(src, dst string) error {
	// cp -a preserves mode; works on ext4/xfs/zfs mounts without clonefile.
	cmd := exec.Command("cp", "-a", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
