package projects_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The actor of a request carries the roles read when the request began. Two project owners who
// demote each other at once must not both succeed: the roles are read again under the project lock.
func TestIntegrationProjectMemberChangesUseCurrentRoles(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("projects_stale_role"))
	tenant, project := uuid.New(), uuid.New()
	owner, target := uuid.New(), uuid.New()
	pgtest.MustExec(t, pg.AdminConn(t),
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenant.String()+`', 't', 'T')`,
		`INSERT INTO users (id, email) VALUES ('`+owner.String()+`', 'o@example.com'), ('`+target.String()+`', 't@example.com')`,
		`INSERT INTO projects (id, tenant_id, name, slug) VALUES ('`+project.String()+`', '`+tenant.String()+`', 'P', 'p')`,
		`INSERT INTO memberships (tenant_id, user_id) VALUES ('`+tenant.String()+`', '`+owner.String()+`'), ('`+tenant.String()+`', '`+target.String()+`')`,
		`INSERT INTO project_members (tenant_id, project_id, user_id, role) VALUES
		 ('`+tenant.String()+`', '`+project.String()+`', '`+owner.String()+`', 'viewer'),
		 ('`+tenant.String()+`', '`+project.String()+`', '`+target.String()+`', 'member')`)
	svc := projects.NewService(pg.Pool(t, pg.APIURL, 4))
	// The database says viewer now; the request still believes owner.
	stale := projects.Actor{TenantID: tenant, Access: authz.Actor{UserID: owner, Projects: map[uuid.UUID]authz.Role{project: authz.Owner}}}
	ctx := context.Background()
	if _, err := svc.PutMember(ctx, stale, project, target, authz.Viewer); !errors.Is(err, authz.ErrProjectForbidden) {
		t.Errorf("PutMember with a stale role: %v", err)
	}
	if err := svc.RemoveMember(ctx, stale, project, target); !errors.Is(err, authz.ErrProjectForbidden) {
		t.Errorf("RemoveMember with a stale role: %v", err)
	}
	name := "Renamed"
	if _, err := svc.Update(ctx, stale, project, projects.UpdateInput{Name: &name}); !errors.Is(err, authz.ErrProjectForbidden) {
		t.Errorf("Update with a stale role: %v", err)
	}
	if err := svc.Delete(ctx, stale, project); !errors.Is(err, authz.ErrProjectForbidden) {
		t.Errorf("Delete with a stale role: %v", err)
	}
}
