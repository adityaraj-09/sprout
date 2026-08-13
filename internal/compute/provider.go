// Package compute is Layer 4 for Phase 2: start/stop Postgres processes.
//
// Storage clones PGDATA. Compute only runs a postmaster against that directory.
// Local uses pg_ctl (Phase 1). Docker wraps the same data dir in a container.
package compute

import (
	"context"
	"fmt"
	"os"

	"github.com/adityaraj/sprout/internal/postgres"
)

// Spec describes one Postgres workload to run.
type Spec struct {
	Name    string // logical name (main, feature-x)
	DataDir string // host path to PGDATA (already prepared)
	Port    int    // host port clients connect to
	LogFile string // used by local provider
}

// Handle is whatever we need later to stop/inspect the workload.
type Handle struct {
	Provider    string // "local" | "docker"
	ContainerID string // docker only
	Name        string
	Port        int
	DataDir     string
}

// Provider starts and stops Postgres compute.
type Provider interface {
	Name() string
	Start(ctx context.Context, spec Spec) (Handle, error)
	Stop(ctx context.Context, h Handle) error
	IsRunning(ctx context.Context, h Handle) (bool, error)
}

// Local runs Postgres on the host via pg_ctl (Phase 1 behavior).
type Local struct {
	Bins postgres.Binaries
}

func NewLocal(bins postgres.Binaries) *Local {
	return &Local{Bins: bins}
}

func (l *Local) Name() string { return "local" }

func (l *Local) instance(spec Spec) *postgres.Instance {
	return &postgres.Instance{
		Name:    spec.Name,
		DataDir: spec.DataDir,
		Port:    spec.Port,
		LogFile: spec.LogFile,
		Bins:    l.Bins,
	}
}

func (l *Local) Start(ctx context.Context, spec Spec) (Handle, error) {
	_ = ctx
	inst := l.instance(spec)
	h := Handle{Provider: "local", Name: spec.Name, Port: spec.Port, DataDir: spec.DataDir}
	if inst.IsRunning() {
		return h, nil
	}
	if err := inst.Start(); err != nil {
		return Handle{}, err
	}
	return h, nil
}

func (l *Local) Stop(ctx context.Context, h Handle) error {
	_ = ctx
	inst := &postgres.Instance{
		Name: h.Name, DataDir: h.DataDir, Port: h.Port,
		Bins: l.Bins,
	}
	return inst.Stop()
}

func (l *Local) IsRunning(ctx context.Context, h Handle) (bool, error) {
	_ = ctx
	inst := &postgres.Instance{Port: h.Port, DataDir: h.DataDir, Bins: l.Bins}
	return inst.IsRunning(), nil
}

// Detect picks compute. Default prefers local pg_ctl so initdb and runtime share
// the same major version. Docker is used when SPROUT_COMPUTE=docker|auto and
// SPROUT_COMPUTE=docker is set, or auto with SPROUT_PREFER_DOCKER=true.
func Detect(bins postgres.Binaries, prefer string) (Provider, error) {
	switch prefer {
	case "local":
		return NewLocal(bins), nil
	case "docker":
		d, err := NewDocker(bins)
		if err != nil {
			return nil, err
		}
		return d, nil
	case "", "auto":
		// Prefer local: initdb uses host binaries; docker:14 vs host:16 breaks PGDATA.
		if os.Getenv("SPROUT_PREFER_DOCKER") == "true" {
			if d, err := NewDocker(bins); err == nil && d.Available() {
				return d, nil
			}
		}
		return NewLocal(bins), nil
	default:
		return nil, fmt.Errorf("unknown compute provider %q (local|docker|auto)", prefer)
	}
}
