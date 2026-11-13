package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/notify/notifydb"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects"
)

// Channel statuses (notification_channels.status).
const (
	StatusActive = "active"
	StatusFailed = "failed"
)

// Channel types (notification_channels.type).
const (
	// TypeSlack is a Slack Incoming Webhook.
	TypeSlack = "slack_webhook"
	// TypeWebhook posts the message of a template to any https endpoint.
	TypeWebhook = "webhook"
)

// ChannelTypes lists every channel type.
var ChannelTypes = []string{TypeSlack, TypeWebhook}

// Audit actions written by this module. Their metadata never contains the webhook URL.
const (
	ActionChannelCreated = "channel.created"
	ActionChannelUpdated = "channel.updated"
	ActionChannelDeleted = "channel.deleted"
	ActionChannelTested  = "channel.tested"
)

var (
	// ErrChannelNotFound means no live channel with that id exists in the tenant (or a delivery
	// names a channel that does not exist in its tenant).
	ErrChannelNotFound = errors.New("notify: channel not found")
	// ErrBadTemplate means a stored template does not render to a usable body; retrying will not help.
	ErrBadTemplate = errors.New("notify: webhook template does not render to a valid body")
	// ErrUndecryptable means the stored config cannot be decrypted (unknown key or tampering).
	ErrUndecryptable = errors.New("notify: channel config cannot be decrypted")
)

// sender sends one Slack message; *SlackNotifier implements it.
type sender interface {
	Send(ctx context.Context, webhookURL, text string) error
}

// webhookSender sends one rendered body to a webhook; *WebhookNotifier implements it.
type webhookSender interface {
	Send(ctx context.Context, webhookURL, contentType, body string) error
}

// Actor is who changes channels; it scopes every operation to one tenant, and Access to the
// projects whose channels the user may manage.
type Actor struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	IP       netip.Addr
	Access   authz.Actor
}

// canManage checks that the actor may change a channel of a project (nil = organization-wide).
// An organization-wide channel needs the permission through the organization role. A project the
// actor has no role in is reported as a missing channel, so ids of other projects reveal nothing.
func (a Actor) canManage(project *uuid.UUID) error {
	if project == nil {
		if !a.Access.Holds("channel.manage") {
			return authz.ErrForbidden
		}
		return nil
	}
	if !a.Access.Sees(*project) {
		return ErrChannelNotFound
	}
	if !a.Access.CanIn(*project, "channel.manage") {
		return authz.ErrProjectForbidden
	}
	return nil
}

// canUse checks the project a channel is created in or moved to: unknown to the actor is a
// validation error like a project that does not exist.
func (a Actor) canUse(project *uuid.UUID) error {
	if project != nil && !a.Access.Sees(*project) {
		var verr validation.Error
		verr.Add("projectId", "does not exist")
		return &verr
	}
	return a.canManage(project)
}

// Channel is what the api may show about a channel: never the webhook URL.
type Channel struct {
	ID              uuid.UUID
	Name            string
	Type            string
	Events          []string
	Mention         []string
	ReminderMinutes int
	// ContentType and BodyTemplate are set on webhook channels whose settings are visible; the
	// template is not secret.
	ContentType  *string
	BodyTemplate *string
	// ProjectID is the channel's project; nil for an organization-wide channel.
	ProjectID *uuid.UUID
	// ConfigHidden is true when the settings were withheld by ForViewer.
	ConfigHidden bool
	// Status is StatusActive or StatusFailed.
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is a new channel; nil fields take their defaults (every event, no mention, no
// reminders, the Default project).
type CreateInput struct {
	// ProjectID is the channel's project (the Default project when nil); OrganizationWide makes
	// the channel organization-wide instead.
	ProjectID        *uuid.UUID
	OrganizationWide bool
	// Type is TypeSlack when empty.
	Type       string
	Name       string
	WebhookURL string
	// ContentType and BodyTemplate are for webhook channels; the defaults are json and its
	// template.
	ContentType     *string
	BodyTemplate    *string
	Events          *[]string
	Mention         *[]string
	ReminderMinutes *int
}

// UpdateInput changes the non-nil fields. ProjectID moves the channel to a project,
// OrganizationWide makes it organization-wide.
type UpdateInput struct {
	ProjectID        *uuid.UUID
	OrganizationWide bool
	Name             *string
	WebhookURL       *string
	ContentType      *string
	BodyTemplate     *string
	Events           *[]string
	Mention          *[]string
	ReminderMinutes  *int
}

// Target is a channel resolved for delivery.
type Target struct {
	// Type is the channel type (TypeSlack), for metrics.
	Type       string
	Status     string
	WebhookURL string
	Mention    []string
	// ContentType, BodyTemplate and Hook are set for webhook channels.
	ContentType  string
	BodyTemplate string
	Hook         WebhookContext
	// Deleted channels have no webhook; their deliveries fail.
	Deleted bool
}

// Service manages the tenant's notification channels.
type Service struct {
	pool      *db.Pool
	keys      *crypto.Keyring
	sender    sender
	webhook   webhookSender
	publicURL string
}

// NewService returns a Service. sender may be nil in processes that never send (it is used by
// Test only).
func NewService(pool *db.Pool, keys *crypto.Keyring, s sender, publicURL string) *Service {
	return &Service{pool: pool, keys: keys, sender: s, publicURL: publicURL}
}

// WithWebhook sets the sender of test messages to webhook channels and returns s.
func (s *Service) WithWebhook(w webhookSender) *Service {
	s.webhook = w
	return s
}

// channelAAD binds a ciphertext to its row, so a value copied to another channel (or tenant)
// does not decrypt.
func channelAAD(id uuid.UUID) []byte {
	return []byte("notification_channels.config_encrypted:" + id.String())
}

// ForViewer withholds the settings of an organization-wide channel from an actor without the
// organization role: people who only hold project roles may select it for their monitors and see
// its name, nothing else.
func (c Channel) ForViewer(a authz.Actor) Channel {
	if c.ProjectID != nil || a.Holds("channel.manage") {
		return c
	}
	c.ConfigHidden = true
	c.Events, c.Mention, c.ReminderMinutes = []string{}, []string{}, 0
	c.ContentType, c.BodyTemplate = nil, nil
	return c
}

// Visible reports whether a channel is visible within scope: organization-wide channels are
// visible to everyone, the others to those who see their project.
func (c Channel) Visible(scope authz.Scope) bool {
	return c.ProjectID == nil || scope.Includes(*c.ProjectID)
}

// List returns the tenant's live channels visible in scope, oldest first. With forProject, only
// the channels a monitor of that project may select: its own and the organization-wide ones.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, scope authz.Scope, forProject *uuid.UUID) ([]Channel, error) {
	out := []Channel{}
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := notifydb.New(tx).ListChannels(ctx, tenantID)
		if err != nil {
			return fmt.Errorf("notify: list channels: %w", err)
		}
		for _, r := range rows {
			c := toChannel(r)
			if !c.Visible(scope) {
				continue
			}
			if forProject != nil && c.ProjectID != nil && *c.ProjectID != *forProject {
				continue
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// Get returns one live channel visible in scope, or ErrChannelNotFound.
func (s *Service) Get(ctx context.Context, tenantID uuid.UUID, scope authz.Scope, id uuid.UUID) (Channel, error) {
	var out Channel
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = get(ctx, notifydb.New(tx), tenantID, id)
		if err == nil && !out.Visible(scope) {
			err = ErrChannelNotFound
		}
		return err
	})
	return out, err
}

func get(ctx context.Context, q *notifydb.Queries, tenantID, id uuid.UUID) (Channel, error) {
	r, err := q.GetChannelInfo(ctx, notifydb.GetChannelInfoParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("notify: get channel: %w", err)
	}
	return toChannel(notifydb.ListChannelsRow(r)), nil
}

func toChannel(r notifydb.ListChannelsRow) Channel {
	c := Channel{ID: r.ID, Name: r.Name, Type: r.Type, Events: r.Events, Mention: r.Mention,
		ReminderMinutes: int(r.ReminderMinutes), Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ProjectID: r.ProjectID,
		ContentType: r.ContentType, BodyTemplate: r.BodyTemplate}
	if c.Events == nil {
		c.Events = []string{}
	}
	if c.Mention == nil {
		c.Mention = []string{}
	}
	return c
}

// channelFields are the validated settings of a channel.
type channelFields struct {
	name     string
	events   []string
	mention  []string
	reminder int
}

func (f *channelFields) apply(verr *validation.Error, name *string, events, mention *[]string, reminder *int) {
	if name != nil {
		v, msg := ValidateChannelName(*name)
		if msg != "" {
			verr.Add("name", msg)
		}
		f.name = v
	}
	if events != nil {
		v, msg := ValidateEvents(*events)
		if msg != "" {
			verr.Add("events", msg)
		}
		f.events = v
	}
	if mention != nil {
		v, msg := ValidateMention(*mention)
		if msg != "" {
			verr.Add("mention", msg)
		}
		f.mention = v
	}
	if reminder != nil {
		if msg := ValidateReminderMinutes(*reminder); msg != "" {
			verr.Add("reminderMinutes", msg)
		}
		f.reminder = *reminder
	}
}

// checkTarget validates the destination URL of a channel of channelType and returns the form to
// store.
func checkTarget(verr *validation.Error, channelType, raw string) string {
	if channelType == TypeWebhook {
		u, msg := ValidateWebhookTargetURL(raw)
		if msg != "" {
			verr.Add("webhookUrl", msg)
		}
		return u
	}
	if msg := ValidateWebhookURL(raw); msg != "" {
		verr.Add("webhookUrl", msg)
	}
	return raw
}

// checkWebhookFields validates the content type, template and mention of a channel of
// channelType. contentType and tpl are the values after the change; changed says whether the
// request touched either of them or the mention.
func checkWebhookFields(verr *validation.Error, channelType string, contentType, tpl *string, mention []string) {
	if channelType != TypeWebhook {
		if contentType != nil {
			verr.Add("contentType", "is only for webhook channels")
		}
		if tpl != nil {
			verr.Add("bodyTemplate", "is only for webhook channels")
		}
		return
	}
	if len(mention) > 0 {
		verr.Add("mention", "is only for Slack channels")
	}
	if contentType == nil || tpl == nil {
		return
	}
	if msg := ValidateContentType(*contentType); msg != "" {
		verr.Add("contentType", msg)
		return
	}
	if msg := ValidateTemplate(*contentType, *tpl); msg != "" {
		verr.Add("bodyTemplate", msg)
	}
}

// Create validates in, checks the tenant's channel quota (a *plans.LimitError when it is used
// up) and stores the channel with its webhook URL encrypted under the active key.
func (s *Service) Create(ctx context.Context, a Actor, in CreateInput) (Channel, error) {
	f := channelFields{events: DefaultEvents, mention: []string{}}
	var verr validation.Error
	if in.Type == "" {
		in.Type = TypeSlack
	}
	if !slices.Contains(ChannelTypes, in.Type) {
		verr.Add("type", `must be "slack_webhook" or "webhook"`)
		return Channel{}, &verr
	}
	f.apply(&verr, &in.Name, in.Events, in.Mention, in.ReminderMinutes)
	target := checkTarget(&verr, in.Type, in.WebhookURL)
	contentType, tpl := in.ContentType, in.BodyTemplate
	if in.Type == TypeWebhook {
		if contentType == nil {
			ct := ContentJSON
			contentType = &ct
		}
		if tpl == nil {
			d := DefaultTemplate(*contentType)
			tpl = &d
		}
	}
	checkWebhookFields(&verr, in.Type, contentType, tpl, f.mention)
	checkProject(&verr, in.ProjectID, in.OrganizationWide)
	if err := verr.Err(); err != nil {
		return Channel{}, err
	}
	id := uuidv7.New()
	sealed, err := s.keys.Encrypt([]byte(target), channelAAD(id))
	if err != nil {
		return Channel{}, fmt.Errorf("notify: encrypt: %w", err)
	}
	var out Channel
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		// The tenant row lock taken here serialises concurrent creates, so the count stays true
		// until this transaction commits.
		if err := plans.CheckCreate(ctx, tx, a.TenantID, plans.ResourceChannel); err != nil {
			return err
		}
		q := notifydb.New(tx)
		project, err := channelProject(ctx, tx, a.TenantID, in.ProjectID, in.OrganizationWide)
		if err != nil {
			return err
		}
		if err := a.canUse(project); err != nil {
			return err
		}
		if err := q.InsertChannel(ctx, notifydb.InsertChannelParams{
			ID: id, TenantID: a.TenantID, Type: in.Type, Name: f.name, ConfigEncrypted: sealed, ProjectID: project,
			ContentType: contentType, BodyTemplate: tpl,
			Events: f.events, Mention: f.mention, ReminderMinutes: int32(f.reminder), //nolint:gosec // validated 0..1440
		}); err != nil {
			return fmt.Errorf("notify: insert channel: %w", err)
		}
		if err := s.audit(ctx, tx, a, ActionChannelCreated, id, map[string]any{"keyId": s.keys.ActiveKeyID(), "type": in.Type, "project": projectMeta(project)}); err != nil {
			return err
		}
		out, err = get(ctx, q, a.TenantID, id)
		return err
	})
	if err != nil {
		return Channel{}, err
	}
	return out, nil
}

// Update changes the given fields. A new webhook URL replaces the stored one and makes a failed
// channel active again; the id stays, so queued deliveries go to the new URL.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, in UpdateInput) (Channel, error) {
	var verr validation.Error
	var upd channelFields
	upd.apply(&verr, in.Name, in.Events, in.Mention, in.ReminderMinutes)
	checkProject(&verr, in.ProjectID, in.OrganizationWide)
	if err := verr.Err(); err != nil {
		return Channel{}, err
	}
	var out Channel
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := notifydb.New(tx)
		if in.ProjectID != nil {
			// Monitors before the channel, the order of a monitor move.
			if _, err := q.LockLinkedMonitors(ctx, notifydb.LockLinkedMonitorsParams{TenantID: a.TenantID, ChannelID: id}); err != nil {
				return fmt.Errorf("notify: lock linked monitors: %w", err)
			}
		}
		if _, err := lock(ctx, q, a.TenantID, id); err != nil {
			return err
		}
		cur, err := get(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.canManage(cur.ProjectID); err != nil {
			return err
		}
		var tv validation.Error
		var target string
		if in.WebhookURL != nil {
			target = checkTarget(&tv, cur.Type, *in.WebhookURL)
		}
		contentType, tpl := cur.ContentType, cur.BodyTemplate
		touched := in.ContentType != nil || in.BodyTemplate != nil
		if in.ContentType != nil {
			contentType = in.ContentType
		}
		if in.BodyTemplate != nil {
			tpl = in.BodyTemplate
		}
		if touched || in.Mention != nil {
			var m []string
			if in.Mention != nil {
				m = upd.mention
			}
			if touched {
				checkWebhookFields(&tv, cur.Type, contentType, tpl, m)
			} else {
				checkWebhookFields(&tv, cur.Type, nil, nil, m)
			}
		}
		if err := tv.Err(); err != nil {
			return err
		}
		f := channelFields{name: cur.Name, events: cur.Events, mention: cur.Mention, reminder: cur.ReminderMinutes}
		if in.Name != nil {
			f.name = upd.name
		}
		if in.Events != nil {
			f.events = upd.events
		}
		if in.Mention != nil {
			f.mention = upd.mention
		}
		if in.ReminderMinutes != nil {
			f.reminder = upd.reminder
		}
		var changed []string
		if f.name != cur.Name {
			changed = append(changed, "name")
		}
		if !slices.Equal(f.events, cur.Events) {
			changed = append(changed, "events")
		}
		if !slices.Equal(f.mention, cur.Mention) {
			changed = append(changed, "mention")
		}
		if f.reminder != cur.ReminderMinutes {
			changed = append(changed, "reminderMinutes")
		}
		if touched && (!equalPtr(contentType, cur.ContentType) || !equalPtr(tpl, cur.BodyTemplate)) {
			changed = append(changed, "template")
		}
		if len(changed) > 0 {
			if err := q.UpdateChannel(ctx, notifydb.UpdateChannelParams{
				TenantID: a.TenantID, ID: id, Name: f.name, Events: f.events, Mention: f.mention,
				ContentType: contentType, BodyTemplate: tpl,
				ReminderMinutes: int32(f.reminder), //nolint:gosec // validated 0..1440
			}); err != nil {
				return fmt.Errorf("notify: update channel: %w", err)
			}
		}
		meta := map[string]any{}
		if in.ProjectID != nil || in.OrganizationWide {
			project, err := channelProject(ctx, tx, a.TenantID, in.ProjectID, in.OrganizationWide)
			if err != nil {
				return err
			}
			if !sameProject(project, cur.ProjectID) {
				if err := a.canUse(project); err != nil {
					return err
				}
				if err := q.SetChannelProject(ctx, notifydb.SetChannelProjectParams{TenantID: a.TenantID, ID: id, ProjectID: project}); err != nil {
					return fmt.Errorf("notify: move channel: %w", err)
				}
				changed = append(changed, "projectId")
				meta["project"] = projectMeta(project)
				if project != nil {
					detached, err := q.DetachChannelFromOtherProjects(ctx, notifydb.DetachChannelFromOtherProjectsParams{TenantID: a.TenantID, ChannelID: id, ProjectID: *project})
					if err != nil {
						return fmt.Errorf("notify: detach channel: %w", err)
					}
					meta["monitorsDetached"] = detached
				}
			}
		}
		if in.WebhookURL != nil {
			sealed, err := s.keys.Encrypt([]byte(target), channelAAD(id))
			if err != nil {
				return fmt.Errorf("notify: encrypt: %w", err)
			}
			if err := q.ReplaceChannelConfig(ctx, notifydb.ReplaceChannelConfigParams{TenantID: a.TenantID, ID: id, ConfigEncrypted: sealed}); err != nil {
				return fmt.Errorf("notify: replace webhook: %w", err)
			}
			changed = append(changed, "webhookUrl")
			meta["keyId"] = s.keys.ActiveKeyID()
		}
		if len(changed) > 0 {
			meta["fields"] = changed
			if err := s.audit(ctx, tx, a, ActionChannelUpdated, id, meta); err != nil {
				return err
			}
		}
		out, err = get(ctx, q, a.TenantID, id)
		return err
	})
	if err != nil {
		return Channel{}, err
	}
	return out, nil
}

// Delete soft-deletes a channel: its webhook URL is wiped, monitors that selected it lose it
// from their selection, and its pending deliveries are failed with last_error channel_deleted.
// Deliveries already in progress fail in jobs when they find the channel deleted.
func (s *Service) Delete(ctx context.Context, a Actor, id uuid.UUID) error {
	return db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := notifydb.New(tx)
		ch, err := lock(ctx, q, a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.canManage(ch.ProjectID); err != nil {
			return err
		}
		detached, err := q.DetachChannel(ctx, notifydb.DetachChannelParams{TenantID: a.TenantID, ChannelID: id})
		if err != nil {
			return fmt.Errorf("notify: detach channel: %w", err)
		}
		if err := q.SoftDeleteChannel(ctx, notifydb.SoftDeleteChannelParams{TenantID: a.TenantID, ID: id}); err != nil {
			return fmt.Errorf("notify: delete channel: %w", err)
		}
		failed, err := q.FailDeletedChannelDeliveries(ctx, id)
		if err != nil {
			return fmt.Errorf("notify: fail deliveries of deleted channel: %w", err)
		}
		return s.audit(ctx, tx, a, ActionChannelDeleted, id, map[string]any{"monitorsDetached": detached, "deliveriesFailed": failed})
	})
}

func lock(ctx context.Context, q *notifydb.Queries, tenantID, id uuid.UUID) (notifydb.LockChannelRow, error) {
	row, err := q.LockChannel(ctx, notifydb.LockChannelParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrChannelNotFound
	}
	if err != nil {
		return row, fmt.Errorf("notify: lock channel: %w", err)
	}
	return row, nil
}

// Test sends a test message to a channel's webhook. It returns ErrChannelNotFound for an unknown
// channel and a *SendError when Slack does not accept the message; a revoked webhook (404/410)
// marks the channel failed, like a failed delivery.
func (s *Service) Test(ctx context.Context, a Actor, id uuid.UUID) error {
	var target Target
	var body string
	isWebhook := false
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		ch, err := lock(ctx, notifydb.New(tx), a.TenantID, id)
		if err != nil {
			return err
		}
		if err := a.canManage(ch.ProjectID); err != nil {
			return err
		}
		target, err = s.open(id, ch.Status, ch.ConfigEncrypted)
		if err != nil {
			return err
		}
		if isWebhook = ch.Type == TypeWebhook; isWebhook {
			if ch.ContentType == nil || ch.BodyTemplate == nil {
				return ErrBadTemplate
			}
			if body, err = s.PreviewTemplate(*ch.ContentType, *ch.BodyTemplate); err != nil {
				return err
			}
			target.ContentType = *ch.ContentType
		}
		return nil
	})
	if err != nil {
		return err
	}

	var sendErr error
	if isWebhook {
		if s.webhook == nil {
			return errors.New("notify: no webhook sender configured")
		}
		sendErr = s.webhook.Send(ctx, target.WebhookURL, target.ContentType, body)
	} else {
		sendErr = s.sender.Send(ctx, target.WebhookURL, TestMessage(s.publicURL))
	}
	result := "ok"
	var se *SendError
	if errors.As(sendErr, &se) {
		result = se.Kind
	} else if sendErr != nil {
		result = KindOther
	}
	// The outcome is recorded even when the request was cancelled meanwhile.
	bg := context.WithoutCancel(ctx)
	err = db.WithTenantTx(bg, s.pool, a.TenantID, func(tx pgx.Tx) error {
		if se != nil && se.Permanent {
			if _, err := s.MarkFailed(bg, tx, a.TenantID, id); err != nil {
				return err
			}
		}
		return s.audit(bg, tx, a, ActionChannelTested, id, map[string]any{"result": result})
	})
	if sendErr != nil {
		return sendErr
	}
	return err
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, a Actor, action string, id uuid.UUID, meta map[string]any) error {
	return audit.Record(ctx, tx, audit.Entry{
		TenantID: a.TenantID, ActorUserID: &a.UserID, Action: action,
		EntityType: "notification_channel", EntityID: id.String(), Metadata: meta, IP: a.IP,
	})
}

// DeliveryTarget reads and decrypts a channel for delivery. tx must be a tenant transaction of
// tenantID (jobs uses the tenant returned by claim_deliveries, so RLS still scopes the read). A
// deleted channel is returned with Deleted set and no URL.
func (s *Service) DeliveryTarget(ctx context.Context, tx pgx.Tx, tenantID, channelID uuid.UUID) (Target, error) {
	ch, err := notifydb.New(tx).GetChannel(ctx, notifydb.GetChannelParams{TenantID: tenantID, ID: channelID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, ErrChannelNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("notify: get channel: %w", err)
	}
	if !slices.Contains(ChannelTypes, ch.Type) {
		return Target{}, ErrChannelNotFound
	}
	if ch.Deleted {
		return Target{Type: ch.Type, Status: ch.Status, Deleted: true}, nil
	}
	t, err := s.open(channelID, ch.Status, ch.ConfigEncrypted)
	t.Type, t.Mention = ch.Type, ch.Mention
	if err != nil || ch.Type != TypeWebhook {
		return t, err
	}
	if ch.ContentType == nil || ch.BodyTemplate == nil {
		return t, ErrBadTemplate
	}
	t.ContentType, t.BodyTemplate = *ch.ContentType, *ch.BodyTemplate
	return t, nil
}

// LoadWebhookContext reads the names a webhook template may use for the monitor of a delivery;
// a monitor that no longer exists leaves them empty. tx must be a tenant transaction.
func (s *Service) LoadWebhookContext(ctx context.Context, tx pgx.Tx, monitorID uuid.UUID) (WebhookContext, error) {
	// Not a sqlc query: sqlc cannot resolve the columns of a function returning a table.
	var c WebhookContext
	err := tx.QueryRow(ctx, `SELECT monitor_type, project_name, organization_name FROM webhook_delivery_context($1)`, monitorID).
		Scan(&c.MonitorType, &c.ProjectName, &c.OrganizationName)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookContext{}, nil
	}
	if err != nil {
		return WebhookContext{}, fmt.Errorf("notify: webhook context: %w", err)
	}
	return c, nil
}

// LoadSpeculaContext is the Specula counterpart of LoadWebhookContext: the organization name for a
// template, and whether the channel may still serve the Specula (shared with the channel's
// project). A Specula that no longer exists is not routed. tx must be a tenant transaction.
func (s *Service) LoadSpeculaContext(ctx context.Context, tx pgx.Tx, speculaID, channelID uuid.UUID) (WebhookContext, bool, error) {
	var c WebhookContext
	var routed bool
	err := tx.QueryRow(ctx, `SELECT routed, organization_name FROM specula_delivery_context($1, $2)`, speculaID, channelID).
		Scan(&routed, &c.OrganizationName)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookContext{}, false, nil
	}
	if err != nil {
		return WebhookContext{}, false, fmt.Errorf("notify: specula context: %w", err)
	}
	return c, routed, nil
}

// PreviewTemplate renders a template with sample data without sending anything. The error is a
// *validation.Error with field errors when the template is not acceptable.
func (s *Service) PreviewTemplate(contentType, tpl string) (string, error) {
	var verr validation.Error
	if msg := ValidateContentType(contentType); msg != "" {
		verr.Add("contentType", msg)
	} else if msg := ValidateTemplate(contentType, tpl); msg != "" {
		verr.Add("bodyTemplate", msg)
	}
	if err := verr.Err(); err != nil {
		return "", err
	}
	return RenderTemplate(contentType, tpl, SampleData(s.publicURL))
}

func (s *Service) open(id uuid.UUID, status string, sealed []byte) (Target, error) {
	plain, err := s.keys.Decrypt(sealed, channelAAD(id))
	if err != nil {
		return Target{}, ErrUndecryptable
	}
	return Target{Status: status, WebhookURL: string(plain)}, nil
}

// MarkFailed flags an active channel as failed after a permanent delivery failure; the api
// shows the status and a new webhook URL makes it active again. tx must be a tenant transaction
// of tenantID. It reports whether the status changed.
func (s *Service) MarkFailed(ctx context.Context, tx pgx.Tx, tenantID, channelID uuid.UUID) (bool, error) {
	n, err := notifydb.New(tx).MarkChannelFailed(ctx, notifydb.MarkChannelFailedParams{TenantID: tenantID, ID: channelID})
	if err != nil {
		return false, fmt.Errorf("notify: mark channel failed: %w", err)
	}
	return n == 1, nil
}

func checkProject(verr *validation.Error, project *uuid.UUID, orgWide bool) {
	if project != nil && orgWide {
		verr.Add("organizationWide", "cannot be combined with projectId")
	}
}

// channelProject resolves the project of a created or moved channel (nil = organization-wide).
func channelProject(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, project *uuid.UUID, orgWide bool) (*uuid.UUID, error) {
	if orgWide {
		return nil, nil
	}
	id, err := projects.ForUse(ctx, tx, tenantID, project)
	if errors.Is(err, projects.ErrNotFound) {
		var verr validation.Error
		verr.Add("projectId", "does not exist")
		return nil, &verr
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameProject(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func projectMeta(p *uuid.UUID) string {
	if p == nil {
		return "organization"
	}
	return p.String()
}

// DetachForeignChannels drops from a monitor's selection the channels of projects other than
// projectID (organization-wide channels stay); monitors calls it when a monitor moves.
// It returns the number of channels dropped.
func DetachForeignChannels(ctx context.Context, tx pgx.Tx, tenantID, monitorID, projectID uuid.UUID) (int64, error) {
	q := notifydb.New(tx)
	// The caller holds the monitor row lock; the selected channels are locked after it, so a
	// concurrent channel move (monitors, then the channel) waits instead of racing.
	if _, err := q.LockSelectedChannels(ctx, notifydb.LockSelectedChannelsParams{TenantID: tenantID, MonitorID: monitorID}); err != nil {
		return 0, fmt.Errorf("notify: lock selected channels: %w", err)
	}
	n, err := q.DetachForeignChannels(ctx, notifydb.DetachForeignChannelsParams{TenantID: tenantID, MonitorID: monitorID, ProjectID: projectID})
	if err != nil {
		return 0, fmt.Errorf("notify: detach channels of other projects: %w", err)
	}
	return n, nil
}

// SetMonitorChannels replaces the channel selection of a monitor (monitor_channels, owned by
// this module) inside the caller's tenant transaction; monitors calls it on create and update.
// ids must name live channels of the monitor's project or organization-wide ones, at most
// MaxMonitorChannels after deduplication; a *validation.Error on field channelIds otherwise. An
// empty selection means the active channels of the monitor's project. It
// reports whether the selection changed.
func SetMonitorChannels(ctx context.Context, tx pgx.Tx, tenantID, monitorID, projectID uuid.UUID, ids []uuid.UUID) (bool, error) {
	want := []uuid.UUID{}
	for _, id := range ids {
		if !slices.Contains(want, id) {
			want = append(want, id)
		}
	}
	if len(want) > MaxMonitorChannels {
		var verr validation.Error
		verr.Add("channelIds", "must name at most 10 channels")
		return false, &verr
	}
	q := notifydb.New(tx)
	if len(want) > 0 {
		live, err := q.LockLiveChannels(ctx, notifydb.LockLiveChannelsParams{TenantID: tenantID, Ids: want})
		if err != nil {
			return false, fmt.Errorf("notify: lock channels: %w", err)
		}
		if len(live) != len(want) {
			var verr validation.Error
			verr.Add("channelIds", "contains a channel that does not exist")
			return false, &verr
		}
		for _, c := range live {
			if c.ProjectID != nil && *c.ProjectID != projectID {
				var verr validation.Error
				// The same answer as an unknown id: channels of other projects do not exist for it.
				verr.Add("channelIds", "contains a channel that does not exist")
				return false, &verr
			}
		}
	}
	cur, err := MonitorChannelIDs(ctx, tx, tenantID, monitorID)
	if err != nil {
		return false, err
	}
	slices.SortFunc(want, uuidCompare)
	slices.SortFunc(cur, uuidCompare)
	if slices.Equal(cur, want) {
		return false, nil
	}
	if err := q.DeleteMonitorChannels(ctx, notifydb.DeleteMonitorChannelsParams{TenantID: tenantID, MonitorID: monitorID}); err != nil {
		return false, fmt.Errorf("notify: clear monitor channels: %w", err)
	}
	for _, id := range want {
		if err := q.InsertMonitorChannel(ctx, notifydb.InsertMonitorChannelParams{TenantID: tenantID, MonitorID: monitorID, ChannelID: id}); err != nil {
			return false, fmt.Errorf("notify: insert monitor channel: %w", err)
		}
	}
	return true, nil
}

// MonitorChannelIDs returns the channel selection of a monitor (empty: every active channel).
func MonitorChannelIDs(ctx context.Context, tx pgx.Tx, tenantID, monitorID uuid.UUID) ([]uuid.UUID, error) {
	ids, err := notifydb.New(tx).ListMonitorChannelIDs(ctx, notifydb.ListMonitorChannelIDsParams{TenantID: tenantID, MonitorID: monitorID})
	if err != nil {
		return nil, fmt.Errorf("notify: monitor channels: %w", err)
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids, nil
}

func uuidCompare(a, b uuid.UUID) int {
	return bytes.Compare(a[:], b[:])
}
