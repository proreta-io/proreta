package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
)

func (s *server) VerifyEmail(ctx context.Context, req gen.VerifyEmailRequestObject) (gen.VerifyEmailResponseObject, error) {
	if !s.auth.EmailEnabled() {
		return nil, errNotFound
	}
	r := httpRequest(ctx)
	if err := s.auth.VerifyEmail(ctx, req.Body.Token, s.limiterIP(ctx, r)); err != nil {
		return nil, err
	}
	return gen.VerifyEmail204Response{}, nil
}

func (s *server) ResendEmailVerification(ctx context.Context, _ gen.ResendEmailVerificationRequestObject) (gen.ResendEmailVerificationResponseObject, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.auth.ResendVerification(ctx, p, httpx.ClientIP(httpRequest(ctx), s.cfg.TrustedProxyHops)); err != nil {
		return nil, err
	}
	return gen.ResendEmailVerification204Response{}, nil
}

func (s *server) RequestPasswordReset(ctx context.Context, req gen.RequestPasswordResetRequestObject) (gen.RequestPasswordResetResponseObject, error) {
	if err := s.auth.RequestPasswordReset(ctx, req.Body.Email, s.limiterIP(ctx, httpRequest(ctx))); err != nil {
		return nil, err
	}
	return gen.RequestPasswordReset202Response{}, nil
}

func (s *server) ResetPassword(ctx context.Context, req gen.ResetPasswordRequestObject) (gen.ResetPasswordResponseObject, error) {
	if err := s.auth.ResetPassword(ctx, req.Body.Token, req.Body.Password, s.limiterIP(ctx, httpRequest(ctx))); err != nil {
		return nil, err
	}
	return gen.ResetPassword204Response{}, nil
}
