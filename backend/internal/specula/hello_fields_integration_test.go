package specula_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// What a hello says about the buffer and the schedule is read back unchanged by Get and List.
func TestIntegrationHelloFieldsReadBack(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculahellof"))
	ctx := context.Background()
	user := uuid.New()
	pgtest.MustExec(t, env.AdminConn(t),
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	svc := specula.NewService(env.Pool(t, env.APIURL, 4))
	a := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	oldest := issued.Add(-time.Hour)
	depth := 42
	in := specula.HelloInput{
		Version: "v1", Hostname: "h", Capabilities: []string{"schedule", "tcp"},
		ScheduleIssuedAt: &issued, BufferDepth: &depth, OldestUnsentAt: &oldest, BufferDegraded: true,
	}
	if _, _, err := specula.Hello(ctx, env.Pool(t, env.WorkerURL, 4), s.ID, in); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, a.TenantID, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, a.TenantID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	for name, g := range map[string]specula.Specula{"get": got, "list": list[0]} {
		if !reflect.DeepEqual(g.Capabilities, in.Capabilities) || g.BufferDepth == nil || *g.BufferDepth != depth ||
			g.ScheduleIssuedAt == nil || !g.ScheduleIssuedAt.Equal(issued) ||
			g.OldestUnsentAt == nil || !g.OldestUnsentAt.Equal(oldest) || !g.BufferDegraded {
			t.Fatalf("%s: %+v", name, g)
		}
	}
}
