package specula_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// Registering a Specula counts against max_agents; a revoked one frees its slot, and concurrent
// registrations cannot exceed the limit.
func TestIntegrationSpeculaQuota(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculaquota"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name, limits) VALUES ('`+tenantA+`', 'a', 'A', '{"max_agents": 3}')`,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantB+`', 'b', 'B')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	svc := specula.NewService(env.Pool(t, env.APIURL, 16))
	a := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	b := specula.Actor{TenantID: uuid.MustParse(tenantB), UserID: user}

	create := func(act specula.Actor, name string) (specula.Specula, error) {
		s, _, err := svc.Create(ctx, act, specula.CreateInput{Name: name, AllProjects: true})
		return s, err
	}
	var lerr *plans.LimitError

	// The free plan allows one.
	if _, err := create(b, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := create(b, "two"); !errors.As(err, &lerr) || lerr.Resource != plans.ResourceAgent || lerr.Max != 1 {
		t.Fatalf("over the free limit: %v", err)
	}

	// A revoked Specula frees its slot; the override of tenant A allows three.
	first, err := create(a, "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, a, first.ID); err != nil {
		t.Fatal(err)
	}
	// 20 concurrent registrations against 3 slots: exactly 3 win.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := create(a, fmt.Sprintf("racer-%d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.As(err, &lerr):
				refused++
			default:
				t.Errorf("racer %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if ok != 3 || refused != 17 {
		t.Fatalf("created %d, refused %d; want 3 and 17", ok, refused)
	}
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM specula_agents WHERE tenant_id = $1 AND revoked_at IS NULL`, tenantA).Scan(&n); err != nil || n != 3 {
		t.Fatalf("live Specula = %d (%v)", n, err)
	}
}
