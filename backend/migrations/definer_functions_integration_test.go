package migrations_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// definerCalls calls every allow-listed SECURITY DEFINER function with harmless arguments.
var definerCalls = map[string]string{
	"auth_lookup_session":               `SELECT * FROM auth_lookup_session('\x00')`,
	"auth_login_tenant":                 `SELECT auth_login_tenant(gen_random_uuid())`,
	"auth_create_tenant":                `SELECT auth_create_tenant(gen_random_uuid(), 'lint-probe', 'Probe')`,
	"auth_count_other_memberships":      `SELECT auth_count_other_memberships(gen_random_uuid(), gen_random_uuid())`,
	"auth_revoke_user_sessions":         `SELECT auth_revoke_user_sessions(gen_random_uuid())`,
	"ensure_partitions":                 `SELECT ensure_partitions()`,
	"claim_due_monitors":                `SELECT * FROM claim_due_monitors(1, 'default')`,
	"compute_rollups":                   `SELECT compute_rollups(now() - interval '2 hours', now())`,
	"compute_rollups_catch_up":          `SELECT compute_rollups_catch_up(1, 0)`,
	"list_droppable_partitions":         `SELECT * FROM list_droppable_partitions()`,
	"detach_expired_partition":          `SELECT detach_expired_partition('check_results_p200001')`,
	"drop_detached_partition":           `SELECT drop_detached_partition('check_results_p200001')`,
	"purge_expired_sessions":            `SELECT purge_expired_sessions()`,
	"claim_deliveries":                  `SELECT * FROM claim_deliveries(1)`,
	"finish_delivery":                   `SELECT finish_delivery(gen_random_uuid(), gen_random_uuid(), 'sent', NULL, NULL)`,
	"outbox_pending":                    `SELECT outbox_pending()`,
	"claim_mail":                        `SELECT * FROM claim_mail(1)`,
	"finish_mail":                       `SELECT finish_mail(gen_random_uuid(), gen_random_uuid(), 'sent', NULL, NULL)`,
	"mail_pending":                      `SELECT mail_pending()`,
	"purge_mail_outbox":                 `SELECT purge_mail_outbox('30 days')`,
	"purge_email_verifications":         `SELECT purge_email_verifications()`,
	"purge_password_resets":             `SELECT purge_password_resets()`,
	"email_settings_features":           `SELECT * FROM email_settings_features()`,
	"email_settings_for_jobs":           `SELECT * FROM email_settings_for_jobs()`,
	"email_settings_record_test":        `SELECT email_settings_record_test(gen_random_uuid(), true, NULL, 0)`,
	"admin_email_settings_get":          `SELECT * FROM admin_email_settings_get(gen_random_uuid())`,
	"admin_email_settings_update":       `SELECT * FROM admin_email_settings_update(gen_random_uuid(), 'h', 25, 'none', 'a@b.c', '', NULL, '', gen_random_uuid(), NULL)`,
	"admin_email_settings_clear":        `SELECT admin_email_settings_clear(gen_random_uuid(), gen_random_uuid(), NULL)`,
	"admin_email_settings_set_features": `SELECT admin_email_settings_set_features(gen_random_uuid(), false, false, false, gen_random_uuid(), NULL)`,
	"admin_email_test_request":          `SELECT admin_email_test_request(gen_random_uuid(), 'example.com', gen_random_uuid(), NULL)`,
	"platform_registration_override":    `SELECT platform_registration_override()`,
	"admin_registration_get":            `SELECT * FROM admin_registration_get(gen_random_uuid())`,
	"admin_registration_set":            `SELECT * FROM admin_registration_set(gen_random_uuid(), true, gen_random_uuid(), NULL)`,
	"platform_retention_overrides":      `SELECT * FROM platform_retention_overrides()`,
	"admin_retention_get":               `SELECT * FROM admin_retention_get(gen_random_uuid())`,
	"admin_retention_set":               `SELECT admin_retention_set(gen_random_uuid(), 30, 13, gen_random_uuid(), NULL)`,
	"purge_notification_deliveries":     `SELECT purge_notification_deliveries(30)`,
	"purge_result_rollups":              `SELECT purge_result_rollups(13)`,
	"reset_monitor_runtime":             `SELECT reset_monitor_runtime(gen_random_uuid())`,
	"heartbeat_ping":                    `SELECT * FROM heartbeat_ping('\x00')`,
	"list_due_heartbeats":               `SELECT * FROM list_due_heartbeats(1)`,
	"fail_deleted_channel_deliveries":   `SELECT fail_deleted_channel_deliveries(gen_random_uuid())`,
	"enqueue_incident_reminders":        `SELECT enqueue_incident_reminders(1)`,
	"webhook_delivery_context":          `SELECT * FROM webhook_delivery_context(gen_random_uuid())`,
	// Executable by proreta_api, which gets the in-function refusal (PRA01) for an unknown actor.
	"auth_sync_platform_admin":    `SELECT auth_sync_platform_admin(gen_random_uuid())`,
	"admin_list_tenants":          `SELECT * FROM admin_list_tenants(gen_random_uuid(), '', false, 1, 0)`,
	"admin_get_tenant":            `SELECT * FROM admin_get_tenant(gen_random_uuid(), gen_random_uuid())`,
	"admin_set_tenant_plan":       `SELECT admin_set_tenant_plan(gen_random_uuid(), gen_random_uuid(), 'free', gen_random_uuid(), NULL)`,
	"admin_set_tenant_status":     `SELECT admin_set_tenant_status(gen_random_uuid(), gen_random_uuid(), 'active', gen_random_uuid(), NULL)`,
	"admin_delete_tenant":         `SELECT admin_delete_tenant(gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), NULL)`,
	"admin_list_users":            `SELECT * FROM admin_list_users(gen_random_uuid(), gen_random_uuid(), '', 1, 0)`,
	"admin_reset_user_password":   `SELECT * FROM admin_reset_user_password(gen_random_uuid(), gen_random_uuid(), '$argon2id$x', gen_random_uuid(), NULL)`,
	"admin_stats":                 `SELECT admin_stats(gen_random_uuid())`,
	"admin_list_all_users":        `SELECT * FROM admin_list_all_users(gen_random_uuid(), '', '', 1, 0)`,
	"admin_get_user":              `SELECT * FROM admin_get_user(gen_random_uuid(), gen_random_uuid())`,
	"admin_list_user_memberships": `SELECT * FROM admin_list_user_memberships(gen_random_uuid(), gen_random_uuid())`,
	"admin_set_user_status":       `SELECT * FROM admin_set_user_status(gen_random_uuid(), gen_random_uuid(), 'disabled', gen_random_uuid(), NULL)`,
	"admin_revoke_user_sessions":  `SELECT admin_revoke_user_sessions(gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), NULL)`,
	"admin_set_platform_admin":    `SELECT admin_set_platform_admin(gen_random_uuid(), gen_random_uuid(), true, gen_random_uuid(), NULL)`,
	// org_delete_tenant refuses (PRO02) without a tenant in the transaction.
	"auth_invitation_lookup":      `SELECT * FROM auth_invitation_lookup('\x00')`,
	"auth_user_organizations":     `SELECT * FROM auth_user_organizations(gen_random_uuid())`,
	"org_delete_tenant":           `SELECT org_delete_tenant(gen_random_uuid(), gen_random_uuid(), NULL)`,
	"status_page_published_pages": `SELECT * FROM status_page_published_pages()`,
	"status_page_snapshot":        `SELECT * FROM status_page_snapshot('acme')`,
	"status_page_slug_held":       `SELECT status_page_slug_held('acme')`,
	"auth_domain_lookup":          `SELECT * FROM auth_domain_lookup('example.com')`,
	// Specula: the in-function refusal without a tenant, an unknown token, a trigger function.
	"specula_create":               `SELECT specula_create(gen_random_uuid(), 'x', '\x00', true, NULL, gen_random_uuid())`,
	"specula_revoke":               `SELECT specula_revoke(gen_random_uuid())`,
	"specula_authenticate":         `SELECT * FROM specula_authenticate('\x00')`,
	"specula_hello":                `SELECT specula_hello(gen_random_uuid(), 'v', 'h')`,
	"specula_agents_sync_location": `SELECT specula_agents_sync_location()`,
	"monitor_set_location":         `SELECT monitor_set_location(gen_random_uuid(), gen_random_uuid())`,
	"specula_mark_unreachable":     `SELECT specula_mark_unreachable()`,
	"specula_sync_offline":         `SELECT * FROM specula_sync_offline()`,
	"specula_status_counts":        `SELECT * FROM specula_status_counts()`,
	"specula_delivery_context":     `SELECT * FROM specula_delivery_context(gen_random_uuid(), gen_random_uuid())`,
}

// Every definer function in the allow-list is executable by exactly the roles listed there: a
// runtime role that is not listed gets "permission denied", a listed one does not.
func TestIntegrationDefinerCallsByRole(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("definercalls"))
	ctx := context.Background()
	conns := map[string]*pgx.Conn{"proreta_api": env.Conn(t, env.APIURL), "proreta_worker": env.Conn(t, env.WorkerURL),
		"proreta_statuspage": env.Conn(t, env.StatuspageURL)}
	for name, allowed := range definerAllowList {
		stmt, ok := definerCalls[name]
		if !ok {
			t.Errorf("%s: allow-listed but has no call in definerCalls", name)
			continue
		}
		for _, role := range runtimeRoles {
			t.Run(name+"/"+role, func(t *testing.T) {
				tx, err := conns[role].Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(ctx) }()
				_, err = tx.Exec(ctx, stmt)
				denied := err != nil && strings.Contains(err.Error(), "permission denied for function "+name)
				if want := !slices.Contains(allowed, role); denied != want {
					t.Fatalf("%s as %s: err = %v, want denied=%t", stmt, role, err, want)
				}
			})
		}
	}
}

// The partial unique index allows one open automatic incident per monitor; resolved and manual
// (monitor_id NULL) incidents are not limited.
func TestIntegrationOneOpenIncidentPerMonitor(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("oneopen"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	m := seedMonitor(t, admin, tenantA, "now()", false, false)
	insert := func(monitor string) (string, error) {
		id := uuid.New().String()
		mon := "NULL"
		if monitor != "" {
			mon = "'" + monitor + "'"
		}
		_, err := admin.Exec(ctx, fmt.Sprintf(`INSERT INTO incidents (id, tenant_id, monitor_id, started_at) VALUES ('%s', '%s', %s, now())`, id, tenantA, mon))
		return id, err
	}
	first, err := insert(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insert(m); err == nil || !strings.Contains(err.Error(), "incidents_one_open_per_monitor_idx") {
		t.Fatalf("second open incident: err = %v, want the unique index", err)
	}
	pgtest.MustExec(t, admin, `UPDATE incidents SET resolved_at = now(), resolve_reason = 'recovered' WHERE id = '`+first+`'`)
	if _, err := insert(m); err != nil {
		t.Fatalf("open incident after the first was resolved: %v", err)
	}
	for range 2 {
		if _, err := insert(""); err != nil {
			t.Fatalf("manual incident: %v", err)
		}
	}
}

// The definer functions that compute month and hour boundaries give the same result in any
// session time zone, including ones with odd offsets.
func TestIntegrationNonUTCSessionTimeZone(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("nonutc"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	worker := env.Conn(t, env.WorkerURL)
	// The admin session renders partition bounds in UTC whatever the server time zone is.
	pgtest.MustExec(t, admin, `SET TimeZone = 'UTC'`, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	m := seedMonitor(t, admin, tenantA, "now() + interval '1 hour'", false, false)
	var dbNow time.Time
	if err := admin.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}

	for i, tz := range []string{"Asia/Kolkata", "Pacific/Chatham", "America/St_Johns"} {
		t.Run(tz, func(t *testing.T) {
			pgtest.MustExec(t, worker, `SET TimeZone = '`+tz+`'`)

			// ensure_partitions recreates the missing month with its UTC name and bounds.
			last := "check_results_p" + monthStart(dbNow, 2).Format("200601")
			pgtest.MustExec(t, admin, `DROP TABLE `+last)
			var created int
			if err := worker.QueryRow(ctx, `SELECT ensure_partitions()`).Scan(&created); err != nil || created != 1 {
				t.Fatalf("ensure_partitions created %d (err %v), want 1", created, err)
			}
			var bound string
			if err := admin.QueryRow(ctx, `SELECT pg_get_expr(c.relpartbound, c.oid) FROM pg_class c WHERE c.relname = $1`, last).Scan(&bound); err != nil {
				t.Fatal(err)
			}
			if lo := monthStart(dbNow, 2).Format("2006-01-02 15:04:05"); !strings.Contains(bound, lo+"+00") {
				t.Fatalf("%s bound %s; want it to start at %s UTC", last, bound, lo)
			}

			// compute_rollups buckets by UTC hour.
			hour := dbNow.UTC().Truncate(time.Hour).Add(-2 * time.Hour)
			monthPartition(t, admin, monthStart(hour, 0))
			at := hour.Add(time.Duration(10*i+5) * time.Minute)
			pgtest.MustExec(t, admin, fmt.Sprintf(`INSERT INTO check_results (tenant_id, monitor_id, location_id, checked_at, ok, status_code, latency_ms)
				VALUES ('%s', '%s', '%s', '%s', true, 200, 20)`, tenantA, m, defaultLocation, at.Format(time.RFC3339Nano)))
			if _, err := worker.Exec(ctx, `SELECT compute_rollups($1, $2)`, hour.Add(-time.Hour), hour.Add(2*time.Hour)); err != nil {
				t.Fatal(err)
			}
			var total int
			if err := admin.QueryRow(ctx, `SELECT total FROM result_rollups_hourly WHERE monitor_id = $1 AND hour = $2`, m, hour).Scan(&total); err != nil || total != i+1 {
				t.Fatalf("rollup of %s UTC: total %d (err %v), want %d", hour, total, err, i+1)
			}

			// Retention drops a rolled-up expired month and keeps the current and future ones.
			old := monthStart(dbNow, -3)
			name := monthPartition(t, admin, old)
			pgtest.MustExec(t, admin, fmt.Sprintf(`INSERT INTO check_results (tenant_id, monitor_id, location_id, checked_at, ok, latency_ms)
				VALUES ('%s', '%s', '%s', '%s', true, 30)`, tenantA, m, defaultLocation, old.Add(time.Hour).Format(time.RFC3339)))
			if _, err := worker.Exec(ctx, `SELECT compute_rollups($1, $2)`, old, old.AddDate(0, 1, 0)); err != nil {
				t.Fatal(err)
			}
			if n := retention(t, worker); n < 1 {
				t.Fatalf("retention dropped %d partitions", n)
			}
			got := partitions(t, admin)
			if slices.Contains(got, name) {
				t.Fatalf("expired partition %s kept: %v", name, got)
			}
			for k := range 3 {
				if p := "check_results_p" + monthStart(dbNow, k).Format("200601"); !slices.Contains(got, p) {
					t.Fatalf("partition %s dropped: %v", p, got)
				}
			}
		})
	}
}

// Retention refuses names outside the partition pattern and never touches the current month.
func TestIntegrationRetentionFunctionsRefuseOtherTables(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("retentionguard"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	worker := env.Conn(t, env.WorkerURL)
	for _, bad := range []string{"monitors", "check_results", "check_results_p202613", "check_results_p2026011", `check_results_p202601"; DROP TABLE monitors; --`} {
		for _, fn := range []string{"detach_expired_partition", "drop_detached_partition"} {
			if _, err := worker.Exec(ctx, `SELECT `+fn+`($1)`, bad); err == nil || !strings.Contains(err.Error(), "invalid partition name") {
				t.Errorf("%s(%q): err = %v, want invalid partition name", fn, bad, err)
			}
		}
	}
	var dbNow time.Time
	if err := admin.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	cur := "check_results_p" + monthStart(dbNow, 0).Format("200601")
	for _, fn := range []string{"detach_expired_partition", "drop_detached_partition"} {
		var ok bool
		if err := worker.QueryRow(ctx, `SELECT `+fn+`($1)`, cur).Scan(&ok); err != nil || ok {
			t.Errorf("%s(current month) = %t, err %v; want false", fn, ok, err)
		}
	}
	// An attached partition is never dropped directly.
	old := monthPartition(t, admin, monthStart(dbNow, -6))
	var ok bool
	if err := worker.QueryRow(ctx, `SELECT drop_detached_partition($1)`, old).Scan(&ok); err != nil || ok {
		t.Fatalf("drop_detached_partition(attached) = %t, err %v; want false", ok, err)
	}
	if !slices.Contains(partitions(t, admin), old) {
		t.Fatal("attached partition dropped")
	}
	// A detached leftover of an interrupted run is listed again and dropped.
	pgtest.MustExec(t, admin, `ALTER TABLE check_results DETACH PARTITION `+old)
	rows, _ := worker.Query(ctx, `SELECT name || ':' || detached FROM list_droppable_partitions()`)
	listed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !slices.Equal(listed, []string{old + ":true"}) {
		t.Fatalf("listed %v (err %v), want only the detached %s", listed, err, old)
	}
	if n := retention(t, worker); n != 1 {
		t.Fatalf("retention dropped %d, want the detached leftover", n)
	}
}
