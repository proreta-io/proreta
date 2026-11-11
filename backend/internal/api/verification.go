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
