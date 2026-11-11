package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationPasswordResets checks the grants on password_resets: the api inserts, reads and
// marks a token used, nothing else; the worker role only purges through the definer function.
func TestIntegrationPasswordResets(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("pwreset"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	worker := env.Conn(t, env.WorkerURL)
	const user = "01920000-0000-7000-8000-0000000000e2"
	pgtest.MustExec(t, admin, `INSERT INTO users (id, email) VALUES ('`+user+`', 'ada@example.com')`)

	insert := `INSERT INTO password_resets (token_hash, user_id, email, expires_at) VALUES (sha256($1::bytea), '` + user + `', 'ada@example.com', now() + interval '1 hour')`
	if _, err := api.Exec(ctx, insert, "t1"); err != nil {
		t.Fatalf("api insert: %v", err)
	}
	if _, err := api.Exec(ctx, `UPDATE password_resets SET used_at = now()`); err != nil {
		t.Fatalf("api marks a token used: %v", err)
	}
	var n int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM password_resets WHERE used_at IS NOT NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("api read: %d %v", n, err)
	}
	for _, stmt := range []string{
		`UPDATE password_resets SET user_id = gen_random_uuid()`, `UPDATE password_resets SET expires_at = now() + interval '10 years'`,
		`UPDATE password_resets SET email = 'eve@example.com'`, `DELETE FROM password_resets`, `SELECT purge_password_resets()`,
	} {
		if _, err := api.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("api %q: err = %v, want permission denied", stmt, err)
		}
	}
	for _, stmt := range []string{`SELECT count(*) FROM password_resets`, `DELETE FROM password_resets`, `UPDATE password_resets SET used_at = now()`} {
		if _, err := worker.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("worker %q: err = %v, want permission denied", stmt, err)
		}
	}
	if _, err := admin.Exec(ctx, insert, "t2"); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `UPDATE password_resets SET created_at = now() - interval '3 days', expires_at = now() - interval '2 days' WHERE used_at IS NOT NULL`)
	if err := worker.QueryRow(ctx, `SELECT purge_password_resets()`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v; want the one expired token", n, err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM password_resets`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows left = %d, %v", n, err)
	}
}
