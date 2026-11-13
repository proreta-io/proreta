package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/proreta-io/proreta/backend/internal/safehttp"
)

// EventTest is the {{event}} of a test message.
const EventTest = "test"

// ValidateWebhookTargetURL returns the normalised URL of a generic webhook channel, or a message
// for the user. It is the first layer of SSRF defence only: the guarded client validates every
// address again at connect time.
func ValidateWebhookTargetURL(raw string) (string, string) {
	return safehttp.ValidatePublicHTTPSURL(raw)
}

// WebhookNotifier posts a rendered template body to a generic webhook.
type WebhookNotifier struct {
	client Doer
	now    func() time.Time
}

// NewWebhookNotifier returns a notifier sending through client (NewSlackClient in production).
func NewWebhookNotifier(client Doer) *WebhookNotifier {
	return &WebhookNotifier{client: client, now: time.Now}
}

// Send posts body to webhookURL with the Content-Type of contentType. The URL is validated again
// before every send. Every failure is a *SendError; success is any 2xx answer.
func (n *WebhookNotifier) Send(ctx context.Context, webhookURL, contentType, body string) error {
	if _, msg := ValidateWebhookTargetURL(webhookURL); msg != "" {
		return &SendError{Kind: KindInvalidURL, Permanent: true, Generic: true}
	}
	var header string
	switch contentType {
	case ContentJSON:
		header = "application/json"
	case ContentText:
		header = "text/plain; charset=utf-8"
	default:
		return &SendError{Kind: KindOther, Permanent: true, Generic: true}
	}
	if len(body) > MaxRenderedBytes {
		return &SendError{Kind: KindOther, Permanent: true, Generic: true}
	}
	err := post(ctx, n.client, n.now, webhookURL, header, []byte(body))
	var se *SendError
	if errors.As(err, &se) {
		se.Generic = true
	}
	return err
}

// WebhookContext holds what the outbox payload does not carry but a template may name.
type WebhookContext struct {
	MonitorType      string
	ProjectName      string
	OrganizationName string
}

// WebhookValues turns an outbox delivery into the placeholder values of a template.
func WebhookValues(kind string, raw []byte, publicURL string, c WebhookContext) (map[string]string, error) {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, ErrBadPayload
	}
	v := map[string]string{
		"monitor.id":        p.MonitorID.String(),
		"monitor.name":      p.MonitorName,
		"monitor.url":       DisplayURL(p.URL),
		"monitor.type":      c.MonitorType,
		"project.name":      c.ProjectName,
		"organization.name": c.OrganizationName,
		"error.kind":        p.Reason,
		"link":              trimSlash(publicURL) + "/monitors/" + p.MonitorID.String(),
	}
	if (kind == DeliveryAgentOffline || kind == DeliveryAgentOnline) && p.SpeculaID == nil {
		return nil, ErrBadPayload
	}
	if p.SpeculaID != nil {
		v["specula.id"], v["specula.name"] = p.SpeculaID.String(), p.SpeculaName
		v["link"] = trimSlash(publicURL) + "/specula"
		v["monitor.id"] = ""
	}
	switch kind {
	case DeliveryAgentOffline:
		v["event"], v["status"] = "agent_offline", "offline"
	case DeliveryAgentOnline:
		v["event"], v["status"] = "agent_online", "online"
		v["incident.duration"] = HumanDuration(time.Duration(p.DurationSeconds) * time.Second)
	case DeliveryOpened:
		v["event"], v["status"] = "opened", "down"
	case DeliveryReminder:
		v["event"], v["status"] = "reminder", "down"
		v["incident.duration"] = HumanDuration(time.Duration(p.DurationSeconds) * time.Second)
	case DeliveryResolved:
		v["event"], v["status"] = "resolved", "up"
		v["incident.duration"] = HumanDuration(time.Duration(p.DurationSeconds) * time.Second)
	case DeliveryTLSExpiring:
		if p.TLSNotAfter == nil || p.DaysLeft == nil {
			return nil, ErrBadPayload
		}
		v["event"], v["status"] = "tls_expiring", "up"
		v["tls.expiresAt"] = p.TLSNotAfter.UTC().Format(time.RFC3339)
		v["tls.daysLeft"] = strconv.Itoa(*p.DaysLeft)
	default:
		return nil, ErrBadPayload
	}
	if p.IncidentID != nil {
		v["incident.id"] = p.IncidentID.String()
	}
	if p.StartedAt != nil {
		v["incident.startedAt"] = p.StartedAt.UTC().Format(time.RFC3339)
	}
	if p.ResolvedAt != nil {
		v["incident.resolvedAt"] = p.ResolvedAt.UTC().Format(time.RFC3339)
	}
	return v, nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// WebhookBody renders the body of a delivery for a webhook channel. A JSON body that is not one
// valid document (a value in a place a string cannot be) is an error, not a malformed request.
func (t Target) WebhookBody(kind string, raw []byte, publicURL string) (string, error) {
	v, err := WebhookValues(kind, raw, publicURL, t.Hook)
	if err != nil {
		return "", err
	}
	body, err := RenderTemplate(t.ContentType, t.BodyTemplate, v)
	if err != nil {
		return "", ErrBadTemplate
	}
	if t.ContentType == ContentJSON && jsonProblem(body) != "" {
		return "", ErrBadTemplate
	}
	return body, nil
}
