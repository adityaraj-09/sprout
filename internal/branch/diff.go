package branch

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// BranchDiff summarizes schema + row-count changes vs the parent connector/main.
type BranchDiff struct {
	Branch  string         `json:"branch"`
	Parent  string         `json:"parent"`
	Schema  SchemaDiff     `json:"schema"`
	Rows    []TableRowDiff `json:"rows"`
	Summary string         `json:"summary"`
}

type SchemaDiff struct {
	OnlyOnBranch []string            `json:"only_on_branch"`
	OnlyOnParent []string            `json:"only_on_parent"`
	Changed      []ColumnChange      `json:"changed_columns"`
	Tables       map[string][]string `json:"tables,omitempty"` // branch table → columns
}

type ColumnChange struct {
	Table   string   `json:"table"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

type TableRowDiff struct {
	Table      string `json:"table"`
	BranchRows int64  `json:"branch_rows"`
	ParentRows int64  `json:"parent_rows"`
	Delta      int64  `json:"delta"`
}

// DiffBranch compares a branch against its source connector (or main).
func (s *Service) DiffBranch(ctx context.Context, projectID, name, from string) (BranchDiff, error) {
	rec, err := s.lookupBranch(ctx, projectID, name, from)
	if err != nil || rec.Role != "branch" {
		if err != nil && strings.HasPrefix(err.Error(), "ambiguous_branch") {
			return BranchDiff{}, err
		}
		return BranchDiff{}, fmt.Errorf("branch_not_found")
	}
	parentDir, parentPort, parentName, _, err := s.resolveBranchSource(ctx, projectID, rec.SourceConnector)
	if err != nil {
		// Fall back to recorded source even if resolve fails naming.
		if rec.SourceConnector == "" {
			return BranchDiff{}, err
		}
		c, cerr := s.Store.GetConnectorByName(ctx, projectID, rec.SourceConnector, rec.CreatedBy)
		if cerr != nil {
			return BranchDiff{}, err
		}
		parentDir, parentPort, parentName, _, err = s.connectorSource(c)
		if err != nil {
			return BranchDiff{}, err
		}
	}
	_ = parentDir

	branchSchema, err := listSchema(ctx, s.Bins.Psql, "127.0.0.1", rec.Port)
	if err != nil {
		return BranchDiff{}, fmt.Errorf("branch schema: %w", err)
	}
	parentSchema, err := listSchema(ctx, s.Bins.Psql, "127.0.0.1", parentPort)
	if err != nil {
		return BranchDiff{}, fmt.Errorf("parent schema: %w", err)
	}
	branchRows, err := listRowCounts(ctx, s.Bins.Psql, "127.0.0.1", rec.Port)
	if err != nil {
		return BranchDiff{}, fmt.Errorf("branch rows: %w", err)
	}
	parentRows, err := listRowCounts(ctx, s.Bins.Psql, "127.0.0.1", parentPort)
	if err != nil {
		return BranchDiff{}, fmt.Errorf("parent rows: %w", err)
	}

	sd := SchemaDiff{Tables: branchSchema}
	for t := range branchSchema {
		if _, ok := parentSchema[t]; !ok {
			sd.OnlyOnBranch = append(sd.OnlyOnBranch, t)
		}
	}
	for t := range parentSchema {
		if _, ok := branchSchema[t]; !ok {
			sd.OnlyOnParent = append(sd.OnlyOnParent, t)
		}
	}
	for t, bcols := range branchSchema {
		pcols, ok := parentSchema[t]
		if !ok {
			continue
		}
		added, removed := diffSets(bcols, pcols)
		if len(added) > 0 || len(removed) > 0 {
			sd.Changed = append(sd.Changed, ColumnChange{Table: t, Added: added, Removed: removed})
		}
	}

	allTables := map[string]struct{}{}
	for t := range branchRows {
		allTables[t] = struct{}{}
	}
	for t := range parentRows {
		allTables[t] = struct{}{}
	}
	var rows []TableRowDiff
	var changedTables int
	for t := range allTables {
		br := branchRows[t]
		pr := parentRows[t]
		d := br - pr
		if d != 0 {
			changedTables++
		}
		rows = append(rows, TableRowDiff{Table: t, BranchRows: br, ParentRows: pr, Delta: d})
	}
	// stable-ish order
	sortTableRowDiff(rows)

	summary := fmt.Sprintf("vs %s: +%d tables only on branch, +%d only on parent, %d column diffs, %d tables with row delta",
		parentName, len(sd.OnlyOnBranch), len(sd.OnlyOnParent), len(sd.Changed), changedTables)

	return BranchDiff{
		Branch:  name,
		Parent:  parentName,
		Schema:  sd,
		Rows:    rows,
		Summary: summary,
	}, nil
}

func listSchema(ctx context.Context, psql, host string, port int) (map[string][]string, error) {
	sql := `
SELECT table_name || E'\t' || column_name || ':' || data_type
FROM information_schema.columns
WHERE table_schema = 'public'
ORDER BY table_name, ordinal_position;
`
	out, err := runPsql(ctx, psql, host, port, sql)
	if err != nil {
		return nil, err
	}
	m := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		m[parts[0]] = append(m[parts[0]], parts[1])
	}
	return m, nil
}

func listRowCounts(ctx context.Context, psql, host string, port int) (map[string]int64, error) {
	sql := `
SELECT c.relname::text || E'\t' || COALESCE(s.n_live_tup, 0)::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE n.nspname = 'public' AND c.relkind = 'r'
ORDER BY 1;
`
	out, err := runPsql(ctx, psql, host, port, sql)
	if err != nil {
		return nil, err
	}
	m := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		m[parts[0]] = n
	}
	return m, nil
}

func runPsql(ctx context.Context, psql, host string, port int, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, psql,
		"-h", host, "-p", strconv.Itoa(port), "-d", "postgres",
		"-v", "ON_ERROR_STOP=1", "-t", "-A", "-c", sql,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func diffSets(branch, parent []string) (added, removed []string) {
	bm := map[string]struct{}{}
	pm := map[string]struct{}{}
	for _, c := range branch {
		bm[c] = struct{}{}
	}
	for _, c := range parent {
		pm[c] = struct{}{}
	}
	for c := range bm {
		if _, ok := pm[c]; !ok {
			added = append(added, c)
		}
	}
	for c := range pm {
		if _, ok := bm[c]; !ok {
			removed = append(removed, c)
		}
	}
	return added, removed
}

func sortTableRowDiff(rows []TableRowDiff) {
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].Table < rows[i].Table {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
}
