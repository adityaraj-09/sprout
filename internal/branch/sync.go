package branch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/progress"
	"github.com/adityaraj/sprout/internal/replica"
)

// SyncResult is returned by sprout sync / the hourly apply ticker.
type SyncResult struct {
	Connector meta.Connector `json:"connector"`
	Lag       replica.Lag    `json:"lag"`
	Reason    string         `json:"reason,omitempty"` // manual | scheduled
	Message   string         `json:"message,omitempty"`
}

// Sync enables the logical subscription, applies queued WAL, then disables apply
// again so the prod slot is kept without a 24/7 apply worker.
func (s *Service) Sync(ctx context.Context, projectID, name string) (SyncResult, error) {
	if err := s.requireOrgOwner(ctx); err != nil {
		return SyncResult{}, err
	}
	c, err := s.resolveConnector(ctx, projectID, name)
	if err != nil {
		return SyncResult{}, err
	}
	return s.applyConnector(ctx, c, "manual")
}

func (s *Service) applyConnector(ctx context.Context, c meta.Connector, reason string) (SyncResult, error) {
	unlock, err := s.lockBranch(ctx, s.connectorLockKey(c.Name, c.CreatedBy))
	if err != nil {
		return SyncResult{}, err
	}
	defer unlock()

	// Same snap lock as branch create so we do not snapshot mid-apply.
	snapUnlock, err := s.lockBranch(ctx, "snap:"+postgres.ReplicaComputeName(c.Name, c.CreatedBy))
	if err != nil {
		return SyncResult{}, err
	}
	defer snapUnlock()

	fresh, err := s.Store.GetConnectorByID(ctx, c.ID)
	if err == nil {
		c = fresh
	}

	if engine.IsMongo(c.Engine) {
		return SyncResult{}, fmt.Errorf("unsupported: mongodb has no incremental apply — reconnect with --wipe to refresh the snapshot")
	}

	switch c.Status {
	case meta.ConnectorIdle:
		return SyncResult{}, fmt.Errorf("invalid_state: connector %q is suspended — resume it first", c.Name)
	case meta.ConnectorBootstrapping:
		return SyncResult{}, fmt.Errorf("invalid_state: connector %q is still bootstrapping", c.Name)
	case meta.ConnectorError:
		return SyncResult{}, fmt.Errorf("invalid_state: connector %q is in error: %s", c.Name, c.ErrorMessage)
	}

	if c.Mode == ModePhysical {
		return s.syncPhysicalStatus(ctx, c, reason)
	}

	progress.Printf(ctx, "→ apply logical WAL on %s (%s)", c.Name, reason)
	rm := &replica.Manager{Bins: s.Bins}
	h := s.connectorHandle(c)
	running, _ := s.Compute.IsRunning(ctx, h)
	if !running {
		if _, err := s.Compute.Start(ctx, compute.Spec{
			Name: h.Name, DataDir: c.DataDir, Port: c.Port, LogFile: s.logPath(h.Name),
		}); err != nil {
			return SyncResult{}, fmt.Errorf("compute_failed: %w", err)
		}
	}

	ok, err := rm.HasLocalSubscription(ctx, "127.0.0.1", c.Port, subName(c))
	if err != nil {
		return SyncResult{}, err
	}
	if !ok {
		return SyncResult{}, fmt.Errorf("no_subscription: %q is a detached snapshot — only the live logical connector can incremental-sync", c.Name)
	}

	if err := rm.SetSubscriptionEnabled(ctx, "127.0.0.1", c.Port, subName(c), true); err != nil {
		return SyncResult{}, err
	}
	st, err := rm.WaitLogicalCatchUp(ctx, "127.0.0.1", c.Port, subName(c), 10*time.Minute)
	if err != nil {
		_ = rm.SetSubscriptionEnabled(ctx, "127.0.0.1", c.Port, subName(c), false)
		return SyncResult{}, err
	}
	if err := rm.SetSubscriptionEnabled(ctx, "127.0.0.1", c.Port, subName(c), false); err != nil {
		return SyncResult{}, err
	}

	c.LastLSN = st.ReceivedLSN
	c.LastSyncedAt = time.Now().UTC()
	c.ApplyPaused = true
	c.Status = meta.ConnectorReplicating
	c.ErrorMessage = ""
	_ = s.Store.UpdateConnector(ctx, c)
	lag := replica.Lag{ReceiveLSN: st.ReceivedLSN, ReplayLSN: st.ReceivedLSN}
	fmt.Printf("✓ synced %q lsn=%s apply_paused=true\n", c.Name, c.LastLSN)
	return SyncResult{
		Connector: c,
		Lag:       lag,
		Reason:    reason,
		Message:   "applied queued WAL; subscription disabled; publisher slot kept",
	}, nil
}

func (s *Service) syncPhysicalStatus(ctx context.Context, c meta.Connector, reason string) (SyncResult, error) {
	rm := &replica.Manager{Bins: s.Bins}
	lag, err := rm.Status(ctx, "127.0.0.1", c.Port)
	if err != nil {
		return SyncResult{}, err
	}
	c.LastLSN = lag.ReplayLSN
	c.LastLagBytes = lag.LagBytes
	c.LastSyncedAt = time.Now().UTC()
	c.ApplyPaused = false
	_ = s.Store.UpdateConnector(ctx, c)
	return SyncResult{
		Connector: c,
		Lag:       lag,
		Reason:    reason,
		Message:   "physical connectors apply continuously; recorded current replay LSN",
	}, nil
}

func dueForScheduledApply(c meta.Connector, now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	if c.Mode != ModeLogical || engine.IsMongo(c.Engine) {
		return false
	}
	switch c.Status {
	case meta.ConnectorIdle, meta.ConnectorError, meta.ConnectorBootstrapping, meta.ConnectorDisconnected:
		return false
	}
	if c.LastSyncedAt.IsZero() {
		return true
	}
	return !now.Before(c.LastSyncedAt.Add(interval))
}

// RunScheduledSync applies due logical connectors. Skips a connector if it is locked.
func (s *Service) RunScheduledSync(ctx context.Context) {
	if s.SyncInterval <= 0 {
		return
	}
	list, err := s.Store.ListConnectors(ctx)
	if err != nil {
		fmt.Printf("scheduled sync: list connectors: %v\n", err)
		return
	}
	now := time.Now().UTC()
	for _, c := range list {
		if !dueForScheduledApply(c, now, s.SyncInterval) {
			continue
		}
		wait, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
		unlock, err := s.lockBranch(wait, s.connectorLockKey(c.Name, c.CreatedBy))
		cancel()
		if err != nil {
			fmt.Printf("scheduled sync: skip %s (busy)\n", c.Name)
			continue
		}
		unlock()
		fmt.Printf("→ scheduled apply %s (every %s)\n", c.Name, s.SyncInterval)
		if _, err := s.applyConnector(ctx, c, "scheduled"); err != nil {
			if strings.HasPrefix(err.Error(), "no_subscription") {
				continue
			}
			fmt.Printf("scheduled sync: %s: %v\n", c.Name, err)
		}
	}
}

// SyncLoop ticks often enough to honor SyncInterval (at least every minute).
func (s *Service) SyncLoop(ctx context.Context) {
	if s.SyncInterval <= 0 {
		return
	}
	every := time.Minute
	if s.SyncInterval < 2*time.Minute {
		every = s.SyncInterval / 4
		if every < 15*time.Second {
			every = 15 * time.Second
		}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	s.RunScheduledSync(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunScheduledSync(ctx)
		}
	}
}
