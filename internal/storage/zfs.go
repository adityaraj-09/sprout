package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ZFS implements Provider with real dataset snapshot + clone.
//
// Teaching notes:
//   zfs snapshot tank/ardent/demo/main@alice   → almost free (metadata)
//   zfs clone   tank/.../main@alice tank/.../branch-alice
//
//   root is interpreted as a dataset prefix, e.g. "tank/ardent/demo".
//   Mountpoints are whatever zfs set mountpoint=... configured.
//
//   Phase 1 on macOS will not use this; keep it for a Linux box later.
type ZFS struct {
	datasetRoot string // tank/ardent/demo
	mainName    string // main
}

func NewZFS(datasetRoot string) (*ZFS, error) {
	if !zfsAvailable() {
		return nil, fmt.Errorf("zfs binary not found")
	}
	return &ZFS{datasetRoot: datasetRoot, mainName: "main"}, nil
}

func (z *ZFS) Name() string { return "zfs" }

func zfsAvailable() bool {
	_, err := exec.LookPath("zfs")
	return err == nil
}

func (z *ZFS) mainDataset() string {
	return filepath.ToSlash(filepath.Join(z.datasetRoot, z.mainName))
}

func (z *ZFS) Snapshot(sourceDir, snapshotName string) (string, error) {
	// sourceDir is unused for ZFS path identity — dataset is canonical.
	// Callers still pass PGDATA for interface uniformity.
	_ = sourceDir
	snap := z.mainDataset() + "@" + snapshotName
	start := time.Now()
	if err := zfsRun("snapshot", snap); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "  [storage/zfs] snapshot %s in %s\n", snap, time.Since(start).Round(time.Millisecond))
	return snap, nil
}

func (z *ZFS) Clone(snapshotRef, destDir string) error {
	// For ZFS, destDir is treated as the full dataset name to create.
	start := time.Now()
	if err := zfsRun("clone", snapshotRef, destDir); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  [storage/zfs] clone %s → %s in %s\n", snapshotRef, destDir, time.Since(start).Round(time.Millisecond))
	return nil
}

func (z *ZFS) Destroy(ref string) error {
	return zfsRun("destroy", "-r", ref)
}

func (z *ZFS) Exists(ref string) bool {
	err := zfsRun("list", "-H", ref)
	return err == nil
}

func zfsRun(args ...string) error {
	cmd := exec.Command("zfs", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
