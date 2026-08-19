package branch

import (
	"context"
	"fmt"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/mongo"
	"github.com/adityaraj/sprout/internal/postgres"
)

// ConnectorLifecycleResult is returned by connector suspend/resume.
type ConnectorLifecycleResult struct {
	Connector meta.Connector      `json:"connector"`
	Branches  []meta.BranchRecord `json:"branches"`
	Message   string              `json:"message,omitempty"`
}

// SuspendConnector stops the connector replica compute and all branches from it.
// Data directories are kept (same idea as branch suspend).
func (s *Service) SuspendConnector(ctx context.Context, projectID, name string) (ConnectorLifecycleResult, error) {
	if err := s.requireOrgOwner(ctx); err != nil {
		return ConnectorLifecycleResult{}, err
	}
	unlock, err := s.lockBranch(ctx, s.connectorLockKey(name, auth.OwnerFrom(ctx)))
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}
	defer unlock()

	c, err := s.lookupConnector(ctx, projectID, name)
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}

	branches, err := s.branchesFromConnector(ctx, projectID, c)
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}

	out := make([]meta.BranchRecord, 0, len(branches))
	for _, b := range branches {
		rec, err := s.suspendBranchBestEffort(ctx, b)
		if err != nil {
			return ConnectorLifecycleResult{}, fmt.Errorf("suspend branch %s: %w", b.Name, err)
		}
		out = append(out, rec)
	}

	h := s.connectorHandle(c)
	_ = s.Compute.Stop(ctx, h)
	c.Status = meta.ConnectorIdle
	c.ErrorMessage = ""
	_ = s.Store.UpdateConnector(ctx, c)

	if br, err := lookupReplicaRow(ctx, s.Store, projectID, c); err == nil {
		br.Status = meta.StatusIdle
		_ = s.Store.UpdateBranch(ctx, br)
	}

	fmt.Printf("✓ connector %q suspended (%d branches idle)\n", c.Name, len(out))
	return ConnectorLifecycleResult{
		Connector: c,
		Branches:  out,
		Message:   fmt.Sprintf("stopped connector %q and %d branch(es)", c.Name, len(out)),
	}, nil
}

// ResumeConnector starts the connector replica and all idle branches from it.
func (s *Service) ResumeConnector(ctx context.Context, projectID, name string) (ConnectorLifecycleResult, error) {
	if err := s.requireOrgOwner(ctx); err != nil {
		return ConnectorLifecycleResult{}, err
	}
	unlock, err := s.lockBranch(ctx, s.connectorLockKey(name, auth.OwnerFrom(ctx)))
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}
	defer unlock()

	c, err := s.lookupConnector(ctx, projectID, name)
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}

	h := s.connectorHandle(c)
	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: h.Name, DataDir: h.DataDir, Port: h.Port,
		LogFile: s.logPath(h.Name), Engine: engine.Normalize(c.Engine),
	}); err != nil {
		return ConnectorLifecycleResult{}, fmt.Errorf("compute_failed: start connector: %w", err)
	}
	if engine.IsMongo(c.Engine) {
		inst := &mongo.Instance{
			Name: h.Name, Owner: c.CreatedBy, DataDir: h.DataDir, Port: h.Port,
			LogFile: s.logPath(h.Name), Bins: mongo.FindOnPath(), Password: c.Password,
		}
		_ = inst.EnsureAppRoles()
	} else {
		inst := &postgres.Instance{
			Name: h.Name, Owner: c.CreatedBy, DataDir: h.DataDir, Port: h.Port,
			LogFile: s.logPath(h.Name), Bins: s.Bins, Password: c.Password,
		}
		_ = inst.EnsureAppRoles()
	}
	c.Status = meta.ConnectorReplicating
	c.ErrorMessage = ""
	_ = s.Store.UpdateConnector(ctx, c)

	if br, err := lookupReplicaRow(ctx, s.Store, projectID, c); err == nil {
		br.Status = meta.StatusActive
		br.ErrorMessage = ""
		br.ConnString = advertiseConnURL(c.Engine, c.Port, c.Password, c.Name, "", c.CreatedBy)
		_ = s.Store.UpdateBranch(ctx, br)
	}

	branches, err := s.branchesFromConnector(ctx, projectID, c)
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}
	out := make([]meta.BranchRecord, 0, len(branches))
	for _, b := range branches {
		rec, err := s.resumeBranchBestEffort(ctx, b)
		if err != nil {
			return ConnectorLifecycleResult{}, fmt.Errorf("resume branch %s: %w", b.Name, err)
		}
		out = append(out, rec)
	}

	fmt.Printf("✓ connector %q resumed (%d branches)\n", c.Name, len(out))
	cs, one := advertiseConnector(c)
	fmt.Println("  ", cs)
	fmt.Println("  ", one)
	return ConnectorLifecycleResult{
		Connector: c,
		Branches:  out,
		Message:   fmt.Sprintf("started connector %q and %d branch(es)", c.Name, len(out)),
	}, nil
}

func (s *Service) branchesFromConnector(ctx context.Context, projectID string, c meta.Connector) ([]meta.BranchRecord, error) {
	list, err := s.Store.ListBranches(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var out []meta.BranchRecord
	for _, b := range list {
		if b.Role != "branch" {
			continue
		}
		if c.ID != "" && b.SourceConnectorID == c.ID {
			out = append(out, b)
			continue
		}
		if b.SourceConnectorID == "" && b.SourceConnector == c.Name && b.CreatedBy == c.CreatedBy {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *Service) suspendBranchBestEffort(ctx context.Context, rec meta.BranchRecord) (meta.BranchRecord, error) {
	if rec.Status == meta.StatusIdle {
		return rec, nil
	}
	if rec.Status != meta.StatusActive && rec.Status != meta.StatusError && rec.Status != meta.StatusCrashed {
		return rec, nil
	}
	unlock, err := s.lockBranch(ctx, s.instKey(rec))
	if err != nil {
		return meta.BranchRecord{}, err
	}
	defer unlock()
	fresh, err := s.Store.GetBranchByID(ctx, rec.ID)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	rec = fresh
	if rec.Status == meta.StatusIdle {
		return rec, nil
	}
	key := s.instKey(rec)
	h := compute.Handle{Provider: s.Compute.Name(), Name: key, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID, Engine: s.sourceEngine(ctx, rec), Password: rec.Password}
	_ = s.Compute.Stop(ctx, h)
	rec.Status = meta.StatusIdle
	rec.ContainerID = ""
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) resumeBranchBestEffort(ctx context.Context, rec meta.BranchRecord) (meta.BranchRecord, error) {
	if rec.Status == meta.StatusActive {
		return rec, nil
	}
	if rec.Status != meta.StatusIdle && rec.Status != meta.StatusCrashed {
		return rec, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}
	unlock, err := s.lockBranch(ctx, s.instKey(rec))
	if err != nil {
		return meta.BranchRecord{}, err
	}
	defer unlock()
	fresh, err := s.Store.GetBranchByID(ctx, rec.ID)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	rec = fresh
	if rec.Status == meta.StatusActive {
		return rec, nil
	}
	key := s.instKey(rec)
	eng := s.sourceEngine(ctx, rec)
	started, err := s.Compute.Start(ctx, compute.Spec{
		Name: key, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(key), Engine: eng,
	})
	if err != nil {
		return meta.BranchRecord{}, err
	}
	rec.ContainerID = started.ContainerID
	rec.Status = meta.StatusActive
	rec.ErrorMessage = ""
	rec.ConnString, _ = advertiseBranch(rec, eng)
	if engine.IsMongo(eng) {
		inst := &mongo.Instance{Name: rec.Name, Source: rec.SourceConnector, Owner: rec.CreatedBy, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(key), Bins: mongo.FindOnPath(), Password: rec.Password}
		_ = inst.EnsureAppRoles()
	} else {
		inst := &postgres.Instance{Name: rec.Name, Source: rec.SourceConnector, Owner: rec.CreatedBy, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(key), Bins: s.Bins, Password: rec.Password}
		_ = inst.EnsureAppRoles()
	}
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}
