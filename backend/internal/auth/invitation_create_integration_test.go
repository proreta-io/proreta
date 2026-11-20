package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// inviteWorld is one organization with an owner and one member of every kind of standing, so the
// permission outcomes of CreateInvitationFor can be pinned per actor.
type inviteWorld struct {
	*verifyEnv
	tenant                                      uuid.UUID
	owner, maintainer, member, viewer, projOnly auth.Actor
	projMaintainerOnly                          auth.Actor
	defaultProject, otherProject                uuid.UUID
	ownerID                                     uuid.UUID
}

func newInviteWorld(t *testing.T, name string) *inviteWorld {
	t.Helper()
	e := newVerifyEnv(t, name, true)
	ownerRes := e.register("owner@example.com")
	w := &inviteWorld{verifyEnv: e, tenant: ownerRes.Tenant.ID, ownerID: ownerRes.User.ID}
	w.owner = auth.Actor{TenantID: w.tenant, UserID: ownerRes.User.ID, IP: testIP}
	join := func(email, role string) auth.Actor {
		res := e.register(email)
		var r any
		if role != "" {
			r = role
		}
		exec(t, e.admin, `INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3)`, w.tenant, res.User.ID, r)
		return auth.Actor{TenantID: w.tenant, UserID: res.User.ID, IP: testIP}
	}
	w.maintainer = join("maintainer@example.com", "maintainer")
	w.member = join("member@example.com", "member")
	w.viewer = join("viewer@example.com", "viewer")
	w.projOnly = join("projviewer@example.com", "")
	w.projMaintainerOnly = join("projmaint@example.com", "")
	if err := e.admin.QueryRow(e.ctx, `SELECT id FROM projects WHERE tenant_id = $1 AND is_default`, w.tenant).Scan(&w.defaultProject); err != nil {
		t.Fatal(err)
	}
	w.otherProject = uuid.New()
	exec(t, e.admin, `INSERT INTO projects (id, tenant_id, name, slug) VALUES ($1, $2, 'Other', 'other')`, w.otherProject, w.tenant)
	exec(t, e.admin, `INSERT INTO project_members (tenant_id, project_id, user_id, role) VALUES ($1, $2, $3, 'viewer'), ($1, $2, $4, 'maintainer')`,
		w.tenant, w.defaultProject, w.projOnly.UserID, w.projMaintainerOnly.UserID)
	return w
}

func exec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (w *inviteWorld) count(table string) int {
	w.t.Helper()
	var n int
	if err := w.admin.QueryRow(w.ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

type auditRow struct {
	Action, EntityType, EntityID string
	Actor                        uuid.UUID
	Meta                         map[string]any
}

// invitedAudit returns the audit rows of invitations created in the organization, oldest first.
func (w *inviteWorld) invitedAudit() []auditRow {
	w.t.Helper()
	rows, err := w.admin.Query(w.ctx, `SELECT action, entity_type, entity_id, actor_user_id, metadata FROM audit_log
		WHERE action IN ('member.invited', 'admin.invitation.created') ORDER BY created_at, id`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var raw []byte
		if err := rows.Scan(&r.Action, &r.EntityType, &r.EntityID, &r.Actor, &raw); err != nil {
			w.t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.Meta); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func fieldNames(err error) []string {
	var verr *validation.Error
	if !errors.As(err, &verr) {
		return nil
	}
	var out []string
	for _, f := range verr.Fields {
		out = append(out, f.Field)
	}
	return out
}

func TestIntegrationCreateInvitationForPermissions(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_perm")
	platform := auth.PlatformActor(w.tenant, w.ownerID, testIP)
	ghost := auth.Actor{TenantID: w.tenant, UserID: uuid.New(), IP: testIP}
	cases := []struct {
		name string
		a    auth.Actor
		in   auth.InvitationInput
		want error // nil = created
	}{
		{"owner invites maintainer", w.owner, auth.InvitationInput{Role: auth.RoleMaintainer}, nil},
		{"owner invites member", w.owner, auth.InvitationInput{Role: auth.RoleMember}, nil},
		{"owner invites viewer", w.owner, auth.InvitationInput{Role: auth.RoleViewer}, nil},
		{"owner cannot invite owner", w.owner, auth.InvitationInput{Role: auth.RoleOwner}, authz.ErrForbidden},
		{"maintainer invites maintainer", w.maintainer, auth.InvitationInput{Role: auth.RoleMaintainer}, nil},
		{"maintainer cannot invite owner", w.maintainer, auth.InvitationInput{Role: auth.RoleOwner}, authz.ErrForbidden},
		{"member cannot invite", w.member, auth.InvitationInput{Role: auth.RoleViewer}, authz.ErrForbidden},
		{"viewer cannot invite", w.viewer, auth.InvitationInput{Role: auth.RoleViewer}, authz.ErrForbidden},
		// An unknown role at organization level is a refusal, not a validation error.
		{"unknown role is forbidden", w.owner, auth.InvitationInput{Role: "bogus"}, authz.ErrForbidden},
		{"empty role is forbidden", w.owner, auth.InvitationInput{}, authz.ErrForbidden},
		{"platform admin invites owner", platform, auth.InvitationInput{Role: auth.RoleOwner}, nil},
		{"platform admin unknown role", platform, auth.InvitationInput{Role: "bogus"}, authz.ErrForbidden},
		{"not a member", ghost, auth.InvitationInput{Role: auth.RoleViewer}, auth.ErrUnauthenticated},
		{"project-only member has no organization role", w.projOnly, auth.InvitationInput{Role: auth.RoleViewer}, authz.ErrForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := w.count("invitations")
			inv, token, emailed, err := w.svc.CreateInvitationFor(w.ctx, c.a, c.in)
			if c.want == nil {
				if err != nil || token == "" || emailed || inv.ID == uuid.Nil || inv.Status != auth.InvitationOpen || inv.Role != c.in.Role {
					t.Fatalf("got %+v %q %v %v", inv, token, emailed, err)
				}
				if w.count("invitations") != before+1 {
					t.Fatal("no invitation row")
				}
				return
			}
			// The refusals come back as the sentinel itself (not wrapped), with nothing returned or stored.
			if err != c.want || token != "" || emailed || inv.ID != uuid.Nil || w.count("invitations") != before {
				t.Fatalf("got %+v %q %v %v (%d rows), want %v", inv, token, emailed, err, w.count("invitations"), c.want)
			}
		})
	}
}

func TestIntegrationCreateInvitationForValidation(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_valid")
	long := strings.Repeat("x", 81)
	cases := []struct {
		name   string
		a      auth.Actor
		in     auth.InvitationInput
		fields []string
	}{
		{"label too long", w.owner, auth.InvitationInput{Role: auth.RoleViewer, Label: long}, []string{"label"}},
		{"label counted in runes", w.owner, auth.InvitationInput{Role: auth.RoleViewer, Label: strings.Repeat("я", 81)}, []string{"label"}},
		{"email needed for SendEmail true", w.owner, auth.InvitationInput{Role: auth.RoleViewer, SendEmail: ptr(true)}, []string{"email"}},
		{"blank email for SendEmail true", w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "  ", SendEmail: ptr(true)}, []string{"email"}},
		{"bad email", w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "nope"}, []string{"email"}},
		{"project role invalid", w.owner, auth.InvitationInput{Role: "bogus", ProjectID: &w.defaultProject}, []string{"role"}},
		{"project role empty", w.owner, auth.InvitationInput{ProjectID: &w.defaultProject}, []string{"role"}},
		// All problems are reported together, in the order label, email, role.
		{"all three in order", w.owner, auth.InvitationInput{Role: "bogus", Label: long, Email: "nope", ProjectID: &w.defaultProject}, []string{"label", "email", "role"}},
		// Validation runs before permissions: a viewer with bad input is told what is wrong.
		{"validation before permission", w.viewer, auth.InvitationInput{Role: auth.RoleViewer, Label: long}, []string{"label"}},
		// A bad project role is a validation error even for a project the actor cannot see.
		{"validation before project lookup", w.owner, auth.InvitationInput{Role: "bogus", ProjectID: ptr(uuid.New())}, []string{"role"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, token, emailed, err := w.svc.CreateInvitationFor(w.ctx, c.a, c.in)
			got := fieldNames(err)
			if len(got) != len(c.fields) || strings.Join(got, ",") != strings.Join(c.fields, ",") || token != "" || emailed {
				t.Fatalf("fields = %v (err %v), want %v", got, err, c.fields)
			}
		})
	}
	if n := w.count("invitations") + w.count("mail_outbox WHERE kind = 'invitation'"); n != 0 {
		t.Fatalf("%d rows written by refused requests", n)
	}

	// The email problem message when the installation has no mail.
	off := newVerifyEnv(t, "auth_inv_valid_off", false)
	a := off.actor(off.register("owner@example.com"))
	for _, in := range []auth.InvitationInput{
		{Role: auth.RoleViewer, Email: "a@example.org"},
		{Role: auth.RoleViewer, SendEmail: ptr(true)},
		{Role: auth.RoleViewer, Email: "nope", SendEmail: ptr(true)},
	} {
		_, _, _, err := off.svc.CreateInvitationFor(off.ctx, a, in)
		var verr *validation.Error
		if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "email" || verr.Fields[0].Message != "this installation does not send email" {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	// SendEmail=false ignores an address (even a bad one) and works without mail.
	if _, _, emailed, err := off.svc.CreateInvitationFor(off.ctx, a, auth.InvitationInput{Role: auth.RoleViewer, Email: "nope", SendEmail: ptr(false)}); err != nil || emailed {
		t.Fatalf("SendEmail=false: %v %v", emailed, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestIntegrationCreateInvitationForLabelAndEmailFlags(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_label")
	longAddr := strings.Repeat("a", 70) + "@example.org" // 82 characters
	cases := []struct {
		name      string
		in        auth.InvitationInput
		label     string
		emailed   bool
		mailCount int
	}{
		{"label is trimmed", auth.InvitationInput{Role: auth.RoleViewer, Label: "  Ann  "}, "Ann", false, 0},
		{"80 runes is allowed", auth.InvitationInput{Role: auth.RoleViewer, Label: strings.Repeat("я", 80)}, strings.Repeat("я", 80), false, 0},
		{"address becomes the label", auth.InvitationInput{Role: auth.RoleViewer, Email: " Bob@Example.ORG "}, "bob@example.org", true, 1},
		{"blank label takes the address", auth.InvitationInput{Role: auth.RoleViewer, Label: "   ", Email: "carol@example.org"}, "carol@example.org", true, 1},
		{"address too long for a label", auth.InvitationInput{Role: auth.RoleViewer, Email: longAddr}, "", true, 1},
		{"SendEmail false drops the address", auth.InvitationInput{Role: auth.RoleViewer, Email: "dan@example.org", SendEmail: ptr(false)}, "", false, 0},
		{"SendEmail true sends", auth.InvitationInput{Role: auth.RoleViewer, Email: "eve@example.org", SendEmail: ptr(true)}, "eve@example.org", true, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := w.count("mail_outbox WHERE kind = 'invitation'")
			inv, _, emailed, err := w.svc.CreateInvitationFor(w.ctx, w.owner, c.in)
			if err != nil || inv.Label != c.label || emailed != c.emailed {
				t.Fatalf("label %q emailed %v err %v", inv.Label, emailed, err)
			}
			var stored string
			if err := w.admin.QueryRow(w.ctx, `SELECT label FROM invitations WHERE id = $1`, inv.ID).Scan(&stored); err != nil || stored != c.label {
				t.Fatalf("stored label %q %v", stored, err)
			}
			if got := w.count("mail_outbox WHERE kind = 'invitation'") - before; got != c.mailCount {
				t.Fatalf("%d mails queued, want %d", got, c.mailCount)
			}
			// Free the seat for the next case (the tenant has no cap, this keeps the list short).
			if err := w.svc.RevokeInvitation(w.ctx, w.owner, inv.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIntegrationCreateInvitationForRowsAndAudit(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_rows")
	inv, token, _, err := w.svc.CreateInvitationFor(w.ctx, w.maintainer, auth.InvitationInput{Role: auth.RoleMember, Label: "Zed"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.CreatedBy != "maintainer@example.com" || inv.Status != auth.InvitationOpen || inv.ProjectID != nil || inv.ProjectName != nil ||
		!inv.CreatedAt.Equal(w.clk.now) || inv.ExpiresAt.Sub(inv.CreatedAt) != auth.InvitationTTL {
		t.Fatalf("invitation = %+v", inv)
	}
	var hash []byte
	var createdBy uuid.UUID
	var expires time.Time
	var project *uuid.UUID
	if err := w.admin.QueryRow(w.ctx, `SELECT token_hash, created_by, expires_at, project_id FROM invitations WHERE id = $1`, inv.ID).Scan(&hash, &createdBy, &expires, &project); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	if string(hash) != string(sum[:]) || createdBy != w.maintainer.UserID || !expires.Equal(inv.ExpiresAt) || project != nil {
		t.Fatalf("row: %x %v %v %v", hash, createdBy, expires, project)
	}
	rows := w.invitedAudit()
	if len(rows) != 1 {
		t.Fatalf("audit rows = %+v", rows)
	}
	r := rows[0]
	wantExpiry := inv.ExpiresAt.UTC().Format(time.RFC3339)
	if r.Action != "member.invited" || r.EntityType != "invitation" || r.EntityID != inv.ID.String() || r.Actor != w.maintainer.UserID ||
		len(r.Meta) != 2 || r.Meta["role"] != "member" || r.Meta["expiresAt"] != wantExpiry {
		t.Fatalf("audit = %+v", r)
	}

	// With an address the customer row also says emailed.
	if _, _, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "g@example.org"}); err != nil {
		t.Fatal(err)
	}
	if r := w.invitedAudit()[1]; len(r.Meta) != 3 || r.Meta["emailed"] != true {
		t.Fatalf("audit with mail = %+v", r)
	}

	// The platform admin's row has its own action and a different metadata shape: no expiry,
	// emailed always present.
	platform := auth.PlatformActor(w.tenant, w.ownerID, testIP)
	pinv, _, _, err := w.svc.CreateInvitationFor(w.ctx, platform, auth.InvitationInput{Role: auth.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	if pinv.CreatedBy != "owner@example.com" {
		t.Fatalf("created by %q", pinv.CreatedBy)
	}
	if r := w.invitedAudit()[2]; r.Action != "admin.invitation.created" || r.EntityID != pinv.ID.String() || len(r.Meta) != 2 || r.Meta["role"] != "owner" || r.Meta["emailed"] != false {
		t.Fatalf("platform audit = %+v", r)
	}
	if _, _, emailed, err := w.svc.CreateInvitationFor(w.ctx, platform, auth.InvitationInput{Role: auth.RoleViewer, Email: "h@example.org"}); err != nil || !emailed {
		t.Fatal(emailed, err)
	}
	if r := w.invitedAudit()[3]; len(r.Meta) != 2 || r.Meta["emailed"] != true {
		t.Fatalf("platform audit with mail = %+v", r)
	}

	// An unknown creator id for the platform actor fails when the creator is read; the
	// failure is wrapped once more by the method.
	ghost := auth.PlatformActor(w.tenant, uuid.New(), testIP)
	_, _, _, err = w.svc.CreateInvitationFor(w.ctx, ghost, auth.InvitationInput{Role: auth.RoleViewer})
	if !errors.Is(err, pgx.ErrNoRows) || !strings.HasPrefix(err.Error(), "auth: create invitation: get creator: ") {
		t.Fatalf("ghost creator: %v", err)
	}
}

func TestIntegrationCreateInvitationForProject(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_project")
	platform := auth.PlatformActor(w.tenant, w.ownerID, testIP)
	missing := uuid.New()
	cases := []struct {
		name string
		a    auth.Actor
		in   auth.InvitationInput
		want error
	}{
		{"owner invites viewer into a project", w.owner, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &w.defaultProject}, nil},
		{"owner invites owner into a project", w.owner, auth.InvitationInput{Role: auth.RoleOwner, ProjectID: &w.otherProject}, nil},
		{"maintainer invites member into a project", w.maintainer, auth.InvitationInput{Role: auth.RoleMember, ProjectID: &w.otherProject}, nil},
		{"project maintainer invites member", w.projMaintainerOnly, auth.InvitationInput{Role: auth.RoleMember, ProjectID: &w.defaultProject}, nil},
		{"project maintainer cannot invite owner", w.projMaintainerOnly, auth.InvitationInput{Role: auth.RoleOwner, ProjectID: &w.defaultProject}, authz.ErrForbidden},
		{"project viewer may not manage members", w.projOnly, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &w.defaultProject}, authz.ErrProjectForbidden},
		{"project-only member cannot see another project", w.projMaintainerOnly, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &w.otherProject}, projects.ErrNotFound},
		{"unknown project", w.owner, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &missing}, projects.ErrNotFound},
		{"org viewer may not manage members", w.viewer, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &w.defaultProject}, authz.ErrProjectForbidden},
		{"platform admin invites into a project", platform, auth.InvitationInput{Role: auth.RoleOwner, ProjectID: &w.otherProject}, nil},
		{"platform admin unknown project", platform, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &missing}, projects.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := w.count("invitations")
			inv, token, _, err := w.svc.CreateInvitationFor(w.ctx, c.a, c.in)
			if c.want != nil {
				if err != c.want || token != "" || inv.ID != uuid.Nil || w.count("invitations") != before {
					t.Fatalf("got %+v %v, want %v", inv, err, c.want)
				}
				return
			}
			if err != nil || token == "" || inv.ProjectID == nil || *inv.ProjectID != *c.in.ProjectID || inv.ProjectName == nil || inv.Role != c.in.Role {
				t.Fatalf("got %+v %v", inv, err)
			}
			var stored *uuid.UUID
			if err := w.admin.QueryRow(w.ctx, `SELECT project_id FROM invitations WHERE id = $1`, inv.ID).Scan(&stored); err != nil || stored == nil || *stored != *c.in.ProjectID {
				t.Fatalf("stored project %v %v", stored, err)
			}
		})
	}
	// The project name is the real one, and the audit row names the project.
	inv, _, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &w.otherProject, Email: "p@example.org"})
	if err != nil || *inv.ProjectName != "Other" {
		t.Fatalf("%+v %v", inv, err)
	}
	rows := w.invitedAudit()
	last := rows[len(rows)-1]
	if last.Meta["projectId"] != w.otherProject.String() || last.Meta["emailed"] != true || len(last.Meta) != 4 {
		t.Fatalf("project audit = %+v", last)
	}
	// The mail names the project; an organization invitation's mail has no project parameter.
	mails, _ := w.invitationMails()
	if got := mails[len(mails)-1]["project"]; got != "Other" {
		t.Fatalf("mail params = %v", mails[len(mails)-1])
	}
	if _, _, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "q@example.org"}); err != nil {
		t.Fatal(err)
	}
	mails, _ = w.invitationMails()
	if _, ok := mails[len(mails)-1]["project"]; ok {
		t.Fatalf("organization mail has a project: %v", mails[len(mails)-1])
	}
	// The platform admin's row for a project carries the project id too.
	if _, _, _, err := w.svc.CreateInvitationFor(w.ctx, platform, auth.InvitationInput{Role: auth.RoleViewer, ProjectID: &w.defaultProject}); err != nil {
		t.Fatal(err)
	}
	rows = w.invitedAudit()
	last = rows[len(rows)-1]
	if last.Action != "admin.invitation.created" || last.Meta["projectId"] != w.defaultProject.String() || len(last.Meta) != 3 {
		t.Fatalf("platform project audit = %+v", last)
	}
}

// The quota and the permissions are checked inside the transaction, after the mail budget was
// spent; every failure gives the budget back and leaves no row.
func TestIntegrationCreateInvitationForRefundsTheMailBudget(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_refund")
	svc, err := auth.NewService(w.env.Pool(t, w.env.APIURL, 8), auth.Options{
		SessionTTL: time.Hour, Mail: mail.NewQueue(w.keys, mail.AllFeatures, nil), Clock: w.clk,
		InviteMailLimiter:          auth.NewLimiter(100, time.Hour, 100, clock.System{}),
		InviteMailRecipientLimiter: auth.NewLimiter(1, 24*time.Hour, 100, clock.System{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A refused request (the viewer may not invite) gives the recipient's budget back ...
	if _, _, _, err := svc.CreateInvitationFor(w.ctx, w.viewer, auth.InvitationInput{Role: auth.RoleViewer, Email: "r@example.org"}); err != authz.ErrForbidden {
		t.Fatalf("viewer: %v", err)
	}
	if _, _, emailed, err := svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "r@example.org"}); err != nil || !emailed {
		t.Fatalf("after the refusal: %v %v", emailed, err)
	}
	// ... and so does one that hit the plan limit.
	pgtest.MustExec(t, w.admin, pgtest.CappedPlanSQL, `UPDATE tenants SET plan_key = 'capped'`)
	var limit *plans.LimitError
	_, _, _, err = svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "s@example.org"})
	if !errors.As(err, &limit) || err != error(limit) {
		t.Fatalf("limit: %T %v", err, err)
	}
	pgtest.MustExec(t, w.admin, `UPDATE tenants SET plan_key = 'internal'`)
	if _, _, emailed, err := svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "s@example.org"}); err != nil || !emailed {
		t.Fatalf("after the limit: %v %v", emailed, err)
	}
	// Only the two successful ones were queued.
	if _, to := w.invitationMails(); len(to) != 2 {
		t.Fatalf("mails to %v", to)
	}
}
