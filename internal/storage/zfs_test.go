package storage

import "testing"

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
