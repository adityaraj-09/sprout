package meta

import (
	"fmt"
	"strings"
)

// ResolveBranch picks one branch from same-name candidates.
// from empty: unique match, or ambiguous_branch if two connectors share the label.
// from set: match source_connector exactly.
func ResolveBranch(name, from string, matches []BranchRecord) (BranchRecord, error) {
	if from != "" {
		filtered := make([]BranchRecord, 0, len(matches))
		for _, b := range matches {
			if b.SourceConnector == from {
				filtered = append(filtered, b)
			}
		}
		matches = filtered
	}
	switch len(matches) {
	case 0:
		return BranchRecord{}, fmt.Errorf("branch not found")
	case 1:
		return matches[0], nil
	default:
		srcs := make([]string, 0, len(matches))
		seen := map[string]bool{}
		for _, b := range matches {
			src := b.SourceConnector
			if src == "" {
				src = "main"
			}
			if seen[src] {
				continue
			}
			seen[src] = true
			srcs = append(srcs, src)
		}
		return BranchRecord{}, fmt.Errorf("ambiguous_branch: %q exists from %s — pass --from=<connector>", name, strings.Join(srcs, ", "))
	}
}
