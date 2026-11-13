package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// Audit actions of the plan operations.
const (
	ActionPlanCreated = "admin.plan.created"
	ActionPlanUpdated = "admin.plan.updated"
	ActionPlanDeleted = "admin.plan.deleted"
)

var (
	// ErrSystemPlan means a seeded plan cannot be deleted.
	ErrSystemPlan = errors.New("admin: a system plan cannot be deleted")
	// ErrPlanInUse means an organization (a deleted one too) still refers to the plan.
	ErrPlanInUse = errors.New("admin: the plan is in use")

	planKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Plan is a plan as the admin sees it.
type Plan struct {
	Key  string
	Name string
	// System is true for the seeded plans.
	System bool
	Limits plans.Limits
	// Organizations is the number of organizations on the plan that are not deleted.
	Organizations int
	// Deletable is true for a custom plan nobody refers to.
	Deletable bool
	// PeakUsage is the highest use per resource among those organizations; set by GetPlan only.
	PeakUsage *plans.Counts
}

// limitFields names every limit with the API field it is reported under.
func limitFields(l *plans.Limits) []struct {
	field string
	value *int
} {
	return []struct {
		field string
		value *int
	}{{"monitors", &l.MaxMonitors}, {"heartbeats", &l.MaxHeartbeats}, {"channels", &l.MaxChannels}, {"members", &l.MaxMembers},
		{"projects", &l.MaxProjects}, {"statusPages", &l.MaxStatusPages}, {"specula", &l.MaxAgents}}
}

// ValidatePlan checks a create (key != "") or an update (key == "") request; the definer
// functions check again.
func ValidatePlan(key, name string, limits plans.Limits) error {
	var verr validation.Error
	if key != "" && !planKeyPattern.MatchString(key) {
		verr.Add("key", "use lowercase letters, digits and hyphens, 1 to 63 characters, starting with a letter or digit")
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > 200 || strings.TrimSpace(name) == "" {
		verr.Add("name", "must be 1 to 200 characters")
	}
	for _, f := range limitFields(&limits) {
		if *f.value < 0 || *f.value > plans.Unlimited {
			verr.Add("limits."+f.field, fmt.Sprintf("must be a whole number from 0 to %d", plans.Unlimited))
		}
	}
	return verr.Err()
}

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	var raw []byte
	if err := row.Scan(&p.Key, &p.Name, &raw, &p.System, &p.Organizations, &p.Deletable); err != nil {
		return Plan{}, err
	}
	var err error
	p.Limits, err = plans.ParseLimits(p.Key, raw)
	return p, err
}

const planColumns = `key, name, limits, is_system, organizations, deletable`

// ListPlans returns every plan, system plans first.
func (s *Service) ListPlans(ctx context.Context, actor Actor) ([]Plan, error) {
	out := []Plan{}
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = out[:0]
		rows, err := tx.Query(ctx, `SELECT `+planColumns+` FROM admin_plans_list($1)`, actor.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPlan(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, mapError("list plans", err)
	}
	return out, nil
}

// GetPlan returns one plan with its peak usage.
func (s *Service) GetPlan(ctx context.Context, actor Actor, key string) (Plan, error) {
	var p Plan
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if p, err = readPlan(ctx, tx, actor, key); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT admin_plan_peak_usage($1, $2)`, actor.UserID, key).Scan(&raw); err != nil {
			return err
		}
		var u struct {
			Monitors, Heartbeats, Channels, Members, Projects, StatusPages, Specula int
		}
		if err := json.Unmarshal(raw, &u); err != nil {
			return err
		}
		p.PeakUsage = &plans.Counts{Monitors: u.Monitors, Heartbeats: u.Heartbeats, Channels: u.Channels, Members: u.Members,
			Projects: u.Projects, StatusPages: u.StatusPages, Agents: u.Specula}
		return nil
	})
	if err != nil {
		return Plan{}, mapError("get plan", err)
	}
	return p, nil
}

// readPlan reads one plan through the list function (the plan set is a handful of rows).
func readPlan(ctx context.Context, tx pgx.Tx, actor Actor, key string) (Plan, error) {
	return scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM admin_plans_list($1) WHERE key = $2`, actor.UserID, key))
}

// CreatePlan adds a custom plan and returns it.
func (s *Service) CreatePlan(ctx context.Context, actor Actor, key, name string, limits plans.Limits) (Plan, error) {
	if err := ValidatePlan(key, name, limits); err != nil {
		return Plan{}, err
	}
	var p Plan
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT admin_plan_create($1, $2, $3, $4, $5, $6)`, actor.UserID, key, name, limits.JSON(), uuidv7.New(), actor.ip()); err != nil {
			return err
		}
		var err error
		p, err = readPlan(ctx, tx, actor, key)
		return err
	})
	if err != nil {
		return Plan{}, mapError("create plan", err)
	}
	s.recorded(ctx, ActionPlanCreated, "plan", key)
	return p, nil
}

// UpdatePlan changes the name and limits of a plan, a system plan included, and returns it. It
// applies at once to every organization on the plan; nothing existing is deleted.
func (s *Service) UpdatePlan(ctx context.Context, actor Actor, key, name string, limits plans.Limits) (Plan, error) {
	if err := ValidatePlan("", name, limits); err != nil {
		return Plan{}, err
	}
	var p Plan
	var changed bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if changed, err = callChange(ctx, tx, `SELECT admin_plan_update($1, $2, $3, $4, $5, $6)`, actor.UserID, key, name, limits.JSON(), uuidv7.New(), actor.ip()); err != nil {
			return err
		}
		p, err = readPlan(ctx, tx, actor, key)
		return err
	})
	if err != nil {
		return Plan{}, mapError("update plan", err)
	}
	if changed {
		s.recorded(ctx, ActionPlanUpdated, "plan", key)
	}
	return p, nil
}

// DeletePlan deletes a custom plan nobody refers to.
func (s *Service) DeletePlan(ctx context.Context, actor Actor, key string) error {
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT admin_plan_delete($1, $2, $3, $4)`, actor.UserID, key, uuidv7.New(), actor.ip())
		return err
	})
	if err != nil {
		return mapError("delete plan", err)
	}
	s.recorded(ctx, ActionPlanDeleted, "plan", key)
	return nil
}
