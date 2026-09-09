// Package compute is Layer 4 for Phase 2: start/stop Postgres processes.
//
// Storage clones PGDATA. Compute only runs a postmaster against that directory.
// Local uses pg_ctl (Phase 1). Docker wraps the same data dir in a container.
package compute

import (
	"context"
	"fmt"
	"os"

	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/mongo"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/qdrant"
)

// Spec describes one database workload to run.
type Spec struct {
	Name    string // logical name (main, feature-x)
	DataDir string // host path to data dir (already prepared)
	Port    int    // host port clients connect to
	LogFile string // used by local provider
	Engine  string // postgres (default) | mongodb | qdrant
}

func specEngine(spec Spec) string {
	return detectEngine(spec.Engine, spec.DataDir)
}

func handleEngine(h Handle) string {
	return detectEngine(h.Engine, h.DataDir)
}

func detectEngine(tag, dataDir string) string {
	n := engine.Normalize(tag)
	if n == engine.Mongo || n == engine.Qdrant {
		return n
	}
	if qdrant.HasDataDir(dataDir) {
		return engine.Qdrant
	}
	if mongo.HasDataDir(dataDir) {
		return engine.Mongo
	}
	return engine.Postgres
}

// Handle is whatever we need later to stop/inspect the workload.
type Handle struct {
	Provider    string // "local" | "docker"
	ContainerID string // docker only
	Name        string
	Port        int
	DataDir     string
	Engine      string
	Password    string
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
	h := Handle{Provider: "local", Name: spec.Name, Port: spec.Port, DataDir: spec.DataDir, Engine: spec.Engine}
	switch specEngine(spec) {
	case engine.Mongo:
		inst := &mongo.Instance{
			Name: spec.Name, DataDir: spec.DataDir, Port: spec.Port, LogFile: spec.LogFile,
			Bins: mongo.FindOnPath(),
		}
		if err := inst.Start(); err != nil {
			return Handle{}, err
		}
		return h, nil
	case engine.Qdrant:
		inst := &qdrant.Instance{
			Name: spec.Name, DataDir: spec.DataDir, Port: spec.Port, LogFile: spec.LogFile,
			Bins: qdrant.FindOnPath(),
		}
		if err := inst.Start(); err != nil {
			return Handle{}, err
		}
		return h, nil
	}
	inst := l.instance(spec)
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
	switch handleEngine(h) {
	case engine.Mongo:
		inst := &mongo.Instance{Name: h.Name, DataDir: h.DataDir, Port: h.Port, Bins: mongo.FindOnPath(), Password: h.Password}
		return inst.Stop()
	case engine.Qdrant:
		inst := &qdrant.Instance{Name: h.Name, DataDir: h.DataDir, Port: h.Port, Bins: qdrant.FindOnPath(), Password: h.Password}
		return inst.Stop()
	}
	inst := &postgres.Instance{
		Name: h.Name, DataDir: h.DataDir, Port: h.Port,
		Bins: l.Bins,
	}
	return inst.Stop()
}

func (l *Local) IsRunning(ctx context.Context, h Handle) (bool, error) {
	_ = ctx
	switch handleEngine(h) {
	case engine.Mongo:
		inst := &mongo.Instance{Port: h.Port, DataDir: h.DataDir, Bins: mongo.FindOnPath(), Password: h.Password}
		return inst.IsRunning(), nil
	case engine.Qdrant:
		inst := &qdrant.Instance{Port: h.Port, DataDir: h.DataDir, Bins: qdrant.FindOnPath(), Password: h.Password}
		return inst.IsRunning(), nil
	}
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
