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
	shared := func(ids ...uuid.UUID) specula.Specula { return specula.Specula{ProjectIDs: ids} }
	cases := []struct {
		name  string
		actor authz.Actor
		sp    specula.Specula
		want  bool
	}{
		{"owner sees a Specula of a project it has no direct role in", authz.Actor{Role: authz.Owner}, shared(other), true},
		{"maintainer sees a revoked one", authz.Actor{Role: authz.Maintainer}, specula.Specula{AllProjects: true, RevokedAt: &revoked}, true},
		// An organization role covers every project.
		{"org member sees one shared with any project", authz.Actor{Role: authz.Member}, shared(other), true},
		{"org viewer does not see a revoked one", authz.Actor{Role: authz.Viewer}, specula.Specula{AllProjects: true, RevokedAt: &revoked}, false},
		// A person with project roles only sees what their projects may use.
		{"project-only member sees one shared with all projects", authz.Actor{Projects: map[uuid.UUID]authz.Role{mine: authz.Member}}, specula.Specula{AllProjects: true}, true},
		{"project-only member sees one shared with its project", authz.Actor{Projects: map[uuid.UUID]authz.Role{mine: authz.Member}}, shared(mine), true},
		{"project-only member does not see one shared with another project", authz.Actor{Projects: map[uuid.UUID]authz.Role{mine: authz.Member}}, shared(other), false},
		{"project-only maintainer cannot manage, so a revoked one stays hidden", authz.Actor{Projects: map[uuid.UUID]authz.Role{mine: authz.Maintainer}}, specula.Specula{AllProjects: true, RevokedAt: &revoked}, false},
	}
	for _, tc := range cases {
		if got := visibleSpecula(tc.actor, tc.sp); got != tc.want {
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
