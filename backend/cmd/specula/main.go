// Command specula is the executor that runs inside a customer's network. It claims due checks
// from Proreta over HTTPS, runs them against services that are not reachable from outside and
// reports the results. It has no database and makes outbound calls only.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/platform/metrics"
	"github.com/proreta-io/proreta/backend/internal/safehttp"
	"github.com/proreta-io/proreta/backend/internal/specula/executor"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "specula:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadSpecula(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel).With("service", "specula", "version", version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	hostname, _ := os.Hostname()

	// The checks reach private addresses on purpose: the customer chose the targets and runs
	// this process in the network they live in.
	prober := checks.NewHTTPSProber(safehttp.NewPrivateClient(safehttp.Options{Timeout: checks.DefaultTimeout, MaxRedirects: 3}))
	exec := executor.New(executor.Config{
		BaseURL: cfg.URL, Token: cfg.Token.Value(), Version: version, Hostname: hostname,
		Concurrency: cfg.Concurrency, PollInterval: cfg.PollInterval,
		// The gateway is reached through the environment's proxy settings, unlike the checks.
		HTTP:   &http.Client{Timeout: 25 * time.Second},
		Prober: prober, Log: log,
	})

	reg := metrics.NewRegistry("specula", version, commit)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpx.Serve(gctx, log, 25*time.Second, httpx.NewServer(cfg.AdminAddr, httpx.AdminHandler(log, func(context.Context) error { return nil }, reg), log))
	})
	g.Go(func() error {
		err := exec.Run(gctx)
		if errors.Is(err, executor.ErrRejected) {
			log.Error("Proreta rejected the token: it is wrong or was revoked")
		}
		stop()
		return err
	})
	return g.Wait()
}
