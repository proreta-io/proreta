package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
)

func (s *server) AdminSetPlatformAdmin(ctx context.Context, req gen.AdminSetPlatformAdminRequestObject) (gen.AdminSetPlatformAdminResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.admin.SetPlatformAdmin(ctx, a, req.UserId, req.Body.Enabled)
	if err != nil {
		return nil, err
	}
	return gen.AdminSetPlatformAdmin200JSONResponse(toAdminUserDetail(d)), nil
}
