package specula_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	tenantA = "01920000-0000-7000-8000-00000000000a"
	tenantB = "01920000-0000-7000-8000-00000000000b"
)

func TestIntegrationSpecula(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("specula"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal'), ('`+tenantB+`', 'b', 'B', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	apiPool := env.Pool(t, env.APIURL, 4)
	worker := env.Pool(t, env.WorkerURL, 4)
	svc := specula.NewService(apiPool)
	sched := scheduler.New()
	mon := monitors.NewService(apiPool, sched, incidents.NewService(sched))
	a := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	b := specula.Actor{TenantID: uuid.MustParse(tenantB), UserID: user}

	t.Run("create, authenticate and the token is shown once", func(t *testing.T) {
		s, token, err := svc.Create(ctx, a, specula.CreateInput{Name: " Prod cluster ", AllProjects: true})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(token, specula.TokenPrefix) || s.Name != "Prod cluster" || !s.AllProjects || s.RevokedAt != nil {
			t.Fatalf("created %+v token %q", s, token)
		}
		var stored []byte
		if err := admin.QueryRow(ctx, `SELECT token_hash FROM specula_agents WHERE id = $1`, s.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if string(stored) != string(specula.HashToken(token)) || strings.Contains(string(stored), token) {
			t.Fatal("stored token hash is wrong")
		}
		id, err := specula.Authenticate(ctx, worker, token)
		if err != nil || id.SpeculaID != s.ID || id.TenantID != a.TenantID || id.LocationID != s.LocationID {
			t.Fatalf("authenticate: %+v %v", id, err)
		}
		var kind string
		if err := admin.QueryRow(ctx, `SELECT kind FROM probe_locations WHERE id = $1`, s.LocationID).Scan(&kind); err != nil || kind != "agent" {
			t.Fatalf("location kind %q %v", kind, err)
		}
		if err := specula.Hello(ctx, worker, s.ID, "0.1.0", "pod-1"); err != nil {
			t.Fatal(err)
		}
		got, err := svc.Get(ctx, a.TenantID, s.ID)
		if err != nil || got.Version != "0.1.0" || got.Hostname != "pod-1" || got.LastSeenAt == nil {
			t.Fatalf("after hello: %+v %v", got, err)
		}
		for _, bad := range []string{"", "nope", specula.TokenPrefix + "x", strings.Replace(token, "spc_", "spd_", 1)} {
			if _, err := specula.Authenticate(ctx, worker, bad); !errors.Is(err, specula.ErrUnauthorized) {
				t.Errorf("token %q: %v", bad, err)
			}
		}
	})

	t.Run("organizations are isolated", func(t *testing.T) {
		s, token, err := svc.Create(ctx, a, specula.CreateInput{Name: "Isolated", AllProjects: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Get(ctx, b.TenantID, s.ID); !errors.Is(err, specula.ErrNotFound) {
			t.Fatalf("other tenant get: %v", err)
		}
		if list, err := svc.List(ctx, b.TenantID); err != nil || len(list) != 0 {
			t.Fatalf("other tenant list: %v %v", list, err)
		}
		if err := svc.Revoke(ctx, b, s.ID); !errors.Is(err, specula.ErrNotFound) {
			t.Fatalf("other tenant revoke: %v", err)
		}
		if _, err := specula.Authenticate(ctx, worker, token); err != nil {
			t.Fatalf("still valid: %v", err)
		}
		// The same name is fine in another organization.
		if _, _, err := svc.Create(ctx, b, specula.CreateInput{Name: "Isolated", AllProjects: true}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("validation", func(t *testing.T) {
		field := func(err error) string {
			var v *validation.Error
			if !errors.As(err, &v) || len(v.Fields) != 1 {
				t.Fatalf("want one field error, got %v", err)
			}
			return v.Fields[0].Field
		}
		_, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "  ", AllProjects: true})
		if field(err) != "name" {
			t.Fatal(err)
		}
		_, _, err = svc.Create(ctx, a, specula.CreateInput{Name: strings.Repeat("a", 81), AllProjects: true})
		if field(err) != "name" {
			t.Fatal(err)
		}
		_, _, err = svc.Create(ctx, a, specula.CreateInput{Name: "No projects"})
		if field(err) != "projectIds" {
			t.Fatal(err)
		}
		_, _, err = svc.Create(ctx, a, specula.CreateInput{Name: "Stranger", ProjectIDs: []uuid.UUID{uuid.New()}})
		if field(err) != "projectIds" {
			t.Fatal(err)
		}
		if _, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "Dup", AllProjects: true}); err != nil {
			t.Fatal(err)
		}
		_, _, err = svc.Create(ctx, a, specula.CreateInput{Name: "dup", AllProjects: true})
		if field(err) != "name" {
			t.Fatal(err)
		}
	})

	t.Run("a revoked token stops working at once and the location hands out no jobs", func(t *testing.T) {
		s, token, err := svc.Create(ctx, a, specula.CreateInput{Name: "Short lived", AllProjects: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Revoke(ctx, a, s.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := specula.Authenticate(ctx, worker, token); !errors.Is(err, specula.ErrUnauthorized) {
			t.Fatalf("authenticate after revoke: %v", err)
		}
		if err := specula.Hello(ctx, worker, s.ID, "v", "h"); !errors.Is(err, specula.ErrUnauthorized) {
			t.Fatalf("hello after revoke: %v", err)
		}
		if err := svc.Revoke(ctx, a, s.ID); !errors.Is(err, specula.ErrNotFound) {
			t.Fatalf("second revoke: %v", err)
		}
		var status string
		if err := admin.QueryRow(ctx, `SELECT status FROM probe_locations WHERE id = $1`, s.LocationID).Scan(&status); err != nil || status != "disabled" {
			t.Fatalf("location status %q %v", status, err)
		}
		// The name is free again.
		if _, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "Short lived", AllProjects: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Update(ctx, a, s.ID, specula.UpdateInput{Name: ptr("x")}); !errors.Is(err, specula.ErrNotFound) {
			t.Fatalf("update revoked: %v", err)
		}
	})

	t.Run("a suspended organization cannot authenticate", func(t *testing.T) {
		_, token, err := svc.Create(ctx, b, specula.CreateInput{Name: "Suspended soon", AllProjects: true})
		if err != nil {
			t.Fatal(err)
		}
		pgtest.MustExec(t, admin, `UPDATE tenants SET status = 'suspended' WHERE id = '`+tenantB+`'`)
		defer pgtest.MustExec(t, admin, `UPDATE tenants SET status = 'active' WHERE id = '`+tenantB+`'`)
		if _, err := specula.Authenticate(ctx, worker, token); !errors.Is(err, specula.ErrUnauthorized) {
			t.Fatalf("suspended: %v", err)
		}
	})

	t.Run("sharing decides which monitors the location may claim", func(t *testing.T) {
		actor := monitors.Actor{TenantID: a.TenantID, UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
		url := "https://internal.example.com/health"
		m, err := mon.Create(ctx, actor, monitors.CreateInput{Name: "Internal", HTTP: &monitors.HTTPSettings{URL: &url}})
		if err != nil {
			t.Fatal(err)
		}
		var otherProject uuid.UUID
		pgtest.MustExec(t, admin, `INSERT INTO projects (id, tenant_id, name, slug) VALUES (gen_random_uuid(), '`+tenantA+`', 'Other', 'other')`)
		if err := admin.QueryRow(ctx, `SELECT id FROM projects WHERE tenant_id = $1 AND slug = 'other'`, tenantA).Scan(&otherProject); err != nil {
			t.Fatal(err)
		}
		s, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "Shared", ProjectIDs: []uuid.UUID{otherProject}})
		if err != nil {
			t.Fatal(err)
		}
		code := locationCode(t, ctx, admin, s.LocationID)
		pgtest.MustExec(t, admin,
			`INSERT INTO monitor_locations (tenant_id, monitor_id, location_id) VALUES ('`+tenantA+`', '`+m.ID.String()+`', '`+s.LocationID.String()+`')`,
			`INSERT INTO monitor_location_state (tenant_id, monitor_id, location_id, next_run_at) VALUES ('`+tenantA+`', '`+m.ID.String()+`', '`+s.LocationID.String()+`', now() - interval '1 minute')`)
		claim := func() int {
			t.Helper()
			jobs, err := sched.Claim(ctx, worker, 10, code)
			if err != nil {
				t.Fatal(err)
			}
			// Free the lease again so the next claim sees the row.
			pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET lease_token = NULL, lease_until = NULL WHERE monitor_id = '`+m.ID.String()+`'`)
			return len(jobs)
		}
		if n := claim(); n != 0 {
			t.Fatalf("a project outside the list was claimed (%d)", n)
		}
		if _, err := svc.Update(ctx, a, s.ID, specula.UpdateInput{ProjectIDs: &[]uuid.UUID{m.ProjectID}}); err != nil {
			t.Fatal(err)
		}
		if n := claim(); n != 1 {
			t.Fatalf("a shared project was not claimed (%d)", n)
		}
		if _, err := svc.Update(ctx, a, s.ID, specula.UpdateInput{AllProjects: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		if n := claim(); n != 1 {
			t.Fatalf("all projects: %d", n)
		}
		// The location of another organization's Specula never hands this tenant's monitors out.
		sb, _, err := svc.Create(ctx, b, specula.CreateInput{Name: "Other org", AllProjects: true})
		if err != nil {
			t.Fatal(err)
		}
		pgtest.MustExec(t, admin, `ALTER TABLE monitor_location_state DISABLE TRIGGER ALL`,
			`UPDATE monitor_location_state SET location_id = '`+sb.LocationID.String()+`' WHERE monitor_id = '`+m.ID.String()+`' AND location_id = '`+s.LocationID.String()+`'`)
		jobs, err := sched.Claim(ctx, worker, 10, locationCode(t, ctx, admin, sb.LocationID))
		if err != nil || len(jobs) != 0 {
			t.Fatalf("cross-tenant claim: %v %v", jobs, err)
		}
	})

	t.Run("a revoked Specula's location is not claimed from", func(t *testing.T) {
		s, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "Revoked claim", AllProjects: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Revoke(ctx, a, s.ID); err != nil {
			t.Fatal(err)
		}
		jobs, err := sched.Claim(ctx, worker, 10, locationCode(t, ctx, admin, s.LocationID))
		if err != nil || len(jobs) != 0 {
			t.Fatalf("claim: %v %v", jobs, err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

func locationCode(t *testing.T, ctx context.Context, admin *pgx.Conn, id uuid.UUID) string {
	t.Helper()
	var code string
	if err := admin.QueryRow(ctx, `SELECT code FROM probe_locations WHERE id = $1`, id).Scan(&code); err != nil {
		t.Fatal(err)
	}
	return code
}
