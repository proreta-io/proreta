package admin

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func TestValidatePlan(t *testing.T) {
	ok := plans.Limits{MaxMonitors: 5, MaxHeartbeats: 0, MaxChannels: 1, MaxMembers: 1, MaxProjects: 1, MaxStatusPages: 1, MaxAgents: plans.Unlimited}
	bad := ok
	bad.MaxMembers, bad.MaxAgents = -1, plans.Unlimited+1
	for _, c := range []struct {
		name, key, plan string
		limits          plans.Limits
		fields          []string
	}{
		{"valid", "gold", "Gold", ok, nil},
		{"valid update", "", "Gold", ok, nil},
		{"key shape", "Gold", "Gold", ok, []string{"key"}},
		{"key starts with hyphen", "-a", "Gold", ok, []string{"key"}},
		{"key too long", strings.Repeat("a", 64), "Gold", ok, []string{"key"}},
		{"key 63 characters", strings.Repeat("a", 63), "Gold", ok, nil},
		{"blank name", "gold", "  ", ok, []string{"name"}},
		{"long name", "gold", strings.Repeat("я", 201), ok, []string{"name"}},
		{"200 character name", "gold", strings.Repeat("я", 200), ok, nil},
		{"limits out of range", "gold", "Gold", bad, []string{"limits.members", "limits.specula"}},
	} {
		err := ValidatePlan(c.key, c.plan, c.limits)
		var verr *validation.Error
		var got []string
		if errors.As(err, &verr) {
			for _, f := range verr.Fields {
				got = append(got, f.Field)
			}
		} else if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if strings.Join(got, ",") != strings.Join(c.fields, ",") {
			t.Errorf("%s: fields %v, want %v", c.name, got, c.fields)
		}
	}
}

func TestMapErrorPlans(t *testing.T) {
	for code, want := range map[string]error{"PRA33": ErrSystemPlan, "PRA34": ErrPlanInUse} {
		if got := mapError("op", &pgconn.PgError{Code: code}); !errors.Is(got, want) {
			t.Errorf("%s: %v, want %v", code, got, want)
		}
	}
	for code, field := range map[string]string{"PRA30": "key", "PRA31": "name", "PRA32": "limits"} {
		var verr *validation.Error
		if got := mapError("op", &pgconn.PgError{Code: code}); !errors.As(got, &verr) || verr.Fields[0].Field != field {
			t.Errorf("%s: %v, want a validation error on %s", code, got, field)
		}
	}
}
