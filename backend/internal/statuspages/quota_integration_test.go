package statuspages_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationStatusPageQuotaIsPerScope checks that max_status_pages counts per project and,
// separately, for the organization-level pages, and that raising the limit is all it takes to
// allow several pages in one project.
func TestIntegrationStatusPageQuotaIsPerScope(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("statuspagesquota"))
	ctx := context.Background()
	f := fixture{t: t, admin: env.AdminConn(t)}
	tenant := uuid.NewString()
	f.exec(`INSERT INTO tenants (id, slug, name) VALUES ($1, 'sp-q', 'Q')`, tenant)
	f.exec(`INSERT INTO users (id, email) VALUES ($1, 'owner@example.com')`, owner)
	p1, p2 := uuid.New(), uuid.New()
	f.exec(`INSERT INTO projects (id, tenant_id, name, slug) VALUES ($1, $3, 'One', 'one'), ($2, $3, 'Two', 'two')`, p1, p2, tenant)

	svc := statuspages.NewService(env.Pool(t, env.APIURL, 8), statuspages.Options{Clock: clock.NewFake(time.Now().UTC())})
	a := ownerOf(tenant)
	create := func(slug string, project *uuid.UUID) (statuspages.Page, error) {
		return svc.Create(ctx, a, statuspages.CreateInput{ProjectID: project, Slug: slug, Title: slug})
	}
	mustLimit := func(err error, scope string) {
		t.Helper()
		var lerr *plans.LimitError
		if !errors.As(err, &lerr) || lerr.Scope != scope || lerr.Max != 1 {
			t.Fatalf("want a limit error with scope %q and max 1, got %v", scope, err)
		}
	}

	// The free plan allows one page per scope: each project and the organization get their own.
	first, err := create("q-one", &p1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create("q-two", &p2); err != nil {
		t.Fatalf("a second project must get its own page: %v", err)
	}
	if _, err := create("q-org", nil); err != nil {
		t.Fatalf("the organization-level page is counted separately: %v", err)
	}
	_, err = create("q-one-b", &p1)
	mustLimit(err, "project")
	_, err = create("q-org-b", nil)
	mustLimit(err, "organization")

	// Raising the limit is the only change needed for several pages in one project.
	f.exec(`UPDATE tenants SET limits = '{"max_status_pages": 2}' WHERE id = $1`, tenant)
	if _, err := create("q-one-b", &p1); err != nil {
		t.Fatalf("after raising the limit: %v", err)
	}
	_, err = create("q-one-c", &p1)
	var lerr *plans.LimitError
	if !errors.As(err, &lerr) || lerr.Max != 2 {
		t.Fatalf("third page in the project: %v", err)
	}
	pages, err := svc.List(ctx, a, &p1)
	if err != nil || len(pages) != 2 {
		t.Fatalf("list of the project: %d pages, %v", len(pages), err)
	}

	// A deleted page frees its slot.
	f.exec(`UPDATE tenants SET limits = '{}' WHERE id = $1`, tenant)
	if _, err := create("q-two-b", &p2); err == nil {
		t.Fatal("project two is at the limit of 1")
	}
	if err := svc.Delete(ctx, a, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := create("q-one-d", &p1); err == nil {
		t.Fatal("project one still holds one page (limit 1 again)")
	}
	pages, _ = svc.List(ctx, a, &p1)
	for _, p := range pages {
		if err := svc.Delete(ctx, a, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := create("q-one-e", &p1); err != nil {
		t.Fatalf("a deleted page frees its slot: %v", err)
	}
}

// TestIntegrationStatusPageQuotaConcurrent runs many creates in one project at once: the tenant
// lock keeps the count true, so exactly the limit succeeds.
func TestIntegrationStatusPageQuotaConcurrent(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("statuspagesconc"))
	ctx := context.Background()
	f := fixture{t: t, admin: env.AdminConn(t)}
	tenant := uuid.NewString()
	f.exec(`INSERT INTO tenants (id, slug, name, limits) VALUES ($1, 'sp-c', 'C', '{"max_status_pages": 3}')`, tenant)
	f.exec(`INSERT INTO users (id, email) VALUES ($1, 'owner@example.com')`, owner)
	p := uuid.New()
	f.exec(`INSERT INTO projects (id, tenant_id, name, slug) VALUES ($1, $2, 'One', 'one')`, p, tenant)

	svc := statuspages.NewService(env.Pool(t, env.APIURL, 12), statuspages.Options{Clock: clock.NewFake(time.Now().UTC())})
	a := ownerOf(tenant)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Create(ctx, a, statuspages.CreateInput{ProjectID: &p, Slug: fmt.Sprintf("conc-%d", i), Title: "t"})
			var lerr *plans.LimitError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.As(err, &lerr):
				refused++
			default:
				t.Errorf("create %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if ok != 3 || refused != 7 {
		t.Fatalf("created %d, refused %d; want 3 and 7", ok, refused)
	}
}
