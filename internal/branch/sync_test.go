package branch

import (
	"testing"
	"time"

	"github.com/adityaraj/sprout/internal/meta"
)

func TestDueForScheduledApply(t *testing.T) {
	now := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	interval := time.Hour
	live := meta.Connector{Mode: ModeLogical, Engine: "postgres", Status: meta.ConnectorReplicating}

	if dueForScheduledApply(live, now, 0) {
		t.Fatal("interval 0 disables the ticker")
	}
	if !dueForScheduledApply(live, now, interval) {
		t.Fatal("never-synced logical connector is due")
	}

	live.LastSyncedAt = now.Add(-30 * time.Minute)
	if dueForScheduledApply(live, now, interval) {
		t.Fatal("synced 30m ago is not due for 1h interval")
	}
	live.LastSyncedAt = now.Add(-time.Hour)
	if !dueForScheduledApply(live, now, interval) {
		t.Fatal("synced exactly 1h ago is due")
	}

	idle := live
	idle.Status = meta.ConnectorIdle
	if dueForScheduledApply(idle, now, interval) {
		t.Fatal("suspended connector is skipped")
	}

	phys := live
	phys.Mode = ModePhysical
	if dueForScheduledApply(phys, now, interval) {
		t.Fatal("physical is always-on, not scheduled")
	}

	mongo := live
	mongo.Engine = "mongodb"
	if dueForScheduledApply(mongo, now, interval) {
		t.Fatal("mongo has no incremental apply")
	}

	qd := live
	qd.Engine = "qdrant"
	if dueForScheduledApply(qd, now, interval) {
		t.Fatal("qdrant has no incremental apply")
	}
}
