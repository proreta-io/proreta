// Package specula owns specula_agents and specula_projects: executors that run inside a
// customer's network and call Proreta over HTTPS. A Specula belongs to an organization and is
// shared with all of its projects or a list of them. This file is the management side (create,
// list, rename, share, revoke) used by the api; auth.go is what the gateway uses.
package specula

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// Audit actions written by this module.
const (
	ActionCreated = "specula.created"
	ActionUpdated = "specula.updated"
	ActionRevoked = "specula.revoked"

	// The names written instead when the platform admin acts (Actor.Admin).
	AdminActionCreated = "admin.specula.created"
	AdminActionUpdated = "admin.specula.updated"
	AdminActionRevoked = "admin.specula.revoked"
)

// TokenPrefix starts every Specula token, so a leaked one is recognisable.
const TokenPrefix = "spc_"

const (
	tokenBytes = 32
	maxName    = 80
)

var (
	// ErrNotFound means no live Specula with that id exists in the organization.
	ErrNotFound = errors.New("specula: not found")
	// ErrUnauthorized means the token is unknown, revoked, or its organization is not active.
	ErrUnauthorized = errors.New("specula: unauthorized")
)

// NewToken returns a new token and its SHA-256, the only form that is stored.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("specula: random token: %w", err)
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns the SHA-256 of a token as presented. Any string is hashed, so an unknown
// token costs the same as a known one up to the lookup.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Actor is who performs a change; it scopes every operation to one organization. The caller
// checks that the user may manage Specula instances.
type Actor struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	IP       netip.Addr
	// Admin marks a platform admin acting in the organization: the audit rows carry the admin.* names.
	Admin bool
}

// Specula is one registered executor.
type Specula struct {
	ID          uuid.UUID
	Name        string
	AllProjects bool
	// ProjectIDs lists the projects that may use it when AllProjects is false.
	ProjectIDs []uuid.UUID
	LocationID uuid.UUID
	Version    string
	Hostname   string
	LastSeenAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// CreateInput is a new Specula. ProjectIDs is used only when AllProjects is false.
type CreateInput struct {
	Name        string
	AllProjects bool
	ProjectIDs  []uuid.UUID
}

// UpdateInput changes the non-nil fields. ProjectIDs replaces the list; it matters only while
// the Specula is not shared with all projects.
type UpdateInput struct {
	Name        *string
	AllProjects *bool
	ProjectIDs  *[]uuid.UUID
}

// Service manages Specula instances.
type Service struct {
	pool *db.Pool
}

// NewService returns a Service using the api's pool.
func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

const nameIndex = "specula_agents_name_idx"

// Create registers a Specula and returns it with its token, which cannot be read again.
func (s *Service) Create(ctx context.Context, a Actor, in CreateInput) (Specula, string, error) {
	var verr validation.Error
	name, msg := validateName(in.Name)
	if msg != "" {
		verr.Add("name", msg)
	}
	projects := unique(in.ProjectIDs)
	if !in.AllProjects && len(projects) == 0 {
		verr.Add("projectIds", "choose at least one project, or share with all projects")
	}
	if err := verr.Err(); err != nil {
		return Specula{}, "", err
	}
	token, hash, err := NewToken()
	if err != nil {
		return Specula{}, "", err
	}
	id := uuidv7.New()
	var out Specula
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		if !in.AllProjects {
			if err := s.checkProjects(ctx, tx, a.TenantID, projects); err != nil {
				return err
			}
		}
		if err := plans.CheckCreate(ctx, tx, a.TenantID, plans.ResourceAgent); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT specula_create($1, $2, $3, $4, $5, $6)`, id, name, hash, in.AllProjects, projects, a.UserID); err != nil {
			if db.IsUniqueViolation(err, nameIndex) {
				var v validation.Error
				v.Add("name", "is already used by another Specula")
				return &v
			}
			return fmt.Errorf("create: %w", err)
		}
		if err := s.audit(ctx, tx, a, pick(a, ActionCreated, AdminActionCreated), id, map[string]any{"allProjects": in.AllProjects, "projectCount": len(projects)}); err != nil {
			return err
		}
		out, err = get(ctx, tx, a.TenantID, id)
		return err
	})
	if err != nil {
		return Specula{}, "", wrap(err)
	}
	return out, token, nil
}

// Get returns a Specula of the organization, revoked ones included.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (Specula, error) {
	var out Specula
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = get(ctx, tx, tenantID, id)
		return err
	})
	return out, wrap(err)
}

// List returns the organization's Specula instances, newest first. Revoked ones are included so
// the page can show what happened; callers filter as needed.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID) ([]Specula, error) {
	var out []Specula
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectSpecula+` WHERE a.tenant_id = $1 ORDER BY a.created_at DESC, a.id DESC`, tenantID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, scan)
		if err != nil {
			return err
		}
		for i := range out {
			if out[i].ProjectIDs, err = projectIDs(ctx, tx, out[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	return out, wrap(err)
}

// Update renames a Specula and changes who may use it. A revoked Specula cannot be changed.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, in UpdateInput) (Specula, error) {
	var out Specula
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		cur, err := get(ctx, tx, a.TenantID, id)
		if err != nil {
			return err
		}
		if cur.RevokedAt != nil {
			return ErrNotFound
		}
		var verr validation.Error
		var changed []string
		name, all := cur.Name, cur.AllProjects
		if in.Name != nil {
			v, msg := validateName(*in.Name)
			if msg != "" {
				verr.Add("name", msg)
			}
			if v != name {
				changed = append(changed, "name")
			}
			name = v
		}
		if in.AllProjects != nil {
			if *in.AllProjects != all {
				changed = append(changed, "allProjects")
			}
			all = *in.AllProjects
		}
		list := cur.ProjectIDs
		if in.ProjectIDs != nil {
			list = unique(*in.ProjectIDs)
			if !sameSet(list, cur.ProjectIDs) {
				changed = append(changed, "projectIds")
			}
		}
		if !all && len(list) == 0 {
			verr.Add("projectIds", "choose at least one project, or share with all projects")
		}
		if err := verr.Err(); err != nil {
			return err
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		if !all {
			if err := s.checkProjects(ctx, tx, a.TenantID, list); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE specula_agents SET name = $3, all_projects = $4, updated_at = now() WHERE tenant_id = $1 AND id = $2`,
			a.TenantID, id, name, all); err != nil {
			if db.IsUniqueViolation(err, nameIndex) {
				var v validation.Error
				v.Add("name", "is already used by another Specula")
				return &v
			}
			return fmt.Errorf("update: %w", err)
		}
		// The list is kept while the Specula is shared with everyone, so switching back restores it.
		if in.ProjectIDs != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM specula_projects WHERE tenant_id = $1 AND specula_id = $2`, a.TenantID, id); err != nil {
				return fmt.Errorf("update projects: %w", err)
			}
			for _, p := range list {
				if _, err := tx.Exec(ctx, `INSERT INTO specula_projects (tenant_id, specula_id, project_id) VALUES ($1, $2, $3)`, a.TenantID, id, p); err != nil {
					return fmt.Errorf("update projects: %w", err)
				}
			}
		}
		if err := s.audit(ctx, tx, a, pick(a, ActionUpdated, AdminActionUpdated), id, map[string]any{"fields": changed}); err != nil {
			return err
		}
		out, err = get(ctx, tx, a.TenantID, id)
		return err
	})
	return out, wrap(err)
}

// Revoke stops a Specula at once: its token no longer authenticates and its location hands out
// no jobs. Revoking a revoked Specula is ErrNotFound.
func (s *Service) Revoke(ctx context.Context, a Actor, id uuid.UUID) error {
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		var revoked bool
		if err := tx.QueryRow(ctx, `SELECT specula_revoke($1)`, id).Scan(&revoked); err != nil {
			return fmt.Errorf("revoke: %w", err)
		}
		if !revoked {
			return ErrNotFound
		}
		return s.audit(ctx, tx, a, pick(a, ActionRevoked, AdminActionRevoked), id, nil)
	})
	return wrap(err)
}

func pick(a Actor, customer, admin string) string {
	if a.Admin {
		return admin
	}
	return customer
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, a Actor, action string, id uuid.UUID, meta map[string]any) error {
	actor := a.UserID
	return audit.Record(ctx, tx, audit.Entry{
		TenantID: a.TenantID, ActorUserID: &actor, Action: action,
		EntityType: "specula", EntityID: id.String(), Metadata: meta, IP: a.IP,
	})
}

// checkProjects fails with a field error when a project is not a live project of the tenant.
func (s *Service) checkProjects(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE tenant_id = $1 AND deleted_at IS NULL AND id = ANY($2)`, tenantID, ids).Scan(&n); err != nil {
		return fmt.Errorf("check projects: %w", err)
	}
	if n != len(ids) {
		var v validation.Error
		v.Add("projectIds", "contains a project that does not exist")
		return &v
	}
	return nil
}

const selectSpecula = `SELECT a.id, a.name, a.all_projects, a.location_id, COALESCE(a.version, ''), COALESCE(a.hostname, ''),
       a.last_seen_at, a.revoked_at, a.created_at FROM specula_agents AS a`

func scan(r pgx.CollectableRow) (Specula, error) {
	var s Specula
	err := r.Scan(&s.ID, &s.Name, &s.AllProjects, &s.LocationID, &s.Version, &s.Hostname, &s.LastSeenAt, &s.RevokedAt, &s.CreatedAt)
	return s, err
}

func get(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (Specula, error) {
	rows, err := tx.Query(ctx, selectSpecula+` WHERE a.tenant_id = $1 AND a.id = $2`, tenantID, id)
	if err != nil {
		return Specula{}, err
	}
	s, err := pgx.CollectOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Specula{}, ErrNotFound
	}
	if err != nil {
		return Specula{}, err
	}
	s.ProjectIDs, err = projectIDs(ctx, tx, id)
	return s, err
}

func projectIDs(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT project_id FROM specula_projects WHERE specula_id = $1 ORDER BY created_at, project_id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

func validateName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "is required"
	}
	if utf8.RuneCountInString(name) > maxName {
		return "", "must be at most 80 characters"
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", "must not contain control or invisible formatting characters"
		}
	}
	return name, ""
}

func unique(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func sameSet(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	have := map[uuid.UUID]bool{}
	for _, id := range a {
		have[id] = true
	}
	for _, id := range b {
		if !have[id] {
			return false
		}
	}
	return true
}

func wrap(err error) error {
	var verr *validation.Error
	var lerr *plans.LimitError
	if err == nil || errors.Is(err, ErrNotFound) || errors.As(err, &verr) || errors.As(err, &lerr) {
		return err
	}
	return fmt.Errorf("specula: %w", err)
}

// Statuses of a Specula as the app shows them.
const (
	StatusPending = "pending"
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusRevoked = "revoked"
)

// OnlineWindow is how recently a Specula must have been heard from to count as online. It
// reports every minute, so this tolerates a missed report or two.
const OnlineWindow = 90 * time.Second

// Status is the state of s at now.
func (s Specula) Status(now time.Time) string {
	switch {
	case s.RevokedAt != nil:
		return StatusRevoked
	case s.LastSeenAt == nil:
		return StatusPending
	case now.Sub(*s.LastSeenAt) <= OnlineWindow:
		return StatusOnline
	}
	return StatusOffline
}

// UsableBy reports whether a project may use the Specula (it is not revoked and is shared with
// the project).
func (s Specula) UsableBy(project uuid.UUID) bool {
	return s.RevokedAt == nil && (s.AllProjects || slices.Contains(s.ProjectIDs, project))
}
