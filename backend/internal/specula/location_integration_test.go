package specula_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationMonitorLocation(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculaloc"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	apiPool := env.Pool(t, env.APIURL, 4)
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc)
	svc := specula.NewService(apiPool)
	ma := monitors.Actor{TenantID: uuid.MustParse(tenantA), UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	sa := specula.Actor{TenantID: ma.TenantID, UserID: user}
	sb := specula.Actor{TenantID: uuid.MustParse(tenantB), UserID: user}

	var otherProject uuid.UUID
	pgtest.MustExec(t, admin, `INSERT INTO projects (id, tenant_id, name, slug) VALUES (gen_random_uuid(), '`+tenantA+`', 'Other', 'other')`)
	if err := admin.QueryRow(ctx, `SELECT id FROM projects WHERE tenant_id = $1 AND slug = 'other'`, tenantA).Scan(&otherProject); err != nil {
		t.Fatal(err)
	}
	all, _, err := svc.Create(ctx, sa, specula.CreateInput{Name: "All projects", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	onlyOther, _, err := svc.Create(ctx, sa, specula.CreateInput{Name: "Only Other", ProjectIDs: []uuid.UUID{otherProject}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := svc.Create(ctx, sb, specula.CreateInput{Name: "Foreign", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	revoked, _, err := svc.Create(ctx, sa, specula.CreateInput{Name: "Revoked", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, sa, revoked.ID); err != nil {
		t.Fatal(err)
	}

	privateURL, publicURL := "http://orders.shop.svc:8080/health", "https://example.com/health"
	field := func(err error, want string) {
		t.Helper()
		var v *validation.Error
		if !errors.As(err, &v) || len(v.Fields) != 1 || v.Fields[0].Field != want {
			t.Fatalf("want one field error on %s, got %v", want, err)
		}
	}
	locations := func(id uuid.UUID) (n int, code string) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT count(*), COALESCE(min(l.code), '') FROM monitor_location_state s JOIN probe_locations l ON l.id = s.location_id WHERE s.monitor_id = $1`, id).Scan(&n, &code); err != nil {
			t.Fatal(err)
		}
		return n, code
	}
	create := func(url string, id *uuid.UUID) (monitors.Monitor, error) {
		return mon.Create(ctx, ma, monitors.CreateInput{Name: "m", SpeculaID: id, HTTP: &monitors.HTTPSettings{URL: &url}})
	}

	t.Run("create at a Specula accepts private targets and runs only there", func(t *testing.T) {
		m, err := create(privateURL, &all.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n, code := locations(m.ID); n != 1 || code != "specula-"+stripDashes(all.ID) {
			t.Fatalf("locations %d %q", n, code)
		}
		if m.URL != privateURL {
			t.Fatalf("url %q", m.URL)
		}
		// The same target from Proreta's own worker is refused.
		_, err = create(privateURL, nil)
		field(err, "http.url")
	})

	t.Run("a Specula the project may not use is refused", func(t *testing.T) {
		for name, id := range map[string]uuid.UUID{"not shared with the project": onlyOther.ID, "other organization": foreign.ID, "revoked": revoked.ID, "unknown": uuid.New()} {
			_, err := create(privateURL, &id)
			t.Run(name, func(t *testing.T) { field(err, "speculaId") })
		}
		hb := 300
		_, err := mon.Create(ctx, ma, monitors.CreateInput{Type: "heartbeat", Name: "hb", SpeculaID: &all.ID, Heartbeat: &monitors.HeartbeatSettings{ExpectedIntervalSeconds: &hb}})
		field(err, "speculaId")
	})

	t.Run("moving a monitor between the worker and a Specula", func(t *testing.T) {
		m, err := create(publicURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n, code := locations(m.ID); n != 1 || code != "default" {
			t.Fatalf("start: %d %q", n, code)
		}
		// An open incident ends with its own reason when the monitor moves.
		incID := uuid.New().String()
		pgtest.MustExec(t, admin,
			`INSERT INTO incidents (id, tenant_id, monitor_id, started_at) VALUES ('`+incID+`', '`+tenantA+`', '`+m.ID.String()+`', now())`,
			`UPDATE monitor_state SET current_incident_id = '`+incID+`', status = 'down' WHERE monitor_id = '`+m.ID.String()+`'`)
		if _, err := mon.Update(ctx, ma, m.ID, monitors.UpdateInput{Location: &monitors.Location{SpeculaID: &all.ID}}); err != nil {
			t.Fatal(err)
		}
		if n, code := locations(m.ID); n != 1 || code != "specula-"+stripDashes(all.ID) {
			t.Fatalf("after move: %d %q", n, code)
		}
		var reason string
		if err := admin.QueryRow(ctx, `SELECT resolve_reason FROM incidents WHERE monitor_id = $1`, m.ID).Scan(&reason); err != nil || reason != "location_changed" {
			t.Fatalf("incident reason %q %v", reason, err)
		}
		// A private target can now be saved; back to the worker it cannot.
		if _, err := mon.Update(ctx, ma, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{URL: &privateURL}}); err != nil {
			t.Fatal(err)
		}
		_, err = mon.Update(ctx, ma, m.ID, monitors.UpdateInput{Location: &monitors.Location{}})
		field(err, "http.url")
		if n, code := locations(m.ID); n != 1 || code != "specula-"+stripDashes(all.ID) {
			t.Fatalf("a refused move changed the location: %d %q", n, code)
		}
		if _, err := mon.Update(ctx, ma, m.ID, monitors.UpdateInput{Location: &monitors.Location{}, HTTP: &monitors.HTTPSettings{URL: &publicURL}}); err != nil {
			t.Fatal(err)
		}
		if n, code := locations(m.ID); n != 1 || code != "default" {
			t.Fatalf("back on the worker: %d %q", n, code)
		}
		// Results of the earlier location stay readable: its assignment row is kept.
		var assigned int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM monitor_locations WHERE monitor_id = $1`, m.ID).Scan(&assigned); err != nil || assigned != 2 {
			t.Fatalf("assignments %d %v", assigned, err)
		}
		// A move to a Specula the project may not use changes nothing.
		_, err = mon.Update(ctx, ma, m.ID, monitors.UpdateInput{Location: &monitors.Location{SpeculaID: &onlyOther.ID}})
		field(err, "speculaId")
		// Leaving the location unchanged is not a move.
		if _, err := mon.Update(ctx, ma, m.ID, monitors.UpdateInput{Location: &monitors.Location{}}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("moving the monitor to a project the Specula is not shared with", func(t *testing.T) {
		m, err := create(privateURL, &all.ID)
		if err != nil {
			t.Fatal(err)
		}
		pgtest.MustExec(t, admin, `UPDATE specula_agents SET all_projects = false WHERE id = '`+all.ID.String()+`'`)
		defer pgtest.MustExec(t, admin, `UPDATE specula_agents SET all_projects = true WHERE id = '`+all.ID.String()+`'`)
		_, err = mon.Update(ctx, ma, m.ID, monitors.UpdateInput{ProjectID: &otherProject})
		field(err, "speculaId")
	})
}

func stripDashes(id uuid.UUID) string {
	b := make([]byte, 0, 32)
	for _, c := range []byte(id.String()) {
		if c != '-' {
			b = append(b, c)
		}
	}
	return string(b)
}
