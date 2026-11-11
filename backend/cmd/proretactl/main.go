// Command proretactl is the Proreta admin CLI.
//
//	proretactl hashpw [-m KiB] [-t iterations] [-p parallelism]   print an argon2id hash of a password read from stdin
//	proretactl db bootstrap                                       create/repair the database and roles, or skip when they are ready (see internal/dbbootstrap)
//	proretactl secrets bootstrap                                  create the admin and encryption Secrets if absent (see internal/secretsbootstrap)
//	proretactl mail test -to ADDRESS                              send a test message with the PRORETA_SMTP_* settings, bypassing the queue
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/jackc/pgx/v5"
	"golang.org/x/term"

	"github.com/proreta-io/proreta/backend/internal/dbbootstrap"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/secretsbootstrap"
)

var (
	version = "dev"
	commit  = "unknown"
)

const usage = `usage:
  proretactl hashpw [-m KiB] [-t iterations] [-p parallelism]
  proretactl db bootstrap
  proretactl secrets bootstrap
  proretactl mail test -to ADDRESS
  proretactl version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "proretactl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	switch {
	case args[0] == "hashpw":
		return hashpw(args[1:], os.Stdin, os.Stdout, os.Stderr)
	case args[0] == "db" && len(args) > 1 && args[1] == "bootstrap":
		return dbBootstrap()
	case args[0] == "secrets" && len(args) > 1 && args[1] == "bootstrap":
		return secretsBootstrap()
	case args[0] == "mail" && len(args) > 1 && args[1] == "test":
		return mailTest(args[2:], os.LookupEnv, os.Stdout)
	case args[0] == "version":
		fmt.Printf("proretactl %s (%s)\n", version, commit)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

func hashpw(args []string, stdin *os.File, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hashpw", flag.ContinueOnError)
	p := crypto.DefaultArgon2Params
	m := fs.Uint("m", uint(p.Memory), "memory in KiB")
	t := fs.Uint("t", uint(p.Iterations), "iterations")
	par := fs.Uint("p", uint(p.Parallelism), "parallelism (1-255)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *m > 1<<32-1 || *t > 1<<32-1 || *par < 1 || *par > 255 {
		return errors.New("parameter out of range")
	}
	if *m > crypto.MaxArgon2Memory {
		return fmt.Errorf("-m must be at most %d KiB (256 MiB): the api refuses larger hashes", crypto.MaxArgon2Memory)
	}
	p.Memory, p.Iterations, p.Parallelism = uint32(*m), uint32(*t), uint8(*par) //nolint:gosec // range-checked above

	password, err := readPassword(stdin, stderr)
	if err != nil {
		return err
	}
	h, err := crypto.HashPassword(password, p)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, h)
	return err
}

// readPassword prompts without echo on a terminal, or reads one line from a pipe.
func readPassword(stdin *os.File, stderr io.Writer) (string, error) {
	fd := int(stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(stderr, "Password: ")
		a, err := term.ReadPassword(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(stderr, "Repeat password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(a, b) {
			return "", errors.New("passwords do not match")
		}
		return checkPassword(string(a))
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = trimNewline(line)
	return checkPassword(line)
}

func trimNewline(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\n' {
		s = s[:n-1]
		if n := len(s); n > 0 && s[n-1] == '\r' {
			s = s[:n-1]
		}
	}
	return s
}

func checkPassword(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty password")
	}
	return p, nil
}

func dbBootstrap() error {
	cfg, err := config.LoadBootstrap(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stderr, cfg.LogLevel).With("service", "proretactl", "command", "db bootstrap", "version", version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	ns := cfg.Namespace
	var secrets dbbootstrap.SecretStore
	if cfg.SecretFile != "" {
		// Local development: no Kubernetes, the credentials go to a file.
		ns, secrets = "local", dbbootstrap.FileSecrets{Path: cfg.SecretFile}
	} else {
		if ns == "" {
			if ns, err = dbbootstrap.InClusterNamespace(); err != nil {
				return err
			}
		}
		if secrets, err = dbbootstrap.NewInClusterSecrets(); err != nil {
			return err
		}
	}
	var admin *pgx.ConnConfig
	if cfg.AdminUser != "" {
		if admin, err = adminConnConfig(cfg); err != nil {
			return err
		}
	}
	res, err := dbbootstrap.Ensure(ctx, dbbootstrap.Options{
		Force:      cfg.Force,
		Admin:      admin,
		Host:       cfg.Host,
		Port:       cfg.Port,
		DBName:     cfg.DBName,
		SSLMode:    cfg.SSLMode,
		Namespace:  ns,
		SecretName: cfg.SecretName,
		Secrets:    secrets,
		Out:        os.Stdout,
		Logger:     log,
	})
	if err != nil {
		return err
	}
	if res.Skipped {
		log.Info("bootstrap skipped", "reason", res.SkipReason)
		return nil
	}
	log.Info("bootstrap complete")
	return nil
}

func secretsBootstrap() error {
	cfg, err := secretsbootstrap.LoadConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stderr, cfg.LogLevel).With("service", "proretactl", "command", "secrets bootstrap", "version", version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	opts := cfg.Options
	if opts.Namespace = cfg.Namespace; opts.Namespace == "" {
		if opts.Namespace, err = dbbootstrap.InClusterNamespace(); err != nil {
			return err
		}
	}
	if opts.Secrets, err = dbbootstrap.NewInClusterSecrets(); err != nil {
		return err
	}
	opts.Logger = log
	res, err := secretsbootstrap.Run(ctx, opts)
	if err != nil {
		return err
	}
	log.Info("secrets bootstrap complete", "admin_created", res.AdminCreated, "admin_initial_created", res.AdminInitialCreated, "crypto_created", res.CryptoCreated)
	return nil
}

// adminConnConfig builds the admin connection to database "postgres". The URL is assembled
// with net/url so the password is escaped, and is never logged.
func adminConnConfig(cfg config.Bootstrap) (*pgx.ConnConfig, error) {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.AdminUser, cfg.AdminPassword.Value()),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     "/postgres",
		RawQuery: url.Values{"sslmode": {cfg.SSLMode}, "application_name": {"proretactl-bootstrap"}}.Encode(),
	}
	c, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("invalid admin connection settings")
	}
	return c, nil
}

// mailTest sends one test message straight to the SMTP server named by PRORETA_SMTP_*, with a
// 30 s timeout. It does not touch the database or the queue: it answers "do these settings work?".
// The error says which step failed, never what the server answered.
func mailTest(args []string, lookup config.LookupFunc, stdout io.Writer) error {
	fs := flag.NewFlagSet("mail test", flag.ContinueOnError)
	to := fs.String("to", "", "recipient address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("mail test: -to ADDRESS is required")
	}
	cfg, err := config.LoadMailer(lookup)
	if err != nil {
		return err
	}
	msg, err := mail.Render(mail.KindTest, cfg.PublicURL, *to, nil)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := mail.NewSMTPSender(cfg.SMTP).Send(ctx, msg); err != nil {
		return fmt.Errorf("mail test: %s: %s", cfg.SMTP, mail.ErrorKind(err))
	}
	_, err = fmt.Fprintf(stdout, "test message accepted by %s\n", cfg.SMTP)
	return err
}
