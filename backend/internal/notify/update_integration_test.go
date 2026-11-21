package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const hookURL = "https://hooks.example.com/services/abc123"

// updateWorld is one organization with an organization-wide Slack channel, a webhook channel in the
// default project, and a second project.
type updateWorld struct {
	t        *testing.T
	ctx      context.Context
	svc      *notify.Service
	admin    *pgx.Conn
	pool     *db.Pool
	tenant   uuid.UUID
	owner    notify.Actor
	defProj  uuid.UUID
	other    uuid.UUID
	slack    notify.Channel // in the default project
	webhook  notify.Channel // in the default project
	orgChan  notify.Channel // organization-wide
	monitor  uuid.UUID
	monitor2 uuid.UUID
}

func newUpdateWorld(t *testing.T, name string) *updateWorld {
	t.Helper()
	env := pgtest.New(t, pgtest.DBName(name))
	ctx := context.Background()
	w := &updateWorld{t: t, ctx: ctx, admin: env.AdminConn(t), tenant: uuid.MustParse(tenantA), monitor: uuid.New(), monitor2: uuid.New(), other: uuid.New()}
	pgtest.MustExec(t, w.admin,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+userA+`', 'a@example.com')`)
	if err := w.admin.QueryRow(ctx, `SELECT id FROM projects WHERE tenant_id = $1 AND is_default`, w.tenant).Scan(&w.defProj); err != nil {
		t.Fatal(err)
	}
	if _, err := w.admin.Exec(ctx, `INSERT INTO projects (id, tenant_id, name, slug) VALUES ($1, $2, 'Other', 'other')`, w.other, w.tenant); err != nil {
		t.Fatal(err)
	}
	for _, m := range []uuid.UUID{w.monitor, w.monitor2} {
		if _, err := w.admin.Exec(ctx, `INSERT INTO monitors (id, tenant_id, project_id, name, config) VALUES ($1, $2, $3, $4, '{"url":"https://a.example","expectedStatus":"2xx"}')`,
			m, w.tenant, w.defProj, m.String()); err != nil {
			t.Fatal(err)
		}
	}
	w.pool = env.Pool(t, env.APIURL, 4)
	w.svc = notify.NewService(w.pool, keyring(t), &fakeSender{}, "https://proreta.example.com")
	w.owner = notify.Actor{TenantID: w.tenant, UserID: uuid.MustParse(userA), Access: authz.Actor{Role: authz.Owner}}
	var err error
	if w.slack, err = w.svc.Create(ctx, w.owner, notify.CreateInput{Name: "slack", WebhookURL: urlA, Events: &[]string{"down", "recovered"}, Mention: &[]string{"@here"}, ReminderMinutes: ptr(30)}); err != nil {
		t.Fatal(err)
	}
	if w.webhook, err = w.svc.Create(ctx, w.owner, notify.CreateInput{Name: "hook", Type: notify.TypeWebhook, WebhookURL: hookURL}); err != nil {
		t.Fatal(err)
	}
	if w.orgChan, err = w.svc.Create(ctx, w.owner, notify.CreateInput{Name: "org", WebhookURL: urlA2, OrganizationWide: true}); err != nil {
		t.Fatal(err)
	}
	if w.slack.ProjectID == nil || *w.slack.ProjectID != w.defProj || w.orgChan.ProjectID != nil {
		t.Fatalf("setup projects: %v %v", w.slack.ProjectID, w.orgChan.ProjectID)
	}
	return w
}

func (w *updateWorld) get(id uuid.UUID) notify.Channel {
	w.t.Helper()
	c, err := w.svc.Get(w.ctx, w.tenant, everyProject, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

type updateAudit struct {
	meta  map[string]any
	count int
}

// updatedAudit returns the newest channel.updated row of the channel and how many there are.
func (w *updateWorld) updatedAudit(id uuid.UUID) updateAudit {
	w.t.Helper()
	var out updateAudit
	if err := w.admin.QueryRow(w.ctx, `SELECT count(*) FROM audit_log WHERE action = 'channel.updated' AND entity_id = $1`, id.String()).Scan(&out.count); err != nil {
		w.t.Fatal(err)
	}
	var raw []byte
	err := w.admin.QueryRow(w.ctx, `SELECT metadata FROM audit_log WHERE action = 'channel.updated' AND entity_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, id.String()).Scan(&raw)
	if err == nil {
		if err := json.Unmarshal(raw, &out.meta); err != nil {
			w.t.Fatal(err)
		}
	}
	return out
}

func (w *updateWorld) fields(a updateAudit) []string {
	w.t.Helper()
	raw, _ := a.meta["fields"].([]any)
	var out []string
	for _, f := range raw {
		out = append(out, f.(string))
	}
	return out
}

func (w *updateWorld) link(monitor, channel uuid.UUID) {
	w.t.Helper()
	if _, err := w.admin.Exec(w.ctx, `INSERT INTO monitor_channels (tenant_id, monitor_id, channel_id) VALUES ($1, $2, $3)`, w.tenant, monitor, channel); err != nil {
		w.t.Fatal(err)
	}
}

func fieldsOf(err error) []string {
	var verr *validation.Error
	var out []string
	if errors.As(err, &verr) {
		for _, f := range verr.Fields {
			out = append(out, f.Field)
		}
	}
	return out
}

func TestIntegrationUpdateValidationAndOrder(t *testing.T) {
	w := newUpdateWorld(t, "notify_upd_valid")
	long := strings.Repeat("n", 200)
	seven := 3
	other := w.other
	// Request-level problems come first and are all reported, in the order name, events,
	// mention, reminderMinutes, organizationWide.
	_, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{
		Name: &long, Events: &[]string{}, Mention: &[]string{"@everyone"}, ReminderMinutes: &seven, ProjectID: &other, OrganizationWide: true,
	})
	if got := strings.Join(fieldsOf(err), ","); got != "name,events,mention,reminderMinutes,organizationWide" {
		t.Fatalf("fields = %q (%v)", got, err)
	}
	// They are reported before the channel is even looked up.
	_, err = w.svc.Update(w.ctx, w.owner, uuid.New(), notify.UpdateInput{Name: ptr(" ")})
	if got := strings.Join(fieldsOf(err), ","); got != "name" {
		t.Fatalf("unknown channel, bad name: %v", err)
	}
	if _, err := w.svc.Update(w.ctx, w.owner, uuid.New(), notify.UpdateInput{Name: ptr("x")}); !errors.Is(err, notify.ErrChannelNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}
	// A bad name is trimmed and stored trimmed.
	got, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{Name: ptr("  spaced  ")})
	if err != nil || got.Name != "spaced" {
		t.Fatalf("trim: %+v %v", got, err)
	}

	// Checks that need the stored channel run after the permission check, in the order webhook
	// URL, then content type / template / mention.
	cases := []struct {
		name string
		id   uuid.UUID
		in   notify.UpdateInput
		want string
	}{
		{"slack: webhook url must be Slack", w.slack.ID, notify.UpdateInput{WebhookURL: ptr("https://evil.example/x")}, "webhookUrl"},
		{"webhook: url must be public https", w.webhook.ID, notify.UpdateInput{WebhookURL: ptr("http://hooks.example.com/x")}, "webhookUrl"},
		{"slack: content type is webhook-only", w.slack.ID, notify.UpdateInput{ContentType: ptr("json")}, "contentType"},
		{"slack: template is webhook-only", w.slack.ID, notify.UpdateInput{BodyTemplate: ptr("x")}, "bodyTemplate"},
		{"slack: both are reported", w.slack.ID, notify.UpdateInput{ContentType: ptr("json"), BodyTemplate: ptr("x")}, "contentType,bodyTemplate"},
		{"webhook: mention is slack-only", w.webhook.ID, notify.UpdateInput{Mention: &[]string{"@here"}}, "mention"},
		{"webhook: bad content type", w.webhook.ID, notify.UpdateInput{ContentType: ptr("xml")}, "contentType"},
		{"webhook: empty template", w.webhook.ID, notify.UpdateInput{BodyTemplate: ptr(" ")}, "bodyTemplate"},
		{"webhook: unknown placeholder", w.webhook.ID, notify.UpdateInput{BodyTemplate: ptr(`{"x":"{{nope}}"}`)}, "bodyTemplate"},
		// The template is checked against the content type that applies after the change.
		{"webhook: template is not JSON", w.webhook.ID, notify.UpdateInput{ContentType: ptr("json"), BodyTemplate: ptr("plain {{status}}")}, "bodyTemplate"},
		{"url error before template error", w.webhook.ID, notify.UpdateInput{WebhookURL: ptr("http://x"), BodyTemplate: ptr(" ")}, "webhookUrl,bodyTemplate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := w.get(c.id)
			_, err := w.svc.Update(w.ctx, w.owner, c.id, c.in)
			if got := strings.Join(fieldsOf(err), ","); got != c.want {
				t.Fatalf("fields = %q (%v), want %q", got, err, c.want)
			}
			if after := w.get(c.id); after.Name != before.Name || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("a refused update changed the channel: %+v -> %+v", before, after)
			}
		})
	}
	if n := w.updatedAudit(w.webhook.ID).count + w.updatedAudit(w.slack.ID).count; n != 1 {
		t.Fatalf("%d audit rows, want the single trim", n)
	}

	// The permission check comes before the channel-level validation: a viewer is refused, not told
	// the URL is bad.
	viewer := w.owner
	viewer.Access = authz.Actor{Role: authz.Viewer}
	if _, err := w.svc.Update(w.ctx, viewer, w.orgChan.ID, notify.UpdateInput{WebhookURL: ptr("https://evil.example/x")}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("viewer on an organization channel: %v", err)
	}
}

func TestIntegrationUpdatePermissions(t *testing.T) {
	w := newUpdateWorld(t, "notify_upd_perm")
	projMaint := w.owner
	projMaint.Access = authz.Actor{Projects: map[uuid.UUID]authz.Role{w.defProj: authz.Maintainer}}
	projViewer := w.owner
	projViewer.Access = authz.Actor{Projects: map[uuid.UUID]authz.Role{w.defProj: authz.Viewer}}
	orgMember := w.owner
	orgMember.Access = authz.Actor{Role: authz.Member}
	cases := []struct {
		name string
		a    notify.Actor
		id   uuid.UUID
		in   notify.UpdateInput
		want error
	}{
		{"project maintainer edits a channel of the project", projMaint, w.slack.ID, notify.UpdateInput{Name: ptr("renamed")}, nil},
		{"project maintainer cannot touch an organization channel", projMaint, w.orgChan.ID, notify.UpdateInput{Name: ptr("x")}, authz.ErrForbidden},
		{"project viewer may not manage", projViewer, w.slack.ID, notify.UpdateInput{Name: ptr("x")}, authz.ErrProjectForbidden},
		{"a project the actor has no role in looks missing", projMaint, w.moveTo(w.other), notify.UpdateInput{Name: ptr("x")}, notify.ErrChannelNotFound},
		{"org member may not manage channels", orgMember, w.slack.ID, notify.UpdateInput{Name: ptr("x")}, authz.ErrProjectForbidden},
		{"organization channel for the owner", w.owner, w.orgChan.ID, notify.UpdateInput{Name: ptr("org2")}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := w.svc.Update(w.ctx, c.a, c.id, c.in)
			if c.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != c.want {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	// A project maintainer may not move a channel into a project they do not see (validation error,
	// as for a project that does not exist) nor make it organization-wide.
	if _, err := w.svc.Update(w.ctx, projMaint, w.slack.ID, notify.UpdateInput{ProjectID: &w.other}); strings.Join(fieldsOf(err), ",") != "projectId" {
		t.Fatalf("move into an unseen project: %v", err)
	}
	if _, err := w.svc.Update(w.ctx, projMaint, w.slack.ID, notify.UpdateInput{OrganizationWide: true}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("make organization-wide: %v", err)
	}
	if c := w.get(w.slack.ID); c.ProjectID == nil || *c.ProjectID != w.defProj {
		t.Fatalf("refused moves changed the project: %+v", c)
	}
}

// moveTo returns the id of a channel in the given project (created on demand), for the cases that
// need a channel the actor cannot see.
func (w *updateWorld) moveTo(project uuid.UUID) uuid.UUID {
	w.t.Helper()
	c, err := w.svc.Create(w.ctx, w.owner, notify.CreateInput{Name: "elsewhere", WebhookURL: urlB, ProjectID: &project})
	if err != nil {
		w.t.Fatal(err)
	}
	return c.ID
}

func TestIntegrationUpdateChangedFieldsAndAudit(t *testing.T) {
	w := newUpdateWorld(t, "notify_upd_audit")
	// No fields: no write, no audit row, no change of updated_at.
	before := w.get(w.slack.ID)
	same, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{})
	if err != nil || !same.UpdatedAt.Equal(before.UpdatedAt) || w.updatedAudit(w.slack.ID).count != 0 {
		t.Fatalf("empty update: %+v %v", same, err)
	}
	// Fields sent with their current values are not a change either (events are compared after
	// normalisation, so another order is the same set).
	same, err = w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{
		Name: ptr("slack"), Events: &[]string{"recovered", "down"}, Mention: &[]string{"@here"}, ReminderMinutes: ptr(30), ProjectID: &w.defProj,
	})
	if err != nil || !same.UpdatedAt.Equal(before.UpdatedAt) || w.updatedAudit(w.slack.ID).count != 0 {
		t.Fatalf("no-op update: %+v %v audit %d", same, err, w.updatedAudit(w.slack.ID).count)
	}

	// Each field, alone, is named in the audit row, in the fixed order name, events, mention,
	// reminderMinutes (then template, projectId, webhookUrl).
	got, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{
		ReminderMinutes: ptr(60), Mention: &[]string{}, Events: &[]string{"down"}, Name: ptr("renamed"),
	})
	if err != nil || got.Name != "renamed" || !slices.Equal(got.Events, []string{"down"}) || len(got.Mention) != 0 || got.ReminderMinutes != 60 {
		t.Fatalf("update: %+v %v", got, err)
	}
	a := w.updatedAudit(w.slack.ID)
	if a.count != 1 || strings.Join(w.fields(a), ",") != "name,events,mention,reminderMinutes" || len(a.meta) != 1 {
		t.Fatalf("audit = %+v", a)
	}
	// Webhook URL alone: a new key id in the metadata, never the URL; the status goes back to
	// active and the old URL is gone.
	pgtest.MustExec(t, w.admin, `UPDATE notification_channels SET status = 'failed' WHERE id = '`+w.slack.ID.String()+`'`)
	got, err = w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{WebhookURL: ptr(urlB)})
	if err != nil || got.Status != notify.StatusActive {
		t.Fatalf("webhook update: %+v %v", got, err)
	}
	a = w.updatedAudit(w.slack.ID)
	raw, _ := json.Marshal(a.meta)
	if a.count != 2 || strings.Join(w.fields(a), ",") != "webhookUrl" || a.meta["keyId"] != "k2" || len(a.meta) != 2 || strings.Contains(string(raw), "secret") {
		t.Fatalf("webhook audit = %+v", a)
	}
	// The same URL again is still a change (it is re-sealed), and an update with a webhook URL
	// and no other field never touches the other columns.
	if _, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{WebhookURL: ptr(urlB)}); err != nil || w.updatedAudit(w.slack.ID).count != 3 {
		t.Fatalf("same URL: %v", err)
	}

	// Template: a webhook channel's content type and template, changed together or alone.
	hook := w.get(w.webhook.ID)
	if hook.ContentType == nil || *hook.ContentType != "json" || hook.BodyTemplate == nil {
		t.Fatalf("webhook defaults: %+v", hook)
	}
	// Sending the stored values is not a change.
	if _, err := w.svc.Update(w.ctx, w.owner, w.webhook.ID, notify.UpdateInput{ContentType: hook.ContentType, BodyTemplate: hook.BodyTemplate}); err != nil || w.updatedAudit(w.webhook.ID).count != 0 {
		t.Fatalf("same template: %v", err)
	}
	got, err = w.svc.Update(w.ctx, w.owner, w.webhook.ID, notify.UpdateInput{ContentType: ptr("text"), BodyTemplate: ptr("{{monitor.name}} is {{status}}")})
	if err != nil || *got.ContentType != "text" || *got.BodyTemplate != "{{monitor.name}} is {{status}}" {
		t.Fatalf("template update: %+v %v", got, err)
	}
	if a := w.updatedAudit(w.webhook.ID); strings.Join(w.fields(a), ",") != "template" {
		t.Fatalf("template audit = %+v", a)
	}
	got, err = w.svc.Update(w.ctx, w.owner, w.webhook.ID, notify.UpdateInput{BodyTemplate: ptr("{{monitor.name}} changed")})
	if err != nil || *got.ContentType != "text" || *got.BodyTemplate != "{{monitor.name}} changed" {
		t.Fatalf("template-only update: %+v %v", got, err)
	}
	// A slack channel sent no template fields keeps NULL ones.
	if c := w.get(w.slack.ID); c.ContentType != nil || c.BodyTemplate != nil {
		t.Fatalf("slack channel got a template: %+v", c)
	}
	// Mention on a webhook channel can only be sent empty: an empty list passes, a value is refused.
	if _, err := w.svc.Update(w.ctx, w.owner, w.webhook.ID, notify.UpdateInput{Mention: &[]string{}}); err != nil {
		t.Fatalf("empty mention on a webhook: %v", err)
	}
	// A bad request is validated before anything is written, so a name change sent with a bad
	// template leaves the name alone.
	if _, err := w.svc.Update(w.ctx, w.owner, w.webhook.ID, notify.UpdateInput{Name: ptr("lost"), BodyTemplate: ptr(" ")}); strings.Join(fieldsOf(err), ",") != "bodyTemplate" {
		t.Fatal(err)
	}
	if c := w.get(w.webhook.ID); c.Name != "hook" {
		t.Fatalf("name = %q", c.Name)
	}
}

func TestIntegrationUpdateMovesAChannel(t *testing.T) {
	w := newUpdateWorld(t, "notify_upd_move")
	w.link(w.monitor, w.slack.ID)
	w.link(w.monitor, w.orgChan.ID)
	w.link(w.monitor2, w.slack.ID)

	// Moving to another project drops the channel from monitors of other projects, keeps the
	// organization-wide channels, and records the move and the count.
	got, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{ProjectID: &w.other, Name: ptr("moved")})
	if err != nil || got.ProjectID == nil || *got.ProjectID != w.other || got.Name != "moved" {
		t.Fatalf("move: %+v %v", got, err)
	}
	a := w.updatedAudit(w.slack.ID)
	if strings.Join(w.fields(a), ",") != "name,projectId" || a.meta["project"] != w.other.String() || a.meta["monitorsDetached"] != float64(2) || len(a.meta) != 3 {
		t.Fatalf("move audit = %+v", a)
	}
	var n int
	if err := w.admin.QueryRow(w.ctx, `SELECT count(*) FROM monitor_channels WHERE channel_id = $1`, w.slack.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("links left: %d %v", n, err)
	}
	if err := w.admin.QueryRow(w.ctx, `SELECT count(*) FROM monitor_channels WHERE channel_id = $1`, w.orgChan.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("organization channel links: %d %v", n, err)
	}
	// Same project again: nothing to do.
	if _, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{ProjectID: &w.other}); err != nil || w.updatedAudit(w.slack.ID).count != 1 {
		t.Fatalf("same project: %v", err)
	}
	// Organization-wide: no detach step, project recorded as "organization", links stay.
	w.link(w.monitor, w.webhook.ID)
	got, err = w.svc.Update(w.ctx, w.owner, w.webhook.ID, notify.UpdateInput{OrganizationWide: true})
	if err != nil || got.ProjectID != nil {
		t.Fatalf("organization-wide: %+v %v", got, err)
	}
	a = w.updatedAudit(w.webhook.ID)
	if strings.Join(w.fields(a), ",") != "projectId" || a.meta["project"] != "organization" || len(a.meta) != 2 {
		t.Fatalf("organization audit = %+v", a)
	}
	if err := w.admin.QueryRow(w.ctx, `SELECT count(*) FROM monitor_channels WHERE channel_id = $1`, w.webhook.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("links after going organization-wide: %d %v", n, err)
	}
	// An already organization-wide channel asked to be so again: no change.
	if _, err := w.svc.Update(w.ctx, w.owner, w.orgChan.ID, notify.UpdateInput{OrganizationWide: true}); err != nil || w.updatedAudit(w.orgChan.ID).count != 0 {
		t.Fatalf("org to org: %v", err)
	}
	// A move and a new URL together: fields in the order projectId, webhookUrl, and the key id
	// next to the project.
	got, err = w.svc.Update(w.ctx, w.owner, w.orgChan.ID, notify.UpdateInput{ProjectID: &w.defProj, WebhookURL: ptr(urlA)})
	if err != nil || got.ProjectID == nil || *got.ProjectID != w.defProj {
		t.Fatalf("move with url: %+v %v", got, err)
	}
	a = w.updatedAudit(w.orgChan.ID)
	if strings.Join(w.fields(a), ",") != "projectId,webhookUrl" || a.meta["keyId"] != "k2" || a.meta["monitorsDetached"] != float64(0) || len(a.meta) != 4 {
		t.Fatalf("move with url audit = %+v", a)
	}

	// A project that does not exist is a validation error ...
	missing := uuid.New()
	if _, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{ProjectID: &missing}); strings.Join(fieldsOf(err), ",") != "projectId" {
		t.Fatalf("missing project: %v", err)
	}
	// ... found only after the field update was written, so the transaction must roll the field
	// update and the earlier steps back.
	before := w.get(w.slack.ID)
	if _, err := w.svc.Update(w.ctx, w.owner, w.slack.ID, notify.UpdateInput{Name: ptr("rolled back"), ProjectID: &missing, WebhookURL: ptr(urlA2)}); strings.Join(fieldsOf(err), ",") != "projectId" {
		t.Fatalf("missing project with a name: %v", err)
	}
	if after := w.get(w.slack.ID); after.Name != before.Name || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("rollback: %+v -> %+v", before, after)
	}
	if a := w.updatedAudit(w.slack.ID); a.count != 1 {
		t.Fatalf("audit rows = %d", a.count)
	}
	// A project that is deleted is the same.
	pgtest.MustExec(t, w.admin, `UPDATE projects SET deleted_at = now() WHERE id = '`+w.other.String()+`'`)
	if _, err := w.svc.Update(w.ctx, w.owner, w.orgChan.ID, notify.UpdateInput{ProjectID: &w.other}); strings.Join(fieldsOf(err), ",") != "projectId" {
		t.Fatalf("deleted project: %v", err)
	}
}
