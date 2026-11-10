package migrations_test

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/migrations"
)

// applyExtra applies the embedded migrations plus extra (file name to SQL) as proreta_owner,
// the way cmd/migrate does, and returns the number applied.
func applyExtra(t *testing.T, env *pgtest.Env, extra map[string]string) int {
	t.Helper()
	files := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = &fstest.MapFile{Data: body}
	}
	for name, sql := range extra {
		files[name] = &fstest.MapFile{Data: []byte(sql)}
	}
	cfg, err := pgx.ParseConfig(env.OwnerURL)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Up(context.Background())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return len(res)
}

// TestIntegrationUpgradePath is the template for upgrade-path tests: apply the baseline, seed
// data the way the application writes it, apply the next migration, and check the data and the
// new schema. A real migration test copies this test, replaces the synthetic migration with the
// new file's name (its SQL is already embedded, so extra is empty) and asserts what the
// migration promises for existing rows. The synthetic migration here exercises the rules every
// migration follows: lock_timeout first, a constant-default column on a table with rows, and the
// default privileges that give runtime roles access to a new table.
func TestIntegrationUpgradePath(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("upgrade"))
	ctx := context.Background()
	admin := env.AdminConn(t)

	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	seedMonitor(t, admin, tenantA, "now()", false, false)

	const next = "29991231235959_upgrade_probe.sql"
	n := applyExtra(t, env, map[string]string{next: `-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE monitors ADD COLUMN upgrade_probe text NOT NULL DEFAULT 'filled';
CREATE TABLE upgrade_probe (id uuid PRIMARY KEY);
`})
	if n != 1 {
		t.Fatalf("applied %d migrations, want 1", n)
	}
	var filled int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM monitors WHERE upgrade_probe = 'filled'`).Scan(&filled); err != nil || filled != 1 {
		t.Fatalf("existing monitor after the upgrade: %d (%v)", filled, err)
	}
	api := env.Conn(t, env.APIURL)
	if _, err := api.Exec(ctx, `INSERT INTO upgrade_probe (id) VALUES (gen_random_uuid())`); err != nil {
		t.Fatalf("default privileges on a new table: %v", err)
	}
	if v := schemaViolations(t, ctx, admin); len(v) > 0 {
		t.Fatalf("schema lint after the upgrade: %v", v)
	}
}
