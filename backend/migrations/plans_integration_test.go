package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationSeeds checks the rows the baseline seeds: the three plans with their limits and
// no features, every limit the code knows unlimited on community, and the default probe
// location the worker claims for by default.
func TestIntegrationSeeds(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("seeds"))
	ctx := context.Background()
	admin := env.AdminConn(t)

	var seeded string
	if err := admin.QueryRow(ctx, `SELECT string_agg(key || '=' || name || ' ' || limits::text || ' ' || features::text, '; ' ORDER BY key) FROM plans`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	want := `community=Community {"max_members": 2147483647, "max_channels": 2147483647, "max_monitors": 2147483647, "max_projects": 2147483647, "max_heartbeats": 2147483647, "max_status_pages": 2147483647} {}; ` +
		`free=Free {"max_members": 3, "max_channels": 1, "max_monitors": 3, "max_projects": 50, "max_heartbeats": 1, "max_status_pages": 1} {}; ` +
		`internal=Internal {"max_members": 50, "max_channels": 10, "max_monitors": 1000, "max_projects": 50, "max_heartbeats": 100, "max_status_pages": 1} {}`
	if seeded != want {
		t.Fatalf("plans = %s\nwant    %s", seeded, want)
	}
	// plans.Resolve fails on a missing key, so this also proves every limit the code knows exists.
	var raw []byte
	if err := admin.QueryRow(ctx, `SELECT limits FROM plans WHERE key = 'community'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	l, err := plans.Resolve(plans.KeyCommunity, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []plans.Resource{plans.ResourceMonitor, plans.ResourceHeartbeat, plans.ResourceChannel,
		plans.ResourceMember, plans.ResourceProject, plans.ResourceStatusPage} {
		if l.Of(r) != plans.Unlimited {
			t.Errorf("community %s limit = %d, want plans.Unlimited", r, l.Of(r))
		}
	}

	var loc string
	if err := admin.QueryRow(ctx, `SELECT string_agg(id || ' ' || code || ' ' || name || ' ' || provider || ' ' || region || ' ' || kind || ' ' || status, '; ')
		FROM probe_locations`).Scan(&loc); err != nil {
		t.Fatal(err)
	}
	if want := defaultLocation + " " + scheduler.DefaultLocationCode + " Default self-hosted default platform active"; loc != want {
		t.Fatalf("probe locations = %q, want %q", loc, want)
	}
	var tenants int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&tenants); err != nil || tenants != 0 {
		t.Fatalf("tenants seeded: %d (%v); the api creates the default tenant at start", tenants, err)
	}
}

// TestIntegrationPlans checks that new tenants get free, that the plan key is a foreign key,
// that feature names are slugs, and that plans are read-only for the runtime roles.
func TestIntegrationPlans(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("plans"))
	ctx := context.Background()
	admin := env.AdminConn(t)

	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	var plan string
	if err := admin.QueryRow(ctx, `SELECT plan_key FROM tenants WHERE id = $1`, tenantA).Scan(&plan); err != nil || plan != "free" {
		t.Fatalf("new tenant plan = %q (%v), want free", plan, err)
	}
	if _, err := admin.Exec(ctx, `UPDATE tenants SET plan_key = 'gold' WHERE id = $1`, tenantA); err == nil || !strings.Contains(err.Error(), "tenants_plan_key_fkey") {
		t.Fatalf("unknown plan key accepted: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE tenants SET limits = '[]' WHERE id = $1`, tenantA); err == nil || !strings.Contains(err.Error(), "tenants_limits_is_object") {
		t.Fatalf("non-object limits accepted: %v", err)
	}

	for _, bad := range []string{`'{Bad}'`, `'{"a b"}'`, `'{""}'`, `ARRAY['` + strings.Repeat("a", 65) + `']`, `'{NULL}'`} {
		if _, err := admin.Exec(ctx, `UPDATE plans SET features = `+bad+` WHERE key = 'free'`); err == nil || !strings.Contains(err.Error(), "plans_features_names") {
			t.Errorf("feature names %s: err = %v, want plans_features_names", bad, err)
		}
	}
	pgtest.MustExec(t, admin, `UPDATE plans SET features = '{sso,audit-export,a_b}' WHERE key = 'free'`,
		`UPDATE plans SET features = '{}' WHERE key = 'free'`)

	for _, url := range []string{env.APIURL, env.WorkerURL} {
		conn := env.Conn(t, url)
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM plans`).Scan(&n); err != nil || n != 3 {
			t.Fatalf("runtime role reads plans: n=%d err=%v", n, err)
		}
		for _, stmt := range []string{
			`INSERT INTO plans (key, name, limits) VALUES ('gold', 'Gold', '{}')`,
			`UPDATE plans SET limits = '{"max_monitors": 100000}'`,
			`UPDATE plans SET features = '{x}'`,
			`DELETE FROM plans WHERE key = 'free'`,
			`TRUNCATE plans`,
		} {
			if _, err := conn.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("%s: err = %v, want permission denied", stmt, err)
			}
		}
	}
}
