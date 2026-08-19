package branch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/mongo"
	"github.com/adityaraj/sprout/internal/postgres"
)

// DoctorCheck is one health/DX check result.
type DoctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
	Level  string `json:"level"` // info | warn | error
}

// DoctorReport aggregates environment readiness.
type DoctorReport struct {
	OK     bool          `json:"ok"`
	Checks []DoctorCheck `json:"checks"`
}

// Doctor inspects binaries, storage, networking, and control-plane readiness.
func (s *Service) Doctor(ctx context.Context) DoctorReport {
	var checks []DoctorCheck
	add := func(c DoctorCheck) { checks = append(checks, c) }

	// Binaries
	bins := []struct{ name, path string }{
		{"initdb", s.Bins.InitDB},
		{"postgres", s.Bins.Postgres},
		{"psql", s.Bins.Psql},
		{"pg_dump", findOnPath("pg_dump")},
		{"pg_basebackup", s.Bins.PgBaseBackup},
	}
	for _, b := range bins {
		if b.path == "" {
			add(DoctorCheck{Name: "bin:" + b.name, OK: false, Level: "error",
				Detail: "not found on PATH",
				Hint:   "install PostgreSQL client/server matching your primary major (e.g. postgresql-17 / postgresql-client-17)"})
			continue
		}
		major, err := postgres.ClientMajor(b.path)
		detail := b.path
		if err == nil {
			detail = fmt.Sprintf("%s (PG%d)", b.path, major)
		}
		add(DoctorCheck{Name: "bin:" + b.name, OK: true, Level: "info", Detail: detail})
	}
	for _, name := range []string{"mongod", "mongodump", "mongorestore", "mongosh"} {
		p, err := exec.LookPath(name)
		if err != nil {
			add(DoctorCheck{Name: "bin:" + name, OK: true, Level: "info",
				Detail: "not found (optional; needed for --engine=mongodb)",
				Hint:   "install MongoDB server and database tools"})
			continue
		}
		add(DoctorCheck{Name: "bin:" + name, OK: true, Level: "info", Detail: p})
	}

	// Storage / compute
	add(DoctorCheck{Name: "storage", OK: true, Level: "info", Detail: s.Storage.Name(),
		Hint: storageHint(s.Storage.Name())})
	add(DoctorCheck{Name: "compute", OK: true, Level: "info", Detail: s.Compute.Name()})

	// Data root writable
	root := s.Root
	if err := os.MkdirAll(root, 0o755); err != nil {
		add(DoctorCheck{Name: "data_root", OK: false, Level: "error", Detail: err.Error(), Hint: "fix SPROUT_DATA permissions"})
	} else {
		probe := filepath.Join(root, ".sprout-doctor-write")
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			add(DoctorCheck{Name: "data_root", OK: false, Level: "error", Detail: root + ": " + err.Error(),
				Hint: "ZFS/mount may be root-owned — chown the dataset to the sprout user"})
		} else {
			_ = os.Remove(probe)
			add(DoctorCheck{Name: "data_root", OK: true, Level: "info", Detail: root})
		}
	}

	// Public host / listen / auth
	host := postgres.PublicHost()
	listen := postgres.ListenAddresses()
	remote := postgres.RemoteAccess()
	add(DoctorCheck{Name: "public_host", OK: true, Level: "info",
		Detail: fmt.Sprintf("SPROUT_PUBLIC_HOST=%s listen_addresses=%s db_user=%s subdomain=%v", host, listen, postgres.DBUser(), postgres.BranchSubdomain())})
	if postgres.BranchSubdomain() {
		if postgres.ProxyEnabled() || mongo.ProxyEnabled() {
			detail := fmt.Sprintf("hostnames are <name>-<owner>-<connector>.%s", host)
			if postgres.ProxyEnabled() {
				detail += fmt.Sprintf("; Postgres :%d", postgres.ProxyPort())
			}
			if mongo.ProxyEnabled() {
				detail += fmt.Sprintf("; Mongo :%d", mongo.ProxyPort())
			}
			add(DoctorCheck{Name: "dns", OK: true, Level: "warn",
				Detail: detail,
				Hint:   "wildcard A/AAAA for *." + host + " → this VM; setcap cap_net_bind_service=+ep if bind fails"})
		} else {
			add(DoctorCheck{Name: "dns", OK: true, Level: "warn",
				Detail: fmt.Sprintf("branch URLs use <name>-<owner>-<connector>.%s:<port>", host),
				Hint:   "create a wildcard A/AAAA record for *." + host + " pointing at this VM"})
		}
	}
	if postgres.ProxyEnabled() {
		add(DoctorCheck{Name: "pg_proxy", OK: true, Level: "info",
			Detail: fmt.Sprintf("SNI proxy :%d → %s:<instance port>", postgres.ProxyPort(), postgres.ProxyBackendHost()),
			Hint:   "clients connect on 5432; TLS SNI selects test-x vs test-y. Self-signed cert in $SPROUT_DATA/tls unless SPROUT_TLS_CERT is set"})
	}
	if mongo.ProxyEnabled() {
		add(DoctorCheck{Name: "mongo_proxy", OK: true, Level: "info",
			Detail: fmt.Sprintf("Mongo SNI passthrough :%d → %s:<instance port>", mongo.ProxyPort(), mongo.ProxyBackendHost()),
			Hint:   "clients connect on 27017 with tls=true; hostname selects the mongod. Unique ports stay on loopback"})
	}
	if postgres.ProxyEnabled() || mongo.ProxyEnabled() {
		hint := "open NSG/security group for 8080 (API)"
		if postgres.ProxyEnabled() {
			hint += ", 5432 (Postgres)"
		}
		if mongo.ProxyEnabled() {
			hint += ", 27017 (Mongo)"
		}
		add(DoctorCheck{Name: "firewall", OK: true, Level: "warn",
			Detail: "public database ports are SNI proxies; instance ports stay on loopback",
			Hint:   hint})
	} else if remote {
		if postgres.TrustRemote() {
			if os.Getenv("SPROUT_SAFE") != "true" {
				add(DoctorCheck{Name: "auth", OK: false, Level: "error",
					Detail: "remote Postgres uses trust auth (SPROUT_TRUST_REMOTE=true)",
					Hint:   "unset SPROUT_TRUST_REMOTE to use SCRAM, or set SPROUT_SAFE=true only on a locked-down lab"})
			} else {
				add(DoctorCheck{Name: "auth", OK: true, Level: "warn",
					Detail: "remote Postgres uses trust auth; SPROUT_SAFE=true",
					Hint:   "lock NSG to your IPs; prefer SCRAM (default) for anything real"})
			}
		} else {
			add(DoctorCheck{Name: "auth", OK: true, Level: "info",
				Detail: "remote auth=scram-sha-256 (loopback still trust)",
				Hint:   "connection strings include a generated password; set SPROUT_TRUST_REMOTE=true only for labs"})
		}
		add(DoctorCheck{Name: "firewall", OK: true, Level: "warn",
			Detail: "Postgres accepts remote TCP",
			Hint:   "open NSG/security group for branch ports (typically 55432-55500)"})
	} else {
		add(DoctorCheck{Name: "firewall", OK: true, Level: "info",
			Detail: "Postgres bound to loopback only",
			Hint:   "set SPROUT_PUBLIC_HOST=<vm-ip> and open firewall to reach branches remotely"})
	}

	// Control store
	if _, err := s.Store.ListProjects(ctx); err != nil {
		add(DoctorCheck{Name: "control_plane", OK: false, Level: "error", Detail: err.Error()})
	} else {
		add(DoctorCheck{Name: "control_plane", OK: true, Level: "info", Detail: "readable"})
	}

	s.doctorInventory(ctx, add)

	// GitHub device-flow login
	gh := auth.FromEnv()
	switch {
	case !gh.Enabled():
		add(DoctorCheck{Name: "github_auth", OK: true, Level: "info",
			Detail: "off (shared SPROUT_TOKEN only)",
			Hint:   "set SPROUT_GITHUB_CLIENT_ID so anyone with GitHub can sprout login"})
	case gh.Restricted():
		add(DoctorCheck{Name: "github_auth", OK: true, Level: "info",
			Detail: fmt.Sprintf("device flow (allowlist users=%d orgs=%d) host=%s", len(gh.Users), len(gh.Orgs), gh.HostURL()),
			Hint:   "unset SPROUT_GITHUB_USERS / SPROUT_GITHUB_ORGS to allow any GitHub user"})
	default:
		add(DoctorCheck{Name: "github_auth", OK: true, Level: "info",
			Detail: fmt.Sprintf("device flow public (any GitHub user) host=%s", gh.HostURL()),
			Hint:   "Enable Device Flow on the GitHub OAuth App; optional SPROUT_GITHUB_USERS / SPROUT_GITHUB_ORGS to restrict"})
	}

	// OS hint
	add(DoctorCheck{Name: "runtime", OK: true, Level: "info",
		Detail: fmt.Sprintf("%s/%s go=%s", runtime.GOOS, runtime.GOARCH, runtime.Version())})

	ok := true
	for _, c := range checks {
		if !c.OK && c.Level == "error" {
			ok = false
		}
	}
	return DoctorReport{OK: ok, Checks: checks}
}

func findOnPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

func storageHint(name string) string {
	switch name {
	case "copy":
		return "copy storage is correct for Azure without ZFS/APFS — branches are slower (full cp -a)"
	case "zfs":
		return "ZFS CoW: each main/replica/branch is a child dataset; set SPROUT_STORAGE=copy to force full copies"
	case "apfs":
		return "APFS CoW clones enabled (macOS)"
	default:
		return ""
	}
}

const hungAfter = 10 * time.Minute

func (s *Service) doctorInventory(ctx context.Context, add func(DoctorCheck)) {
	orgs, err := s.Store.ListOrgs(ctx, auth.OwnerFrom(ctx))
	if err != nil {
		add(DoctorCheck{Name: "orgs", OK: false, Level: "warn", Detail: err.Error()})
	} else {
		add(DoctorCheck{Name: "orgs", OK: true, Level: "info",
			Detail: fmt.Sprintf("%d visible", len(orgs))})
	}

	cons, err := s.ListConnectors(ctx)
	if err != nil {
		add(DoctorCheck{Name: "connectors", OK: false, Level: "warn", Detail: err.Error()})
		cons = nil
	} else {
		add(DoctorCheck{Name: "connectors", OK: true, Level: "info",
			Detail: fmt.Sprintf("%d configured (%s)", len(cons), countByStatus(connectorStatuses(cons)))})
	}

	allBranches, err := s.Store.ListAllBranches(ctx)
	if err != nil {
		add(DoctorCheck{Name: "branches", OK: false, Level: "warn", Detail: err.Error()})
		allBranches = nil
	}
	branches := s.filterBranchesForActor(ctx, allBranches)
	add(DoctorCheck{Name: "branches", OK: true, Level: "info",
		Detail: fmt.Sprintf("%d configured (%s)", len(branches), countByStatus(branchStatuses(branches)))})

	hung := hungJobs(cons, branches, hungAfter)
	if len(hung) == 0 {
		add(DoctorCheck{Name: "hung_jobs", OK: true, Level: "info", Detail: "none"})
	} else {
		add(DoctorCheck{Name: "hung_jobs", OK: false, Level: "warn",
			Detail: strings.Join(hung, "; "),
			Hint:   "creating/deleting/bootstrapping older than 10m — inspect logs and retry or delete"})
	}

	root := s.Root
	size, newest, walkErr := dirStats(root)
	if walkErr != nil {
		add(DoctorCheck{Name: "disk", OK: false, Level: "warn", Detail: walkErr.Error()})
	} else {
		add(DoctorCheck{Name: "disk", OK: true, Level: "info",
			Detail: fmt.Sprintf("%s in %s (newest mtime %s)", humanBytes(size), root, newest.UTC().Format(time.RFC3339))})
	}

	known := map[string]struct{}{}
	for _, c := range cons {
		if c.DataDir != "" {
			known[filepath.Clean(c.DataDir)] = struct{}{}
		}
	}
	for _, b := range branches {
		if b.DataDir != "" {
			known[filepath.Clean(b.DataDir)] = struct{}{}
		}
	}

	var missing []string
	now := time.Now()
	for _, c := range cons {
		if c.DataDir == "" {
			continue
		}
		st, err := os.Stat(c.DataDir)
		if err != nil {
			missing = append(missing, fmt.Sprintf("connector %s:%s", c.Name, c.DataDir))
			continue
		}
		age := now.Sub(st.ModTime()).Truncate(time.Second)
		add(DoctorCheck{Name: "data_dir:" + c.Name, OK: true, Level: "info",
			Detail: fmt.Sprintf("connector %s age=%s status=%s", c.DataDir, age, c.Status)})
	}
	for _, b := range branches {
		if b.DataDir == "" || b.Role != "branch" {
			continue
		}
		st, err := os.Stat(b.DataDir)
		if err != nil {
			missing = append(missing, fmt.Sprintf("branch %s:%s", b.Name, b.DataDir))
			continue
		}
		age := now.Sub(st.ModTime()).Truncate(time.Second)
		add(DoctorCheck{Name: "data_dir:" + b.Name, OK: true, Level: "info",
			Detail: fmt.Sprintf("branch %s age=%s status=%s", b.DataDir, age, b.Status)})
	}

	var orphans []string
	for _, sub := range []string{"replicas", "branches"} {
		entries, err := os.ReadDir(filepath.Join(root, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Clean(filepath.Join(root, sub, e.Name()))
			if _, ok := known[p]; !ok {
				orphans = append(orphans, filepath.Join(sub, e.Name()))
			}
		}
	}
	sort.Strings(missing)
	sort.Strings(orphans)
	switch {
	case len(missing) == 0 && len(orphans) == 0:
		add(DoctorCheck{Name: "disk_mismatch", OK: true, Level: "info", Detail: "control plane matches data dirs"})
	default:
		detail := fmt.Sprintf("missing=%d orphan=%d", len(missing), len(orphans))
		if len(missing) > 0 {
			detail += "; missing: " + strings.Join(missing, ", ")
		}
		if len(orphans) > 0 {
			detail += "; orphan: " + strings.Join(orphans, ", ")
		}
		add(DoctorCheck{Name: "disk_mismatch", OK: true, Level: "warn", Detail: detail,
			Hint: "orphan dirs are not in control.db; missing dirs were recorded but deleted on disk"})
	}
}

func connectorStatuses(list []meta.Connector) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Status)
	}
	return out
}

func branchStatuses(list []meta.BranchRecord) []string {
	out := make([]string, 0, len(list))
	for _, b := range list {
		out = append(out, b.Status)
	}
	return out
}

func countByStatus(statuses []string) string {
	if len(statuses) == 0 {
		return "none"
	}
	n := map[string]int{}
	var keys []string
	for _, s := range statuses {
		if s == "" {
			s = "unknown"
		}
		if n[s] == 0 {
			keys = append(keys, s)
		}
		n[s]++
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, n[k]))
	}
	return strings.Join(parts, " ")
}

func hungJobs(cons []meta.Connector, branches []meta.BranchRecord, maxAge time.Duration) []string {
	now := time.Now()
	var out []string
	for _, c := range cons {
		if !isHungStatus(c.Status) {
			continue
		}
		if now.Sub(c.UpdatedAt) < maxAge || c.UpdatedAt.IsZero() {
			continue
		}
		out = append(out, fmt.Sprintf("connector %s %s for %s", c.Name, c.Status, now.Sub(c.UpdatedAt).Truncate(time.Second)))
	}
	for _, b := range branches {
		if !isHungStatus(b.Status) {
			continue
		}
		if now.Sub(b.UpdatedAt) < maxAge || b.UpdatedAt.IsZero() {
			continue
		}
		out = append(out, fmt.Sprintf("branch %s %s for %s", b.Name, b.Status, now.Sub(b.UpdatedAt).Truncate(time.Second)))
	}
	return out
}

func isHungStatus(status string) bool {
	switch status {
	case meta.StatusCreating, meta.StatusDeleting, meta.StatusResetting, meta.ConnectorBootstrapping:
		return true
	default:
		return false
	}
}

func dirStats(root string) (size int64, newest time.Time, err error) {
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if newest.IsZero() {
		newest = time.Now()
	}
	return size, newest, err
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	f := float64(n)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB"} {
		f /= 1024
		if f < 1024 {
			return fmt.Sprintf("%.1f%s", f, unit)
		}
	}
	return fmt.Sprintf("%.1fPiB", f/1024)
}
