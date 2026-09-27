package branch

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/mongo"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/qdrant"
	"github.com/adityaraj/sprout/internal/replica"
)

// PreflightOpts is a dry probe of an upstream URL. Nothing is created.
type PreflightOpts struct {
	URL    string
	Engine string
	Mode   string
	Tables []string
}

// PreflightCheck is one probe result, optionally with SQL the operator should run.
type PreflightCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
	SQL    string `json:"sql,omitempty"`
}

// PreflightReport is the connector preflight response.
type PreflightReport struct {
	OK      bool             `json:"ok"`
	Engine  string           `json:"engine"`
	Mode    string           `json:"mode"`
	URLHost string           `json:"url_host,omitempty"`
	Checks  []PreflightCheck `json:"checks"`
	FixSQL  []string         `json:"fix_sql,omitempty"`
}

// Preflight inspects an upstream database without creating a slot, publication, or replica.
func (s *Service) Preflight(ctx context.Context, opts PreflightOpts) (PreflightReport, error) {
	if strings.TrimSpace(opts.URL) == "" {
		return PreflightReport{}, fmt.Errorf("invalid_body: url required")
	}
	eng := engine.Normalize(opts.Engine)
	if opts.Engine == "" {
		eng = engine.InferFromURL(opts.URL)
	}
	if !engine.IsKnown(eng) {
		return PreflightReport{}, fmt.Errorf("invalid_engine: use %s", engine.KnownList())
	}
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode == "" {
		if engine.IsDumpSnapshot(eng) {
			mode = ModeLogical
		} else {
			mode = ModeLogical // preflight defaults to logical; physical needs WAL shipping access
		}
	}
	if mode != ModePhysical && mode != ModeLogical {
		return PreflightReport{}, fmt.Errorf("invalid_mode: use physical or logical")
	}

	rep := PreflightReport{Engine: eng, Mode: mode, OK: true}
	add := func(c PreflightCheck) {
		rep.Checks = append(rep.Checks, c)
		if !c.OK {
			rep.OK = false
		}
		if c.SQL != "" {
			rep.FixSQL = append(rep.FixSQL, c.SQL)
		}
	}

	switch {
	case engine.IsMongo(eng):
		s.preflightMongo(ctx, opts.URL, add)
	case engine.IsQdrant(eng):
		s.preflightQdrant(ctx, opts.URL, add)
	default:
		s.preflightPostgres(ctx, opts, mode, add)
	}
	for _, c := range rep.Checks {
		if c.Name == "connect" && c.Detail != "" {
			rep.URLHost = c.Detail
			break
		}
	}
	return rep, nil
}

func (s *Service) preflightPostgres(ctx context.Context, opts PreflightOpts, mode string, add func(PreflightCheck)) {
	if s.Bins.Psql == "" {
		add(PreflightCheck{Name: "local_psql", OK: false,
			Detail: "psql not on PATH",
			Hint:   "install postgresql-client matching the primary major (e.g. postgresql-client-17)"})
		return
	}
	conn, err := replica.ParseURL(opts.URL)
	if err != nil {
		add(PreflightCheck{Name: "url", OK: false, Detail: err.Error(),
			Hint: "use postgresql://user:pass@host:5432/postgres?sslmode=require"})
		return
	}
	add(PreflightCheck{Name: "url", OK: true, Detail: fmt.Sprintf("%s:%d/%s user=%s", conn.Host, conn.Port, conn.Database, conn.User)})

	rm := &replica.Manager{Bins: s.Bins}
	if err := rm.Ping(ctx, conn); err != nil {
		add(PreflightCheck{Name: "connect", OK: false, Detail: err.Error(),
			Hint: "check host, firewall, SSL, and credentials — preflight does not create anything"})
		return
	}
	add(PreflightCheck{Name: "connect", OK: true, Detail: fmt.Sprintf("%s:%d", conn.Host, conn.Port)})

	if mode != ModeLogical {
		add(PreflightCheck{Name: "mode", OK: true,
			Detail: "physical uses pg_basebackup; the role needs REPLICATION and wal_level ≥ replica",
			Hint:   "on the primary: ALTER SYSTEM SET wal_level = replica; (restart) and GRANT REPLICATION TO current_user"})
		return
	}

	wal, err := rmQuery(ctx, rm, conn, "SHOW wal_level;")
	if err != nil {
		add(PreflightCheck{Name: "wal_level", OK: false, Detail: err.Error()})
	} else {
		level := strings.TrimSpace(wal)
		ok := level == "logical"
		c := PreflightCheck{Name: "wal_level", OK: ok, Detail: "wal_level=" + level}
		if !ok {
			c.Hint = "managed Postgres (Supabase/RDS): enable logical replication in the dashboard"
			c.SQL = "ALTER SYSTEM SET wal_level = logical; -- then restart Postgres"
		}
		add(c)
	}

	slots, err := rmQuery(ctx, rm, conn, "SHOW max_replication_slots;")
	used := "0"
	if u, uerr := rmQuery(ctx, rm, conn, "SELECT count(*)::text FROM pg_replication_slots;"); uerr == nil {
		used = strings.TrimSpace(u)
	}
	if err != nil {
		add(PreflightCheck{Name: "slots", OK: false, Detail: err.Error()})
	} else {
		maxSlots, _ := strconv.Atoi(strings.TrimSpace(slots))
		usedN, _ := strconv.Atoi(used)
		remain := maxSlots - usedN
		ok := remain > 0
		c := PreflightCheck{Name: "slots", OK: ok,
			Detail: fmt.Sprintf("max_replication_slots=%d used=%d remaining=%d", maxSlots, usedN, remain)}
		if !ok {
			c.Hint = "raise max_replication_slots or drop unused slots"
			c.SQL = "ALTER SYSTEM SET max_replication_slots = 10;"
		}
		add(c)
	}

	senders, err := rmQuery(ctx, rm, conn, "SHOW max_wal_senders;")
	if err != nil {
		add(PreflightCheck{Name: "wal_senders", OK: false, Detail: err.Error()})
	} else {
		n, _ := strconv.Atoi(strings.TrimSpace(senders))
		ok := n >= 1
		c := PreflightCheck{Name: "wal_senders", OK: ok, Detail: "max_wal_senders=" + strings.TrimSpace(senders)}
		if !ok {
			c.SQL = "ALTER SYSTEM SET max_wal_senders = 10;"
		}
		add(c)
	}

	roleSQL := `SELECT current_user || E'\t' || current_setting('is_superuser') || E'\t' || COALESCE((SELECT rolreplication::text FROM pg_roles WHERE rolname = current_user), 'unknown');`
	roleOut, err := rmQuery(ctx, rm, conn, roleSQL)
	if err != nil {
		add(PreflightCheck{Name: "role", OK: false, Detail: err.Error()})
	} else {
		parts := strings.Split(strings.TrimSpace(roleOut), "\t")
		detail := strings.TrimSpace(roleOut)
		ok := true
		hint := ""
		if len(parts) >= 3 && parts[1] != "on" && parts[2] != "true" {
			// not superuser and maybe not replication — still often OK on managed PG with grants
			hint = "need CREATE on database + ability to CREATE PUBLICATION; on RDS also grant rds_replication"
		}
		rds, _ := rmQuery(ctx, rm, conn, `SELECT count(*)::text FROM pg_roles WHERE rolname = 'rds_replication';`)
		if strings.TrimSpace(rds) == "1" {
			in, _ := rmQuery(ctx, rm, conn, `SELECT pg_has_role(current_user, 'rds_replication', 'member')::text;`)
			detail += " rds_replication=" + strings.TrimSpace(in)
			if strings.TrimSpace(in) != "true" {
				ok = false
				hint = "GRANT rds_replication TO current_user; (RDS/Aurora)"
			}
		}
		add(PreflightCheck{Name: "role", OK: ok, Detail: detail, Hint: hint})
	}

	noPK, err := rmQuery(ctx, rm, conn, replicaIdentitySQL(opts.Tables))
	if err != nil {
		add(PreflightCheck{Name: "replica_identity", OK: true,
			Detail: "could not inspect replica identity: " + err.Error()})
	} else {
		var tables []string
		for _, line := range strings.Split(strings.TrimSpace(noPK), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				tables = append(tables, line)
			}
		}
		if len(tables) == 0 {
			add(PreflightCheck{Name: "replica_identity", OK: true, Detail: "all listed tables have a primary key or replica identity"})
		} else {
			shown := tables
			if len(shown) > 8 {
				shown = append(shown[:8], fmt.Sprintf("…+%d more", len(tables)-8))
			}
			sql := "ALTER TABLE " + tables[0] + " REPLICA IDENTITY FULL; -- repeat per table, or add a PRIMARY KEY"
			add(PreflightCheck{Name: "replica_identity", OK: false,
				Detail: fmt.Sprintf("%d table(s) without PK/replica identity: %s", len(tables), strings.Join(shown, ", ")),
				Hint:   "logical replication UPDATEs/DELETEs need a PK or REPLICA IDENTITY FULL",
				SQL:    sql})
		}
	}

	if err := rm.CheckToolsForPrimary(ctx, conn); err != nil {
		add(PreflightCheck{Name: "local_tools", OK: false, Detail: err.Error(),
			Hint: "install postgresql / postgresql-client matching the primary major and put it first on PATH"})
	} else {
		add(PreflightCheck{Name: "local_tools", OK: true, Detail: postgresToolDetail(s.Bins)})
	}
}

func replicaIdentitySQL(tables []string) string {
	filter := ""
	if len(tables) > 0 {
		quoted := make([]string, 0, len(tables))
		for _, t := range tables {
			t = strings.TrimSpace(t)
			if t != "" {
				quoted = append(quoted, "'"+strings.ReplaceAll(t, "'", "''")+"'")
			}
		}
		if len(quoted) > 0 {
			filter = " AND c.relname IN (" + strings.Join(quoted, ",") + ")"
		}
	}
	return `
SELECT n.nspname || '.' || c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r' AND n.nspname = 'public'` + filter + `
  AND c.relreplident = 'd'
  AND NOT EXISTS (SELECT 1 FROM pg_constraint x WHERE x.conrelid = c.oid AND x.contype = 'p')
ORDER BY 1;
`
}

func postgresToolDetail(b postgres.Binaries) string {
	parts := []string{}
	if b.Psql != "" {
		parts = append(parts, "psql="+b.Psql)
	}
	if b.PgBaseBackup != "" {
		parts = append(parts, "pg_basebackup="+b.PgBaseBackup)
	}
	if len(parts) == 0 {
		return "ok"
	}
	return strings.Join(parts, " ")
}

func rmQuery(ctx context.Context, rm *replica.Manager, c replica.Conn, sql string) (string, error) {
	return rm.QueryPrimary(ctx, c, sql)
}

func (s *Service) preflightMongo(ctx context.Context, raw string, add func(PreflightCheck)) {
	conn, err := mongo.ParseURL(raw)
	if err != nil {
		add(PreflightCheck{Name: "url", OK: false, Detail: err.Error(),
			Hint: "use mongodb:// or mongodb+srv:// with a database path for --tables="})
		return
	}
	add(PreflightCheck{Name: "url", OK: true, Detail: fmt.Sprintf("%s:%d db=%s", conn.Host, conn.Port, conn.Database)})

	host := conn.Host
	port := conn.Port
	if !conn.SRV {
		d := net.JoinHostPort(host, strconv.Itoa(port))
		c, err := net.DialTimeout("tcp", d, 8*time.Second)
		if err != nil {
			add(PreflightCheck{Name: "connect", OK: false, Detail: err.Error(),
				Hint: "check Atlas IP allowlist / TLS; preflight does not dump data"})
		} else {
			_ = c.Close()
			add(PreflightCheck{Name: "connect", OK: true, Detail: d})
		}
	} else {
		add(PreflightCheck{Name: "connect", OK: true,
			Detail: "mongodb+srv — TCP probe skipped; mongodump will resolve SRV at connect time"})
	}

	dump, err := exec.LookPath("mongodump")
	if err != nil {
		add(PreflightCheck{Name: "local_tools", OK: false, Detail: "mongodump not on PATH",
			Hint: "install MongoDB Database Tools + mongod on the sprout-server host"})
	} else {
		mongod, _ := exec.LookPath("mongod")
		detail := "mongodump=" + dump
		if mongod != "" {
			detail += " mongod=" + mongod
		} else {
			add(PreflightCheck{Name: "local_mongod", OK: false, Detail: "mongod not on PATH",
				Hint: "local branches need a mongod binary on the server"})
			add(PreflightCheck{Name: "local_tools", OK: true, Detail: detail})
			return
		}
		add(PreflightCheck{Name: "local_tools", OK: true, Detail: detail})
	}
	_ = ctx
}

func (s *Service) preflightQdrant(ctx context.Context, raw string, add func(PreflightCheck)) {
	conn, err := qdrant.ParseURL(raw)
	if err != nil {
		add(PreflightCheck{Name: "url", OK: false, Detail: err.Error(),
			Hint: "use https://CLUSTER.cloud.qdrant.io:6333?api-key=KEY or qdrant://host:6333"})
		return
	}
	add(PreflightCheck{Name: "url", OK: true, Detail: conn.BaseURL()})
	cli := qdrant.NewClient(conn)
	if err := cli.Ready(ctx); err != nil {
		add(PreflightCheck{Name: "connect", OK: false, Detail: err.Error(),
			Hint: "check api-key, TLS, and that the HTTP API is on 6333"})
		return
	}
	add(PreflightCheck{Name: "connect", OK: true, Detail: conn.BaseURL()})
	names, err := cli.ListCollections(ctx)
	if err != nil {
		add(PreflightCheck{Name: "collections", OK: false, Detail: err.Error()})
	} else {
		add(PreflightCheck{Name: "collections", OK: true,
			Detail: fmt.Sprintf("%d collection(s)", len(names)),
			Hint:   "sprout connect copies snapshots; there is no incremental sync — refresh with --wipe"})
	}
	bin, err := exec.LookPath("qdrant")
	if err != nil {
		add(PreflightCheck{Name: "local_tools", OK: false, Detail: "qdrant binary not on PATH",
			Hint: "install the Qdrant server binary on the sprout-server host"})
	} else {
		add(PreflightCheck{Name: "local_tools", OK: true, Detail: bin})
	}
}
