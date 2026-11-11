package migrations_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationMigrateFromScratchAndRerun(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("migrations"))
	ctx := context.Background()

	n, err := env.Migrate(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second run applied %d migrations, err=%v; want 0", n, err)
	}
	admin := env.AdminConn(t)
	var tables []string
	// Partitions are named after the current month; they are checked in results_integration_test.go.
	rows, _ := admin.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition AND c.relname <> 'goose_db_version' ORDER BY 1`)
	tables, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := "audit_log,auth_identities,check_results,email_settings,email_verifications,heartbeat_tokens,incident_monitors,incidents,invitations,mail_outbox,memberships,monitor_channels,monitor_location_state," +
		"monitor_locations,monitor_state,monitors,notification_channels,notification_deliveries,org_auth_domains,password_resets,plans,platform_settings,probe_locations,project_members,projects,result_rollups_hourly,rollup_state,sessions," +
		"status_page_components,status_page_released_slugs,status_page_snapshots,status_pages,tenants,users"
	if got := strings.Join(tables, ","); got != want {
		t.Fatalf("tables = %s, want %s", got, want)
	}
}

// definerAllowList names every SECURITY DEFINER function and the roles allowed to execute it.
// A new definer function fails the lint until it is reviewed and added here.
var definerAllowList = map[string][]string{
	"auth_lookup_session": {"proreta_api"},
	// The login finds the user's tenant before the tenant is known.
	"auth_login_tenant": {"proreta_api"},
	// Tenant creation with a plan chosen by the database, the admin adoption check and the
	// cross-tenant session revocation of BootstrapAdmin.
	"auth_create_tenant":           {"proreta_api"},
	"auth_count_other_memberships": {"proreta_api"},
	"auth_revoke_user_sessions":    {"proreta_api"},
	// The platform admin panel: the flag of the configured admin, and the
	// cross-tenant admin operations, each of which checks the acting user's flag inside.
	"auth_sync_platform_admin":  {"proreta_api"},
	"admin_list_tenants":        {"proreta_api"},
	"admin_get_tenant":          {"proreta_api"},
	"admin_set_tenant_plan":     {"proreta_api"},
	"admin_set_tenant_status":   {"proreta_api"},
	"admin_delete_tenant":       {"proreta_api"},
	"admin_list_users":          {"proreta_api"},
	"admin_reset_user_password": {"proreta_api"},
	"admin_stats":               {"proreta_api"},
	"ensure_partitions":         {"proreta_worker"},
	"claim_due_monitors":        {"proreta_worker"},
	"compute_rollups":           {"proreta_worker"},
	// Rollups from the watermark in rollup_state.
	"compute_rollups_catch_up": {"proreta_worker"},
	// Retention in three steps driven by jobs.
	"list_droppable_partitions": {"proreta_worker"},
	"detach_expired_partition":  {"proreta_worker"},
	"drop_detached_partition":   {"proreta_worker"},
	"purge_expired_sessions":    {"proreta_worker"},
	"claim_deliveries":          {"proreta_worker"},
	"finish_delivery":           {"proreta_worker"},
	// Outbox backlog gauge of jobs.
	"outbox_pending": {"proreta_worker"},
	// The outgoing email queue: jobs claims, finishes, counts and purges messages.
	"claim_mail":        {"proreta_worker"},
	"finish_mail":       {"proreta_worker"},
	"mail_pending":      {"proreta_worker"},
	"purge_mail_outbox": {"proreta_worker"},
	// Expired email verification tokens.
	"purge_email_verifications": {"proreta_worker"},
	// Expired password reset tokens.
	"purge_password_resets": {"proreta_worker"},
	// The outgoing email settings: the api reads the switches, jobs reads the connection and
	// records test results, the platform admin reads and changes them.
	"email_settings_features":           {"proreta_api"},
	"email_settings_for_jobs":           {"proreta_worker"},
	"email_settings_record_test":        {"proreta_worker"},
	"admin_email_settings_get":          {"proreta_api"},
	"admin_email_settings_update":       {"proreta_api"},
	"admin_email_settings_clear":        {"proreta_api"},
	"admin_email_settings_set_features": {"proreta_api"},
	"admin_email_test_request":          {"proreta_api"},
	// Self-service registration: the api reads the admin's choice, the platform admin changes it.
	"platform_registration_override": {"proreta_api"},
	"admin_registration_get":         {"proreta_api"},
	"admin_registration_set":         {"proreta_api"},
	// Pause, resume and URL change reset the schedule within the caller's tenant.
	"reset_monitor_runtime": {"proreta_api"},
	// The public heartbeat ping finds its heartbeat by token hash before the tenant is known, and
	// jobs lists the heartbeats to evaluate across tenants.
	"heartbeat_ping":      {"proreta_api"},
	"list_due_heartbeats": {"proreta_worker"},
	// Notification channels: deleting a channel fails its pending deliveries in
	// the caller's tenant, and jobs queues reminders of open incidents across tenants.
	"fail_deleted_channel_deliveries": {"proreta_api"},
	"enqueue_incident_reminders":      {"proreta_worker"},
	// Members and invitations: an invitation token is looked up before the
	// tenant is known, the organization switcher lists the user's own memberships, and an owner
	// soft-deletes the organization of its own transaction.
	"auth_invitation_lookup":  {"proreta_api"},
	"auth_user_organizations": {"proreta_api"},
	"org_delete_tenant":       {"proreta_api"},
	// Status page: jobs lists the published pages across tenants to rebuild their
	// snapshots, and the public renderer reads one prepared snapshot by slug, its only privilege.
	"status_page_published_pages": {"proreta_worker"},
	"status_page_snapshot":        {"proreta_statuspage"},
	// A freed address stays reserved for its organization for 30 days; the api asks whether it is
	// held for someone else before the tenant could see the row.
	"status_page_slug_held": {"proreta_api"},
	// OAuth login: a Google sign-in finds the organization that claimed its
	// Workspace domain before the tenant is known.
	"auth_domain_lookup": {"proreta_api"},
}

// extraPolicies are the permissive policies on tenant tables that are not tenant isolation
// policies, keyed by "table.policy", with the only command and roles they may have.
var extraPolicies = map[string]struct {
	cmd   string
	roles []string
}{
	// The SECURITY DEFINER session lookup runs as proreta_owner before the tenant is known.
	"sessions.owner_session_lookup": {cmd: "r", roles: []string{"proreta_owner"}},
	// auth_login_tenant reads the user's memberships before the tenant is known.
	"memberships.owner_membership_lookup": {cmd: "r", roles: []string{"proreta_owner"}},
	// The cross-tenant claim, rollup and session purge functions.
	"sessions.owner_session_purge":             {cmd: "d", roles: []string{"proreta_owner"}},
	"monitors.owner_claim_read":                {cmd: "r", roles: []string{"proreta_owner"}},
	"monitor_location_state.owner_claim_read":  {cmd: "r", roles: []string{"proreta_owner"}},
	"monitor_location_state.owner_claim_lease": {cmd: "w", roles: []string{"proreta_owner"}},
	"check_results.owner_rollup_read":          {cmd: "r", roles: []string{"proreta_owner"}},
	"result_rollups_hourly.owner_rollup_write": {cmd: "*", roles: []string{"proreta_owner"}},
	// claim_deliveries and finish_delivery.
	"notification_deliveries.owner_delivery_read":   {cmd: "r", roles: []string{"proreta_owner"}},
	"notification_deliveries.owner_delivery_update": {cmd: "w", roles: []string{"proreta_owner"}},
	// heartbeat_ping and list_due_heartbeats.
	"heartbeat_tokens.owner_heartbeat_read": {cmd: "r", roles: []string{"proreta_owner"}},
	"heartbeat_tokens.owner_heartbeat_ping": {cmd: "w", roles: []string{"proreta_owner"}},
	// enqueue_incident_reminders.
	"incidents.owner_reminder_read":                 {cmd: "r", roles: []string{"proreta_owner"}},
	"notification_channels.owner_reminder_read":     {cmd: "r", roles: []string{"proreta_owner"}},
	"monitor_channels.owner_reminder_read":          {cmd: "r", roles: []string{"proreta_owner"}},
	"notification_deliveries.owner_reminder_insert": {cmd: "a", roles: []string{"proreta_owner"}},
	// auth_invitation_lookup.
	"invitations.owner_invitation_lookup": {cmd: "r", roles: []string{"proreta_owner"}},
	// status_page_published_pages and status_page_snapshot.
	"status_pages.owner_statuspage_read":          {cmd: "r", roles: []string{"proreta_owner"}},
	"status_page_snapshots.owner_statuspage_read": {cmd: "r", roles: []string{"proreta_owner"}},
	// status_page_slug_held.
	"status_page_released_slugs.owner_slug_hold_read": {cmd: "r", roles: []string{"proreta_owner"}},
	// auth_domain_lookup.
	"org_auth_domains.owner_domain_lookup": {cmd: "r", roles: []string{"proreta_owner"}},
}

var runtimeRoles = []string{"proreta_api", "proreta_statuspage", "proreta_worker"}

// TestIntegrationSchemaLint is the guard rail for new migrations: a tenant table without forced
// RLS and a tenant isolation policy, an extra permissive policy, a runtime role owning objects,
// bypassing RLS or being a member of proreta_owner, or a SECURITY DEFINER function that is not
// allow-listed, has no pinned search_path or is executable by others fails CI here.
func TestIntegrationSchemaLint(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("schemalint"))
	ctx := context.Background()
	admin := env.AdminConn(t)

	if v := schemaViolations(t, ctx, admin); len(v) > 0 {
		t.Fatalf("schema lint failed:\n  %s", strings.Join(v, "\n  "))
	}

	tenantTable := `CREATE TABLE lint_probe (id uuid PRIMARY KEY, tenant_id uuid NOT NULL);
		ALTER TABLE lint_probe ENABLE ROW LEVEL SECURITY;
		ALTER TABLE lint_probe FORCE ROW LEVEL SECURITY;`
	isolation := `CREATE POLICY tenant_isolation ON lint_probe
		USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
		WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);`
	definer := `CREATE FUNCTION lint_definer() RETURNS int LANGUAGE sql SECURITY DEFINER
		SET search_path = public, pg_temp AS 'SELECT 1';
		ALTER FUNCTION lint_definer() OWNER TO proreta_owner;
		REVOKE ALL ON FUNCTION lint_definer() FROM PUBLIC;`

	// Each rule must catch its violation. Every probe runs in a transaction that is rolled back.
	cases := []struct {
		name  string
		setup string
		want  []string
	}{
		{"forgotten RLS and policy, runtime owner",
			`CREATE TABLE lint_probe (id uuid PRIMARY KEY, tenant_id uuid NOT NULL);
			 ALTER TABLE lint_probe OWNER TO proreta_api;`,
			[]string{"lint_probe: row level security not enabled", "lint_probe: row level security not forced",
				"lint_probe: no policy", "proreta_api owns table lint_probe"}},
		{"USING (true) as the only policy",
			tenantTable + `CREATE POLICY open ON lint_probe USING (true) WITH CHECK (true);`,
			[]string{"lint_probe: no tenant isolation policy", "lint_probe: policy open is not a tenant isolation policy"}},
		{"isolation policy for SELECT only",
			tenantTable + `CREATE POLICY tenant_isolation ON lint_probe FOR SELECT
				USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);`,
			[]string{"lint_probe: no tenant isolation policy"}},
		{"isolation policy without WITH CHECK",
			tenantTable + `CREATE POLICY tenant_isolation ON lint_probe
				USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);`,
			[]string{"lint_probe: no tenant isolation policy"}},
		{"isolation policy for another role",
			tenantTable + `CREATE POLICY tenant_isolation ON lint_probe TO proreta_owner
				USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
				WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);`,
			[]string{"lint_probe: no tenant isolation policy"}},
		{"extra permissive policy next to a valid one",
			tenantTable + isolation + `CREATE POLICY peek ON lint_probe FOR SELECT TO proreta_api USING (true);`,
			[]string{"lint_probe: policy peek is not a tenant isolation policy"}},
		{"allow-listed policy with another shape",
			`ALTER POLICY owner_session_lookup ON sessions TO proreta_api;`,
			[]string{"sessions: policy owner_session_lookup must be FOR r TO proreta_owner"}},
		{"definer function not allow-listed",
			definer,
			[]string{"lint_definer: SECURITY DEFINER function not in the allow-list"}},
		{"definer function without pinned search_path",
			`ALTER FUNCTION auth_lookup_session(bytea) RESET search_path;`,
			[]string{"auth_lookup_session: SECURITY DEFINER function without pinned search_path"}},
		{"definer function not owned by proreta_owner",
			`ALTER FUNCTION auth_lookup_session(bytea) OWNER TO CURRENT_USER;`,
			[]string{"auth_lookup_session: SECURITY DEFINER function not owned by proreta_owner"}},
		{"definer function granted to another runtime role",
			`GRANT EXECUTE ON FUNCTION auth_lookup_session(bytea) TO proreta_worker;`,
			[]string{"auth_lookup_session: EXECUTE granted to proreta_worker", "auth_lookup_session: proreta_worker may execute it"}},
		{"definer function executable by PUBLIC",
			`GRANT EXECUTE ON FUNCTION auth_lookup_session(bytea) TO PUBLIC;`,
			[]string{"auth_lookup_session: executable by PUBLIC"}},
		{"isolation policy widened with OR",
			tenantTable + `CREATE POLICY tenant_isolation ON lint_probe
				USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid OR true)
				WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);`,
			[]string{"lint_probe: no tenant isolation policy"}},
		{"helper function executable by PUBLIC",
			`GRANT EXECUTE ON FUNCTION uuidv7_now() TO PUBLIC;`,
			[]string{"uuidv7_now: executable by PUBLIC"}},
		{"helper function executable by a runtime role",
			`GRANT EXECUTE ON FUNCTION admin_audit(uuid, uuid, uuid, text, text, text, jsonb, inet) TO proreta_api;`,
			[]string{"admin_audit: EXECUTE granted to proreta_api"}},
		{"partition without RLS",
			`CREATE TABLE lint_part PARTITION OF check_results FOR VALUES FROM ('2001-01-01 00:00:00+00') TO ('2001-02-01 00:00:00+00');`,
			[]string{"lint_part: row level security not enabled", "lint_part: row level security not forced", "lint_part: no policy"}},
		{"runtime role member of proreta_owner",
			`GRANT proreta_owner TO proreta_api;`,
			[]string{"proreta_api: member of proreta_owner"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, c.setup); err != nil {
				t.Fatalf("setup: %v", err)
			}
			v := strings.Join(schemaViolations(t, ctx, tx), "\n")
			for _, want := range c.want {
				if !strings.Contains(v, want) {
					t.Errorf("lint did not report %q; got:\n%s", want, v)
				}
			}
		})
	}
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func schemaViolations(t *testing.T, ctx context.Context, q querier) []string {
	t.Helper()
	var out []string
	collect := func(sql string, args ...any) {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			t.Fatalf("lint query: %v\n%s", err, sql)
		}
		v, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v...)
	}

	// users and auth_identities have no tenant_id and intentionally no RLS: users are global, and
	// the column grants in the baseline are their only guard (see TestIntegrationOAuthLogin).
	// Every table (including partitions) in public with a tenant_id column.
	collect(`
SELECT c.relname || ': ' || problem
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped
CROSS JOIN LATERAL (VALUES
    (NOT c.relrowsecurity, 'row level security not enabled'),
    (NOT c.relforcerowsecurity, 'row level security not forced'),
    (NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid), 'no policy')
) AS checks(failed, problem)
WHERE c.relkind IN ('r', 'p') AND checks.failed
ORDER BY 1`)
	out = append(out, policyViolations(t, ctx, q)...)

	// Runtime roles: never superuser, never BYPASSRLS, never members of proreta_owner (they would
	// inherit ownership and FORCE RLS would not stop them), and they own nothing.
	collect(`
SELECT rolname || ': ' || problem
FROM pg_roles
CROSS JOIN LATERAL (VALUES
    (rolsuper, 'is superuser'),
    (rolbypassrls, 'has BYPASSRLS'),
    (pg_has_role(oid, 'proreta_owner', 'MEMBER'), 'member of proreta_owner')
) AS checks(failed, problem)
WHERE rolname = ANY($1) AND checks.failed`, runtimeRoles)
	collect(`
WITH r AS (SELECT oid, rolname FROM pg_roles WHERE rolname = ANY($1))
SELECT r.rolname || ' owns ' || kind || ' ' || name FROM (
    SELECT relowner AS owner, CASE relkind WHEN 'S' THEN 'sequence' WHEN 'v' THEN 'view' WHEN 'i' THEN 'index' ELSE 'table' END AS kind, relname::text AS name FROM pg_class
    UNION ALL SELECT proowner, 'function', proname::text FROM pg_proc
    UNION ALL SELECT nspowner, 'schema', nspname::text FROM pg_namespace
    UNION ALL SELECT typowner, 'type', typname::text FROM pg_type
    UNION ALL SELECT datdba, 'database', datname::text FROM pg_database
) o JOIN r ON r.oid = o.owner
ORDER BY 1`, runtimeRoles)

	out = append(out, definerViolations(t, ctx, q)...)
	return out
}

// policyViolations requires on every tenant table a permissive policy for all commands, to
// PUBLIC or runtime roles only, whose USING and WITH CHECK both read app.tenant_id, and rejects
// every other permissive policy unless it is in extraPolicies with exactly that shape.
// Permissive policies are OR-ed, so one stray USING (true) would expose all tenants.
func policyViolations(t *testing.T, ctx context.Context, q querier) []string {
	t.Helper()
	rows, err := q.Query(ctx, `
SELECT c.relname::text, p.polname::text, p.polcmd::text, p.polpermissive,
       ARRAY(SELECT CASE WHEN r = 0 THEN 'public' ELSE pg_get_userbyid(r)::text END FROM unnest(p.polroles) AS r ORDER BY 1),
       COALESCE(pg_get_expr(p.polqual, p.polrelid), ''), COALESCE(pg_get_expr(p.polwithcheck, p.polrelid), '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped
JOIN pg_policy p ON p.polrelid = c.oid
WHERE c.relkind IN ('r', 'p')
ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("lint policies: %v", err)
	}
	type policy struct {
		table, name, cmd string
		permissive       bool
		roles            []string
		qual, check      string
	}
	pols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (policy, error) {
		var p policy
		err := r.Scan(&p.table, &p.name, &p.cmd, &p.permissive, &p.roles, &p.qual, &p.check)
		return p, err
	})
	if err != nil {
		t.Fatal(err)
	}
	allowedRoles := append([]string{"public"}, runtimeRoles...)
	isolated := map[string]bool{}
	var out []string
	for _, p := range pols {
		if !p.permissive {
			continue // restrictive policies only narrow access
		}
		rolesOK := len(p.roles) > 0
		for _, r := range p.roles {
			rolesOK = rolesOK && slices.Contains(allowedRoles, r)
		}
		if p.cmd == "*" && rolesOK && !hasOr(p.qual) && !hasOr(p.check) && strings.Contains(p.qual, "app.tenant_id") && strings.Contains(p.check, "app.tenant_id") {
			isolated[p.table] = true
			continue
		}
		extra, ok := extraPolicies[p.table+"."+p.name]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s: policy %s is not a tenant isolation policy and not allow-listed", p.table, p.name))
		case p.cmd != extra.cmd || !slices.Equal(p.roles, extra.roles):
			out = append(out, fmt.Sprintf("%s: policy %s must be FOR %s TO %s (is FOR %s TO %s)",
				p.table, p.name, extra.cmd, strings.Join(extra.roles, ","), p.cmd, strings.Join(p.roles, ",")))
		}
	}
	tables := map[string]bool{}
	for _, p := range pols {
		tables[p.table] = true
	}
	for tbl := range tables {
		if !isolated[tbl] {
			out = append(out, tbl+": no tenant isolation policy (permissive, all commands, PUBLIC or runtime roles, USING and WITH CHECK on app.tenant_id)")
		}
	}
	slices.Sort(out)
	return out
}

// hasOr reports whether a policy expression combines conditions with OR, which would widen a
// tenant isolation policy beyond comparing tenant_id with app.tenant_id.
func hasOr(expr string) bool {
	return strings.Contains(strings.ToUpper(expr), " OR ")
}

// definerViolations checks every SECURITY DEFINER function in public against definerAllowList
// (owner, exact search_path pin, grants) and requires every other function that is not an
// extension member to be executable by neither PUBLIC nor a runtime role.
func definerViolations(t *testing.T, ctx context.Context, q querier) []string {
	t.Helper()
	rows, err := q.Query(ctx, `
SELECT p.proname::text,
       pg_get_userbyid(p.proowner)::text,
       'search_path=public, pg_temp' = ANY(COALESCE(p.proconfig, '{}')),
       p.prosecdef,
       has_function_privilege('public', p.oid, 'EXECUTE'),
       ARRAY(SELECT DISTINCT pg_get_userbyid(a.grantee)::text
             FROM aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) AS a
             WHERE a.privilege_type = 'EXECUTE' AND a.grantee <> 0 AND a.grantee <> p.proowner
             ORDER BY 1),
       ARRAY(SELECT r FROM unnest($1::text[]) AS r WHERE has_function_privilege(r, p.oid, 'EXECUTE') ORDER BY 1)
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace AND n.nspname = 'public'
WHERE NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
ORDER BY 1`, runtimeRoles)
	if err != nil {
		t.Fatalf("lint definer functions: %v", err)
	}
	var out []string
	for rows.Next() {
		var name, owner string
		var pinned, definer, public bool
		var grantees, executors []string
		if err := rows.Scan(&name, &owner, &pinned, &definer, &public, &grantees, &executors); err != nil {
			t.Fatal(err)
		}
		// A function that is not SECURITY DEFINER (helpers, triggers) is never allow-listed: it
		// stays executable by its owner only.
		allowed, listed := definerAllowList[name]
		if definer {
			if !listed {
				out = append(out, name+": SECURITY DEFINER function not in the allow-list (definerAllowList)")
			}
			if owner != "proreta_owner" {
				out = append(out, name+": SECURITY DEFINER function not owned by proreta_owner")
			}
			if !pinned {
				out = append(out, name+": SECURITY DEFINER function without pinned search_path")
			}
		} else if listed {
			allowed = nil
		}
		if public {
			out = append(out, name+": executable by PUBLIC")
		}
		for _, g := range grantees {
			if !slices.Contains(allowed, g) {
				out = append(out, name+": EXECUTE granted to "+g)
			}
		}
		// Effective privilege also catches a grant through a group role.
		for _, r := range executors {
			if !slices.Contains(allowed, r) {
				out = append(out, name+": "+r+" may execute it")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
