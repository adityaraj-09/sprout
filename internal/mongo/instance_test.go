package mongo

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStopReturnsImmediatelyWhenNotRunning(t *testing.T) {
	inst := &Instance{DataDir: t.TempDir(), Port: 1}
	start := time.Now()
	if err := inst.Stop(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Stop on a dead instance took %s", time.Since(start))
	}
}

func TestWaitDeadEmptyDir(t *testing.T) {
	inst := &Instance{DataDir: filepath.Join(t.TempDir(), "db"), Port: 0}
	if err := inst.waitDead(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
