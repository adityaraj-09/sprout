package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/storage"
)

const (
	defaultStuckAfter          = 2 * time.Minute
	defaultBootstrapStuckAfter = 20 * time.Minute
)

// Reconciler aligns metadata with real compute/storage after crashes.
type Reconciler struct {
	Store      meta.Store
	Compute    compute.Provider
	Storage    storage.Provider
	Root       string
	AutoResume bool

	StuckAfter          time.Duration // branches / dead bootstrap; 0 = 2m
	BootstrapStuckAfter time.Duration // live logical copy; 0 = 20m
}

func (r *Reconciler) stuckAfter() time.Duration {
	if r.StuckAfter > 0 {
		return r.StuckAfter
	}
	return defaultStuckAfter
}

func (r *Reconciler) bootstrapStuckAfter() time.Duration {
	if r.BootstrapStuckAfter > 0 {
		return r.BootstrapStuckAfter
	}
	return defaultBootstrapStuckAfter
}

func (r *Reconciler) logPath(name string) string {
	return filepath.Join(r.Root, "logs", name+".log")
}

func instKey(b meta.BranchRecord) string {
	if k := postgres.HostLabel(b.Name, b.SourceConnector, b.CreatedBy); k != "" {
		return k
	}
	return b.Name
}

func replicaName(c meta.Connector) string {
	return postgres.ReplicaComputeName(c.Name, c.CreatedBy)
}

func (r *Reconciler) RunOnce(ctx context.Context) {
	r.reconcileBranches(ctx)
	r.reconcileConnectors(ctx)
}

func (r *Reconciler) reconcileBranches(ctx context.Context) {
	branches, err := r.Store.ListAllBranches(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: list branches: %v\n", err)
		return
	}
	for _, b := range branches {
		if b.Role == "replica" {
			continue // owned by the connector row
		}
		r.fixBranch(ctx, b)
	}
}

func (r *Reconciler) fixBranch(ctx context.Context, b meta.BranchRecord) {
	key := instKey(b)
	h := compute.Handle{
		Provider: r.Compute.Name(), Name: key, Port: b.Port,
		DataDir: b.DataDir, ContainerID: b.ContainerID,
	}
	running, _ := r.Compute.IsRunning(ctx, h)
	age := time.Since(b.UpdatedAt)

	switch b.Status {
	case meta.StatusCreating, meta.StatusResetting, meta.StatusDeleting:
		if age > r.stuckAfter() {
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
		if running {
			return
		}
		if r.AutoResume {
			if err := r.startBranch(ctx, b); err != nil {
				r.markBranch(ctx, b, meta.StatusCrashed, "reconcile: compute down: "+err.Error())
				return
			}
			fmt.Fprintf(os.Stderr, "reconcile: auto-resumed branch %s\n", b.Name)
			return
		}
		r.markBranch(ctx, b, meta.StatusCrashed, "reconcile: compute down")
	case meta.StatusCrashed:
		if !r.AutoResume {
			return
		}
		if running {
			r.markBranch(ctx, b, meta.StatusActive, "")
			return
		}
		if err := r.startBranch(ctx, b); err != nil {
			r.markBranch(ctx, b, meta.StatusCrashed, "reconcile: auto-resume failed: "+err.Error())
			return
		}
		fmt.Fprintf(os.Stderr, "reconcile: auto-resumed crashed branch %s\n", b.Name)
	}
}

func (r *Reconciler) startBranch(ctx context.Context, b meta.BranchRecord) error {
	_, err := r.Compute.Start(ctx, compute.Spec{
		Name: instKey(b), DataDir: b.DataDir, Port: b.Port, LogFile: r.logPath(instKey(b)),
	})
	if err != nil {
		return err
	}
	b.Status = meta.StatusActive
	b.ErrorMessage = ""
	return r.Store.UpdateBranch(ctx, b)
}

func (r *Reconciler) markBranch(ctx context.Context, b meta.BranchRecord, status, msg string) {
	if b.Status != status || b.ErrorMessage != msg {
		fmt.Fprintf(os.Stderr, "reconcile: %s %s → %s %s\n", b.Name, b.Status, status, msg)
	}
	b.Status = status
	b.ErrorMessage = msg
	b.ContainerID = ""
	_ = r.Store.UpdateBranch(ctx, b)
}

func (r *Reconciler) reconcileConnectors(ctx context.Context) {
	list, err := r.Store.ListConnectors(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: list connectors: %v\n", err)
		return
	}
	for _, c := range list {
		r.fixConnector(ctx, c)
	}
}

func (r *Reconciler) fixConnector(ctx context.Context, c meta.Connector) {
	h := compute.Handle{
		Provider: r.Compute.Name(), Name: replicaName(c),
		Port: c.Port, DataDir: c.DataDir,
	}
	running, _ := r.Compute.IsRunning(ctx, h)
	age := time.Since(c.UpdatedAt)

	switch c.Status {
	case meta.ConnectorBootstrapping:
		// Logical copy_data often runs longer than 2m. Never kill a live subscriber
		// until well past WaitLogicalSync (10m).
		limit := r.stuckAfter()
		if running {
			limit = r.bootstrapStuckAfter()
		}
		if age > limit {
			fmt.Fprintf(os.Stderr, "reconcile: connector %s stuck bootstrapping — marking error\n", c.Name)
			_ = r.Compute.Stop(ctx, h)
			r.markConnector(ctx, c, meta.ConnectorError, "reconcile: bootstrap timed out")
		}
	case meta.ConnectorReplicating:
		if running {
			return
		}
		if r.AutoResume {
			if err := r.startConnector(ctx, c); err != nil {
				r.markConnector(ctx, c, meta.ConnectorCrashed, "reconcile: compute down: "+err.Error())
				return
			}
			fmt.Fprintf(os.Stderr, "reconcile: auto-resumed connector %s\n", c.Name)
			return
		}
		r.markConnector(ctx, c, meta.ConnectorCrashed, "reconcile: compute down")
	case meta.ConnectorCrashed:
		if !r.AutoResume {
			return
		}
		if running {
			r.markConnector(ctx, c, meta.ConnectorReplicating, "")
			return
		}
		if err := r.startConnector(ctx, c); err != nil {
			r.markConnector(ctx, c, meta.ConnectorCrashed, "reconcile: auto-resume failed: "+err.Error())
			return
		}
		fmt.Fprintf(os.Stderr, "reconcile: auto-resumed crashed connector %s\n", c.Name)
	}
}

func (r *Reconciler) startConnector(ctx context.Context, c meta.Connector) error {
	_, err := r.Compute.Start(ctx, compute.Spec{
		Name: replicaName(c), DataDir: c.DataDir, Port: c.Port,
		LogFile: r.logPath(replicaName(c)),
	})
	if err != nil {
		return err
	}
	c.Status = meta.ConnectorReplicating
	c.ErrorMessage = ""
	if err := r.Store.UpdateConnector(ctx, c); err != nil {
		return err
	}
	r.syncReplicaRow(ctx, c)
	return nil
}

func (r *Reconciler) markConnector(ctx context.Context, c meta.Connector, status, msg string) {
	c.Status = status
	c.ErrorMessage = msg
	_ = r.Store.UpdateConnector(ctx, c)
	r.syncReplicaRow(ctx, c)
}

func (r *Reconciler) syncReplicaRow(ctx context.Context, c meta.Connector) {
	br, err := r.Store.GetBranchByID(ctx, "replica-"+c.ID)
	if err != nil {
		br, err = r.Store.FindBranch(ctx, c.ProjectID, "replica-"+c.Name, "", c.CreatedBy)
	}
	if err != nil {
		return
	}
	switch c.Status {
	case meta.ConnectorReplicating:
		br.Status = meta.StatusActive
	case meta.ConnectorIdle:
		br.Status = meta.StatusIdle
	case meta.ConnectorCrashed:
		br.Status = meta.StatusCrashed
	default:
		br.Status = meta.StatusError
	}
	br.ErrorMessage = c.ErrorMessage
	_ = r.Store.UpdateBranch(ctx, br)
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
