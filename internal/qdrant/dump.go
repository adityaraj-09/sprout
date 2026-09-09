package qdrant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adityaraj/sprout/internal/progress"
)

func (c Conn) Ping(ctx context.Context) error {
	return NewClient(c).Ready(ctx)
}

func (c Conn) Estimate(ctx context.Context, collections []string) (map[string]any, error) {
	cli := NewClient(c)
	names, err := cli.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	if len(collections) > 0 {
		allow := map[string]struct{}{}
		for _, n := range collections {
			allow[n] = struct{}{}
		}
		filtered := names[:0]
		for _, n := range names {
			if _, ok := allow[n]; ok {
				filtered = append(filtered, n)
			}
		}
		names = filtered
	}
	counts := map[string]int64{}
	var points int64
	for _, n := range names {
		info, err := cli.CollectionInfo(ctx, n)
		if err != nil {
			return nil, err
		}
		counts[n] = info.PointsCount
		points += info.PointsCount
	}
	return map[string]any{
		"engine":      "qdrant",
		"note":        "qdrant connect is a snapshot copy into a local qdrant (no continuous sync)",
		"collections": names,
		"points":      points,
		"counts":      counts,
	}, nil
}

func DumpImport(ctx context.Context, src Conn, local *Instance, collections []string) error {
	if local == nil {
		return fmt.Errorf("local qdrant instance required")
	}
	remote := NewClient(src)
	dest := local.LocalClient()
	names, err := remote.ListCollections(ctx)
	if err != nil {
		return err
	}
	if len(collections) > 0 {
		allow := map[string]struct{}{}
		for _, n := range collections {
			n = strings.TrimSpace(n)
			if n != "" {
				allow[n] = struct{}{}
			}
		}
		filtered := make([]string, 0, len(collections))
		seen := map[string]struct{}{}
		for _, n := range names {
			if _, ok := allow[n]; ok {
				filtered = append(filtered, n)
				seen[n] = struct{}{}
			}
		}
		for n := range allow {
			if _, ok := seen[n]; !ok {
				return fmt.Errorf("collection %q not found on source", n)
			}
		}
		names = filtered
	}
	if len(names) == 0 {
		progress.Println(ctx, "→ no collections to copy")
		return nil
	}
	dir, err := os.MkdirTemp("", "sprout-qdrant-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	for _, name := range names {
		progress.Printf(ctx, "→ copy collection %s", name)
		if err := copyCollection(ctx, remote, dest, name, dir); err != nil {
			return err
		}
	}
	return nil
}

func copyCollection(ctx context.Context, remote, dest *Client, name, tmpDir string) error {
	snap, snapErr := remote.CreateSnapshot(ctx, name)
	if snapErr == nil && snap.Name != "" {
		file := filepath.Join(tmpDir, snap.Name)
		if err := remote.DownloadSnapshot(ctx, name, snap.Name, file); err == nil {
			upErr := dest.UploadSnapshot(ctx, name, file)
			_ = remote.DeleteSnapshot(ctx, name, snap.Name)
			if upErr == nil {
				return nil
			}
			progress.Printf(ctx, "  snapshot restore failed (%v) — falling back to point copy", upErr)
		} else {
			progress.Printf(ctx, "  snapshot download failed (%v) — falling back to point copy", err)
			_ = remote.DeleteSnapshot(ctx, name, snap.Name)
		}
	} else if snapErr != nil {
		progress.Printf(ctx, "  snapshot create failed (%v) — falling back to point copy", snapErr)
	}
	return remote.ScrollCopy(ctx, dest, name)
}
