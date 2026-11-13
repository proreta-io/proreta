// Command jobs delivers the notification outbox to Slack, the mail outbox over SMTP (once the
// platform admin has configured a server in the admin panel) and runs the singleton housekeeping tasks: partitions, hourly rollups, retention,
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
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
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
	notifyClient := notify.NewSlackClient(cfg.BlockedCIDRs)
	deliverer := jobs.NewDeliverer(jobs.DeliveryConfig{PublicURL: cfg.PublicURL, DrainTimeout: cfg.ShutdownTimeout}, jobs.DeliveryDeps{
		Pool:     pool,
		Channels: notify.NewService(pool, keys, nil, cfg.PublicURL),
		Notifier: notify.NewSlackNotifier(notifyClient),
		Webhook:  notify.NewWebhookNotifier(notifyClient),
		Metrics:  m,
		Log:      log,
	})
	sched := scheduler.New()
	mon := monitors.NewService(pool, sched, incidents.NewService(sched))
	domainChecks := jobs.StatusDomainsConfig{Enabled: cfg.DomainChecks.Enabled, CNAMETarget: cfg.DomainChecks.CNAMETarget, Log: log}
	if domainChecks.Enabled {
		if domainChecks.Resolver, err = domains.NewNetResolver(domains.NetResolverConfig{Server: cfg.DomainChecks.DNS.Resolver, Timeout: cfg.DomainChecks.DNS.Timeout}); err != nil {
			return err
		}
	}
	tasks := append(jobs.DefaultTasks(m, log, mon), jobs.StatusPageTask(statuspages.NewService(pool, statuspages.Options{Logger: log}), m), jobs.CheckStatusPageDomains(domainChecks, m))
	housekeeper := jobs.NewHousekeeper(pool, tasks, m, log, clock.System{})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpx.Serve(gctx, log, cfg.ShutdownTimeout,
			httpx.NewServer(cfg.AdminAddr, httpx.AdminHandler(log, pool.Ping, reg), log))
	})
	g.Go(func() error { return deliverer.Run(gctx) })
	// The SMTP settings are read from the database (Admin, Email in the panel) for every message;
	// until the platform admin has set a server up the loop claims nothing.
	mailer := jobs.NewMailDeliverer(jobs.MailConfig{PublicURL: cfg.PublicURL, DrainTimeout: cfg.ShutdownTimeout}, jobs.MailDeps{
		Pool: pool, Keys: keys, Settings: mail.NewSettings(pool, keys, log), Metrics: m, Log: log,
	})
	g.Go(func() error { return mailer.Run(gctx) })
	g.Go(func() error { return housekeeper.Run(gctx) })
	return g.Wait()
}
