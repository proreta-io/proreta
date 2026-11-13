package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const goodLimits = `{"max_monitors": 5, "max_heartbeats": 2, "max_channels": 2, "max_members": 4, "max_projects": 10, "max_status_pages": 1, "max_agents": 1}`

// Plan administration: only the platform admin writes, every plan always carries all limits,
// system plans survive, a plan in use cannot be deleted, and every change leaves one audit row.
func TestIntegrationAdminPlans(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("admin_plans"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	seedPlatform(t, admin)

	exec := func(sql string, args ...any) string {
		t.Helper()
		_, err := api.Exec(ctx, sql, args...)
		return sqlState(err)
	}
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if n := count(`SELECT count(*) FROM plans WHERE is_system = (key IN ('free', 'internal', 'community'))`); n != 3 {
		t.Fatal("exactly the seeded plans must be system plans")
	}

	t.Run("non-admin refused", func(t *testing.T) {
		for _, stmt := range []string{
			`SELECT * FROM admin_plans_list($1)`,
			`SELECT admin_plan_peak_usage($1, 'free')`,
			`SELECT admin_plan_create($1, 'x', 'X', '` + goodLimits + `', gen_random_uuid(), NULL)`,
			`SELECT admin_plan_update($1, 'free', 'Free', '` + goodLimits + `', gen_random_uuid(), NULL)`,
			`SELECT admin_plan_delete($1, 'free', gen_random_uuid(), NULL)`,
		} {
			if got := exec(stmt, userOwnerA); got != "PRA01" {
				t.Errorf("%s: state %q, want PRA01", stmt, got)
			}
		}
		if n := count(`SELECT count(*) FROM plans`); n != 3 {
			t.Fatalf("plans = %d, want 3", n)
		}
	})

	create := func(key, name, limits string) string {
		return exec(`SELECT admin_plan_create($1, $2, $3, $4::jsonb, gen_random_uuid(), NULL)`, userPlatform, key, name, limits)
	}

	t.Run("create validates", func(t *testing.T) {
		for _, c := range []struct{ key, name, limits, state string }{
			{"Gold", "Gold", goodLimits, "PRA31"},
			{"-gold", "Gold", goodLimits, "PRA31"},
			{"gold_x", "Gold", goodLimits, "PRA31"},
			{strings.Repeat("a", 64), "Gold", goodLimits, "PRA31"},
			{"gold", "", goodLimits, "PRA31"},
			{"gold", "  ", goodLimits, "PRA31"},
			{"gold", strings.Repeat("n", 201), goodLimits, "PRA31"},
			{"gold", "Gold", `{}`, "PRA32"},
			{"gold", "Gold", `{"max_monitors": 5}`, "PRA32"},
			{"gold", "Gold", strings.Replace(goodLimits, `"max_agents": 1`, `"max_agents": -1`, 1), "PRA32"},
			{"gold", "Gold", strings.Replace(goodLimits, `"max_agents": 1`, `"max_agents": 1.5`, 1), "PRA32"},
			{"gold", "Gold", strings.Replace(goodLimits, `"max_agents": 1`, `"max_agents": "1"`, 1), "PRA32"},
			{"gold", "Gold", strings.Replace(goodLimits, `"max_agents": 1`, `"max_agents": 2147483648`, 1), "PRA32"},
			{"gold", "Gold", strings.Replace(goodLimits, `"max_agents": 1`, `"max_agents": null`, 1), "PRA32"},
			{"gold", "Gold", strings.Replace(goodLimits, `"max_agents": 1`, `"max_agents": 1, "max_extra": 1`, 1), "PRA32"},
			{"gold", "Gold", `[]`, "PRA32"},
		} {
			if got := create(c.key, c.name, c.limits); got != c.state {
				t.Errorf("create(%q, %q, %s): state %q, want %s", c.key, c.name, c.limits, got, c.state)
			}
		}
		if n := count(`SELECT count(*) FROM plans`); n != 3 {
			t.Fatalf("a refused create left plans = %d", n)
		}
	})

	t.Run("create, update, delete", func(t *testing.T) {
		if got := create("gold", "Gold", strings.Replace(goodLimits, `"max_agents": 1`, `"max_agents": 2147483647`, 1)); got != "" {
			t.Fatalf("create: %q", got)
		}
		if got := create("gold", "Gold again", goodLimits); got != "PRA30" {
			t.Fatalf("duplicate key: %q, want PRA30", got)
		}
		if n := count(`SELECT count(*) FROM audit_log WHERE action = 'admin.plan.created' AND entity_type = 'plan' AND entity_id = 'gold' AND actor_user_id = $1 AND tenant_id = $2
			AND metadata -> 'limits' ->> 'max_agents' = '2147483647'`, userPlatform, tenantDef); n != 1 {
			t.Fatalf("created audit rows = %d, want 1", n)
		}

		update := func(key, name, limits string) (changed bool, state string) {
			t.Helper()
			err := api.QueryRow(ctx, `SELECT admin_plan_update($1, $2, $3, $4::jsonb, gen_random_uuid(), NULL)`, userPlatform, key, name, limits).Scan(&changed)
			return changed, sqlState(err)
		}
		if _, st := update("nope", "N", goodLimits); st != "PRA02" {
			t.Fatalf("update unknown: %q", st)
		}
		if _, st := update("gold", "Gold", `{"max_monitors": 1}`); st != "PRA32" {
			t.Fatalf("update with missing keys: %q", st)
		}
		if ch, st := update("gold", "Gold", goodLimits); !ch || st != "" {
			t.Fatalf("update: changed %v, state %q", ch, st)
		}
		if ch, st := update("gold", "Gold", goodLimits); ch || st != "" {
			t.Fatalf("unchanged update: changed %v, state %q", ch, st)
		}
		if n := count(`SELECT count(*) FROM audit_log WHERE action = 'admin.plan.updated' AND entity_id = 'gold'
			AND metadata ->> 'previousName' = 'Gold' AND metadata -> 'previousLimits' ->> 'max_agents' = '2147483647' AND metadata -> 'limits' ->> 'max_agents' = '1'`); n != 1 {
			t.Fatalf("updated audit rows = %d, want 1 (the no-op wrote none)", n)
		}
		// A system plan can be renamed and its limits changed; the key stays.
		if ch, st := update("free", "Starter", goodLimits); !ch || st != "" {
			t.Fatalf("update system plan: changed %v, state %q", ch, st)
		}
		if n := count(`SELECT count(*) FROM plans WHERE key = 'free' AND name = 'Starter' AND is_system AND limits ->> 'max_monitors' = '5'`); n != 1 {
			t.Fatal("system plan not updated")
		}

		del := func(key string) string {
			return exec(`SELECT admin_plan_delete($1, $2, gen_random_uuid(), NULL)`, userPlatform, key)
		}
		for key, want := range map[string]string{"free": "PRA33", "internal": "PRA33", "community": "PRA33", "nope": "PRA02"} {
			if got := del(key); got != want {
				t.Errorf("delete %s: %q, want %s", key, got, want)
			}
		}
		// In use: a live organization, and a deleted one too.
		pgtest.MustExec(t, admin, `UPDATE tenants SET plan_key = 'gold' WHERE id = '`+tenantB+`'`)
		if got := del("gold"); got != "PRA34" {
			t.Fatalf("delete in use: %q, want PRA34", got)
		}
		pgtest.MustExec(t, admin, `UPDATE tenants SET deleted_at = now(), status = 'suspended' WHERE id = '`+tenantB+`'`)
		if got := del("gold"); got != "PRA34" {
			t.Fatalf("delete used by a deleted organization: %q, want PRA34", got)
		}
		var orgs int
		var deletable bool
		if err := api.QueryRow(ctx, `SELECT organizations, deletable FROM admin_plans_list($1) WHERE key = 'gold'`, userPlatform).Scan(&orgs, &deletable); err != nil || orgs != 0 || deletable {
			t.Fatalf("list: organizations %d, deletable %v, err %v; want 0, false", orgs, deletable, err)
		}
		pgtest.MustExec(t, admin, `UPDATE tenants SET plan_key = 'free' WHERE id = '`+tenantB+`'`)
		if got := del("gold"); got != "" {
			t.Fatalf("delete: %q", got)
		}
		if n := count(`SELECT count(*) FROM plans WHERE key = 'gold'`); n != 0 {
			t.Fatal("plan not deleted")
		}
		if n := count(`SELECT count(*) FROM audit_log WHERE action = 'admin.plan.deleted' AND entity_id = 'gold' AND metadata ->> 'name' = 'Gold'`); n != 1 {
			t.Fatalf("deleted audit rows = %d, want 1", n)
		}
	})

	t.Run("peak usage", func(t *testing.T) {
		var monitors, heartbeats, members int
		if err := api.QueryRow(ctx, `SELECT (u ->> 'monitors')::int, (u ->> 'heartbeats')::int, (u ->> 'members')::int
			FROM admin_plan_peak_usage($1, 'free') AS u`, userPlatform).Scan(&monitors, &heartbeats, &members); err != nil {
			t.Fatal(err)
		}
		// Tenant A (free) holds what seedPlatform made: 2 live http monitors, 1 heartbeat, 1 member.
		if monitors != 2 || heartbeats != 1 || members != 1 {
			t.Fatalf("peak = monitors %d, heartbeats %d, members %d; want 2, 1, 1", monitors, heartbeats, members)
		}
		if got := exec(`SELECT admin_plan_peak_usage($1, 'nope')`, userPlatform); got != "PRA02" {
			t.Fatalf("unknown plan: %q", got)
		}
	})
}
