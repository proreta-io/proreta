// Command worker claims due checks from the database, runs them through the SSRF-guarded HTTPS
// prober and records results, incident transitions and outbox rows (internal/worker).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/platform/metrics"
	"github.com/proreta-io/proreta/backend/internal/safehttp"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/worker"
	"github.com/proreta-io/proreta/backend/migrations"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel).With("service", "worker", "version", version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL.Value(), db.Options{MaxConns: cfg.DatabaseMaxConns, ApplicationName: "proreta-worker"})
	if err != nil {
		return err
	}
	defer pool.Close()
	// An image older than the schema must not run against it.
	if err := migrations.CheckNotAhead(ctx, pool); err != nil {
		return err
	}

	var requestKeys *crypto.Keyring
	if len(cfg.Request.Keys) > 0 {
		if requestKeys, err = crypto.NewKeyring(cfg.Request.Keys, cfg.Request.KeyID); err != nil {
			return err
		}
	}

	reg := metrics.NewRegistry("worker", version, commit)
	sched := scheduler.New()
	client := safehttp.NewClient(safehttp.Options{
		Timeout: checks.DefaultTimeout, MaxRedirects: 3, BlockedPrefixes: cfg.BlockedCIDRs,
	})
	dial := safehttp.NewDialer(safehttp.Options{Timeout: checks.DefaultTimeout, BlockedPrefixes: cfg.BlockedCIDRs})
	w := worker.New(worker.Config{
		ClaimBatch: cfg.ClaimBatch, Concurrency: cfg.Concurrency,
		TenantConcurrency: cfg.TenantConcurrency, HostConcurrency: cfg.HostConcurrency,
		DrainTimeout: cfg.ShutdownTimeout, Location: cfg.Location,
	}, worker.Deps{
		Pool: pool, Scheduler: sched, Incidents: incidents.NewService(sched),
		Prober: checks.Mux{HTTP: checks.NewHTTPSProber(client), TCP: checks.NewTCPProber(dial)}, RequestKeys: requestKeys, Metrics: worker.NewMetrics(reg), Log: log,
	})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpx.Serve(gctx, log, cfg.ShutdownTimeout,
			httpx.NewServer(cfg.AdminAddr, httpx.AdminHandler(log, pool.Ping, reg), log))
	})
	g.Go(func() error { return w.Run(gctx) })
	return g.Wait()
}
