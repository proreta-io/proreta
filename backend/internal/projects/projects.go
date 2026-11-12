// Package projects owns projects and project_members: the projects of an
// organization, who belongs to them, and the access an actor has to each. Isolation between
// projects of one organization is enforced here and by the callers through internal/authz, not by
// RLS: every read filters by the actor's projects and every unknown or invisible project
// is ErrNotFound.
package projects

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects/projectsdb"
)

// Audit actions written by this module.
const (
	ActionCreated       = "project.created"
	ActionUpdated       = "project.updated"
	ActionDeleted       = "project.deleted"
	ActionMemberChanged = "project.member_changed"
)

// Audit actions written instead of the ones above when a platform admin acts (Actor.Admin).
const (
	AdminActionCreated       = "admin.project.created"
	AdminActionUpdated       = "admin.project.updated"
	AdminActionDeleted       = "admin.project.deleted"
	AdminActionMemberAdded   = "admin.project_member.added"
	AdminActionMemberUpdated = "admin.project_member.updated"
	AdminActionMemberRemoved = "admin.project_member.removed"
)

var (
	// ErrNotFound means no live project with that id exists in the tenant, or the actor does not
	// see it (the two are not told apart, so ids of other projects reveal nothing).
	ErrNotFound = errors.New("projects: not found")
	// ErrMemberNotFound means the user is not a member of the organization (or of the project, on
	// removal).
	ErrMemberNotFound = errors.New("projects: member not found")
	// ErrSlugTaken means another live project of the tenant has the slug.
	ErrSlugTaken = errors.New("projects: slug taken")
	// ErrNotEmpty means the project still has live monitors or channels.
	ErrNotEmpty = errors.New("projects: not empty")
	// ErrDefault means the operation is not possible on the Default project (delete).
	ErrDefault = errors.New("projects: default project")
)

// Actor is who reads or changes projects.
type Actor struct {
	TenantID uuid.UUID
	IP       netip.Addr
	Access   authz.Actor
	// Admin marks a platform admin acting inside the tenant: the audit rows carry the admin.*
	// action names instead of the customer-facing ones (never both).
	Admin bool
}

// Project is a project with its counts and the actor's role in it.
type Project struct {
	ID           uuid.UUID
	Name         string
	Slug         string
	IsDefault    bool
	AllMembers   bool
	Role         authz.Role
	MonitorCount int
	ChannelCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Member is a person with a role in a project.
type Member struct {
	UserID      uuid.UUID
	Login       string
	DisplayName string
	// OrganizationRole applies to every project; empty for a person who belongs to the
	// organization only through projects.
	OrganizationRole authz.Role
	// ProjectRole is the direct role in this project, nil when the person is here only through
	// the organization role.
	ProjectRole *authz.Role
	// EffectiveRole is the higher of the two.
	EffectiveRole authz.Role
	// Explicit is true when the person has a direct role.
	Explicit bool
	// AllProjects is true when the person has an organization role.
	AllProjects bool
}

// Service implements projects and their members.
type Service struct {
	pool *db.Pool
}

// NewService returns a Service.
func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

// Access returns the direct role of a user in each live project it was given one in. The
// organization role is not part of it: it covers every project and authz.Actor combines the two.
// The api calls it once per request.
func (s *Service) Access(ctx context.Context, tenantID, userID uuid.UUID) (map[uuid.UUID]authz.Role, error) {
	out := map[uuid.UUID]authz.Role{}
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := projectsdb.New(tx).ListUserProjects(ctx, projectsdb.ListUserProjectsParams{TenantID: tenantID, UserID: userID})
		if err != nil {
			return fmt.Errorf("projects: access: %w", err)
		}
		for _, r := range rows {
			out[r.ID] = authz.Role(r.Role)
		}
		return nil
	})
	return out, err
}

// need checks that the actor holds a project-scope permission in a project: ErrNotFound when it
// has no role there (so ids of other projects reveal nothing), authz.ErrProjectForbidden when its
// role does not hold the permission.
func need(a Actor, id uuid.UUID, p authz.Permission) error {
	if !a.Access.Sees(id) {
		return ErrNotFound
	}
	if !a.Access.CanIn(id, p) {
		return authz.ErrProjectForbidden
	}
	return nil
}

// ForUse resolves the project a monitor or channel is created in or moved to, inside the caller's
// tenant transaction: id, or the Default project when id is nil. The project stays live until the
// transaction ends (FOR KEY SHARE against a concurrent delete). ErrNotFound for an unknown or
// deleted project; the caller checks the actor's access.
func ForUse(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, id *uuid.UUID) (uuid.UUID, error) {
	q := projectsdb.New(tx)
	var got uuid.UUID
	var err error
	if id == nil {
		got, err = q.LockDefaultProject(ctx, tenantID)
	} else {
		got, err = q.LockProjectForUse(ctx, projectsdb.LockProjectForUseParams{TenantID: tenantID, ID: *id})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("projects: lock project: %w", err)
	}
	return got, nil
}

// List returns the projects the actor sees, Default first, then by name.
func (s *Service) List(ctx context.Context, a Actor) ([]Project, error) {
	out := []Project{}
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		rows, err := projectsdb.New(tx).ListProjects(ctx, a.TenantID)
		if err != nil {
			return fmt.Errorf("projects: list: %w", err)
		}
		for _, r := range rows {
			if role, ok := a.Access.RoleIn(r.ID); ok {
				out = append(out, fromRow(r, role))
			}
		}
		return nil
	})
	return out, err
}

// Get returns one project the actor sees.
func (s *Service) Get(ctx context.Context, a Actor, id uuid.UUID) (Project, error) {
	var out Project
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = get(ctx, tx, a, id)
		return err
	})
	return out, err
}

func get(ctx context.Context, tx pgx.Tx, a Actor, id uuid.UUID) (Project, error) {
	role, ok := a.Access.RoleIn(id)
	if !ok {
		return Project{}, ErrNotFound
	}
	rows, err := projectsdb.New(tx).ListProjects(ctx, a.TenantID)
	if err != nil {
		return Project{}, fmt.Errorf("projects: get: %w", err)
	}
	for _, r := range rows {
		if r.ID == id {
			return fromRow(r, role), nil
		}
	}
	return Project{}, ErrNotFound
}

func fromRow(r projectsdb.ListProjectsRow, role authz.Role) Project {
	return Project{ID: r.ID, Name: r.Name, Slug: r.Slug, IsDefault: r.IsDefault, AllMembers: r.AllMembers, Role: role,
		MonitorCount: int(r.MonitorCount), ChannelCount: int(r.ChannelCount), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// ValidateName trims a project name and checks its length (1-60 characters).
func ValidateName(name string) (string, string) {
	v := strings.TrimSpace(name)
	if n := utf8.RuneCountInString(v); n < 1 || n > 60 {
		return v, "must be 1 to 60 characters"
	}
	return v, ""
}

// ValidateSlug checks an explicit slug.
func ValidateSlug(slug string) string {
	if len(slug) < 1 || len(slug) > 63 {
		return "must be 1 to 63 characters"
	}
	for i, c := range slug {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && (c != '-' || i == 0) {
			return "may contain lowercase letters, digits and hyphens and must not start with a hyphen"
		}
	}
	return ""
}

// Slugify derives a slug from a name: lowercase ASCII letters and digits, other runs become one
// hyphen; "project" when nothing is left.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 56 {
		out = strings.TrimRight(out[:56], "-")
	}
	if out == "" {
		return "project"
	}
	return out
}

// CreateInput is a new project; an empty Slug is derived from the name.
type CreateInput struct {
	Name string
	Slug *string
}

// Create adds a project under the project cap (a *plans.LimitError when it is used up).
func (s *Service) Create(ctx context.Context, a Actor, in CreateInput) (Project, error) {
	if !a.Access.Holds("project.create") {
		return Project{}, authz.ErrForbidden
	}
	var verr validation.Error
	name, msg := ValidateName(in.Name)
	if msg != "" {
		verr.Add("name", msg)
	}
	if in.Slug != nil {
		if msg := ValidateSlug(*in.Slug); msg != "" {
			verr.Add("slug", msg)
		}
	}
	if err := verr.Err(); err != nil {
		return Project{}, err
	}
	id := uuidv7.New()
	var out Project
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		// The tenant row lock serialises creates and renames, so the slug check stays true.
		if err := plans.CheckCreate(ctx, tx, a.TenantID, plans.ResourceProject); err != nil {
			return err
		}
		q := projectsdb.New(tx)
		slug, err := pickSlug(ctx, q, a.TenantID, uuid.Nil, name, in.Slug)
		if err != nil {
			return err
		}
		if err := q.InsertProject(ctx, projectsdb.InsertProjectParams{ID: id, TenantID: a.TenantID, Name: name, Slug: slug}); err != nil {
			return mapWriteErr("insert project", err)
		}
		if err := s.audit(ctx, tx, a, pick(a, ActionCreated, AdminActionCreated), id, map[string]any{"slug": slug}); err != nil {
			return err
		}
		out, err = get(ctx, tx, a, id)
		return err
	})
	return out, err
}

// pickSlug returns the explicit slug when it is free (ErrSlugTaken otherwise), or the slug
// derived from the name with a numeric suffix when the plain one is in use.
func pickSlug(ctx context.Context, q *projectsdb.Queries, tenantID, self uuid.UUID, name string, explicit *string) (string, error) {
	inUse := func(slug string) (bool, error) {
		used, err := q.SlugInUse(ctx, projectsdb.SlugInUseParams{TenantID: tenantID, Slug: slug, ExceptID: self})
		if err != nil {
			return false, fmt.Errorf("projects: slug check: %w", err)
		}
		return used, nil
	}
	if explicit != nil {
		used, err := inUse(*explicit)
		if err != nil {
			return "", err
		}
		if used {
			return "", ErrSlugTaken
		}
		return *explicit, nil
	}
	base := Slugify(name)
	for i := 1; i <= 60; i++ {
		slug := base
		if i > 1 {
			slug = base + "-" + strconv.Itoa(i)
		}
		used, err := inUse(slug)
		if err != nil {
			return "", err
		}
		if !used {
			return slug, nil
		}
	}
	return "", ErrSlugTaken
}

// UpdateInput changes the non-nil fields.
type UpdateInput struct {
	Name *string
	Slug *string
}

// Update renames a project.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, in UpdateInput) (Project, error) {
	if err := need(a, id, "project.update"); err != nil {
		return Project{}, err
	}
	var verr validation.Error
	var name string
	if in.Name != nil {
		var msg string
		if name, msg = ValidateName(*in.Name); msg != "" {
			verr.Add("name", msg)
		}
	}
	if in.Slug != nil {
		if msg := ValidateSlug(*in.Slug); msg != "" {
			verr.Add("slug", msg)
		}
	}
	if err := verr.Err(); err != nil {
		return Project{}, err
	}
	var out Project
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		if err := plans.Lock(ctx, tx, a.TenantID); err != nil {
			return err
		}
		q := projectsdb.New(tx)
		cur, err := lockProject(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		newName, newSlug := cur.Name, cur.Slug
		if in.Name != nil {
			newName = name
		}
		if in.Slug != nil && *in.Slug != cur.Slug {
			if newSlug, err = pickSlug(ctx, q, a.TenantID, id, newName, in.Slug); err != nil {
				return err
			}
		}
		var changed []string
		if newName != cur.Name {
			changed = append(changed, "name")
		}
		if newSlug != cur.Slug {
			changed = append(changed, "slug")
		}
		if len(changed) > 0 {
			if err := q.UpdateProject(ctx, projectsdb.UpdateProjectParams{TenantID: a.TenantID, ID: id, Name: newName, Slug: newSlug}); err != nil {
				return mapWriteErr("update project", err)
			}
			key := "fields"
			if a.Admin {
				key = "changed"
			}
			if err := s.audit(ctx, tx, a, pick(a, ActionUpdated, AdminActionUpdated), id, map[string]any{key: changed}); err != nil {
				return err
			}
		}
		out, err = get(ctx, tx, a, id)
		return err
	})
	return out, err
}

// Delete soft-deletes an empty project that is not the Default one and removes its member list.
func (s *Service) Delete(ctx context.Context, a Actor, id uuid.UUID) error {
	if err := need(a, id, "project.delete"); err != nil {
		return err
	}
	return db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := projectsdb.New(tx)
		cur, err := lockProject(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		if cur.IsDefault {
			return ErrDefault
		}
		// The row lock above waits for transactions that create or move monitors and channels
		// into the project (FOR KEY SHARE), so these counts stay true.
		n, err := q.CountProjectContents(ctx, projectsdb.CountProjectContentsParams{TenantID: a.TenantID, ProjectID: id})
		if err != nil {
			return fmt.Errorf("projects: count contents: %w", err)
		}
		if n.Monitors > 0 || n.Channels > 0 {
			return ErrNotEmpty
		}
		removed, err := q.DeleteProjectMembers(ctx, projectsdb.DeleteProjectMembersParams{TenantID: a.TenantID, ProjectID: id})
		if err != nil {
			return fmt.Errorf("projects: delete members: %w", err)
		}
		if err := q.SoftDeleteProject(ctx, projectsdb.SoftDeleteProjectParams{TenantID: a.TenantID, ID: id}); err != nil {
			return fmt.Errorf("projects: delete: %w", err)
		}
		return s.audit(ctx, tx, a, pick(a, ActionDeleted, AdminActionDeleted), id, map[string]any{"slug": cur.Slug, "membersRemoved": removed})
	})
}

// Members lists the people with a role in a project the actor sees.
func (s *Service) Members(ctx context.Context, a Actor, id uuid.UUID) ([]Member, error) {
	if err := need(a, id, "project.members.read"); err != nil {
		return nil, err
	}
	out := []Member{}
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := projectsdb.New(tx)
		if err := ensureLive(ctx, tx, a.TenantID, id); err != nil {
			return err
		}
		rows, err := q.ListProjectMembers(ctx, projectsdb.ListProjectMembersParams{TenantID: a.TenantID, ProjectID: id})
		if err != nil {
			return fmt.Errorf("projects: members: %w", err)
		}
		for _, r := range rows {
			out = append(out, newMember(r.UserID, r.Login, r.DisplayName, r.OrganizationRole, r.ProjectRole))
		}
		return nil
	})
	return out, err
}

func newMember(userID uuid.UUID, login, displayName, orgRole string, projectRole *string) Member {
	org := authz.Role(orgRole)
	m := Member{UserID: userID, Login: login, DisplayName: displayName, OrganizationRole: org, AllProjects: authz.Valid(org)}
	if projectRole != nil {
		pr := authz.Role(*projectRole)
		m.ProjectRole, m.Explicit = &pr, true
	}
	if m.ProjectRole != nil {
		m.EffectiveRole = authz.Higher(org, *m.ProjectRole)
	} else {
		m.EffectiveRole = authz.Higher(org, "")
	}
	return m
}

// ensureLive returns ErrNotFound unless a live project with the id exists.
func ensureLive(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	var live bool
	err := tx.QueryRow(ctx, `SELECT true FROM projects WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, id).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("projects: get project: %w", err)
	}
	return nil
}

// PutMember gives a person of the organization a direct role in a project, or changes it. The
// direct role raises the organization role inside the project and never lowers it. Nobody grants
// a role above their own in the project and nobody changes a person whose effective role there is
// above their own; organization owners are the last resort, so a project has no last-owner rule.
func (s *Service) PutMember(ctx context.Context, a Actor, id, userID uuid.UUID, role authz.Role) (Member, error) {
	if err := need(a, id, "project.members.manage"); err != nil {
		return Member{}, err
	}
	if !authz.Valid(role) {
		var verr validation.Error
		verr.Add("role", `must be "owner", "maintainer", "member" or "viewer"`)
		return Member{}, &verr
	}
	var out Member
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := projectsdb.New(tx)
		if _, err := lockProject(ctx, q, a.TenantID, id); err != nil {
			return err
		}
		cur, err := q.GetMemberRoles(ctx, projectsdb.GetMemberRolesParams{TenantID: a.TenantID, ProjectID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMemberNotFound
		} else if err != nil {
			return fmt.Errorf("projects: membership: %w", err)
		}
		before := newMember(userID, "", "", cur.OrganizationRole, cur.ProjectRole)
		if !authz.CanManageIn(a.Access, id, before.EffectiveRole, role) {
			return authz.ErrForbidden
		}
		if err := q.UpsertProjectMember(ctx, projectsdb.UpsertProjectMemberParams{TenantID: a.TenantID, ProjectID: id, UserID: userID, Role: string(role)}); err != nil {
			return fmt.Errorf("projects: put member: %w", err)
		}
		prev := ""
		if cur.ProjectRole != nil {
			prev = *cur.ProjectRole
		}
		if a.Admin {
			// Saving the same role again changes nothing and is not audited.
			if !before.Explicit {
				err = s.audit(ctx, tx, a, AdminActionMemberAdded, id, map[string]any{"userId": userID.String(), "role": string(role)})
			} else if prev != string(role) {
				err = s.audit(ctx, tx, a, AdminActionMemberUpdated, id, map[string]any{"userId": userID.String(), "from": prev, "to": string(role)})
			}
		} else {
			err = s.audit(ctx, tx, a, ActionMemberChanged, id, map[string]any{"userId": userID.String(), "change": "put", "role": string(role)})
		}
		if err != nil {
			return err
		}
		rows, err := q.ListProjectMembers(ctx, projectsdb.ListProjectMembersParams{TenantID: a.TenantID, ProjectID: id})
		if err != nil {
			return fmt.Errorf("projects: members: %w", err)
		}
		for _, r := range rows {
			if r.UserID == userID {
				out = newMember(r.UserID, r.Login, r.DisplayName, r.OrganizationRole, r.ProjectRole)
				return nil
			}
		}
		return ErrMemberNotFound
	})
	return out, err
}

// RemoveMember takes a person's direct role off a project; an organization role stays. The rules
// of PutMember apply.
func (s *Service) RemoveMember(ctx context.Context, a Actor, id, userID uuid.UUID) error {
	if err := need(a, id, "project.members.manage"); err != nil {
		return err
	}
	return db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := projectsdb.New(tx)
		if _, err := lockProject(ctx, q, a.TenantID, id); err != nil {
			return err
		}
		cur, err := q.GetMemberRoles(ctx, projectsdb.GetMemberRolesParams{TenantID: a.TenantID, ProjectID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && cur.ProjectRole == nil) {
			return ErrMemberNotFound
		} else if err != nil {
			return fmt.Errorf("projects: membership: %w", err)
		}
		if !authz.CanManageIn(a.Access, id, newMember(userID, "", "", cur.OrganizationRole, cur.ProjectRole).EffectiveRole, "") {
			return authz.ErrForbidden
		}
		if _, err := q.DeleteProjectMember(ctx, projectsdb.DeleteProjectMemberParams{TenantID: a.TenantID, ProjectID: id, UserID: userID}); err != nil {
			return fmt.Errorf("projects: remove member: %w", err)
		}
		if a.Admin {
			return s.audit(ctx, tx, a, AdminActionMemberRemoved, id, map[string]any{"userId": userID.String()})
		}
		return s.audit(ctx, tx, a, ActionMemberChanged, id, map[string]any{"userId": userID.String(), "change": "removed"})
	})
}

func pick(a Actor, customer, admin string) string {
	if a.Admin {
		return admin
	}
	return customer
}

func lockProject(ctx context.Context, q *projectsdb.Queries, tenantID, id uuid.UUID) (projectsdb.LockProjectRow, error) {
	row, err := q.LockProject(ctx, projectsdb.LockProjectParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, fmt.Errorf("projects: lock project: %w", err)
	}
	return row, nil
}

func mapWriteErr(op string, err error) error {
	if db.IsUniqueViolation(err, "projects_slug_idx") {
		return ErrSlugTaken
	}
	return fmt.Errorf("projects: %s: %w", op, err)
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, a Actor, action string, id uuid.UUID, meta map[string]any) error {
	actor := a.Access.UserID
	return audit.Record(ctx, tx, audit.Entry{
		TenantID: a.TenantID, ActorUserID: &actor, Action: action,
		EntityType: "project", EntityID: id.String(), Metadata: meta, IP: a.IP,
	})
}
