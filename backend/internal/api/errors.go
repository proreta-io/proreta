package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/queries"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
)

// Problem types (RFC 9457 "type" members).
const (
	problemBadRequest         = "urn:proreta:problem:bad-request"
	problemUnauthenticated    = "urn:proreta:problem:unauthenticated"
	problemInvalidCredentials = "urn:proreta:problem:invalid-credentials" //nolint:gosec // G101: problem type URN, not a credential
	problemCSRF               = "urn:proreta:problem:csrf"
	problemRateLimited        = "urn:proreta:problem:rate-limited"
	problemNotFound           = "urn:proreta:problem:not-found"
	problemMethodNotAllowed   = "urn:proreta:problem:method-not-allowed"
	problemValidation         = "urn:proreta:problem:validation"
	problemPlanLimitReached   = "urn:proreta:problem:plan-limit-reached"
	problemNotHeartbeat       = "urn:proreta:problem:not-a-heartbeat"
	problemEmailTaken         = "urn:proreta:problem:email-taken"
	problemRegistrationOff    = "urn:proreta:problem:registration-disabled"
	problemChannelFailed      = "urn:proreta:problem:channel-delivery-failed"
	problemUnavailable        = "urn:proreta:problem:unavailable"
	problemForbidden          = "urn:proreta:problem:forbidden"
	problemAccountSuspended   = "urn:proreta:problem:account-suspended"
	problemProtectedOrg       = "urn:proreta:problem:protected-organization"
	problemSystemAdmin        = "urn:proreta:problem:system-admin"
	problemOrgDeleted         = "urn:proreta:problem:organization-deleted"
	problemInvitationInvalid  = "urn:proreta:problem:invitation-invalid"
	problemInvitationAccepted = "urn:proreta:problem:invitation-accepted"
	problemAlreadyMember      = "urn:proreta:problem:already-member"
	problemLastOwner          = "urn:proreta:problem:last-owner"
	problemSlugTaken          = "urn:proreta:problem:slug-taken"
	problemProjectNotEmpty    = "urn:proreta:problem:project-not-empty"
	problemDefaultProject     = "urn:proreta:problem:default-project"
	problemPasswordLoginOff   = "urn:proreta:problem:password-login-disabled" //nolint:gosec // G101: problem type URN, not a credential
	problemLastLoginMethod    = "urn:proreta:problem:last-login-method"
	problemNoPassword         = "urn:proreta:problem:no-password" //nolint:gosec // G101: problem type URN, not a credential
	problemDomainNotVerified  = "urn:proreta:problem:domain-not-verified"
	problemDomainTaken        = "urn:proreta:problem:domain-taken"
	problemDomainNotAllowed   = "urn:proreta:problem:domain-not-allowed"
	problemPasswordChange     = "urn:proreta:problem:password-change-required" //nolint:gosec // G101: problem type URN, not a credential
	problemInternal           = "urn:proreta:problem:internal"
	problemFeatureUnavailable = "urn:proreta:problem:feature-unavailable"
	problemEmailConflict      = "urn:proreta:problem:email-settings-conflict"
)

// badRequestError is a request the handler itself rejected (the validator catches most).
type badRequestError struct{ detail string }

func (e *badRequestError) Error() string { return "bad request: " + e.detail }

var (
	errNotFound         = errors.New("not found")
	errMethodNotAllowed = errors.New("method not allowed")
	// errRegistrationDisabled is the kill switch PRORETA_REGISTRATION_ENABLED=false.
	errRegistrationDisabled = errors.New("registration disabled")
	// errOAuthUnavailable means the provider's discovery could not be loaded at the start of a flow.
	errOAuthUnavailable = errors.New("oauth provider unavailable")
)

// writeError is the single place where errors become HTTP responses. Unknown errors are
// logged and returned as a generic 500 without detail.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	p := httpx.Problem{RequestID: logging.RequestID(r.Context()), Instance: r.URL.Path}
	if strings.HasPrefix(r.URL.Path, monitors.HeartbeatPath) {
		// The path of a ping carries its secret token; it is never echoed.
		p.Instance = monitors.HeartbeatPath + "{token}"
	}

	var badReq *badRequestError
	var reqErr *openapi3filter.RequestError
	var rateErr *auth.RateLimitedError
	var valErr *validation.Error
	var limitErr *plans.LimitError
	var sendErr *notify.SendError
	var roleErr *roleError
	var featureErr *featureError
	switch {
	case errors.As(err, &badReq):
		p.Type, p.Title, p.Status, p.Detail = problemBadRequest, "Bad request", http.StatusBadRequest, badReq.detail
	case errors.As(err, &reqErr):
		p.Type, p.Title, p.Status, p.Detail = problemBadRequest, "Bad request", http.StatusBadRequest, requestErrorDetail(reqErr)
		p.FieldErrors = requestFieldErrors(reqErr)
	case errors.As(err, &valErr):
		p.Type, p.Title, p.Status = problemValidation, "Validation failed", http.StatusUnprocessableEntity
		for _, f := range valErr.Fields {
			p.FieldErrors = append(p.FieldErrors, httpx.FieldError{Field: f.Field, Message: f.Message})
		}
	case errors.Is(err, auth.ErrCSRF):
		p.Type, p.Title, p.Status = problemCSRF, "Missing or invalid CSRF token", http.StatusForbidden
	case errors.Is(err, auth.ErrUnauthenticated):
		p.Type, p.Title, p.Status = problemUnauthenticated, "Not authenticated", http.StatusUnauthorized
	case errors.Is(err, auth.ErrInvalidCredentials):
		p.Type, p.Title, p.Status = problemInvalidCredentials, "Wrong login or password", http.StatusUnauthorized
	case errors.Is(err, auth.ErrAccountSuspended):
		// Only after a correct password (auth.Login), so it reveals nothing to a guess.
		p.Type, p.Title, p.Status = problemAccountSuspended, "Account suspended", http.StatusForbidden
		p.Detail = "This organization was suspended by the platform administrator. Contact them to restore access."
	case errors.Is(err, errPasswordChangeRequired):
		p.Type, p.Title, p.Status = problemPasswordChange, "Password change required", http.StatusForbidden
		p.Detail = "Choose a new password first (POST /api/v1/auth/password); nothing else works until then."
	case errors.As(err, &featureErr):
		p.Type, p.Title, p.Status = problemFeatureUnavailable, "Feature not available", http.StatusForbidden
		p.Detail = fmt.Sprintf("The plan of this organization does not include the %q feature.", featureErr.feature)
	case errors.As(err, &roleErr):
		p.Type, p.Title, p.Status = problemForbidden, "Not allowed", http.StatusForbidden
		p.Detail = fmt.Sprintf("This needs the %s role or higher in this organization.", roleErr.required)
	case errors.Is(err, authz.ErrProjectForbidden):
		p.Type, p.Title, p.Status = problemForbidden, "Not allowed", http.StatusForbidden
		p.Detail = "Your role in this project is viewer: you can read its monitors but not change them."
	case errors.Is(err, projects.ErrSlugTaken):
		p.Type, p.Title, p.Status = problemSlugTaken, "This slug is already used by another project", http.StatusConflict
	case errors.Is(err, statuspages.ErrSlugTaken):
		// One answer for every reason (a live page anywhere, or an address another organization freed
		// recently), so it reveals nothing about other organizations' pages.
		p.Type, p.Title, p.Status = problemSlugTaken, "This address is not available", http.StatusConflict
		p.Detail = "Status page addresses are shared by every organization on this Proreta; choose another."
	case errors.Is(err, projects.ErrNotEmpty):
		p.Type, p.Title, p.Status = problemProjectNotEmpty, "The project is not empty", http.StatusConflict
		p.Detail = "Move or delete its monitors and channels first."
	case errors.Is(err, projects.ErrDefault):
		p.Type, p.Title, p.Status = problemDefaultProject, "The Default project cannot be deleted", http.StatusConflict
	case errors.Is(err, authz.ErrForbidden):
		p.Type, p.Title, p.Status = problemForbidden, "Not allowed", http.StatusForbidden
		p.Detail = "Your role does not allow this: maintainers cannot change owners, and nobody can grant a role above their own."
	case errors.Is(err, auth.ErrInvitationInvalid):
		p.Type, p.Title, p.Status = problemInvitationInvalid, "This invitation link is not valid", http.StatusNotFound
		p.Detail = "It may have expired, been used or been revoked. Ask for a new link."
	case errors.Is(err, auth.ErrInvitationAccepted):
		p.Type, p.Title, p.Status = problemInvitationAccepted, "The invitation was already used", http.StatusConflict
		p.Detail = "Remove the member instead."
	case errors.Is(err, auth.ErrAlreadyMember):
		p.Type, p.Title, p.Status = problemAlreadyMember, "Already a member", http.StatusConflict
		p.Detail = "You are already a member of this organization, or your account cannot join other organizations."
	case errors.Is(err, auth.ErrLastOwner):
		p.Type, p.Title, p.Status = problemLastOwner, "The organization needs an owner", http.StatusConflict
		p.Detail = "Make another member an owner first."
	case errors.Is(err, auth.ErrSystemAdminMember):
		p.Type, p.Title, p.Status = problemSystemAdmin, "The platform administrator's membership cannot be changed", http.StatusConflict
		p.Detail = "It comes from the deployment's configuration (PRORETA_ADMIN_LOGIN)."
	case errors.Is(err, auth.ErrProtectedOrganization):
		p.Type, p.Title, p.Status = problemProtectedOrg, "This organization cannot be deleted", http.StatusConflict
		p.Detail = "The default organization belongs to the platform."
	case errors.Is(err, errForbidden), errors.Is(err, admin.ErrForbidden):
		p.Type, p.Title, p.Status = problemForbidden, "Not allowed", http.StatusForbidden
		p.Detail = "Only the platform administrator may do this."
	case errors.Is(err, admin.ErrProtectedTenant), errors.Is(err, admin.ErrOwnTenant):
		p.Type, p.Title, p.Status = problemProtectedOrg, "This organization cannot be suspended or deleted", http.StatusConflict
		p.Detail = "The default organization and the organization you belong to cannot be suspended or deleted."
		if errors.Is(err, admin.ErrOwnTenant) {
			p.Detail = "You belong to this organization: suspending or deleting it would lock you out."
		}
	case errors.Is(err, admin.ErrSystemAdmin):
		p.Type, p.Title, p.Status = problemSystemAdmin, "The system administrator's password cannot be reset here", http.StatusConflict
		p.Detail = "The platform administrator changes its own password under Profile; see docs/runbooks/admin-panel.md for a reset."
	case errors.Is(err, admin.ErrEmailNotTested):
		p.Type, p.Title, p.Status = problemEmailConflict, "The email settings have not passed a test", http.StatusConflict
		p.Detail = "Send a test email with the current settings first; changing any connection setting makes it necessary again."
	case errors.Is(err, admin.ErrEmailNotConfigured):
		p.Type, p.Title, p.Status = problemEmailConflict, "No email server is configured", http.StatusConflict
		p.Detail = "Save the SMTP connection settings first."
	case errors.Is(err, admin.ErrEmailTestPending):
		p.Type, p.Title, p.Status = problemEmailConflict, "A test email is already on its way", http.StatusConflict
		p.Detail = "Wait for its result, up to three minutes."
	case errors.Is(err, admin.ErrTenantDeleted):
		p.Type, p.Title, p.Status = problemOrgDeleted, "The organization is deleted", http.StatusConflict
		p.Detail = "A deleted organization cannot be changed."
	case errors.Is(err, auth.ErrEmailTaken):
		// Neutral wording; registration without verification cannot hide that the account exists.
		p.Type, p.Title, p.Status = problemEmailTaken, "This email is already registered", http.StatusConflict
		p.Detail = "Sign in instead. Lost passwords are reset by the platform administrator."
	case errors.Is(err, auth.ErrPasswordLoginDisabled):
		p.Type, p.Title, p.Status = problemPasswordLoginOff, "Password sign-in is turned off", http.StatusForbidden
		p.Detail = "Sign in with one of the offered providers."
	case errors.Is(err, auth.ErrNoPassword):
		p.Type, p.Title, p.Status = problemNoPassword, "This account has no password", http.StatusConflict
		p.Detail = "You sign in with a provider, so there is no password to change."
	case errors.Is(err, auth.ErrLastLoginMethod):
		p.Type, p.Title, p.Status = problemLastLoginMethod, "This is your last way to sign in", http.StatusConflict
		p.Detail = "Link another provider first."
	case errors.Is(err, auth.ErrDomainNotVerified):
		p.Type, p.Title, p.Status = problemDomainNotVerified, "Domain not verified", http.StatusForbidden
		p.Detail = "Link a Google Workspace account of this domain to your profile, sign in with it, then claim the domain."
	case errors.Is(err, auth.ErrDomainNotAllowed):
		p.Type, p.Title, p.Status = problemDomainNotAllowed, "This domain cannot be claimed on this installation", http.StatusForbidden
		p.Detail = "The operator allows only the domains in PRORETA_OAUTH_ALLOWED_DOMAINS; ask them to add it."
	case errors.Is(err, auth.ErrDomainTaken):
		p.Type, p.Title, p.Status = problemDomainTaken, "This domain is claimed by another organization", http.StatusConflict
	case errors.Is(err, errOAuthUnavailable):
		p.Type, p.Title, p.Status = problemUnavailable, "The sign-in provider is unavailable, try again later", http.StatusServiceUnavailable
	case errors.Is(err, errRegistrationDisabled):
		p.Type, p.Title, p.Status = problemRegistrationOff, "Registration is turned off", http.StatusForbidden
	case errors.As(err, &rateErr):
		secs := int(math.Ceil(rateErr.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		p.Type, p.Title, p.Status = problemRateLimited, "Too many attempts, try again later", http.StatusTooManyRequests
	case errors.As(err, &limitErr):
		p.Type, p.Title, p.Status = problemPlanLimitReached, "Plan limit reached", http.StatusConflict
		p.Detail = fmt.Sprintf("The %s plan allows at most %d %s.", limitErr.PlanName, limitErr.Max, limitErr.Resource)
		p.Limit, p.Max = string(limitErr.Resource), &limitErr.Max
	case errors.Is(err, monitors.ErrNotHeartbeat):
		p.Type, p.Title, p.Status = problemNotHeartbeat, "Not a heartbeat monitor", http.StatusConflict
	case errors.As(err, &sendErr) && sendErr.Kind == notify.KindRateLimited:
		secs := int(math.Ceil(sendErr.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		p.Type, p.Title, p.Status = problemRateLimited, "Slack is rate-limiting this webhook, try again later", http.StatusTooManyRequests
	case errors.As(err, &sendErr):
		// Only the kind is shown: Slack's answer and transport errors are never echoed.
		p.Type, p.Title, p.Status, p.Detail = problemChannelFailed, "Slack did not accept the message", http.StatusBadGateway, slackFailureDetail(sendErr)
	case errors.Is(err, scheduler.ErrNoLocation):
		// Operational, not the caller's fault: the location that runs new monitors is missing or
		// disabled.
		log.ErrorContext(r.Context(), "request failed", "error", err)
		p.Type, p.Title, p.Status = problemUnavailable, "Monitoring location unavailable, try again later", http.StatusServiceUnavailable
	case errors.Is(err, auth.ErrUserNotFound):
		p.Type, p.Title, p.Status = problemNotFound, "No account found", http.StatusNotFound
		p.Detail = "No account matches. Invite the person instead if they do not have one yet."
	case errors.Is(err, errNotFound), errors.Is(err, routers.ErrPathNotFound), errors.Is(err, monitors.ErrNotFound), errors.Is(err, queries.ErrNotFound), errors.Is(err, admin.ErrNotFound),
		errors.Is(err, notify.ErrChannelNotFound), errors.Is(err, auth.ErrMemberNotFound), errors.Is(err, auth.ErrInvitationNotFound),
		errors.Is(err, auth.ErrOrganizationNotFound), errors.Is(err, projects.ErrNotFound), errors.Is(err, projects.ErrMemberNotFound),
		errors.Is(err, statuspages.ErrNotFound), errors.Is(err, auth.ErrIdentityNotFound), errors.Is(err, auth.ErrAuthDomainNotFound),
		errors.Is(err, auth.ErrVerificationInvalid), errors.Is(err, auth.ErrResetInvalid), errors.Is(err, auth.ErrEmailDisabled):
		p.Type, p.Title, p.Status = problemNotFound, "Not found", http.StatusNotFound
	case errors.Is(err, errMethodNotAllowed), errors.Is(err, routers.ErrMethodNotAllowed):
		p.Type, p.Title, p.Status = problemMethodNotAllowed, "Method not allowed", http.StatusMethodNotAllowed
	case errors.Is(err, context.Canceled):
		// The client went away; nobody reads the response.
		log.InfoContext(r.Context(), "request cancelled", "error", err)
		p.Type, p.Title, p.Status = problemInternal, "Request cancelled", 499
	default:
		log.ErrorContext(r.Context(), "request failed", "error", err)
		p.Type, p.Title, p.Status = problemInternal, "Internal server error", http.StatusInternalServerError
	}
	httpx.WriteProblem(w, p)
}

// requestErrorDetail describes a validation failure without echoing the submitted value
// (it may be a password).
func requestErrorDetail(e *openapi3filter.RequestError) string {
	var where string
	switch {
	case e.Parameter != nil:
		where = fmt.Sprintf("%s parameter %q", e.Parameter.In, e.Parameter.Name)
	case e.RequestBody != nil:
		where = "request body"
	default:
		where = "request"
	}
	var se *openapi3.SchemaError
	if errors.As(e.Err, &se) {
		if ptr := se.JSONPointer(); len(ptr) > 0 {
			return fmt.Sprintf("%s: field %q: %s", where, strings.Join(ptr, "."), se.Reason)
		}
		return fmt.Sprintf("%s: %s", where, se.Reason)
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", where, e.Reason)
	}
	return where + " is invalid"
}

// requestFieldErrors attributes a request body schema violation to its field, so clients can
// show it next to the input. Parameters and unparsable bodies have no field.
func requestFieldErrors(e *openapi3filter.RequestError) []httpx.FieldError {
	var se *openapi3.SchemaError
	if e.RequestBody == nil || !errors.As(e.Err, &se) {
		return nil
	}
	ptr := se.JSONPointer()
	if len(ptr) == 0 {
		return nil
	}
	return []httpx.FieldError{{Field: strings.Join(ptr, "."), Message: se.Reason}}
}

func slackFailureDetail(e *notify.SendError) string {
	switch e.Kind {
	case notify.KindNotFound:
		return "Slack does not know this webhook any more (it was removed or revoked); store a new webhook URL."
	case notify.KindHTTPStatus:
		return fmt.Sprintf("Slack answered HTTP %d.", e.Status)
	case notify.KindTimeout:
		return "Slack did not answer in time."
	}
	return "Slack could not be reached (" + e.Kind + ")."
}
