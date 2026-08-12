package branch

import (
	"context"
	"fmt"

	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/meta"
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
	unlock := s.lockBranch("connector:" + name)
	defer unlock()

	c, err := s.Store.GetConnectorByName(ctx, projectID, name)
	if err != nil {
		return ConnectorLifecycleResult{}, fmt.Errorf("connector_not_found: %s", name)
	}

	branches, err := s.branchesFromConnector(ctx, projectID, c.Name)
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}

	out := make([]meta.BranchRecord, 0, len(branches))
	for _, b := range branches {
		rec, err := s.suspendBranchBestEffort(ctx, projectID, b.Name)
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

	if br, err := s.Store.GetBranch(ctx, projectID, "replica-"+c.Name); err == nil {
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
	unlock := s.lockBranch("connector:" + name)
	defer unlock()

	c, err := s.Store.GetConnectorByName(ctx, projectID, name)
	if err != nil {
		return ConnectorLifecycleResult{}, fmt.Errorf("connector_not_found: %s", name)
	}

	h := s.connectorHandle(c)
	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: h.Name, DataDir: h.DataDir, Port: h.Port,
		LogFile: s.logPath(h.Name),
	}); err != nil {
		return ConnectorLifecycleResult{}, fmt.Errorf("compute_failed: start connector: %w", err)
	}
	inst := &postgres.Instance{
		Name: h.Name, DataDir: h.DataDir, Port: h.Port,
		LogFile: s.logPath(h.Name), Bins: s.Bins,
	}
	_ = inst.EnsureAppRoles()
	c.Status = meta.ConnectorReplicating
	c.ErrorMessage = ""
	_ = s.Store.UpdateConnector(ctx, c)

	if br, err := s.Store.GetBranch(ctx, projectID, "replica-"+c.Name); err == nil {
		br.Status = meta.StatusActive
		br.ConnString = postgres.FormatConnString(c.Port, "postgres")
		_ = s.Store.UpdateBranch(ctx, br)
	}

	branches, err := s.branchesFromConnector(ctx, projectID, c.Name)
	if err != nil {
		return ConnectorLifecycleResult{}, err
	}
	out := make([]meta.BranchRecord, 0, len(branches))
	for _, b := range branches {
		rec, err := s.resumeBranchBestEffort(ctx, projectID, b.Name)
		if err != nil {
			return ConnectorLifecycleResult{}, fmt.Errorf("resume branch %s: %w", b.Name, err)
		}
		out = append(out, rec)
	}

	fmt.Printf("✓ connector %q resumed (%d branches)\n", c.Name, len(out))
	fmt.Println("  ", postgres.FormatConnString(c.Port, "postgres"))
	fmt.Println("  ", postgres.PsqlOneLiner(c.Port))
	return ConnectorLifecycleResult{
		Connector: c,
		Branches:  out,
		Message:   fmt.Sprintf("started connector %q and %d branch(es)", c.Name, len(out)),
	}, nil
}

func (s *Service) branchesFromConnector(ctx context.Context, projectID, connectorName string) ([]meta.BranchRecord, error) {
	list, err := s.Store.ListBranches(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var out []meta.BranchRecord
	for _, b := range list {
		if b.Role != "branch" {
			continue
		}
		if b.SourceConnector == connectorName {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *Service) suspendBranchBestEffort(ctx context.Context, projectID, name string) (meta.BranchRecord, error) {
	rec, err := s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return meta.BranchRecord{}, fmt.Errorf("branch_not_found")
	}
	if rec.Status == meta.StatusIdle {
		return rec, nil
	}
	if rec.Status != meta.StatusActive && rec.Status != meta.StatusError {
		return rec, nil
	}
	unlock := s.lockBranch(name)
	defer unlock()
	// re-read under lock
	rec, err = s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	if rec.Status == meta.StatusIdle {
		return rec, nil
	}
	h := compute.Handle{Provider: s.Compute.Name(), Name: rec.Name, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID}
	_ = s.Compute.Stop(ctx, h)
	rec.Status = meta.StatusIdle
	rec.ContainerID = ""
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) resumeBranchBestEffort(ctx context.Context, projectID, name string) (meta.BranchRecord, error) {
	rec, err := s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return meta.BranchRecord{}, fmt.Errorf("branch_not_found")
	}
	if rec.Status == meta.StatusActive {
		return rec, nil
	}
	if rec.Status != meta.StatusIdle {
		return rec, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}
	unlock := s.lockBranch(name)
	defer unlock()
	rec, err = s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	if rec.Status == meta.StatusActive {
		return rec, nil
	}
	started, err := s.Compute.Start(ctx, compute.Spec{
		Name: rec.Name, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(rec.Name),
	})
	if err != nil {
		return meta.BranchRecord{}, err
	}
	rec.ContainerID = started.ContainerID
	rec.Status = meta.StatusActive
	rec.ConnString = postgres.FormatConnString(rec.Port, "postgres")
	inst := &postgres.Instance{Name: rec.Name, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(rec.Name), Bins: s.Bins}
	_ = inst.EnsureAppRoles()
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}
