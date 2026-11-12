package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The platform settings are reachable only through the definer functions: the runtime roles have
// no table privilege, the api may read the registration choice without a session, and only the
// platform admin changes it (an unchanged value writes nothing, a change is audited once).
func TestIntegrationPlatformSettings(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("platformsettings"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	worker := env.Conn(t, env.WorkerURL)
	seedPlatform(t, admin)

	for name, conn := range map[string]*pgx.Conn{"api": api, "worker": worker} {
		for _, stmt := range []string{`SELECT 1 FROM platform_settings`, `UPDATE platform_settings SET registration_enabled = true`} {
			if _, err := conn.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("%s: %s: err = %v, want permission denied", name, stmt, err)
			}
		}
	}
	if _, err := worker.Exec(ctx, `SELECT platform_registration_override()`); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("worker override: err = %v, want permission denied", err)
	}

	override := func() *bool {
		var v *bool
		if err := api.QueryRow(ctx, `SELECT platform_registration_override()`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if override() != nil {
		t.Fatal("fresh installation: override must be NULL (follow the environment)")
	}
	if _, err := api.Exec(ctx, `SELECT * FROM admin_registration_get($1)`, userOwnerA); sqlState(err) != "PRA01" {
		t.Fatalf("get as a regular user: err = %v, want PRA01", err)
	}
	if _, err := api.Exec(ctx, `SELECT * FROM admin_registration_set($1, true, gen_random_uuid(), NULL)`, userOwnerA); sqlState(err) != "PRA01" {
		t.Fatalf("set as a regular user: err = %v, want PRA01", err)
	}
	if override() != nil {
		t.Fatal("a refused set changed the value")
	}

	set := func(v any) (from, to string, changed bool) {
		rows, err := api.Query(ctx, `SELECT from_value, to_value FROM admin_registration_set($1, $2, gen_random_uuid(), NULL)`, userPlatform, v)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&from, &to); err != nil {
				t.Fatal(err)
			}
			changed = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if from, to, ok := set(true); !ok || from != "environment" || to != "enabled" {
		t.Fatalf("set true: %q -> %q changed=%v", from, to, ok)
	}
	if v := override(); v == nil || !*v {
		t.Fatalf("override = %v, want true", v)
	}
	if _, _, ok := set(true); ok {
		t.Fatal("setting the same value must change nothing")
	}
	if from, to, ok := set(false); !ok || from != "enabled" || to != "disabled" {
		t.Fatalf("set false: %q -> %q changed=%v", from, to, ok)
	}
	if v := override(); v == nil || *v {
		t.Fatalf("override = %v, want false", v)
	}
	var enabled *bool
	if err := api.QueryRow(ctx, `SELECT registration_enabled FROM admin_registration_get($1)`, userPlatform).Scan(&enabled); err != nil || enabled == nil || *enabled {
		t.Fatalf("admin get: %v, %v; want false", enabled, err)
	}
	if from, to, ok := set(nil); !ok || from != "disabled" || to != "environment" {
		t.Fatalf("set NULL: %q -> %q changed=%v", from, to, ok)
	}
	if override() != nil {
		t.Fatal("override must be NULL again")
	}

	// Three changes, three audit rows in the admin's own organization, no values but the labels.
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'admin.registration_settings.updated' AND entity_type = 'registration_settings' AND actor_user_id = $1 AND tenant_id = $2`,
		userPlatform, tenantDef).Scan(&n); err != nil || n != 3 {
		t.Fatalf("audit rows = %d, err = %v; want 3", n, err)
	}
}
