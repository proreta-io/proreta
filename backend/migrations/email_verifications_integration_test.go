package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationEmailVerifications checks the grants on email_verifications: the api inserts,
// reads and marks a token used, nothing else; the worker role only purges through the definer
// function, which keeps recent rows.
func TestIntegrationEmailVerifications(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("emailverif"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	worker := env.Conn(t, env.WorkerURL)
	const user = "01920000-0000-7000-8000-0000000000e1"
	pgtest.MustExec(t, admin, `INSERT INTO users (id, email) VALUES ('`+user+`', 'ada@example.com')`)

	insert := `INSERT INTO email_verifications (token_hash, user_id, email, expires_at) VALUES (sha256($1::bytea), '` + user + `', 'ada@example.com', now() + interval '1 day')`
	if _, err := api.Exec(ctx, insert, "t1"); err != nil {
		t.Fatalf("api insert: %v", err)
	}
	if _, err := api.Exec(ctx, `UPDATE email_verifications SET used_at = now()`); err != nil {
		t.Fatalf("api marks a token used: %v", err)
	}
	var n int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM email_verifications WHERE used_at IS NOT NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("api read: %d %v", n, err)
	}
	for _, stmt := range []string{
		`UPDATE email_verifications SET email = 'eve@example.com'`, `UPDATE email_verifications SET user_id = gen_random_uuid()`,
		`UPDATE email_verifications SET expires_at = now() + interval '10 years'`, `DELETE FROM email_verifications`,
		`SELECT purge_email_verifications()`,
	} {
		if _, err := api.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("api %q: err = %v, want permission denied", stmt, err)
		}
	}
	for _, stmt := range []string{`SELECT count(*) FROM email_verifications`, `DELETE FROM email_verifications`, `UPDATE email_verifications SET used_at = now()`} {
		if _, err := worker.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("worker %q: err = %v, want permission denied", stmt, err)
		}
	}
	if _, err := admin.Exec(ctx, insert, "t2"); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `UPDATE email_verifications SET created_at = now() - interval '3 days', expires_at = now() - interval '2 days' WHERE used_at IS NOT NULL`)
	if err := worker.QueryRow(ctx, `SELECT purge_email_verifications()`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v; want the one expired token", n, err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM email_verifications`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows left = %d, %v", n, err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO email_verifications (token_hash, user_id, email, expires_at) VALUES ('\x00', '`+user+`', 'x@example.com', now() + interval '1 day')`); err == nil {
		t.Error("a token hash of the wrong length was accepted")
	}
}
