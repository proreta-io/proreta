package migrations_test

import (
	"context"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// Two platform admins act on the two owners of one organization at the same time. The second call
// has to wait for the first and then see that it would remove the last active owner.
func TestIntegrationLastOwnerGuardIsSerialised(t *testing.T) {
	const owner2 = "01920000-0000-7000-8000-0000000000f7"
	for _, tc := range []struct{ name, first, second string }{
		{"disable and disable",
			`SELECT changed FROM admin_set_user_status($1, $2, 'disabled', gen_random_uuid(), '10.0.0.9')`,
			`SELECT changed FROM admin_set_user_status($1, $2, 'disabled', gen_random_uuid(), '10.0.0.9')`},
		{"delete and disable",
			`SELECT changed FROM admin_delete_user($1, $2, gen_random_uuid(), '10.0.0.9')`,
			`SELECT changed FROM admin_set_user_status($1, $2, 'disabled', gen_random_uuid(), '10.0.0.9')`},
		{"disable and delete",
			`SELECT changed FROM admin_set_user_status($1, $2, 'disabled', gen_random_uuid(), '10.0.0.9')`,
			`SELECT changed FROM admin_delete_user($1, $2, gen_random_uuid(), '10.0.0.9')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := pgtest.New(t, pgtest.DBName("ownerrace"))
			ctx := context.Background()
			admin := env.AdminConn(t)
			seedPlatform(t, admin)
			pgtest.MustExec(t, admin,
				`INSERT INTO users (id, email) VALUES ('`+owner2+`', 'second@alpha.example')`,
				`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantA+`', '`+owner2+`', 'owner')`)
			c1, c2 := env.Conn(t, env.APIURL), env.Conn(t, env.APIURL)

			tx, err := c1.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var changed bool
			if err := tx.QueryRow(ctx, tc.first, userPlatform, userOwnerA).Scan(&changed); err != nil || !changed {
				t.Fatalf("first call: %v %v", changed, err)
			}
			res := make(chan error, 1)
			go func() { res <- c2.QueryRow(ctx, tc.second, userPlatform, owner2).Scan(&changed) }()
			select {
			case err := <-res:
				t.Fatalf("second call did not wait for the first (err %v): write skew", err)
			case <-time.After(700 * time.Millisecond):
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-res; sqlState(err) != "PRA35" {
				t.Fatalf("second call: err = %v, want PRA35", err)
			}
			var n int
			if err := admin.QueryRow(ctx, `SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id
				WHERE m.tenant_id = $1 AND m.role = 'owner' AND u.status = 'active'`, tenantA).Scan(&n); err != nil || n != 1 {
				t.Fatalf("active owners = %d (%v), want 1", n, err)
			}
		})
	}
}
