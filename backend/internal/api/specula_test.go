package api

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/specula"
)

func TestVisibleSpecula(t *testing.T) {
	mine, other := uuid.New(), uuid.New()
	now := time.Now()
	revoked := now
	cases := []struct {
		name string
		role authz.Role
		sp   specula.Specula
		want bool
	}{
		{"owner sees a Specula of a project it is not in", authz.Owner, specula.Specula{ProjectIDs: []uuid.UUID{other}}, true},
		{"maintainer sees a revoked one", authz.Maintainer, specula.Specula{AllProjects: true, RevokedAt: &revoked}, true},
		{"member sees one shared with all projects", authz.Member, specula.Specula{AllProjects: true}, true},
		{"member sees one shared with its project", authz.Member, specula.Specula{ProjectIDs: []uuid.UUID{mine}}, true},
		{"member does not see one shared with another project", authz.Member, specula.Specula{ProjectIDs: []uuid.UUID{other}}, false},
		{"member does not see a revoked one", authz.Member, specula.Specula{AllProjects: true, RevokedAt: &revoked}, false},
		{"viewer sees one shared with its project", authz.Viewer, specula.Specula{ProjectIDs: []uuid.UUID{mine}}, true},
	}
	for _, tc := range cases {
		a := authz.Actor{Role: tc.role, Projects: map[uuid.UUID]authz.Role{mine: authz.Member}}
		if got := visibleSpecula(a, tc.sp); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSpeculaProjectIDsHiddenFromNonManagers(t *testing.T) {
	sp := specula.Specula{ProjectIDs: []uuid.UUID{uuid.New()}}
	if got := toSpecula(sp, false, time.Now()).ProjectIds; len(got) != 0 {
		t.Fatalf("a non-manager sees project ids: %v", got)
	}
	if got := toSpecula(sp, true, time.Now()).ProjectIds; len(got) != 1 {
		t.Fatalf("a manager lost them: %v", got)
	}
	if got := toSpecula(specula.Specula{}, true, time.Now()).ProjectIds; got == nil {
		t.Fatal("project ids must be a list, not null")
	}
}

func TestSpeculaStatus(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	for _, tc := range []struct {
		sp   specula.Specula
		want string
	}{
		{specula.Specula{}, specula.StatusPending},
		{specula.Specula{LastSeenAt: ago(30 * time.Second)}, specula.StatusOnline},
		{specula.Specula{LastSeenAt: ago(specula.OnlineWindow)}, specula.StatusOnline},
		{specula.Specula{LastSeenAt: ago(specula.OnlineWindow + time.Second)}, specula.StatusOffline},
		{specula.Specula{LastSeenAt: ago(time.Second), RevokedAt: ago(time.Second)}, specula.StatusRevoked},
	} {
		if got := tc.sp.Status(now); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.sp, got, tc.want)
		}
	}
}
