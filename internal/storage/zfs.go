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
// Each PGDATA directory is its own child dataset under datasetRoot, mounted
// at the corresponding path under dataRoot:
//
//	sprout/data/main            →  <dataRoot>/main
//	sprout/data/replica-<name>  →  <dataRoot>/replicas/<name>
//	sprout/data/branch-<name>   →  <dataRoot>/branches/<name>
//
// Snapshot/Clone use those datasets so branching from a connector does not
// accidentally snapshot datasetRoot/main.
type ZFS struct {
	datasetRoot string // e.g. sprout/data
	dataRoot    string // e.g. /home/user/sprout-data
}

func NewZFS(datasetRoot, dataRoot string) (*ZFS, error) {
	if !zfsAvailable() {
		return nil, fmt.Errorf("zfs binary not found")
	}
	ds := strings.TrimSpace(strings.ReplaceAll(datasetRoot, "\\", "/"))
	ds = strings.TrimSuffix(ds, "/")
	if ds == "" {
		return nil, fmt.Errorf("ZFS dataset root is empty")
	}
	root := dataRoot
	if root == "" {
		wd, _ := os.Getwd()
		root = filepath.Join(wd, "data")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &ZFS{datasetRoot: ds, dataRoot: abs}, nil
}

func (z *ZFS) Name() string { return "zfs" }

func zfsAvailable() bool {
	_, err := exec.LookPath("zfs")
	return err == nil
}

func zfsJoin(root, name string) string {
	return strings.TrimSuffix(root, "/") + "/" + name
}

// childDataset maps a filesystem path under dataRoot to a ZFS dataset name.
func (z *ZFS) childDataset(path string) string {
	abs := filepath.Clean(path)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	root := filepath.Clean(z.dataRoot)

	if abs == filepath.Join(root, "main") {
		return zfsJoin(z.datasetRoot, "main")
	}
	if rel, err := filepath.Rel(filepath.Join(root, "replicas"), abs); err == nil && isChildRel(rel) {
		return zfsJoin(z.datasetRoot, "replica-"+flattenRel(rel))
	}
	if rel, err := filepath.Rel(filepath.Join(root, "branches"), abs); err == nil && isChildRel(rel) {
		return zfsJoin(z.datasetRoot, "branch-"+flattenRel(rel))
	}
	if rel, err := filepath.Rel(root, abs); err == nil && isChildRel(rel) {
		return zfsJoin(z.datasetRoot, flattenRel(rel))
	}
	return zfsJoin(z.datasetRoot, filepath.Base(abs))
}

func isChildRel(rel string) bool {
	return rel != "" && rel != "." && !strings.HasPrefix(rel, "..")
}

func flattenRel(rel string) string {
	rel = filepath.ToSlash(rel)
	return strings.ReplaceAll(rel, "/", "-")
}

func (z *ZFS) datasetForDir(path string) string {
	abs := filepath.Clean(path)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	if name, mp := lookupZFSMount(abs); name != "" && filepath.Clean(mp) == abs {
		return name
	}
	return z.childDataset(abs)
}

func (z *ZFS) EnsureVolume(path string) error {
	abs := filepath.Clean(path)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	ds := z.childDataset(abs)
	return z.ensureMountedDataset(ds, abs)
}

func (z *ZFS) ensureMountedDataset(dataset, mount string) error {
	if name, mp := lookupZFSMount(mount); name != "" && filepath.Clean(mp) == filepath.Clean(mount) {
		if name == dataset {
			return nil
		}
		return fmt.Errorf("mountpoint %s already used by dataset %s (want %s)", mount, name, dataset)
	}
	if zfsExists(dataset) {
		if err := zfsRun("set", "mountpoint="+mount, dataset); err != nil {
			return err
		}
		_ = zfsRun("mount", dataset)
		return nil
	}

	if st, err := os.Stat(mount); err == nil && st.IsDir() {
		entries, _ := os.ReadDir(mount)
		if len(entries) > 0 {
			return z.promoteDir(dataset, mount)
		}
		if err := os.Remove(mount); err != nil {
			// Non-empty after all, or a mountpoint we cannot remove.
			return z.promoteDir(dataset, mount)
		}
	}
	if err := os.MkdirAll(filepath.Dir(mount), 0o755); err != nil {
		return err
	}
	return zfsRun("create", "-o", "mountpoint="+mount, dataset)
}

func (z *ZFS) promoteDir(dataset, mount string) error {
	tmp := mount + ".sprout-zfs-promote"
	if _, err := os.Stat(tmp); err == nil {
		return fmt.Errorf("zfs promote leftover exists: %s (remove it and retry)", tmp)
	}
	if err := os.Rename(mount, tmp); err != nil {
		return fmt.Errorf("zfs promote rename %s: %w", mount, err)
	}
	if err := os.MkdirAll(filepath.Dir(mount), 0o755); err != nil {
		_ = os.Rename(tmp, mount)
		return err
	}
	if err := zfsRun("create", "-o", "mountpoint="+mount, dataset); err != nil {
		_ = os.Rename(tmp, mount)
		return err
	}
	cmd := exec.Command("cp", "-a", tmp+"/.", mount+"/")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs promote copy: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return os.RemoveAll(tmp)
}

func (z *ZFS) Snapshot(sourceDir, snapshotName string) (string, error) {
	sourceDir = filepath.Clean(sourceDir)
	ds := z.datasetForDir(sourceDir)
	if name, mp := lookupZFSMount(sourceDir); name == "" || filepath.Clean(mp) != sourceDir {
		// Subdirectory of a larger dataset cannot be snapshotted independently.
		if name != "" && filepath.Clean(mp) != sourceDir {
			return "", fmt.Errorf("zfs: %s is a subdirectory of dataset %s (mount %s); each PGDATA must be its own dataset — run with nested volumes or SPROUT_STORAGE=copy", sourceDir, name, mp)
		}
		if err := z.ensureMountedDataset(ds, sourceDir); err != nil {
			return "", fmt.Errorf("zfs snapshot: ensure dataset for %s: %w", sourceDir, err)
		}
		ds = z.datasetForDir(sourceDir)
	}
	snap := ds + "@" + snapshotName
	if zfsExists(snap) {
		return "", fmt.Errorf("snapshot already exists: %s", snap)
	}
	start := time.Now()
	if err := zfsRun("snapshot", snap); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "  [storage/zfs] snapshot %s in %s\n", snap, time.Since(start).Round(time.Millisecond))
	return snap, nil
}

func (z *ZFS) Clone(snapshotRef, destDir string) error {
	if !strings.Contains(snapshotRef, "@") {
		return fmt.Errorf("zfs clone: expected dataset@snap, got %s", snapshotRef)
	}
	destAbs := filepath.Clean(destDir)
	if a, err := filepath.Abs(destAbs); err == nil {
		destAbs = a
	}
	cloneDS := z.childDataset(destAbs)
	if zfsExists(cloneDS) {
		return fmt.Errorf("clone destination dataset already exists: %s", cloneDS)
	}
	if st, err := os.Stat(destAbs); err == nil {
		if st.IsDir() {
			entries, _ := os.ReadDir(destAbs)
			if len(entries) > 0 {
				return fmt.Errorf("clone destination already exists: %s", destAbs)
			}
			_ = os.Remove(destAbs)
		} else {
			return fmt.Errorf("clone destination already exists: %s", destAbs)
		}
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return err
	}
	start := time.Now()
	if err := zfsRun("clone", "-o", "mountpoint="+destAbs, snapshotRef, cloneDS); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  [storage/zfs] clone %s → %s (%s) in %s\n", snapshotRef, cloneDS, destAbs, time.Since(start).Round(time.Millisecond))
	return nil
}

func (z *ZFS) Destroy(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.Contains(ref, "@") {
		if !zfsExists(ref) {
			return nil
		}
		return zfsRun("destroy", "-r", ref)
	}
	abs := filepath.Clean(ref)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	if name, mp := lookupZFSMount(abs); name != "" && filepath.Clean(mp) == abs {
		if err := zfsRun("destroy", "-r", name); err != nil {
			return err
		}
		return nil
	}
	if strings.Contains(ref, "/") && !strings.HasPrefix(ref, "/") && zfsExists(ref) {
		return zfsRun("destroy", "-r", ref)
	}
	ds := z.childDataset(abs)
	if zfsExists(ds) {
		return zfsRun("destroy", "-r", ds)
	}
	return os.RemoveAll(ref)
}

func (z *ZFS) Exists(ref string) bool {
	if strings.Contains(ref, "@") || (strings.Contains(ref, "/") && !strings.HasPrefix(ref, "/")) {
		return zfsExists(ref)
	}
	abs := filepath.Clean(ref)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	if name, mp := lookupZFSMount(abs); name != "" && filepath.Clean(mp) == abs {
		return true
	}
	return zfsExists(z.childDataset(abs))
}

func zfsExists(ref string) bool {
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

func lookupZFSMount(path string) (name, mount string) {
	cmd := exec.Command("zfs", "list", "-H", "-o", "name,mountpoint")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", ""
	}
	return parseZFSList(string(out), path)
}
