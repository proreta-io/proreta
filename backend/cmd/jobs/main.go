// Command jobs delivers the notification outbox to Slack, the mail outbox over SMTP (when
// configured) and runs the singleton housekeeping tasks: partitions, hourly rollups, retention,
// expired-session purge, the heartbeat evaluation and the status page snapshots (internal/jobs).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/platform/metrics"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/migrations"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jobs:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadJobs(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel).With("service", "jobs", "version", version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	keys, err := crypto.NewKeyring(cfg.EncryptionKeys, cfg.EncryptionKeyID)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL.Value(), db.Options{MaxConns: cfg.DatabaseMaxConns, ApplicationName: "proreta-jobs"})
	if err != nil {
		return err
	}
	defer pool.Close()
	// An image older than the schema must not run against it.
	if err := migrations.CheckNotAhead(ctx, pool); err != nil {
		return err
	}

	reg := metrics.NewRegistry("jobs", version, commit)
	m := jobs.NewMetrics(reg)
	deliverer := jobs.NewDeliverer(jobs.DeliveryConfig{PublicURL: cfg.PublicURL, DrainTimeout: cfg.ShutdownTimeout}, jobs.DeliveryDeps{
		Pool:     pool,
		Channels: notify.NewService(pool, keys, nil, cfg.PublicURL),
		Notifier: notify.NewSlackNotifier(notify.NewSlackClient(cfg.BlockedCIDRs)),
		Metrics:  m,
		Log:      log,
	})
	sched := scheduler.New()
	mon := monitors.NewService(pool, sched, incidents.NewService(sched))
	tasks := append(jobs.DefaultTasks(m, log, mon), jobs.StatusPageTask(statuspages.NewService(pool, statuspages.Options{Logger: log}), m))
	housekeeper := jobs.NewHousekeeper(pool, tasks, m, log, clock.System{})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpx.Serve(gctx, log, cfg.ShutdownTimeout,
			httpx.NewServer(cfg.AdminAddr, httpx.AdminHandler(log, pool.Ping, reg), log))
	})
	g.Go(func() error { return deliverer.Run(gctx) })
	if cfg.SMTP.Enabled() {
		mailer := jobs.NewMailDeliverer(jobs.MailConfig{PublicURL: cfg.PublicURL, DrainTimeout: cfg.ShutdownTimeout}, jobs.MailDeps{
			Pool: pool, Keys: keys, Sender: mail.NewSMTPSender(cfg.SMTP), Metrics: m, Log: log,
		})
		log.Info("email enabled", "smtp", cfg.SMTP.String())
		g.Go(func() error { return mailer.Run(gctx) })
	} else {
		log.Info("email disabled: PRORETA_SMTP_HOST is not set")
	}
	g.Go(func() error { return housekeeper.Run(gctx) })
	return g.Wait()
}
