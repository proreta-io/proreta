// Command migrate applies the embedded database migrations as proreta_owner. It runs as a
// one-shot Kubernetes Job before the workloads are updated.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/migrations"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadMigrate(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel).With("service", "migrate", "version", version, "commit", commit)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	connCfg, err := pgx.ParseConfig(cfg.DatabaseURL.Value())
	if err != nil {
		return errors.New("invalid PRORETA_MIGRATE_DATABASE_URL")
	}
	connCfg.RuntimeParams["application_name"] = "proreta-migrate"
	db := stdlib.OpenDB(*connCfg)
	defer db.Close()
	// One connection for the advisory lock, one for the migrations; proreta_owner has CONNECTION LIMIT 3.
	db.SetMaxOpenConns(2)

	results, err := migrations.Up(ctx, db)
	for _, r := range results {
		log.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	if err != nil {
		return err
	}
	if len(results) == 0 {
		log.Info("database is up to date")
	}
	return nil
}
