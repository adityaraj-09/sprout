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
	"github.com/adityaraj/sprout/internal/branch"
	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/config"
	"github.com/adityaraj/sprout/internal/meta"
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

	rec := &reconcile.Reconciler{Store: store, Compute: comp, Storage: stor}
	go rec.Loop(ctx, 30*time.Second)

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
	fmt.Printf("  token:     %s\n", cfg.Token)
	fmt.Printf("  pg_host:   %s (listen_addresses=%s)\n", postgres.PublicHost(), postgres.ListenAddresses())
	if postgres.RemoteAccess() {
		fmt.Println("  warning:   Postgres accepts remote TCP with trust auth — firewall + SPROUT_SAFE=true for anything real")
	}

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
