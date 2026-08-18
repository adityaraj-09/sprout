package storage

import (
	"fmt"
	"reflect"
	"testing"
)

func TestIsZFSBusy(t *testing.T) {
	if isZFSBusy(nil) {
		t.Fatal("nil")
	}
	if !isZFSBusy(fmt.Errorf("zfs set mountpoint=/x ds: exit status 255 (cannot unmount '/old': pool or dataset is busy)")) {
		t.Fatal("expected busy")
	}
	if isZFSBusy(fmt.Errorf("dataset already exists")) {
		t.Fatal("not busy")
	}
}

func TestParseDependentClones(t *testing.T) {
	msg := "zfs destroy -r sprout/data/replica-mongo-adityaraj-09: exit status 1 (cannot destroy 'sprout/data/replica-mongo-adityaraj-09': filesystem has dependent clones\nuse '-R' to destroy the following datasets:\nsprout/data/branch-mango-adityaraj-09-mongo)"
	got := parseDependentClones(msg)
	if len(got) != 1 || got[0] != "sprout/data/branch-mango-adityaraj-09-mongo" {
		t.Fatalf("got %#v", got)
	}
	if parseDependentClones("dataset is busy") != nil {
		t.Fatal("busy is not clones")
	}
}

func TestParseZFSOrigins(t *testing.T) {
	out := "" +
		"sprout/data\t-\n" +
		"sprout/data/replica-mongo-adityaraj-09\t-\n" +
		"sprout/data/branch-mango-adityaraj-09-mongo\tsprout/data/replica-mongo-adityaraj-09@mango-adityaraj-09-mongo\n" +
		"sprout/data/branch-other\tsprout/data/replica-pg@x\n"
	got := parseZFSOrigins(out, "sprout/data/replica-mongo-adityaraj-09")
	if len(got) != 1 || got[0] != "sprout/data/branch-mango-adityaraj-09-mongo" {
		t.Fatalf("got %#v", got)
	}
}

func TestSanitizeZFSSnapName(t *testing.T) {
	if got := sanitizeZFSSnapName("feat-adityaraj-09-mongo"); got != "feat-adityaraj-09-mongo" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeZFSSnapName("feat/mongo@bad"); got != "feat-mongo-bad" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeZFSSnapName("@@@"); got != "snap" {
		t.Fatalf("got %q", got)
	}
}

func TestParseZFSListLongestPrefix(t *testing.T) {
	out := "" +
		"sprout/data\t/home/u/sprout-data\n" +
		"sprout/data/replica-sup\t/home/u/sprout-data/replicas/sup\n" +
		"sprout/data/main\t/home/u/sprout-data/main\n" +
		"other/pool\t-\n"

	name, mp := parseZFSList(out, "/home/u/sprout-data/replicas/sup")
	if name != "sprout/data/replica-sup" || mp != "/home/u/sprout-data/replicas/sup" {
		t.Fatalf("exact replica: got %s (%s)", name, mp)
	}

	name, mp = parseZFSList(out, "/home/u/sprout-data/replicas/sup/base")
	if name != "sprout/data/replica-sup" {
		t.Fatalf("nested file should still map to replica dataset, got %s (%s)", name, mp)
	}

	name, mp = parseZFSList(out, "/home/u/sprout-data/branches/feat")
	if name != "sprout/data" || mp != "/home/u/sprout-data" {
		t.Fatalf("unscoped branch dir should fall back to parent dataset, got %s (%s)", name, mp)
	}
}

func TestChildDatasetNames(t *testing.T) {
	z := &ZFS{datasetRoot: "sprout/data", dataRoot: "/home/u/sprout-data"}
	cases := map[string]string{
		"/home/u/sprout-data/main":            "sprout/data/main",
		"/home/u/sprout-data/replicas/sup":    "sprout/data/replica-sup",
		"/home/u/sprout-data/branches/feat-a": "sprout/data/branch-feat-a",
	}
	for path, want := range cases {
		if got := z.childDataset(path); got != want {
			t.Errorf("childDataset(%s)=%s want %s", path, got, want)
		}
	}
}

func TestZFSExecSpec(t *testing.T) {
	name, args := zfsExecSpec("/usr/sbin/zfs", []string{"snapshot", "sprout/data@x"}, false)
	if name != "/usr/sbin/zfs" || !reflect.DeepEqual(args, []string{"snapshot", "sprout/data@x"}) {
		t.Fatalf("direct command: %q %#v", name, args)
	}
	name, args = zfsExecSpec("/usr/sbin/zfs", []string{"snapshot", "sprout/data@x"}, true)
	want := []string{"-n", "/usr/sbin/zfs", "snapshot", "sprout/data@x"}
	if name != "sudo" || !reflect.DeepEqual(args, want) {
		t.Fatalf("sudo command: %q %#v", name, args)
	}
}

func TestZFSChownSpec(t *testing.T) {
	owner, args := zfsChownSpec("/usr/bin/chown", 1000, 1001, "/data/replicas/check")
	want := []string{"-n", "/usr/bin/chown", "--", "1000:1001", "/data/replicas/check"}
	if owner != "1000:1001" || !reflect.DeepEqual(args, want) {
		t.Fatalf("chown command: owner=%q args=%#v", owner, args)
	}
}
