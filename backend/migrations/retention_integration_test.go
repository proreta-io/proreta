package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// Retention: the purge functions delete only finished deliveries and old rollups, in batches of
// 1000, and refuse a retention below the minimum; the settings are changed only by the platform
// admin, validated, audited once per change, and visible to the worker without a session.
func TestIntegrationRetention(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("retention"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	worker := env.Conn(t, env.WorkerURL)
	seedPlatform(t, admin)

	mon := seedMonitor(t, admin, tenantA, "now()", false, false)
	channel := uuid.NewString()
	pgtest.MustExec(t, admin, `INSERT INTO notification_channels (id, tenant_id, type, config_encrypted) VALUES ('`+channel+`', '`+tenantA+`', 'slack_webhook', '\x01')`)
	insert := func(status, age string) {
		t.Helper()
		sentAt, lease, token := "NULL", "NULL", "NULL"
		switch status {
		case "sent":
			sentAt = "now()"
		case "in_progress":
			lease, token = "now() + interval '1 hour'", "gen_random_uuid()"
		}
		pgtest.MustExec(t, admin, `INSERT INTO notification_deliveries (id, tenant_id, kind, monitor_id, channel_id, payload, status, created_at, sent_at, lease_until, claim_token)
			VALUES (gen_random_uuid(), '`+tenantA+`', 'tls_expiring', '`+mon+`', '`+channel+`', '{}', '`+status+`', now() - interval '`+age+`', `+sentAt+`, `+lease+`, `+token+`)`)
	}
	count := func(q string) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("deliveries", func(t *testing.T) {
		for _, c := range []struct{ status, age string }{
			{"sent", "40 days"}, {"failed", "40 days"}, {"pending", "40 days"}, {"in_progress", "40 days"}, {"sent", "10 days"}, {"failed", "29 days"},
		} {
			insert(c.status, c.age)
		}
		if _, err := worker.Exec(ctx, `SELECT purge_notification_deliveries(6)`); sqlState(err) != "22023" {
			t.Fatalf("retention below the minimum: err = %v, want 22023", err)
		}
		if _, err := api.Exec(ctx, `SELECT purge_notification_deliveries(30)`); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("api purge: err = %v, want permission denied", err)
		}
		var n int
		if err := worker.QueryRow(ctx, `SELECT purge_notification_deliveries(30)`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("purged %d, err %v; want the 2 finished rows older than 30 days", n, err)
		}
		if got := count(`SELECT count(*) FROM notification_deliveries WHERE status IN ('pending', 'in_progress')`); got != 2 {
			t.Fatalf("unfinished rows left = %d, want 2 (never purged)", got)
		}
		if got := count(`SELECT count(*) FROM notification_deliveries`); got != 4 {
			t.Fatalf("rows left = %d, want 4", got)
		}
		// A shorter retention reaches the 29 and 10 day old rows; unfinished ones still stay.
		if err := worker.QueryRow(ctx, `SELECT purge_notification_deliveries(7)`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("purged %d, err %v; want 2", n, err)
		}
		if got := count(`SELECT count(*) FROM notification_deliveries`); got != 2 {
			t.Fatalf("rows left = %d, want the 2 unfinished", got)
		}
		// Batches of 1000 terminate: 2500 old rows go in 1000 + 1000 + 500.
		pgtest.MustExec(t, admin, `INSERT INTO notification_deliveries (id, tenant_id, kind, monitor_id, channel_id, payload, status, created_at, sent_at)
			SELECT gen_random_uuid(), '`+tenantA+`', 'tls_expiring', '`+mon+`', '`+channel+`', '{}', 'sent', now() - interval '60 days', now() FROM generate_series(1, 2500)`)
		for _, want := range []int{1000, 1000, 500, 0} {
			if err := worker.QueryRow(ctx, `SELECT purge_notification_deliveries(30)`).Scan(&n); err != nil || n != want {
				t.Fatalf("batch = %d, err %v; want %d", n, err, want)
			}
		}
	})

	t.Run("rollups", func(t *testing.T) {
		for _, age := range []string{"14 months", "13 months 2 days", "12 months", "5 months", "1 hour"} {
			pgtest.MustExec(t, admin, `INSERT INTO result_rollups_hourly (tenant_id, monitor_id, location_id, hour, total, ok_count, latency_sum_ms, latency_histogram)
				VALUES ('`+tenantA+`', '`+mon+`', gen_random_uuid(), date_trunc('hour', now() - interval '`+age+`', 'UTC'), 1, 1, 10, '{0,0,0,0,0,0,0,0,0,0,0}')`)
		}
		if _, err := worker.Exec(ctx, `SELECT purge_result_rollups(3)`); sqlState(err) != "22023" {
			t.Fatalf("retention below the minimum: err = %v, want 22023", err)
		}
		var n int
		if err := worker.QueryRow(ctx, `SELECT purge_result_rollups(13)`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("purged %d, err %v; want 2", n, err)
		}
		if err := worker.QueryRow(ctx, `SELECT purge_result_rollups(6)`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("purged %d, err %v; want the 5 months old row to stay and the 12 months one to go", n, err)
		}
		if got := count(`SELECT count(*) FROM result_rollups_hourly`); got != 2 {
			t.Fatalf("rollups left = %d, want 2", got)
		}
	})

	t.Run("settings", func(t *testing.T) {
		overrides := func() (d, m *int) {
			t.Helper()
			if err := worker.QueryRow(ctx, `SELECT delivery_days, rollup_months FROM platform_retention_overrides()`).Scan(&d, &m); err != nil {
				t.Fatal(err)
			}
			return d, m
		}
		if d, m := overrides(); d != nil || m != nil {
			t.Fatalf("fresh installation: %v, %v; want NULL, NULL (defaults)", d, m)
		}
		if _, err := api.Exec(ctx, `SELECT * FROM platform_retention_overrides()`); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("api overrides: err = %v, want permission denied", err)
		}
		if _, err := worker.Exec(ctx, `SELECT admin_retention_set($1, 10, 6, gen_random_uuid(), NULL)`, userPlatform); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("worker set: err = %v, want permission denied", err)
		}
		for _, stmt := range []string{`SELECT * FROM admin_retention_get($1)`, `SELECT admin_retention_set($1, 10, 6, gen_random_uuid(), NULL)`} {
			if _, err := api.Exec(ctx, stmt, userOwnerA); sqlState(err) != "PRA01" {
				t.Fatalf("%s as a regular user: err = %v, want PRA01", stmt, err)
			}
		}
		for _, c := range []struct {
			d, m  any
			state string
		}{{6, 6, "PRA16"}, {366, 6, "PRA16"}, {10, 3, "PRA17"}, {10, 61, "PRA17"}} {
			if _, err := api.Exec(ctx, `SELECT admin_retention_set($1, $2, $3, gen_random_uuid(), NULL)`, userPlatform, c.d, c.m); sqlState(err) != c.state {
				t.Fatalf("set(%v, %v): err = %v, want %s", c.d, c.m, err, c.state)
			}
		}
		if d, m := overrides(); d != nil || m != nil {
			t.Fatalf("refused sets changed the values: %v, %v", d, m)
		}
		set := func(d, m any) (changed bool) {
			t.Helper()
			if err := api.QueryRow(ctx, `SELECT admin_retention_set($1, $2, $3, gen_random_uuid(), NULL)`, userPlatform, d, m).Scan(&changed); err != nil {
				t.Fatal(err)
			}
			return changed
		}
		if !set(7, 4) {
			t.Fatal("first set reported no change")
		}
		if d, m := overrides(); d == nil || *d != 7 || m == nil || *m != 4 {
			t.Fatalf("overrides = %v, %v; want 7, 4", d, m)
		}
		if set(7, 4) {
			t.Fatal("setting the same values must change nothing")
		}
		if !set(nil, 4) {
			t.Fatal("clearing one value reported no change")
		}
		if d, m := overrides(); d != nil || m == nil || *m != 4 {
			t.Fatalf("overrides = %v, %v; want NULL, 4", d, m)
		}
		var days *int
		if err := api.QueryRow(ctx, `SELECT delivery_days FROM admin_retention_get($1)`, userPlatform).Scan(&days); err != nil || days != nil {
			t.Fatalf("admin get: %v, %v", days, err)
		}
		// Two changes, two audit rows in the admin's own organization, with the new and old values.
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'admin.retention_settings.updated' AND entity_type = 'retention_settings' AND actor_user_id = $1 AND tenant_id = $2
			AND metadata ->> 'deliveryDays' = '7' AND metadata ->> 'previousDeliveryDays' IS NULL`, userPlatform, tenantDef).Scan(&n); err != nil || n != 1 {
			t.Fatalf("first audit row: n = %d, err = %v", n, err)
		}
		if n := count(`SELECT count(*) FROM audit_log WHERE action = 'admin.retention_settings.updated'`); n != 2 {
			t.Fatalf("audit rows = %d, want 2", n)
		}
	})
}
