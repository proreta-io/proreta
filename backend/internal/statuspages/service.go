// Package statuspages owns the public status page of an organization: its
// configuration (status_pages, status_page_components) and the prepared public view
// (status_page_snapshots). A page belongs to a project, whose owners and maintainers configure it,
// or to the organization, whose owners and maintainers configure it; the snapshot builder (Build) is
// the one place that decides what becomes public. The public renderer lives in
// internal/statuspages/public and reads nothing but snapshots.
package statuspages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/statuspages/statuspagesdb"
)

// Audit actions. Metadata holds field names only, never values or passwords.
const (
	ActionCreated     = "status_page.created"
	ActionUpdated     = "status_page.updated"
	ActionPublished   = "status_page.published"
	ActionUnpublished = "status_page.unpublished"
	ActionDeleted     = "status_page.deleted"
)

var (
	// ErrNotFound means no live status page with that id exists in the tenant.
	ErrNotFound = errors.New("statuspages: not found")
	// ErrSlugTaken means the address is not available: a live page of any organization has it, or
	// another organization released it less than 30 days ago. Both look the same.
	ErrSlugTaken = errors.New("statuspages: slug taken")
)

// Actor is who reads or changes the status page.
type Actor struct {
	TenantID uuid.UUID
	IP       netip.Addr
	Access   authz.Actor
}

// Page is the configuration of a status page as the admin API shows it. The password hash never
// leaves the module; only whether there is one.
type Page struct {
	ID uuid.UUID
	// ProjectID is the project the page belongs to; nil for an organization-level page.
	ProjectID         *uuid.UUID
	Slug              string
	Title             string
	Description       string
	LogoURL           *string
	AccentColor       *string
	Published         bool
	PasswordProtected bool
	ComponentCount    int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ComponentConfig is a component with its monitor, for the admin (never published).
type ComponentConfig struct {
	MonitorID   uuid.UUID
	MonitorName string
	MonitorType string
	ProjectID   uuid.UUID
	PublicName  string
	GroupName   *string
	Position    int
}

// ComponentInput is one component of a PutComponents call; the order of the list is the order on
// the page.
type ComponentInput struct {
	MonitorID  uuid.UUID
	PublicName string
	GroupName  *string
}

// Options tune the service.
type Options struct {
	Clock  clock.Clock
	Logger *slog.Logger
	// HashParams are the argon2id parameters of page passwords; nil means the default.
	HashParams *crypto.Argon2Params
}

// Service implements the status page configuration (api) and the snapshot rebuild (jobs).
type Service struct {
	pool   *db.Pool
	clock  clock.Clock
	log    *slog.Logger
	params crypto.Argon2Params
}

// NewService returns a Service.
func NewService(pool *db.Pool, o Options) *Service {
	s := &Service{pool: pool, clock: o.Clock, log: o.Logger, params: crypto.DefaultArgon2Params}
	if s.clock == nil {
		s.clock = clock.System{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if o.HashParams != nil {
		s.params = *o.HashParams
	}
	return s
}

// canRead reports whether the actor sees pages of a project (nil = organization-level pages).
func (a Actor) canRead(project *uuid.UUID) bool {
	if project == nil {
		return a.Access.Holds("organization.statuspages.read")
	}
	return a.Access.CanIn(*project, "statuspage.read")
}

// checkManage is ErrNotFound for a page the actor cannot see, so ids of other projects reveal
// nothing, and a forbidden error for a page it sees but may not change.
func (a Actor) checkManage(project *uuid.UUID) error {
	if !a.canRead(project) {
		return ErrNotFound
	}
	if project == nil {
		if !a.Access.Holds("organization.statuspages.manage") {
			return authz.ErrForbidden
		}
		return nil
	}
	if !a.Access.CanIn(*project, "statuspage.manage") {
		return authz.ErrProjectForbidden
	}
	return nil
}

// List returns the pages the actor sees: those of its projects and, with an organization role, the
// organization-level ones. With project set, only the pages of that project.
func (s *Service) List(ctx context.Context, a Actor, project *uuid.UUID) ([]Page, error) {
	out := []Page{}
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		all, err := listPages(ctx, tx, a.TenantID)
		if err != nil {
			return err
		}
		for _, p := range all {
			if a.canRead(p.ProjectID) && (project == nil || (p.ProjectID != nil && *p.ProjectID == *project)) {
				out = append(out, p)
			}
		}
		return nil
	})
	return out, err
}

// Get returns one page the actor sees, or ErrNotFound.
func (s *Service) Get(ctx context.Context, a Actor, id uuid.UUID) (Page, error) {
	pages, err := s.List(ctx, a, nil)
	if err != nil {
		return Page{}, err
	}
	for _, p := range pages {
		if p.ID == id {
			return p, nil
		}
	}
	return Page{}, ErrNotFound
}

func listPages(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]Page, error) {
	rows, err := statuspagesdb.New(tx).ListPages(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("statuspages: list: %w", err)
	}
	out := make([]Page, len(rows))
	for i, r := range rows {
		out[i] = Page{ID: r.ID, ProjectID: r.ProjectID, Slug: r.Slug, Title: r.Title, Description: r.Description, LogoURL: r.LogoUrl, AccentColor: r.AccentColor,
			Published: r.Published, PasswordProtected: r.Protected, ComponentCount: int(r.ComponentCount), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

func getPage(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (Page, error) {
	pages, err := listPages(ctx, tx, tenantID)
	if err != nil {
		return Page{}, err
	}
	for _, p := range pages {
		if p.ID == id {
			return p, nil
		}
	}
	return Page{}, ErrNotFound
}

// CreateInput is a new page. Empty LogoURL and AccentColor mean none.
type CreateInput struct {
	// ProjectID is the project of the page; nil creates an organization-level page.
	ProjectID   *uuid.UUID
	Slug        string
	Title       string
	Description string
	LogoURL     string
	AccentColor string
}

// Create adds an unpublished page under the max_status_pages quota (a *plans.LimitError when used up).
// A page in a project needs statuspage.manage there, an organization-level one the organization role.
func (s *Service) Create(ctx context.Context, a Actor, in CreateInput) (Page, error) {
	if in.ProjectID == nil && !a.Access.Holds("organization.statuspages.manage") {
		return Page{}, authz.ErrForbidden
	}
	f, err := validateFields(&in.Slug, &in.Title, &in.Description, &in.LogoURL, &in.AccentColor)
	if err != nil {
		return Page{}, err
	}
	id := uuidv7.New()
	var out Page
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		if err := plans.CheckCreate(ctx, tx, a.TenantID, plans.ResourceStatusPage); err != nil {
			return err
		}
		if in.ProjectID != nil {
			_, err := projects.ForUse(ctx, tx, a.TenantID, in.ProjectID)
			if errors.Is(err, projects.ErrNotFound) || (err == nil && !a.Access.Sees(*in.ProjectID)) {
				var verr validation.Error
				verr.Add("projectId", "does not exist")
				return &verr
			}
			if err != nil {
				return err
			}
			if !a.Access.CanIn(*in.ProjectID, "statuspage.manage") {
				return authz.ErrProjectForbidden
			}
		}
		if err := checkSlugFree(ctx, statuspagesdb.New(tx), f.slug); err != nil {
			return err
		}
		if err := statuspagesdb.New(tx).InsertPage(ctx, statuspagesdb.InsertPageParams{
			ID: id, TenantID: a.TenantID, ProjectID: in.ProjectID, Slug: f.slug, Title: f.title, Description: f.description, LogoUrl: f.logo, AccentColor: f.accent,
		}); err != nil {
			return mapWriteErr("insert page", err)
		}
		if err := s.audit(ctx, tx, a, ActionCreated, id, map[string]any{"slug": f.slug, "project": projectMeta(in.ProjectID)}); err != nil {
			return err
		}
		out, err = getPage(ctx, tx, a.TenantID, id)
		return err
	})
	return out, err
}

type fields struct {
	slug, title, description string
	logo, accent             *string
}

// validateFields checks the non-nil page fields and returns them normalised.
func validateFields(slug, title, description, logo, accent *string) (fields, error) {
	var verr validation.Error
	var f fields
	if slug != nil {
		f.slug = *slug
		if msg := ValidateSlug(f.slug); msg != "" {
			verr.Add("slug", msg)
		}
	}
	if title != nil {
		var msg string
		if f.title, msg = ValidateText(*title, 1, MaxTitle); msg != "" {
			verr.Add("title", msg)
		}
	}
	if description != nil {
		var msg string
		if f.description, msg = ValidateText(*description, 0, MaxDescription); msg != "" {
			verr.Add("description", msg)
		}
	}
	if logo != nil {
		v, msg := ValidateLogoURL(*logo)
		if msg != "" {
			verr.Add("logoUrl", msg)
		} else if v != "" {
			f.logo = &v
		}
	}
	if accent != nil {
		v, msg := ValidateAccent(*accent)
		if msg != "" {
			verr.Add("accentColor", msg)
		} else if v != "" {
			f.accent = &v
		}
	}
	return f, verr.Err()
}

// UpdateInput changes the non-nil fields; an empty LogoURL or AccentColor clears it.
type UpdateInput struct {
	Slug        *string
	Title       *string
	Description *string
	LogoURL     *string
	AccentColor *string
	Published   *bool
}

// Update changes a page and rebuilds (or removes) its snapshot in the same transaction, so a
// change is public on the renderer's next cache miss.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, in UpdateInput) (Page, error) {
	f, err := validateFields(in.Slug, in.Title, in.Description, in.LogoURL, in.AccentColor)
	if err != nil {
		return Page{}, err
	}
	var out Page
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		cur, err := lockPage(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.checkManage(cur.ProjectID); err != nil {
			return err
		}
		next := statuspagesdb.UpdatePageParams{TenantID: a.TenantID, ID: id, Slug: cur.Slug, Title: cur.Title, Description: cur.Description,
			LogoUrl: cur.LogoUrl, AccentColor: cur.AccentColor, Published: cur.Published}
		var changed []string
		if in.Slug != nil && f.slug != cur.Slug {
			next.Slug, changed = f.slug, append(changed, "slug")
		}
		if in.Title != nil && f.title != cur.Title {
			next.Title, changed = f.title, append(changed, "title")
		}
		if in.Description != nil && f.description != cur.Description {
			next.Description, changed = f.description, append(changed, "description")
		}
		if in.LogoURL != nil && !eqPtr(f.logo, cur.LogoUrl) {
			next.LogoUrl, changed = f.logo, append(changed, "logoUrl")
		}
		if in.AccentColor != nil && !eqPtr(f.accent, cur.AccentColor) {
			next.AccentColor, changed = f.accent, append(changed, "accentColor")
		}
		if in.Published != nil {
			next.Published = *in.Published
		}
		if len(changed) == 0 && next.Published == cur.Published {
			out, err = getPage(ctx, tx, a.TenantID, id)
			return err
		}
		if next.Slug != cur.Slug {
			if err := checkSlugFree(ctx, q, next.Slug); err != nil {
				return err
			}
			if err := q.ReleaseSlug(ctx, statuspagesdb.ReleaseSlugParams{TenantID: a.TenantID, Slug: cur.Slug}); err != nil {
				return fmt.Errorf("statuspages: release slug: %w", err)
			}
		}
		if err := q.UpdatePage(ctx, next); err != nil {
			return mapWriteErr("update page", err)
		}
		if len(changed) > 0 {
			if err := s.audit(ctx, tx, a, ActionUpdated, id, map[string]any{"fields": changed}); err != nil {
				return err
			}
		}
		if next.Published != cur.Published {
			action := ActionPublished
			if !next.Published {
				action = ActionUnpublished
			}
			if err := s.audit(ctx, tx, a, action, id, map[string]any{"slug": next.Slug}); err != nil {
				return err
			}
		}
		if _, err := Refresh(ctx, tx, a.TenantID, id, s.clock.Now()); err != nil {
			return err
		}
		out, err = getPage(ctx, tx, a.TenantID, id)
		return err
	})
	return out, err
}

// SetPassword protects the page with a password (argon2id); nil removes the protection. The
// password is never stored or logged in clear. Cookies issued for the old password stop working,
// because the renderer binds them to the hash.
func (s *Service) SetPassword(ctx context.Context, a Actor, id uuid.UUID, password *string) error {
	var hash *string
	if password != nil {
		if msg := ValidatePassword(*password); msg != "" {
			var verr validation.Error
			verr.Add("password", msg)
			return &verr
		}
		h, err := crypto.HashPassword(*password, s.params)
		if err != nil {
			return fmt.Errorf("statuspages: hash password: %w", err)
		}
		hash = &h
	}
	return db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		cur, err := lockPage(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.checkManage(cur.ProjectID); err != nil {
			return err
		}
		if err := q.SetPagePassword(ctx, statuspagesdb.SetPagePasswordParams{TenantID: a.TenantID, ID: id, PasswordHash: hash}); err != nil {
			return fmt.Errorf("statuspages: set password: %w", err)
		}
		change := "set"
		if hash == nil {
			change = "cleared"
		}
		return s.audit(ctx, tx, a, ActionUpdated, id, map[string]any{"fields": []string{"password"}, "password": change})
	})
}

// Delete soft-deletes a page and removes its snapshot; the slug stays reserved for this
// organization for 30 days.
func (s *Service) Delete(ctx context.Context, a Actor, id uuid.UUID) error {
	return db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		cur, err := lockPage(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.checkManage(cur.ProjectID); err != nil {
			return err
		}
		if err := q.SoftDeletePage(ctx, statuspagesdb.SoftDeletePageParams{TenantID: a.TenantID, ID: id}); err != nil {
			return fmt.Errorf("statuspages: delete: %w", err)
		}
		if err := q.ReleaseSlug(ctx, statuspagesdb.ReleaseSlugParams{TenantID: a.TenantID, Slug: cur.Slug}); err != nil {
			return fmt.Errorf("statuspages: release slug: %w", err)
		}
		if err := q.DeleteSnapshot(ctx, statuspagesdb.DeleteSnapshotParams{TenantID: a.TenantID, PageID: id}); err != nil {
			return fmt.Errorf("statuspages: delete snapshot: %w", err)
		}
		return s.audit(ctx, tx, a, ActionDeleted, id, map[string]any{"slug": cur.Slug})
	})
}

// Components lists a page's components with their monitors (those who manage the page only:
// monitor names are shown).
func (s *Service) Components(ctx context.Context, a Actor, id uuid.UUID) ([]ComponentConfig, error) {
	var out []ComponentConfig
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		page, err := getPage(ctx, tx, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.checkManage(page.ProjectID); err != nil {
			return err
		}
		out, err = components(ctx, statuspagesdb.New(tx), a.TenantID, id)
		return err
	})
	return out, err
}

func components(ctx context.Context, q *statuspagesdb.Queries, tenantID, id uuid.UUID) ([]ComponentConfig, error) {
	rows, err := q.ListComponents(ctx, statuspagesdb.ListComponentsParams{TenantID: tenantID, PageID: id})
	if err != nil {
		return nil, fmt.Errorf("statuspages: components: %w", err)
	}
	out := make([]ComponentConfig, len(rows))
	for i, r := range rows {
		out[i] = ComponentConfig{MonitorID: r.MonitorID, MonitorName: r.MonitorName, MonitorType: r.MonitorType, ProjectID: r.ProjectID,
			PublicName: r.PublicName, GroupName: r.GroupName, Position: i}
	}
	return out, nil
}

// PutComponents replaces a page's components: the list is the new content and order (creating,
// renaming, regrouping, reordering and removing in one call). An organization-level page may use
// monitors and heartbeats of any project; a page of a project only those of that project.
func (s *Service) PutComponents(ctx context.Context, a Actor, id uuid.UUID, in []ComponentInput) ([]ComponentConfig, error) {
	var verr validation.Error
	if len(in) > MaxComponents {
		verr.Add("items", fmt.Sprintf("must have at most %d components", MaxComponents))
	}
	seen := map[uuid.UUID]bool{}
	clean := make([]ComponentInput, len(in))
	for i, c := range in {
		field := fmt.Sprintf("items.%d", i)
		name, msg := ValidateText(c.PublicName, 1, MaxNameLength)
		if msg != "" {
			verr.Add(field+".publicName", msg)
		}
		var group *string
		if c.GroupName != nil {
			g, msg := ValidateText(*c.GroupName, 0, MaxNameLength)
			if msg != "" {
				verr.Add(field+".groupName", msg)
			} else if g != "" {
				group = &g
			}
		}
		if seen[c.MonitorID] {
			verr.Add(field+".monitorId", "is already a component of this page")
		}
		seen[c.MonitorID] = true
		clean[i] = ComponentInput{MonitorID: c.MonitorID, PublicName: name, GroupName: group}
	}
	if err := verr.Err(); err != nil {
		return nil, err
	}
	var out []ComponentConfig
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		page, err := lockPage(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.checkManage(page.ProjectID); err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(clean))
		for i, c := range clean {
			ids[i] = c.MonitorID
		}
		live, err := q.LiveMonitorsOf(ctx, statuspagesdb.LiveMonitorsOfParams{TenantID: a.TenantID, Ids: ids})
		if err != nil {
			return fmt.Errorf("statuspages: monitors: %w", err)
		}
		projectOf := map[uuid.UUID]uuid.UUID{}
		for _, m := range live {
			projectOf[m.ID] = m.ProjectID
		}
		for i, c := range clean {
			p, ok := projectOf[c.MonitorID]
			switch {
			case !ok:
				verr.Add(fmt.Sprintf("items.%d.monitorId", i), "does not exist")
			case page.ProjectID != nil && p != *page.ProjectID:
				verr.Add(fmt.Sprintf("items.%d.monitorId", i), "must belong to the project of the page")
			}
		}
		if err := verr.Err(); err != nil {
			return err
		}
		if err := q.DeleteComponents(ctx, statuspagesdb.DeleteComponentsParams{TenantID: a.TenantID, PageID: id}); err != nil {
			return fmt.Errorf("statuspages: delete components: %w", err)
		}
		for i, c := range clean {
			if err := q.InsertComponent(ctx, statuspagesdb.InsertComponentParams{ID: uuidv7.New(), TenantID: a.TenantID, PageID: id,
				MonitorID: c.MonitorID, PublicName: c.PublicName, GroupName: c.GroupName, Position: int32(i)}); err != nil { //nolint:gosec // G115: at most MaxComponents
				return fmt.Errorf("statuspages: insert component: %w", err)
			}
		}
		if err := s.audit(ctx, tx, a, ActionUpdated, id, map[string]any{"fields": []string{"components"}, "components": len(clean)}); err != nil {
			return err
		}
		if _, err := Refresh(ctx, tx, a.TenantID, id, s.clock.Now()); err != nil {
			return err
		}
		out, err = components(ctx, q, a.TenantID, id)
		return err
	})
	return out, err
}

// Preview builds the public snapshot of a page as it would be published now, without storing it
// (those who manage the page only; works for unpublished pages).
func (s *Service) Preview(ctx context.Context, a Actor, id uuid.UUID) (Snapshot, error) {
	var out Snapshot
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		page, err := getPage(ctx, tx, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.checkManage(page.ProjectID); err != nil {
			return err
		}
		out, err = Build(ctx, tx, a.TenantID, id, s.clock.Now())
		return err
	})
	return out, err
}

// RebuildAll rebuilds the snapshot of every published page of an active tenant, each in a tenant
// transaction of its own, and drops components whose monitor was deleted. jobs runs it every 30 s.
// It returns the number of snapshots stored; one page's failure does not stop the rest.
func (s *Service) RebuildAll(ctx context.Context) (int, error) {
	type ref struct{ tenant, page uuid.UUID }
	var pages []ref
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT tenant_id, page_id FROM status_page_published_pages()`)
		if err != nil {
			return err
		}
		pages, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ref, error) {
			var x ref
			err := r.Scan(&x.tenant, &x.page)
			return x, err
		})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("statuspages: list published pages: %w", err)
	}
	n := 0
	var errs []error
	for _, p := range pages {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		err := db.WithTenantTx(ctx, s.pool, p.tenant, func(tx pgx.Tx) error {
			if _, err := statuspagesdb.New(tx).DeleteComponentsOfDeletedMonitors(ctx, p.tenant); err != nil {
				return fmt.Errorf("statuspages: drop components of deleted monitors: %w", err)
			}
			stored, err := Refresh(ctx, tx, p.tenant, p.page, s.clock.Now())
			if stored {
				n++
			}
			return err
		})
		if err != nil {
			s.log.ErrorContext(ctx, "status page snapshot rebuild failed", "tenant_id", p.tenant.String(), "page_id", p.page.String(), "error", err)
			errs = append(errs, err)
		}
	}
	return n, errors.Join(errs...)
}

func lockPage(ctx context.Context, q *statuspagesdb.Queries, tenantID, id uuid.UUID) (statuspagesdb.LockPageRow, error) {
	row, err := q.LockPage(ctx, statuspagesdb.LockPageParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, fmt.Errorf("statuspages: lock page: %w", err)
	}
	return row, nil
}

// checkSlugFree refuses an address another organization released less than 30 days ago (a live
// page's address is refused by the unique index). The answer does not say which case it is.
func checkSlugFree(ctx context.Context, q *statuspagesdb.Queries, slug string) error {
	held, err := q.SlugHeld(ctx, slug)
	if err != nil {
		return fmt.Errorf("statuspages: slug hold: %w", err)
	}
	if held {
		return ErrSlugTaken
	}
	return nil
}

func mapWriteErr(op string, err error) error {
	if db.IsUniqueViolation(err, "status_pages_slug_idx") {
		return ErrSlugTaken
	}
	return fmt.Errorf("statuspages: %s: %w", op, err)
}

func eqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, a Actor, action string, id uuid.UUID, meta map[string]any) error {
	actor := a.Access.UserID
	return audit.Record(ctx, tx, audit.Entry{
		TenantID: a.TenantID, ActorUserID: &actor, Action: action,
		EntityType: "status_page", EntityID: id.String(), Metadata: meta, IP: a.IP,
	})
}

func projectMeta(p *uuid.UUID) any {
	if p == nil {
		return nil
	}
	return p.String()
}
