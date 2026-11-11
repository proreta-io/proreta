// Command api serves the REST API (api/openapi.yaml) and the admin endpoints.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api"
	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/entitlements"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/oauth"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/platform/metrics"
	"github.com/proreta-io/proreta/backend/internal/platformsettings"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/queries"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/migrations"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadAPI(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel).With("service", "api", "version", version)
	if config.CookieInsecureOverHTTPS(cfg.CookieSecure, cfg.PublicURL) {
		log.Warn("PRORETA_COOKIE_SECURE is false while PRORETA_PUBLIC_URL is https: cookies are sent without the Secure attribute")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL.Value(), db.Options{MaxConns: cfg.DatabaseMaxConns, ApplicationName: "proreta-api"})
	if err != nil {
		return err
	}
	defer pool.Close()
	// An image older than the schema must not run against it.
	if err := migrations.CheckNotAhead(ctx, pool); err != nil {
		return err
	}

	reg := metrics.NewRegistry("api", version, commit)
	keys, err := crypto.NewKeyring(cfg.EncryptionKeys, cfg.EncryptionKeyID)
	if err != nil {
		return err
	}
	// The email settings live in the database (Admin, Email in the panel). Until the platform
	// admin has configured and tested a server, every feature is off: nothing is queued and every
	// flow behaves as it did before email existed. The api never reads the SMTP password.
	mailSettings := mail.NewSettings(pool, keys, log)
	mailQueue := mail.NewQueue(keys, mailSettings, reg)
	authSvc, err := auth.NewService(pool, auth.Options{
		SessionTTL: cfg.SessionTTL,
		Clock:      clock.System{},
		Logger:     log,
		Registerer: reg,
		// Only the admin login keeps the password form when it is off.
		PasswordLoginDisabled: !cfg.PasswordLoginEnabled,
		// Only operator-approved Workspace domains can be claimed and joined by.
		AllowedDomains: cfg.OAuth.AllowedDomains,
		Mail:           mailQueue,
	})
	if err != nil {
		return err
	}
	if err := authSvc.BootstrapAdmin(ctx, cfg.AdminLogin, cfg.AdminPasswordHash.Value()); err != nil {
		return err
	}
	log.Info("admin bootstrap done")

	// The platform admin can open or close registration in the panel; PRORETA_REGISTRATION_ENABLED
	// is the value until a choice is stored.
	registration := platformsettings.NewRegistration(pool, cfg.RegistrationEnabled, log)
	adminSvc, err := admin.NewService(pool, authSvc, admin.Options{Logger: log, Registerer: reg, Mail: mailQueue, Settings: mailSettings, Registration: registration})
	if err != nil {
		return err
	}

	flow, err := oauthFlow(cfg)
	if err != nil {
		return err
	}
	if flow != nil {
		names := make([]string, 0, len(flow.Providers()))
		for _, p := range flow.Providers() {
			names = append(names, p.Name())
		}
		log.Info("oauth sign-in enabled", "providers", names, "password_login_enabled", cfg.PasswordLoginEnabled)
	}

	slack := notify.NewSlackNotifier(notify.NewSlackClient(cfg.BlockedCIDRs))

	sched := scheduler.New()
	handler, err := api.NewHandler(api.Config{
		Version:              version,
		Commit:               commit,
		CookieSecure:         cfg.CookieSecure,
		TrustedProxyHops:     cfg.TrustedProxyHops,
		RegistrationEnabled:  cfg.RegistrationEnabled,
		PublicURL:            cfg.PublicURL,
		PasswordLoginEnabled: cfg.PasswordLoginEnabled,
	}, api.Deps{
		Auth:     authSvc,
		Monitors: monitors.NewService(pool, sched, incidents.NewService(sched)),
		Queries:  queries.New(pool, nil),
		Channels: notify.NewService(pool, keys, slack, cfg.PublicURL),
		Plans:    plans.NewService(pool), Projects: projects.NewService(pool),
		StatusPages:  statuspages.NewService(pool, statuspages.Options{Logger: log}),
		Admin:        adminSvc,
		Registration: registration,
		OAuth:        flow,
		Entitlements: entitlements.NewService(pool),
		Logger:       log,
		Metrics:      metrics.NewHTTP(reg),
		Pings:        api.NewPingMetrics(reg),
	})
	if err != nil {
		return err
	}

	return httpx.Serve(ctx, log, cfg.ShutdownTimeout,
		httpx.NewServer(cfg.HTTPAddr, handler, log),
		httpx.NewServer(cfg.AdminAddr, httpx.AdminHandler(log, pool.Ping, reg), log),
	)
}

// oauthFlow builds the OAuth providers that have credentials; nil when there are none.
// The state cookie's key is derived from the active encryption key, so every replica shares it.
func oauthFlow(cfg config.API) (*oauth.Flow, error) {
	var providers []oauth.Provider
	if cfg.OAuth.GoogleEnabled() {
		providers = append(providers, oauth.NewGoogle(oauth.GoogleConfig{
			ClientID: cfg.OAuth.GoogleClientID, ClientSecret: cfg.OAuth.GoogleClientSecret.Value(),
		}))
	}
	if cfg.OAuth.GitHubEnabled() {
		providers = append(providers, oauth.NewGitHub(oauth.GitHubConfig{
			ClientID: cfg.OAuth.GitHubClientID, ClientSecret: cfg.OAuth.GitHubClientSecret.Value(),
		}))
	}
	if len(providers) == 0 {
		return nil, nil
	}
	key, err := oauth.DeriveKey(cfg.EncryptionKeys[cfg.EncryptionKeyID])
	if err != nil {
		return nil, fmt.Errorf("oauth state key: %w", err)
	}
	return oauth.NewFlow(key, cfg.PublicURL, clock.System{}, providers...)
}
