package entitlements_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/entitlements"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationFeatures checks that a tenant's features are its plan's, sorted and deduplicated,
// and that the seeded plan unlocks nothing.
func TestIntegrationFeatures(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("entitlements"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	svc := entitlements.NewService(env.Pool(t, env.APIURL, 2))
	capped, internalOrg := uuid.New(), uuid.New()
	pgtest.MustExec(t, admin, pgtest.CappedPlanSQL,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+capped.String()+`', 'capped-org', 'Capped', 'capped')`,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+internalOrg.String()+`', 'internal-org', 'Internal', 'internal')`)

	for _, id := range []uuid.UUID{capped, internalOrg} {
		got, err := svc.Features(ctx, id)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("seeded plan features of %s = %#v (%v), want empty", id, got, err)
		}
	}

	pgtest.MustExec(t, admin, pgtest.CappedPlanSQL, `UPDATE plans SET features = '{sso,audit-export,sso}' WHERE key = 'capped'`)
	got, err := svc.Features(ctx, capped)
	if err != nil || !slices.Equal(got, []string{"audit-export", "sso"}) {
		t.Fatalf("features = %v (%v), want [audit-export sso]", got, err)
	}
	if ok, err := svc.Has(ctx, capped, "sso"); err != nil || !ok {
		t.Fatalf("Has(sso) = %t (%v), want true", ok, err)
	}
	if ok, err := svc.Has(ctx, internalOrg, "sso"); err != nil || ok {
		t.Fatalf("internal-plan Has(sso) = %t (%v), want false", ok, err)
	}
	if _, err := svc.Features(ctx, uuid.New()); err == nil {
		t.Fatal("features of an unknown tenant: no error")
	}
	if _, err := admin.Exec(ctx, `UPDATE plans SET features = '{NULL}' WHERE key = 'capped'`); err == nil {
		t.Fatal("a NULL feature name was accepted")
	}
}
