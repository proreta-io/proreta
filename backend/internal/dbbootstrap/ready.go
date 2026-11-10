package dbbootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrAdminRequired is returned by Ensure when the database is not ready and no admin login was
// given.
var ErrAdminRequired = errors.New("dbbootstrap: the database is not ready and no database admin login is available")

const probeTimeout = 15 * time.Second

// State is what the readiness probe found, in the order it is checked: a later field is only
// probed when the earlier ones hold.
type State struct {
	// SecretComplete: the Secret exists, has every password and its URLs match this run's settings.
	SecretComplete bool
	// OwnerConnects: proreta_owner connects to the database with the password from the Secret.
	OwnerConnects bool
	// SchemaPresent: goose_db_version exists, so the migrations have started.
	SchemaPresent bool
}

// Decide reports whether the bootstrap can be skipped and why (or why not). A skip needs all three
// facts: bootstrap is only required before the first migration, and a Secret left over from an
// earlier database must never hide a new, empty one.
func Decide(force bool, s State) (skip bool, reason string) {
	switch {
	case force:
		return false, "forced"
	case !s.SecretComplete:
		return false, "the credentials Secret is missing, incomplete or out of date"
	case !s.OwnerConnects:
		return false, "proreta_owner cannot connect with the credentials in the Secret"
	case !s.SchemaPresent:
		return false, "the database has no migration table yet"
	}
	return true, "the credentials Secret is current, proreta_owner connects and the schema is migrated"
}

// Ready probes the Secret and the database with the owner credentials and returns what it found.
// No admin login is used. Probe failures are not errors: they mean "not ready". Passwords are
// never part of the returned detail.
func (o *Options) Ready(ctx context.Context) State {
	var s State
	data, found, err := o.Secrets.Get(ctx, o.Namespace, o.SecretName)
	if err != nil || !found {
		return s
	}
	pw, err := passwordsFrom(data, true)
	if err != nil || len(o.urlDrift(data, pw)) != 0 {
		return s
	}
	s.SecretComplete = true

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cfg, err := pgx.ParseConfig(RoleURL(OwnerRole, pw[KeyOwnerPassword], o.Host, o.Port, o.DBName, o.SSLMode))
	if err != nil {
		return s
	}
	cfg.RuntimeParams["application_name"] = "proretactl-bootstrap-probe"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return s
	}
	defer closeConn(conn)
	s.OwnerConnects = true
	var present bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('public.goose_db_version') IS NOT NULL").Scan(&present); err == nil {
		s.SchemaPresent = present
	}
	return s
}

// Ensure skips the bootstrap when Decide says so (Options.Force overrides) and otherwise runs it.
// A skip returns a Result with Skipped set, without reading the admin login. Without an admin
// login a needed run fails with ErrAdminRequired.
func Ensure(ctx context.Context, o Options) (*Result, error) {
	if o.Secrets == nil || o.Namespace == "" || o.SecretName == "" {
		return nil, errors.New("dbbootstrap: namespace, secret name and secret store are required")
	}
	if o.Host == "" || o.Port == 0 || o.DBName == "" || o.SSLMode == "" {
		return nil, errors.New("dbbootstrap: host, port, database name and sslmode are required")
	}
	skip, reason := Decide(o.Force, o.Ready(ctx))
	if skip {
		return &Result{Skipped: true, SkipReason: reason}, nil
	}
	if o.Admin == nil {
		return nil, fmt.Errorf("%w (%s); provide the admin Secret (database.bootstrap.adminSecret) and run again", ErrAdminRequired, reason)
	}
	return Run(ctx, o)
}
