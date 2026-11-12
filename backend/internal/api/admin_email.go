package api

import (
	"context"
	"strings"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func toAdminEmailSettings(e admin.EmailSettings) gen.AdminEmailSettings {
	out := gen.AdminEmailSettings{
		Configured: e.Configured, Host: e.Host, Port: e.Port, Tls: gen.AdminEmailTls(e.TLS), From: e.From, Username: e.Username,
		PasswordSet: e.PasswordSet, SendInvitations: e.SendInvitations, SendEmailVerification: e.SendEmailVerification,
		AllowPasswordReset: e.AllowPasswordReset, Tested: e.Tested, TestedAt: e.TestedAt, UpdatedAt: e.UpdatedAt,
	}
	if t := e.LastTest; t != nil {
		out.LastTest = &gen.AdminEmailTestResult{State: gen.AdminEmailTestResultState(t.State), RequestedAt: t.RequestedAt,
			FinishedAt: t.FinishedAt, Error: t.Error, SmtpCode: t.SMTPCode}
	}
	return out
}

func (s *server) AdminGetEmailSettings(ctx context.Context, _ gen.AdminGetEmailSettingsRequestObject) (gen.AdminGetEmailSettingsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	e, err := s.admin.EmailSettings(ctx, a)
	if err != nil {
		return nil, err
	}
	return gen.AdminGetEmailSettings200JSONResponse(toAdminEmailSettings(e)), nil
}

func (s *server) AdminUpdateEmailSettings(ctx context.Context, req gen.AdminUpdateEmailSettingsRequestObject) (gen.AdminUpdateEmailSettingsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	in := admin.EmailConnectionInput{Host: req.Body.Host, Port: req.Body.Port, TLS: string(req.Body.Tls), From: req.Body.From, Password: req.Body.Password}
	if req.Body.Username != nil {
		in.Username = *req.Body.Username
	}
	e, err := s.admin.UpdateEmailConnection(ctx, a, in)
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdateEmailSettings200JSONResponse(toAdminEmailSettings(e)), nil
}

func (s *server) AdminDeleteEmailSettings(ctx context.Context, _ gen.AdminDeleteEmailSettingsRequestObject) (gen.AdminDeleteEmailSettingsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.DeleteEmailSettings(ctx, a); err != nil {
		return nil, err
	}
	return gen.AdminDeleteEmailSettings204Response{}, nil
}

func (s *server) AdminUpdateEmailFeatures(ctx context.Context, req gen.AdminUpdateEmailFeaturesRequestObject) (gen.AdminUpdateEmailFeaturesResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	e, err := s.admin.UpdateEmailFeatures(ctx, a, admin.EmailFeatures{
		SendInvitations: req.Body.SendInvitations, SendEmailVerification: req.Body.SendEmailVerification, AllowPasswordReset: req.Body.AllowPasswordReset,
	})
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdateEmailFeatures200JSONResponse(toAdminEmailSettings(e)), nil
}

func (s *server) AdminSendTestEmail(ctx context.Context, req gen.AdminSendTestEmailRequestObject) (gen.AdminSendTestEmailResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	to := ""
	if req.Body != nil && req.Body.To != nil {
		to = strings.TrimSpace(*req.Body.To)
	}
	if to == "" {
		p, err := s.principal(ctx)
		if err != nil {
			return nil, err
		}
		info, err := s.auth.Me(ctx, p)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(info.User.Login, "@") {
			return nil, &validation.Error{Fields: []validation.FieldError{{Field: "to", Message: "is required: your login is not an email address"}}}
		}
		to = info.User.Login
	}
	// Every call counts, whatever its outcome: the limit protects the mail server and the audit log.
	key := a.UserID.String()
	if ok, wait := s.emailTestBurst.Reserve(key); !ok {
		return nil, &auth.RateLimitedError{RetryAfter: wait}
	}
	if ok, wait := s.emailTestHourly.Reserve(key); !ok {
		return nil, &auth.RateLimitedError{RetryAfter: wait}
	}
	e, err := s.admin.SendTestEmail(ctx, a, to)
	if err != nil {
		return nil, err
	}
	return gen.AdminSendTestEmail202JSONResponse(toAdminEmailSettings(e)), nil
}

func toAdminRegistration(r admin.RegistrationSettings) gen.AdminRegistrationSettings {
	return gen.AdminRegistrationSettings{
		Effective: r.Effective, Source: gen.AdminRegistrationSettingsSource(r.Source), EnvironmentValue: r.EnvironmentValue, UpdatedAt: r.UpdatedAt,
	}
}

func (s *server) AdminGetRegistration(ctx context.Context, _ gen.AdminGetRegistrationRequestObject) (gen.AdminGetRegistrationResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.admin.Registration(ctx, a)
	if err != nil {
		return nil, err
	}
	return gen.AdminGetRegistration200JSONResponse(toAdminRegistration(r)), nil
}

func (s *server) AdminUpdateRegistration(ctx context.Context, req gen.AdminUpdateRegistrationRequestObject) (gen.AdminUpdateRegistrationResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.admin.SetRegistration(ctx, a, req.Body.Enabled)
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdateRegistration200JSONResponse(toAdminRegistration(r)), nil
}
