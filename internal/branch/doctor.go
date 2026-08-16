package branch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/adityaraj/sprout/internal/auth"
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

	for _, name := range []string{"mysqld", "mysql", "mysqldump"} {
		p := findOnPath(name)
		if p == "" {
			add(DoctorCheck{Name: "bin:" + name, OK: true, Level: "info",
				Detail: "not found (optional; needed for --engine=mysql)",
				Hint:   "install mysql-server and mysql-client to connect MySQL sources"})
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
		if postgres.ProxyEnabled() {
			add(DoctorCheck{Name: "dns", OK: true, Level: "warn",
				Detail: fmt.Sprintf("URLs use <name>-<owner>-<connector>.%s:5432 (SNI proxy)", host),
				Hint:   "wildcard A/AAAA for *." + host + " → this VM; open NSG 5432; setcap cap_net_bind_service=+ep if bind fails"})
		} else {
			add(DoctorCheck{Name: "dns", OK: true, Level: "warn",
				Detail: fmt.Sprintf("branch URLs use <name>-<owner>-<connector>.%s:<port>", host),
				Hint:   "create a wildcard A/AAAA record for *." + host + " pointing at this VM; SPROUT_PG_PROXY=false keeps unique ports"})
		}
	}
	if postgres.ProxyEnabled() {
		add(DoctorCheck{Name: "pg_proxy", OK: true, Level: "info",
			Detail: fmt.Sprintf("SNI proxy :%d → %s:<instance port>", postgres.ProxyPort(), postgres.ProxyBackendHost()),
			Hint:   "clients connect on 5432; TLS SNI selects test-x vs test-y. Self-signed cert in $SPROUT_DATA/tls unless SPROUT_TLS_CERT is set"})
		add(DoctorCheck{Name: "firewall", OK: true, Level: "warn",
			Detail: "public Postgres is the SNI proxy on 5432",
			Hint:   "open NSG/security group for 5432 (and 8080 for the API); unique branch ports stay on loopback"})
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

	// Connectors summary
	if list, err := s.ListConnectors(ctx); err == nil {
		add(DoctorCheck{Name: "connectors", OK: true, Level: "info",
			Detail: fmt.Sprintf("%d configured", len(list))})
	}

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
