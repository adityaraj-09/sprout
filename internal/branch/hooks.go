package branch

import (
	"context"
	"fmt"
	"strings"

	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/progress"
)

func (s *Service) applyBranchSQL(ctx context.Context, rec meta.BranchRecord) error {
	sql := ""
	eng := s.sourceEngine(ctx, rec)
	if rec.SourceConnectorID != "" {
		if c, err := s.Store.GetConnectorByID(ctx, rec.SourceConnectorID); err == nil {
			sql = strings.TrimSpace(c.BranchSQL)
			if c.Engine != "" {
				eng = engine.Normalize(c.Engine)
			}
		}
	}
	if sql == "" {
		return nil
	}
	if engine.IsDumpSnapshot(eng) {
		progress.Printf(ctx, "skip branch_sql on %s (Postgres only)", eng)
		return nil
	}
	if s.Bins.Psql == "" {
		return fmt.Errorf("branch_sql: psql not on PATH")
	}
	progress.Printf(ctx, "running branch_sql (%d bytes)", len(sql))
	inst := &postgres.Instance{
		Name: rec.Name, Source: rec.SourceConnector, Owner: rec.CreatedBy,
		DataDir: rec.DataDir, Port: rec.Port, Bins: s.Bins, Password: rec.Password,
	}
	_, err := inst.ExecSQLScript("postgres", sql)
	if err != nil {
		return err
	}
	return nil
}

// SetConnectorHook stores SQL that runs on every new Postgres branch from this connector.
func (s *Service) SetConnectorHook(ctx context.Context, projectID, name, sql string) (meta.Connector, error) {
	if err := s.requireOrgOwner(ctx); err != nil {
		return meta.Connector{}, err
	}
	c, err := s.lookupConnector(ctx, projectID, name)
	if err != nil {
		return meta.Connector{}, err
	}
	c.BranchSQL = sql
	if err := s.Store.UpdateConnector(ctx, c); err != nil {
		return meta.Connector{}, err
	}
	return c, nil
}
