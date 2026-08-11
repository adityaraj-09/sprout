package reconcile

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/adityaraj/ardent-clone/internal/compute"
	"github.com/adityaraj/ardent-clone/internal/meta"
	"github.com/adityaraj/ardent-clone/internal/storage"
)

// Reconciler aligns metadata with real compute/storage after crashes.
type Reconciler struct {
	Store   meta.Store
	Compute compute.Provider
	Storage storage.Provider
}

func (r *Reconciler) RunOnce(ctx context.Context) {
	branches, err := r.Store.ListAllBranches(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: list: %v\n", err)
		return
	}
	for _, b := range branches {
		r.fixOne(ctx, b)
	}
}

func (r *Reconciler) fixOne(ctx context.Context, b meta.BranchRecord) {
	h := compute.Handle{
		Provider: r.Compute.Name(), Name: b.Name, Port: b.Port,
		DataDir: b.DataDir, ContainerID: b.ContainerID,
	}
	running, _ := r.Compute.IsRunning(ctx, h)
	age := time.Since(b.UpdatedAt)

	switch b.Status {
	case meta.StatusCreating, meta.StatusResetting, meta.StatusDeleting:
		if age > 2*time.Minute {
			fmt.Fprintf(os.Stderr, "reconcile: %s stuck in %s — marking error + cleanup\n", b.Name, b.Status)
			_ = r.Compute.Stop(ctx, h)
			if b.DataDir != "" && b.Role == "branch" {
				_ = r.Storage.Destroy(b.DataDir)
			}
			if b.Status == meta.StatusCreating && b.SnapshotRef != "" {
				_ = r.Storage.Destroy(b.SnapshotRef)
			}
			if b.Status == meta.StatusDeleting {
				_ = r.Store.DeleteBranch(ctx, b.ID)
				return
			}
			b.Status = meta.StatusError
			b.ErrorMessage = "reconcile: operation timed out"
			_ = r.Store.UpdateBranch(ctx, b)
		}
	case meta.StatusActive:
		if b.Role == "main" {
			return
		}
		if !running {
			fmt.Fprintf(os.Stderr, "reconcile: %s marked active but compute down — marking idle\n", b.Name)
			b.Status = meta.StatusIdle
			b.ContainerID = ""
			_ = r.Store.UpdateBranch(ctx, b)
		}
	}
}

func (r *Reconciler) Loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	r.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.RunOnce(ctx)
		}
	}
}
