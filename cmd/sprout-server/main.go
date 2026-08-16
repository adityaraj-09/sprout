package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adityaraj/sprout/internal/api"
	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/branch"
	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/config"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/mysql"
	"github.com/adityaraj/sprout/internal/mysqlproxy"
	"github.com/adityaraj/sprout/internal/pgproxy"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/reconcile"
	"github.com/adityaraj/sprout/internal/storage"
)

func main() {
	cfg := config.ServerDefaults()
	if err := os.MkdirAll(cfg.DataRoot, 0o755); err != nil {
		fatal(err)
	}

	bins, err := postgres.LookBinaries()
	if err != nil {
		fatal(err)
	}
	stor, err := storage.Detect(cfg.DataRoot)
	if err != nil {
		fatal(err)
	}
	comp, err := compute.Detect(bins, cfg.Compute)
	if err != nil {
		fatal(err)
	}
	store, err := meta.Open(cfg.MetaPath())
	if err != nil {
		fatal(err)
	}
	defer store.Close()

	svc := &branch.Service{
		Root:     cfg.DataRoot,
		Store:    store,
		Storage:  stor,
		Compute:  comp,
		Bins:     bins,
		ColdSnap: cfg.ColdSnap,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	rec := &reconcile.Reconciler{
		Store: store, Compute: comp, Storage: stor,
		Root: cfg.DataRoot, AutoResume: cfg.AutoResume,
	}
	go rec.Loop(ctx, 30*time.Second)

	if postgres.ProxyEnabled() || mysql.ProxyEnabled() {
		tlsCfg, err := pgproxy.LoadTLSConfig(cfg.DataRoot)
		if err != nil {
			fatal(fmt.Errorf("proxy tls: %w", err))
		}
		if postgres.ProxyEnabled() {
			proxy := &pgproxy.Server{
				Addr:      pgproxy.ListenAddr(),
				TLSConfig: tlsCfg,
				Resolve:   pgproxy.StoreResolver(store),
			}
			if err := proxy.Listen(); err != nil {
				fatal(err)
			}
			go func() { _ = proxy.Serve() }()
			go func() {
				<-ctx.Done()
				_ = proxy.Close()
			}()
		}
		if mysql.ProxyEnabled() {
			mp := &mysqlproxy.Server{
				Addr:      mysqlproxy.ListenAddr(),
				TLSConfig: tlsCfg,
				Resolve:   mysqlproxy.StoreResolver(store),
			}
			if err := mp.Listen(); err != nil {
				fatal(err)
			}
			go func() { _ = mp.Serve() }()
			go func() {
				<-ctx.Done()
				_ = mp.Close()
			}()
		}
	}

	srv := api.New(svc, cfg.Token)
	httpSrv := &http.Server{Addr: cfg.Listen, Handler: srv.Handler()}

	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	fmt.Printf("sprout-server listening on http://%s\n", cfg.Listen)
	fmt.Printf("  data_root: %s\n", cfg.DataRoot)
	fmt.Printf("  storage:   %s\n", stor.Name())
	fmt.Printf("  compute:   %s\n", comp.Name())
	fmt.Printf("  meta:      %s\n", cfg.MetaPath())
	if cfg.Token == "dev-token" {
		fmt.Println("  token:     dev-token (change SPROUT_TOKEN if the API is public)")
	} else if cfg.Token != "" {
		fmt.Println("  token:     set")
	}
	gh := auth.FromEnv()
	if gh.Enabled() {
		mode := "public (any GitHub user)"
		if gh.Restricted() {
			mode = fmt.Sprintf("allowlist %d users / %d orgs", len(gh.Users), len(gh.Orgs))
		}
		fmt.Printf("  github:    device flow %s — %s\n", gh.HostURL(), mode)
	}
	fmt.Printf("  pg_host:   %s (listen_addresses=%s subdomain=%v)\n", postgres.PublicHost(), postgres.ListenAddresses(), postgres.BranchSubdomain())
	if postgres.BranchSubdomain() {
		if postgres.ProxyEnabled() {
			fmt.Printf("  dns:       *.%s → this VM; Postgres URLs use :%d (SNI proxy)\n", postgres.PublicHost(), postgres.ProxyPort())
		} else {
			fmt.Printf("  dns:       *.%s → this VM (wildcard A/AAAA); Postgres URLs keep the branch port\n", postgres.PublicHost())
		}
		if mysql.ProxyEnabled() {
			fmt.Printf("  mysql:     hostname proxy :%d (TLS + native password; --ssl-mode=REQUIRED)\n", mysql.ProxyPort())
		}
	}
	if postgres.RemoteAccess() {
		if postgres.TrustRemote() {
			fmt.Println("  warning:   Postgres accepts remote TCP with trust auth — firewall + SPROUT_SAFE=true; prefer default SCRAM")
		} else {
			fmt.Println("  auth:      remote SCRAM-SHA-256 (loopback trust); passwords are in connection strings")
		}
	}

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
