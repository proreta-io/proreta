package migrations_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tokenC = "cccccccccccccccccccccccccccccccc"
)

// domainFixture is three organizations with a published page each, plus an unpublished one of A.
type domainFixture struct {
	t                                *testing.T
	env                              *pgtest.Env
	admin, api, worker, public       *pgx.Conn
	tenantC                          string
	pageA, pageB, pageC, pageAHidden string
}

func newDomainFixture(t *testing.T, name string) *domainFixture {
	t.Helper()
	env := pgtest.New(t, pgtest.DBName(name))
	f := &domainFixture{t: t, env: env, admin: env.AdminConn(t), api: env.Conn(t, env.APIURL), worker: env.Conn(t, env.WorkerURL),
		public: env.Conn(t, env.StatuspageURL), tenantC: uuid.NewString(),
		pageA: uuid.NewString(), pageB: uuid.NewString(), pageC: uuid.NewString(), pageAHidden: uuid.NewString()}
	pgtest.MustExec(t, f.admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B'), ('`+f.tenantC+`', 'c', 'C')`,
		`INSERT INTO status_pages (id, tenant_id, slug, title, published) VALUES
			('`+f.pageA+`', '`+tenantA+`', 'acme', 'Acme', true), ('`+f.pageB+`', '`+tenantB+`', 'beta', 'Beta', true),
			('`+f.pageC+`', '`+f.tenantC+`', 'gamma', 'Gamma', true), ('`+f.pageAHidden+`', '`+tenantA+`', 'hidden', 'Hidden', false)`)
	return f
}

var tokens = map[string]string{tenantA: tokenA, tenantB: tokenB}

// add inserts a domain through the admin connection (which bypasses RLS) and returns its id. set
// is extra column assignments such as "released_at = now()".
func (f *domainFixture) add(tenant, page, host, state string, set ...string) string {
	f.t.Helper()
	id := uuid.NewString()
	token := tokens[tenant]
	if token == "" {
		token = tokenC
	}
	pgtest.MustExec(f.t, f.admin, fmt.Sprintf(`INSERT INTO status_page_domains (id, tenant_id, page_id, host, state, token) VALUES ('%s', '%s', '%s', '%s', '%s', '%s')`,
		id, tenant, page, host, state, token))
	for _, s := range set {
		pgtest.MustExec(f.t, f.admin, `UPDATE status_page_domains SET `+s+` WHERE id = '`+id+`'`)
	}
	return id
}

func (f *domainFixture) lookup(host string) (slug string, found bool) {
	f.t.Helper()
	err := f.public.QueryRow(context.Background(), `SELECT slug FROM status_page_domain_lookup($1)`, host).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		f.t.Fatalf("lookup %s: %v", host, err)
	}
	return slug, true
}

func (f *domainFixture) allowed(host string) bool {
	f.t.Helper()
	var ok bool
	if err := f.public.QueryRow(context.Background(), `SELECT status_page_domain_allowed($1)`, host).Scan(&ok); err != nil {
		f.t.Fatalf("allowed %s: %v", host, err)
	}
	return ok
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// The table is tenant isolated, the api may read, insert and update but not delete, the worker and
// the public renderer have no table rights, and the renderer's functions leak a slug and a boolean.
func TestIntegrationStatusPageDomainsRightsAndRLS(t *testing.T) {
	f := newDomainFixture(t, "spdomainsrls")
	ctx := context.Background()
	idA := f.add(tenantA, f.pageA, "status.acme.com", "verified")
	f.add(tenantB, f.pageB, "status.beta.com", "verified")

	t.Run("tenant isolation", func(t *testing.T) {
		err := inTenant(t, f.api, tenantA, func(tx pgx.Tx) error {
			var n, other int
			if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE tenant_id <> $1) FROM status_page_domains`, tenantA).Scan(&n, &other); err != nil {
				return err
			}
			if n != 1 || other != 0 {
				t.Errorf("tenant A sees %d rows, %d of others", n, other)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		err = inTenant(t, f.api, tenantB, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO status_page_domains (id, tenant_id, page_id, host, token) VALUES ($1, $2, $3, 'sneaky.example.org', $4)`,
				uuid.NewString(), tenantA, f.pageA, tokenA)
			return err
		})
		if pgCode(err) != "42501" {
			t.Errorf("cross-tenant insert: %v, want an RLS violation", err)
		}
		err = inTenant(t, f.api, tenantB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE status_page_domains SET is_primary = true WHERE id = $1`, idA)
			if err == nil && tag.RowsAffected() != 0 {
				t.Errorf("tenant B updated %d rows of tenant A", tag.RowsAffected())
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.api.QueryRow(ctx, `SELECT count(*) FROM status_page_domains`).Scan(&n); err != nil || n != 0 {
			t.Errorf("without a tenant the api sees %d rows (%v)", n, err)
		}
	})

	t.Run("runtime grants", func(t *testing.T) {
		err := inTenant(t, f.api, tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM status_page_domains WHERE id = $1`, idA)
			return err
		})
		if pgCode(err) != "42501" {
			t.Errorf("api delete: %v, want 42501", err)
		}
		for name, conn := range map[string]*pgx.Conn{"worker": f.worker, "public": f.public} {
			for _, stmt := range []string{`SELECT count(*) FROM status_page_domains`, `UPDATE status_page_domains SET is_primary = false`,
				`INSERT INTO status_page_domains (id) VALUES (gen_random_uuid())`} {
				// The renderer's session is also read-only, which is refused first for a write.
				if _, err := conn.Exec(ctx, stmt); pgCode(err) != "42501" && pgCode(err) != "25006" {
					t.Errorf("%s: %s: %v, want 42501", name, stmt, err)
				}
			}
		}
		// The renderer's functions return one slug column and one boolean, nothing else.
		rows, err := f.public.Query(ctx, `SELECT * FROM status_page_domain_lookup('status.acme.com')`)
		if err != nil {
			t.Fatal(err)
		}
		if cols := rows.FieldDescriptions(); len(cols) != 1 || cols[0].Name != "slug" {
			t.Errorf("lookup columns = %v, want just slug", cols)
		}
		rows.Close()
		// Each function is executable by its roles only.
		calls := map[string]struct {
			stmt  string
			roles []string
		}{
			"lookup":        {`SELECT * FROM status_page_domain_lookup('x.acme.com')`, []string{"public"}},
			"allowed":       {`SELECT status_page_domain_allowed('x.acme.com')`, []string{"public"}},
			"due":           {`SELECT * FROM status_page_domains_due(1)`, []string{"worker"}},
			"counts":        {`SELECT * FROM status_page_domain_counts()`, []string{"worker"}},
			"record_check":  {`SELECT * FROM status_page_domain_record_check(gen_random_uuid(), gen_random_uuid(), 'missing', 'missing', NULL)`, []string{"worker", "api"}},
			"held_by_other": {`SELECT status_page_domain_held_by_other('x.acme.com')`, []string{"api"}},
			"takeover":      {`SELECT * FROM status_page_domain_takeover('x.acme.com', gen_random_uuid(), '{}')`, []string{"api"}},
		}
		for name, c := range calls {
			for role, conn := range map[string]*pgx.Conn{"public": f.public, "worker": f.worker, "api": f.api} {
				_, err := conn.Exec(ctx, c.stmt)
				if denied := pgCode(err) == "42501"; denied == slices.Contains(c.roles, role) {
					t.Errorf("%s as %s: %v (allowed for %v)", name, role, err, c.roles)
				}
			}
		}
	})

	t.Run("a host is lowercase and plain", func(t *testing.T) {
		for _, host := range []string{"Status.Acme.com", "acme", "a.b", "status.acme.com.", "-x.acme.com", "x_y.acme.com", "*.acme.com", "sta tus.acme.com", strings.Repeat("a", 64) + ".acme.com"} {
			_, err := f.admin.Exec(ctx, `INSERT INTO status_page_domains (id, tenant_id, page_id, host, token) VALUES (gen_random_uuid(), $1, $2, $3, $4)`, tenantA, f.pageA, host, tokenA)
			if pgCode(err) != "23514" {
				t.Errorf("host %q: %v, want a check violation", host, err)
			}
		}
		_, err := f.admin.Exec(ctx, `INSERT INTO status_page_domains (id, tenant_id, page_id, host, token) VALUES (gen_random_uuid(), $1, $2, 'xn--80ak6aa92e.acme.com', 'not-a-token')`, tenantA, f.pageA)
		if pgCode(err) != "23514" {
			t.Errorf("bad token: %v", err)
		}
		_, err = f.admin.Exec(ctx, `INSERT INTO status_page_domains (id, tenant_id, page_id, host, token, state) VALUES (gen_random_uuid(), $1, $2, 'x.acme.com', $3, 'live')`, tenantA, f.pageA, tokenA)
		if pgCode(err) != "23514" {
			t.Errorf("bad state: %v", err)
		}
	})
}

// lookup and allowed serve exactly the verified and active hosts of live pages of active tenants.
func TestIntegrationStatusPageDomainsLookup(t *testing.T) {
	f := newDomainFixture(t, "spdomainslookup")
	cases := []struct {
		name          string
		state         string
		page          string
		set           string
		lookup, allow bool
	}{
		{"pending", "pending", f.pageA, "", false, false},
		{"verified", "verified", f.pageA, "", true, true},
		{"active", "active", f.pageA, "", true, true},
		{"failed", "failed", f.pageA, "", false, false},
		{"released", "active", f.pageA, "released_at = now()", false, false},
		{"disabled by the admin", "active", f.pageA, "disabled_at = now()", false, false},
		{"unpublished page", "active", f.pageAHidden, "", false, true},
	}
	for _, c := range cases {
		host := strings.ReplaceAll(c.name, " ", "-") + ".acme.com"
		var set []string
		if c.set != "" {
			set = append(set, c.set)
		}
		f.add(tenantA, c.page, host, c.state, set...)
		slug, found := f.lookup(host)
		if found != c.lookup || (found && slug != "acme") {
			t.Errorf("%s: lookup = %q, %v; want found %v", c.name, slug, found, c.lookup)
		}
		if got := f.allowed(host); got != c.allow {
			t.Errorf("%s: allowed = %v, want %v", c.name, got, c.allow)
		}
	}
	// Whole-organization and page states.
	f.add(tenantB, f.pageB, "status.beta.com", "verified")
	f.add(f.tenantC, f.pageC, "status.gamma.com", "active")
	pgtest.MustExec(t, f.admin, `UPDATE tenants SET status = 'suspended' WHERE id = '`+tenantB+`'`, `UPDATE tenants SET deleted_at = now() WHERE id = '`+f.tenantC+`'`)
	for _, host := range []string{"status.beta.com", "status.gamma.com"} {
		if _, found := f.lookup(host); found || f.allowed(host) {
			t.Errorf("%s of a suspended or deleted organization is served", host)
		}
	}
	pgtest.MustExec(t, f.admin, `UPDATE tenants SET status = 'active' WHERE id = '`+tenantB+`'`)
	if slug, found := f.lookup("status.beta.com"); !found || slug != "beta" {
		t.Errorf("reactivated organization: %q %v", slug, found)
	}
	pgtest.MustExec(t, f.admin, `UPDATE status_pages SET published = false, deleted_at = now() WHERE id = '`+f.pageB+`'`)
	if _, found := f.lookup("status.beta.com"); found || f.allowed("status.beta.com") {
		t.Error("host of a deleted page is served")
	}
	// Only the exact stored host matches: no case folding, no trailing dot, no pattern.
	for _, host := range []string{"", "STATUS.ACME.COM", "verified.acme.com.", "nope.example.org", "verified.acme.com' OR '1'='1", "%.acme.com", "verified.acme.com "} {
		if slug, found := f.lookup(host); found {
			t.Errorf("%q found as %q", host, slug)
		}
	}
	if _, found := f.lookup("verified.acme.com"); !found {
		t.Error("the plain host is not found")
	}
}

// Only rows that serve are unique by host; a primary is one per page and never released.
func TestIntegrationStatusPageDomainsUniqueness(t *testing.T) {
	f := newDomainFixture(t, "spdomainsunique")
	ctx := context.Background()
	exec := func(stmt string, args ...any) error { _, err := f.admin.Exec(ctx, stmt, args...); return err }

	// Several organizations may hold a pending claim, and a failed one, on the same host.
	f.add(tenantA, f.pageA, "status.shared.com", "pending")
	f.add(tenantB, f.pageB, "status.shared.com", "pending")
	f.add(f.tenantC, f.pageC, "status.shared.com", "failed")
	// The same organization cannot hold a host twice, whatever the state.
	err := exec(`INSERT INTO status_page_domains (id, tenant_id, page_id, host, token) VALUES (gen_random_uuid(), $1, $2, 'status.shared.com', $3)`, tenantA, f.pageAHidden, tokenA)
	if pgCode(err) != "23505" || !strings.Contains(err.Error(), "status_page_domains_tenant_host_idx") {
		t.Errorf("duplicate inside one organization: %v", err)
	}
	// One of them proves control: it serves, the others stay as they are.
	pgtest.MustExec(t, f.admin, `UPDATE status_page_domains SET state = 'verified' WHERE tenant_id = '`+tenantA+`' AND host = 'status.shared.com'`)
	err = exec(`UPDATE status_page_domains SET state = 'active' WHERE tenant_id = $1 AND host = 'status.shared.com'`, tenantB)
	if pgCode(err) != "23505" || !strings.Contains(err.Error(), "status_page_domains_host_idx") {
		t.Errorf("second serving row: %v", err)
	}
	// A failed row recovering to verified while another serves is refused too.
	err = exec(`UPDATE status_page_domains SET state = 'verified' WHERE tenant_id = $1 AND host = 'status.shared.com'`, f.tenantC)
	if pgCode(err) != "23505" {
		t.Errorf("failed to verified under a holder: %v", err)
	}
	// Releasing the holder frees the host for the next proof.
	pgtest.MustExec(t, f.admin, `UPDATE status_page_domains SET released_at = now() WHERE tenant_id = '`+tenantA+`' AND host = 'status.shared.com'`)
	if err := exec(`UPDATE status_page_domains SET state = 'active' WHERE tenant_id = $1 AND host = 'status.shared.com'`, tenantB); err != nil {
		t.Errorf("serving after the holder was released: %v", err)
	}
	// A released row may be re-added by its organization.
	if err := exec(`INSERT INTO status_page_domains (id, tenant_id, page_id, host, token) VALUES (gen_random_uuid(), $1, $2, 'status.shared.com', $3)`, tenantA, f.pageA, tokenA); err != nil {
		t.Errorf("re-adding a released host: %v", err)
	}

	// One primary per page; a released row cannot be primary.
	one := f.add(tenantA, f.pageA, "one.acme.com", "active", "is_primary = true")
	two := f.add(tenantA, f.pageA, "two.acme.com", "active")
	err = exec(`UPDATE status_page_domains SET is_primary = true WHERE id = $1`, two)
	if pgCode(err) != "23505" || !strings.Contains(err.Error(), "status_page_domains_primary_idx") {
		t.Errorf("second primary: %v", err)
	}
	if err := exec(`UPDATE status_page_domains SET is_primary = false WHERE id = $1`, one); err != nil {
		t.Fatal(err)
	}
	if err := exec(`UPDATE status_page_domains SET is_primary = true WHERE id = $1`, two); err != nil {
		t.Errorf("moving the primary: %v", err)
	}
	err = exec(`UPDATE status_page_domains SET released_at = now() WHERE id = $1`, two)
	if pgCode(err) != "23514" {
		t.Errorf("releasing a primary without clearing it: %v", err)
	}
}

// The state machine in SQL gives the same result as domains.Step for every combination.
func TestIntegrationStatusPageDomainsRecordCheckMatchesStep(t *testing.T) {
	f := newDomainFixture(t, "spdomainsstep")
	ctx := context.Background()
	proofs := map[domains.Proof]string{domains.Unknown: "unknown", domains.Missing: "missing", domains.Present: "present"}
	// Ages are in hours before the transaction's now(); -1 for a NULL last_proof_at.
	scenarios := []struct {
		name                     string
		created, proof, failures int
	}{
		{"fresh", 1, 1, 0},
		{"three days old, two failures", 72, 48, 2},
		{"eight days old, recent proof", 192, 6, 2},
		{"eight days old, stale proof, two failures", 192, 190, 2},
		{"eight days old, stale proof, one failure", 192, 190, 0},
		{"exactly seven days, third failure", 168, 168, 2},
		{"never proven", 192, -1, 2},
	}
	checked := 0
	for _, state := range []domains.State{domains.Pending, domains.Verified, domains.Active, domains.Failed} {
		for _, sc := range scenarios {
			for _, txt := range []domains.Proof{domains.Unknown, domains.Missing, domains.Present} {
				for _, cname := range []domains.Proof{domains.Unknown, domains.Missing, domains.Present} {
					name := fmt.Sprintf("%s/%s/txt=%s/cname=%s", state, sc.name, proofs[txt], proofs[cname])
					tx, err := f.admin.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					proof := "NULL"
					if sc.proof >= 0 {
						proof = fmt.Sprintf("now() - interval '%d hours'", sc.proof)
					}
					id := uuid.NewString()
					if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO status_page_domains (id, tenant_id, page_id, host, token, state, created_at, last_proof_at, check_failures)
						VALUES ('%s', '%s', '%s', 'step.acme.com', '%s', '%s', now() - interval '%d hours', %s, %d)`, id, tenantA, f.pageA, tokenA, state, sc.created, proof, sc.failures)); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					var now time.Time
					var created time.Time
					var lastProof *time.Time
					if err := tx.QueryRow(ctx, `SELECT now(), created_at, last_proof_at FROM status_page_domains WHERE id = $1`, id).Scan(&now, &created, &lastProof); err != nil {
						t.Fatal(err)
					}
					in := domains.Domain{State: state, CreatedAt: created, CheckFailures: sc.failures}
					if lastProof != nil {
						in.LastProofAt = *lastProof
					}
					want := domains.Step(in, domains.Facts{TXT: txt, CNAME: cname}, now)

					var gotState string
					var gotEvents []string
					if err := tx.QueryRow(ctx, `SELECT state, events FROM status_page_domain_record_check($1, $2, $3, $4, 'dns-error')`, tenantA, id, proofs[txt], proofs[cname]).Scan(&gotState, &gotEvents); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					var row struct {
						failures                      int
						proof, verified, checked, rel *time.Time
						lastError                     *string
					}
					if err := tx.QueryRow(ctx, `SELECT check_failures, last_proof_at, verified_at, last_checked_at, released_at, last_error FROM status_page_domains WHERE id = $1`, id).
						Scan(&row.failures, &row.proof, &row.verified, &row.checked, &row.rel, &row.lastError); err != nil {
						t.Fatal(err)
					}
					var events []string
					for _, e := range want.Events {
						events = append(events, string(e))
					}
					expired := slices.Contains(events, string(domains.EventExpired))
					switch {
					case gotState != string(want.Domain.State):
						t.Errorf("%s: state %s, Step says %s", name, gotState, want.Domain.State)
					case len(gotEvents) != len(events) || (len(events) > 0 && !slices.Equal(gotEvents, events)):
						t.Errorf("%s: events %v, Step says %v", name, gotEvents, events)
					case row.failures != want.Domain.CheckFailures:
						t.Errorf("%s: failures %d, Step says %d", name, row.failures, want.Domain.CheckFailures)
					case (row.proof == nil) != want.Domain.LastProofAt.IsZero() || (row.proof != nil && !row.proof.Equal(want.Domain.LastProofAt)):
						t.Errorf("%s: last proof %v, Step says %v", name, row.proof, want.Domain.LastProofAt)
					case (row.rel != nil) != expired:
						t.Errorf("%s: released %v, Step expired %v", name, row.rel, expired)
					case row.checked == nil || !row.checked.Equal(now) || row.lastError == nil || *row.lastError != "dns-error":
						t.Errorf("%s: last check %v error %v", name, row.checked, row.lastError)
					case slices.Contains(events, string(domains.EventVerified)) != (row.verified != nil):
						t.Errorf("%s: verified_at %v, Step events %v", name, row.verified, events)
					}
					_ = tx.Rollback(ctx)
					checked++
				}
			}
		}
	}
	if checked != 4*len(scenarios)*9 {
		t.Fatalf("checked %d combinations", checked)
	}
}

// record_check argument checks, the tenant guard of the api and the unique index when a host is held.
func TestIntegrationStatusPageDomainsRecordCheckGuards(t *testing.T) {
	f := newDomainFixture(t, "spdomainsguards")
	ctx := context.Background()
	mine := f.add(tenantA, f.pageA, "status.acme.com", "pending")
	released := f.add(tenantA, f.pageA, "old.acme.com", "pending", "released_at = now()")
	f.add(tenantB, f.pageB, "status.acme.com", "verified")
	call := func(conn *pgx.Conn, tenant, id, txt, cname string, errCode any) error {
		_, err := conn.Exec(ctx, `SELECT * FROM status_page_domain_record_check($1, $2, $3, $4, $5)`, tenant, id, txt, cname, errCode)
		return err
	}
	for _, bad := range [][2]string{{"yes", "missing"}, {"missing", ""}, {"PRESENT", "missing"}} {
		if err := call(f.admin, tenantA, mine, bad[0], bad[1], nil); pgCode(err) != "PRD01" {
			t.Errorf("%v: %v, want PRD01", bad, err)
		}
	}
	if err := call(f.admin, tenantA, mine, "missing", "missing", "boom"); pgCode(err) != "PRD01" {
		t.Errorf("bad error code: %v, want PRD01", err)
	}
	if err := call(f.admin, tenantA, uuid.NewString(), "missing", "missing", nil); pgCode(err) != "PRD03" {
		t.Errorf("unknown domain: %v, want PRD03", err)
	}
	if err := call(f.admin, tenantA, released, "missing", "missing", nil); pgCode(err) != "PRD03" {
		t.Errorf("released domain: %v, want PRD03", err)
	}
	if err := call(f.admin, tenantB, mine, "missing", "missing", nil); pgCode(err) != "PRD03" {
		t.Errorf("domain of another tenant by the worker: %v, want PRD03", err)
	}
	// The worker works across tenants; the api only inside its own.
	if err := call(f.worker, tenantA, mine, "missing", "missing", "txt-missing"); err != nil {
		t.Errorf("worker: %v", err)
	}
	err := inTenant(t, f.api, tenantB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT * FROM status_page_domain_record_check($1, $2, 'missing', 'missing', NULL)`, tenantA, mine)
		return err
	})
	if pgCode(err) != "PRD02" {
		t.Errorf("api for another tenant: %v, want PRD02", err)
	}
	err = inTenant(t, f.api, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT * FROM status_page_domain_record_check($1, $2, 'present', 'missing', NULL)`, tenantA, mine)
		return err
	})
	if pgCode(err) != "23505" {
		t.Errorf("verifying a host another organization holds: %v, want the unique violation", err)
	}
}

// Which checks are due, and that replicas do not share a domain.
func TestIntegrationStatusPageDomainsDue(t *testing.T) {
	f := newDomainFixture(t, "spdomainsdue")
	ctx := context.Background()
	type c struct {
		name, state, created, checked string
		due                           bool
	}
	cases := []c{
		{"never checked", "pending", "1 hour", "", true},
		{"pending first day, 5 minutes ago", "pending", "1 hour", "5 minutes", false},
		{"pending first day, 11 minutes ago", "pending", "1 hour", "11 minutes", true},
		{"pending later, 30 minutes ago", "pending", "2 days", "30 minutes", false},
		{"pending later, 61 minutes ago", "pending", "2 days", "61 minutes", true},
		{"verified, 5 hours ago", "verified", "9 days", "5 hours", false},
		{"verified, 6 hours ago", "verified", "9 days", "6 hours 1 minute", true},
		{"active, 5 hours ago", "active", "9 days", "5 hours", false},
		{"active, 7 hours ago", "active", "9 days", "7 hours", true},
		{"failed, 2 hours ago", "failed", "30 days", "2 hours", false},
		{"failed, 7 hours ago", "failed", "30 days", "7 hours", true},
	}
	want := map[string]bool{}
	for i, cs := range cases {
		host := fmt.Sprintf("due%d.acme.com", i)
		set := []string{fmt.Sprintf("created_at = now() - interval '%s'", cs.created)}
		if cs.checked != "" {
			set = append(set, fmt.Sprintf("last_checked_at = now() - interval '%s'", cs.checked))
		}
		f.add(tenantA, f.pageA, host, cs.state, set...)
		want[host] = cs.due
	}
	f.add(tenantA, f.pageA, "released.acme.com", "active", "released_at = now()")
	f.add(tenantB, f.pageB, "suspended.beta.com", "active")
	pgtest.MustExec(t, f.admin, `UPDATE tenants SET status = 'suspended' WHERE id = '`+tenantB+`'`)
	f.add(f.tenantC, f.pageC, "deleted.gamma.com", "active")
	pgtest.MustExec(t, f.admin, `UPDATE tenants SET deleted_at = now() WHERE id = '`+f.tenantC+`'`)

	hosts := func(conn *pgx.Conn, limit int) []string {
		t.Helper()
		rows, err := conn.Query(ctx, `SELECT host FROM status_page_domains_due($1)`, limit)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(out)
		return out
	}
	got := hosts(f.worker, 100)
	for host, due := range want {
		if slices.Contains(got, host) != due {
			t.Errorf("%s: due = %v, want %v (got %v)", host, slices.Contains(got, host), due, got)
		}
	}
	for _, h := range []string{"released.acme.com", "suspended.beta.com", "deleted.gamma.com"} {
		if slices.Contains(got, h) {
			t.Errorf("%s is due", h)
		}
	}
	if n := len(hosts(f.worker, 2)); n != 2 {
		t.Errorf("limit 2 returned %d", n)
	}
	// The never-checked row comes first.
	var first string
	if err := f.worker.QueryRow(ctx, `SELECT host FROM status_page_domains_due(1)`).Scan(&first); err != nil || first != "due0.acme.com" {
		t.Errorf("first due = %q (%v), want the never checked one", first, err)
	}

	// A second replica gets none of the rows the first one holds.
	tx, err := f.worker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT host FROM status_page_domains_due(100)`)
	if err != nil {
		t.Fatal(err)
	}
	held, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(held) != len(got) {
		t.Fatalf("first replica got %v (%v)", held, err)
	}
	other := f.env.Conn(t, f.env.WorkerURL)
	if rest := hosts(other, 100); len(rest) != 0 {
		t.Errorf("second replica got %v while the first holds them", rest)
	}
}

// who holds a host, and the takeover rules: only a TXT proof of the claimant takes a host, a stale
// CNAME never does, and both tokens in DNS leave the host with the holder.
func TestIntegrationStatusPageDomainsTakeover(t *testing.T) {
	f := newDomainFixture(t, "spdomainstakeover")
	ctx := context.Background()
	asA := func(fn func(pgx.Tx) error) error { return inTenant(t, f.api, tenantA, fn) }
	var results []string
	takeover := func(tx pgx.Tx, host, claimant string, dns ...string) (granted bool, prevTenant, prevPage *string, err error) {
		err = tx.QueryRow(ctx, `SELECT granted, previous_tenant_id::text, previous_page_id::text FROM status_page_domain_takeover($1, $2, $3)`, host, claimant, dns).Scan(&granted, &prevTenant, &prevPage)
		return
	}
	state := func(tenant, host string) (string, bool) {
		var st string
		var rel bool
		if err := f.admin.QueryRow(ctx, `SELECT state, released_at IS NOT NULL FROM status_page_domains WHERE tenant_id = $1 AND host = $2 ORDER BY created_at DESC LIMIT 1`, tenant, host).Scan(&st, &rel); err != nil {
			t.Fatal(err)
		}
		return st, rel
	}
	_ = results

	t.Run("held by other", func(t *testing.T) {
		f.add(tenantB, f.pageB, "held.example.org", "verified")
		f.add(tenantB, f.pageB, "pending.example.org", "pending")
		f.add(tenantB, f.pageB, "failed.example.org", "failed")
		f.add(tenantB, f.pageB, "released.example.org", "active", "released_at = now()")
		f.add(tenantA, f.pageA, "mine.example.org", "active")
		for host, want := range map[string]bool{"held.example.org": true, "pending.example.org": false, "failed.example.org": false, "released.example.org": false, "mine.example.org": false, "nobody.example.org": false} {
			err := asA(func(tx pgx.Tx) error {
				var got bool
				if err := tx.QueryRow(ctx, `SELECT status_page_domain_held_by_other($1)`, host).Scan(&got); err != nil {
					return err
				}
				if got != want {
					t.Errorf("held_by_other(%s) = %v, want %v", host, got, want)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("the claimant's token alone takes the host", func(t *testing.T) {
		holder := f.add(tenantB, f.pageB, "take.example.org", "active", "is_primary = true")
		claim := f.add(tenantA, f.pageA, "take.example.org", "pending")
		err := asA(func(tx pgx.Tx) error {
			ok, pt, pp, err := takeover(tx, "take.example.org", claim, "proreta-verify-noise", tokenA)
			if err != nil {
				return err
			}
			if !ok || pt == nil || *pt != tenantB || pp == nil || *pp != f.pageB {
				t.Errorf("granted %v, previous %v %v", ok, pt, pp)
			}
			// The claimant's check can now verify it.
			var st string
			if err := tx.QueryRow(ctx, `SELECT state FROM status_page_domain_record_check($1, $2, 'present', 'present', NULL)`, tenantA, claim).Scan(&st); err != nil {
				return err
			}
			if st != "active" {
				t.Errorf("claimant state %s", st)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		var rel bool
		var primary bool
		if err := f.admin.QueryRow(ctx, `SELECT released_at IS NOT NULL, is_primary FROM status_page_domains WHERE id = $1`, holder).Scan(&rel, &primary); err != nil || !rel || primary {
			t.Errorf("holder released %v primary %v (%v)", rel, primary, err)
		}
		if slug, found := f.lookup("take.example.org"); !found || slug != "acme" {
			t.Errorf("host serves %q %v after the takeover", slug, found)
		}
	})

	t.Run("both tokens in DNS: the holder keeps it", func(t *testing.T) {
		f.add(tenantB, f.pageB, "both.example.org", "verified")
		claim := f.add(tenantA, f.pageA, "both.example.org", "pending")
		err := asA(func(tx pgx.Tx) error {
			ok, pt, pp, err := takeover(tx, "both.example.org", claim, tokenA, tokenB)
			if err != nil || ok || pt != nil || pp != nil {
				t.Errorf("granted %v previous %v %v (%v)", ok, pt, pp, err)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if st, rel := state(tenantB, "both.example.org"); st != "verified" || rel {
			t.Errorf("holder is %s released %v", st, rel)
		}
	})

	t.Run("a stale CNAME or a foreign token takes nothing", func(t *testing.T) {
		f.add(tenantB, f.pageB, "stale.example.org", "active")
		claim := f.add(tenantA, f.pageA, "stale.example.org", "pending")
		for name, dns := range map[string][]string{"no TXT at all (a CNAME to us is all there is)": nil, "only the holder's token": {tokenB}, "only a stranger's token": {tokenC}} {
			err := asA(func(tx pgx.Tx) error {
				ok, _, _, err := takeover(tx, "stale.example.org", claim, dns...)
				if err != nil || ok {
					t.Errorf("%s: granted %v (%v)", name, ok, err)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if st, rel := state(tenantB, "stale.example.org"); st != "active" || rel {
			t.Errorf("holder is %s released %v", st, rel)
		}
	})

	t.Run("nobody holds it", func(t *testing.T) {
		claim := f.add(tenantA, f.pageA, "free.example.org", "pending")
		f.add(tenantB, f.pageB, "free.example.org", "failed")
		err := asA(func(tx pgx.Tx) error {
			ok, pt, pp, err := takeover(tx, "free.example.org", claim, tokenA)
			if err != nil || !ok || pt != nil || pp != nil {
				t.Errorf("granted %v previous %v %v (%v)", ok, pt, pp, err)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		serving := f.add(tenantA, f.pageA, "serving.example.org", "verified")
		other := f.add(tenantB, f.pageB, "foreign.example.org", "pending")
		for name, c := range map[string]struct{ host, id string }{
			"a claimant that already serves": {"serving.example.org", serving},
			"a claimant of another tenant":   {"foreign.example.org", other},
			"an unknown claimant":            {"serving.example.org", uuid.NewString()},
			"a host that is not the claim's": {"other.example.org", serving},
		} {
			err := asA(func(tx pgx.Tx) error {
				_, _, _, err := takeover(tx, c.host, c.id, tokenA)
				return err
			})
			if pgCode(err) != "PRD03" {
				t.Errorf("%s: %v, want PRD03", name, err)
			}
		}
		if _, err := f.admin.Exec(ctx, `SELECT * FROM status_page_domain_takeover('serving.example.org', $1, '{}')`, serving); pgCode(err) != "PRD02" {
			t.Errorf("without a tenant: %v, want PRD02", err)
		}
	})
}

// counts has every state, skips released rows and carries no host names.
func TestIntegrationStatusPageDomainsCountsAndPageDelete(t *testing.T) {
	f := newDomainFixture(t, "spdomainscounts")
	ctx := context.Background()
	counts := func() string {
		rows, err := f.worker.Query(ctx, `SELECT state || '=' || n FROM status_page_domain_counts()`)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(out, ",")
	}
	if got := counts(); got != "active=0,failed=0,pending=0,verified=0" {
		t.Fatalf("empty counts = %s", got)
	}
	primary := f.add(tenantA, f.pageA, "a1.acme.com", "active", "is_primary = true")
	f.add(tenantA, f.pageA, "a2.acme.com", "verified")
	f.add(tenantA, f.pageA, "a3.acme.com", "pending")
	f.add(tenantA, f.pageAHidden, "a4.acme.com", "failed")
	f.add(tenantA, f.pageA, "a5.acme.com", "active", "released_at = now()")
	f.add(tenantB, f.pageB, "b1.beta.com", "active", "is_primary = true")
	if got := counts(); got != "active=2,failed=1,pending=1,verified=1" {
		t.Fatalf("counts = %s", got)
	}

	// Deleting a page, as the api does, releases its domains and clears its primary, and no other page's.
	err := inTenant(t, f.api, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE status_pages SET published = false, deleted_at = now() WHERE id = $1`, f.pageA)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var live int
	if err := f.admin.QueryRow(ctx, `SELECT count(*) FROM status_page_domains WHERE page_id = $1 AND released_at IS NULL`, f.pageA).Scan(&live); err != nil || live != 0 {
		t.Errorf("%d domains of the deleted page are not released (%v)", live, err)
	}
	var prim bool
	if err := f.admin.QueryRow(ctx, `SELECT is_primary FROM status_page_domains WHERE id = $1`, primary).Scan(&prim); err != nil || prim {
		t.Errorf("primary of a deleted page: %v (%v)", prim, err)
	}
	if got := counts(); got != "active=1,failed=1,pending=0,verified=0" {
		t.Errorf("counts after the delete = %s", got)
	}
	if _, found := f.lookup("a1.acme.com"); found {
		t.Error("a host of a deleted page is served")
	}
	// The host can be proven by someone else at once.
	f.add(tenantB, f.pageB, "a2.acme.com", "verified")
	if slug, found := f.lookup("a2.acme.com"); !found || slug != "beta" {
		t.Errorf("host after release: %q %v", slug, found)
	}
	// Saving the deleted page again changes nothing for the live ones.
	err = inTenant(t, f.api, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE status_pages SET title = 'Hidden 2' WHERE id = $1`, f.pageAHidden)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var pending string
	if err := f.admin.QueryRow(ctx, `SELECT state FROM status_page_domains WHERE host = 'a4.acme.com'`).Scan(&pending); err != nil || pending != "failed" {
		t.Errorf("domain of the other page: %q (%v)", pending, err)
	}
}
