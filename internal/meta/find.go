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

// FilterBranchesByOwner returns branches visible to owner.
// Empty owner (machine token) matches everything, including unowned pre-GitHub rows.
func FilterBranchesByOwner(owner string, list []BranchRecord) []BranchRecord {
	if owner == "" {
		return list
	}
	out := make([]BranchRecord, 0, len(list))
	for _, b := range list {
		if b.CreatedBy == owner {
			out = append(out, b)
		}
	}
	return out
}

// FilterConnectorsByOwner returns connectors visible to owner.
func FilterConnectorsByOwner(owner string, list []Connector) []Connector {
	if owner == "" {
		return list
	}
	out := make([]Connector, 0, len(list))
	for _, c := range list {
		if c.CreatedBy == owner {
			out = append(out, c)
		}
	}
	return out
}

func resolveConnector(name, owner string, list []Connector) (Connector, error) {
	if owner != "" {
		list = FilterConnectorsByOwner(owner, list)
		if len(list) == 0 {
			return Connector{}, fmt.Errorf("connector not found: %s", name)
		}
		if len(list) == 1 {
			return list[0], nil
		}
		return Connector{}, fmt.Errorf("ambiguous_connector: %q", name)
	}
	if len(list) == 0 {
		return Connector{}, fmt.Errorf("connector not found: %s", name)
	}
	unowned := make([]Connector, 0)
	for _, c := range list {
		if c.CreatedBy == "" {
			unowned = append(unowned, c)
		}
	}
	if len(unowned) == 1 {
		return unowned[0], nil
	}
	if len(list) == 1 {
		return list[0], nil
	}
	return Connector{}, fmt.Errorf("ambiguous_connector: %q exists for multiple users — use a GitHub login", name)
}
