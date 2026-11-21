package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func TestTemporaryPassword(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p, err := temporaryPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != temporaryPasswordLen || len(p) < 20 {
			t.Fatalf("length %d", len(p))
		}
		for _, c := range p {
			if !strings.ContainsRune(temporaryPasswordAlphabet, c) {
				t.Fatalf("%q has %q outside the alphabet", p, c)
			}
		}
		if seen[p] {
			t.Fatalf("repeated password %q", p)
		}
		seen[p] = true
	}
	if len(temporaryPasswordAlphabet) != 32 {
		t.Fatalf("alphabet has %d characters; 256 must be a multiple of it", len(temporaryPasswordAlphabet))
	}
}

func TestPageNormalized(t *testing.T) {
	for _, c := range []struct{ in, want Page }{
		{Page{}, Page{Limit: DefaultLimit}},
		{Page{Limit: 500, Offset: -3}, Page{Limit: MaxLimit}},
		{Page{Query: "x", Limit: 10, Offset: 20}, Page{Query: "x", Limit: 10, Offset: 20}},
	} {
		if got := c.in.normalized(); got != c.want {
			t.Errorf("%+v.normalized() = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestMapError(t *testing.T) {
	for code, want := range map[string]error{
		"PRA01": ErrForbidden, "PRA02": ErrNotFound, "PRA05": ErrProtectedTenant,
		"PRA06": ErrOwnTenant, "PRA07": ErrSystemAdmin, "PRA08": ErrTenantDeleted, "PRA14": ErrLastPlatformAdmin, "PRA36": ErrPlatformAdmin,
	} {
		if got := mapError("op", &pgconn.PgError{Code: code}); !errors.Is(got, want) {
			t.Errorf("%s: %v, want %v", code, got, want)
		}
	}
	for code, field := range map[string]string{"PRA03": "planKey", "PRA04": "status", "PRA15": "enabled", "PRA16": "deliveryDays", "PRA17": "rollupMonths"} {
		var verr *validation.Error
		if got := mapError("op", &pgconn.PgError{Code: code}); !errors.As(got, &verr) || verr.Fields[0].Field != field {
			t.Errorf("%s: %v, want a validation error on %s", code, got, field)
		}
	}
	other := errors.New("boom")
	if got := mapError("op", other); !errors.Is(got, other) || !strings.Contains(got.Error(), "admin: op") {
		t.Errorf("other error: %v", got)
	}
}

func TestListAccountsRejectsUnknownStatus(t *testing.T) {
	var verr *validation.Error
	_, err := (&Service{}).ListAccounts(context.Background(), Actor{}, Page{}, "paused")
	if !errors.As(err, &verr) || verr.Fields[0].Field != "status" {
		t.Fatalf("err = %v, want a validation error on status", err)
	}
}

func TestCreateTenantValidatesBeforeTouchingTheDatabase(t *testing.T) {
	bad, empty := "-x", ""
	long := strings.Repeat("n", maxTenantName+1)
	for name, c := range map[string]struct {
		in    TenantCreate
		field string
	}{
		"empty name":  {TenantCreate{Name: "  ", OwnerEmail: "a@b.example"}, "name"},
		"long name":   {TenantCreate{Name: long, OwnerEmail: "a@b.example"}, "name"},
		"bad slug":    {TenantCreate{Name: "X", Slug: &bad, OwnerEmail: "a@b.example"}, "slug"},
		"empty plan":  {TenantCreate{Name: "X", PlanKey: &empty, OwnerEmail: "a@b.example"}, "planKey"},
		"bad email":   {TenantCreate{Name: "X", OwnerEmail: "nope"}, "ownerEmail"},
		"empty email": {TenantCreate{Name: "X"}, "ownerEmail"},
	} {
		var verr *validation.Error
		_, err := (&Service{}).CreateTenant(context.Background(), Actor{}, c.in)
		if !errors.As(err, &verr) || verr.Fields[0].Field != c.field {
			t.Errorf("%s: %v, want a validation error on %s", name, err, c.field)
		}
	}
}
