// Command speculagw serves the Specula API: executors inside customer networks call it over HTTPS
// to claim due checks and report results (internal/specula/gateway).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/platform/metrics"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/migrations"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "speculagw:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadSpeculaGateway(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel).With("service", "speculagw", "version", version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL.Value(), db.Options{MaxConns: cfg.DatabaseMaxConns, ApplicationName: "proreta-speculagw"})
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

	reg := metrics.NewRegistry("speculagw", version, commit)
	sched := scheduler.New()
	handler := gateway.New(gateway.Deps{
		Pool: pool, Scheduler: sched,
		Recorder:    checkresult.Recorder{Scheduler: sched, Incidents: incidents.NewService(sched), Clock: clock.System{}},
		RequestKeys: requestKeys, Log: log, Registerer: reg,
	})
	return httpx.Serve(ctx, log, cfg.ShutdownTimeout,
		httpx.NewServer(cfg.HTTPAddr, handler, log),
		httpx.NewServer(cfg.AdminAddr, httpx.AdminHandler(log, pool.Ping, reg), log),
	)
}
