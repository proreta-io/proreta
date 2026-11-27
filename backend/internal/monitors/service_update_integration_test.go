package monitors_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const otherURL = "https://example.org/health"

func (w *world) changed(id uuid.UUID) string {
	w.t.Helper()
	meta, _ := w.audit(monitors.ActionUpdated, id)
	var out []string
	if raw, ok := meta["fields"].([]any); ok {
		for _, f := range raw {
			out = append(out, f.(string))
		}
	}
	return strings.Join(out, ",")
}

func (w *world) setUp(id uuid.UUID) {
	w.t.Helper()
	pgtest.MustExec(w.t, w.admin, `UPDATE monitor_state SET status = 'up' WHERE monitor_id = '`+id.String()+`'`)
}

type updateCase struct {
	name string
	make func(*world) monitors.Monitor
	a    func(*world) monitors.Actor
	in   func(*world, monitors.Monitor) monitors.UpdateInput
	// want is the list of changed fields (audit) or, with err set, the expected failure.
	want string
	err  string // validation fields, or "<text>" for another error; empty = success
}

func TestIntegrationUpdateFieldsAndErrors(t *testing.T) {
	w := newWorld(t, "mon_update_fields")
	mkHTTP := func(w *world) monitors.Monitor { return w.http("web") }
	mkTCP := func(w *world) monitors.Monitor { return w.tcp("ssh") }
	mkHB := func(w *world) monitors.Monitor { return w.heartbeat("cron") }
	in := func(u monitors.UpdateInput) func(*world, monitors.Monitor) monitors.UpdateInput {
		return func(*world, monitors.Monitor) monitors.UpdateInput { return u }
	}
	cases := []updateCase{
		// http
		{name: "nothing", make: mkHTTP, in: in(monitors.UpdateInput{}), want: ""},
		{name: "same values", make: mkHTTP, in: in(monitors.UpdateInput{Name: ptr("web"), NotificationsEnabled: ptr(true), HTTP: &monitors.HTTPSettings{
			URL: ptr(publicURL), ExpectedStatus: ptr("2xx"), FailureThreshold: ptr(2), IntervalSeconds: ptr(60), TimeoutSeconds: ptr(10), Method: ptr("GET")}}), want: ""},
		{name: "name is trimmed", make: mkHTTP, in: in(monitors.UpdateInput{Name: ptr("  new  ")}), want: "name"},
		{name: "notifications off", make: mkHTTP, in: in(monitors.UpdateInput{NotificationsEnabled: ptr(false)}), want: "notificationsEnabled"},
		{name: "http everything", make: mkHTTP, in: in(monitors.UpdateInput{Name: ptr("n"), NotificationsEnabled: ptr(false), HTTP: &monitors.HTTPSettings{
			URL: ptr(otherURL), ExpectedStatus: ptr("404"), FailureThreshold: ptr(3), Method: ptr("HEAD")}}),
			want: "name,notificationsEnabled,http.url,http.expectedStatus,http.failureThreshold,http.method"},
		{name: "http bad everything", make: mkHTTP, in: in(monitors.UpdateInput{Name: ptr(" "), HTTP: &monitors.HTTPSettings{
			URL: ptr("http://x"), ExpectedStatus: ptr("abc"), FailureThreshold: ptr(7), IntervalSeconds: ptr(30), TimeoutSeconds: ptr(1), Method: ptr("TRACE")}}),
			err: "name,http.intervalSeconds,http.timeoutSeconds,http.url,http.expectedStatus,http.failureThreshold,http.method"},
		{name: "heartbeat and tcp on http", make: mkHTTP, in: in(monitors.UpdateInput{Heartbeat: &monitors.HeartbeatSettings{}, TCP: &monitors.TCPSettings{}}), err: "heartbeat,tcp"},
		{name: "body on GET", make: mkHTTP, in: in(monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Body: ptr("x")}}), err: "http.body"},
		{name: "headers added", make: mkHTTP, in: in(monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Headers: &[]monitors.HeaderInput{{Name: "X-A", Value: ptr("1")}}}}), want: "http.headers"},
		{name: "header without a value", make: mkHTTP, in: in(monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Headers: &[]monitors.HeaderInput{{Name: "X-A"}}}}), err: "http.headers[0].value"},
		{name: "post with body", make: mkHTTP, in: in(monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Method: ptr("POST"), Body: ptr("b")}}), want: "http.method,http.body"},
		{name: "viewer with bad input is refused first", make: mkHTTP, a: func(w *world) monitors.Actor { return w.projViewer },
			in: in(monitors.UpdateInput{Name: ptr(" ")}), err: "<" + authz.ErrProjectForbidden.Error() + ">"},

		// tcp
		{name: "tcp everything", make: mkTCP, in: in(monitors.UpdateInput{TCP: &monitors.TCPSettings{Host: ptr("example.org"), Port: ptr(2222), FailureThreshold: ptr(3)}}),
			want: "tcp.host,tcp.port,tcp.failureThreshold"},
		{name: "tcp bad everything", make: mkTCP, in: in(monitors.UpdateInput{TCP: &monitors.TCPSettings{Host: ptr("a b"), Port: ptr(0), FailureThreshold: ptr(0)}}), err: "tcp.host,tcp.port,tcp.failureThreshold"},
		{name: "http and heartbeat on tcp", make: mkTCP, in: in(monitors.UpdateInput{HTTP: &monitors.HTTPSettings{}, Heartbeat: &monitors.HeartbeatSettings{}}), err: "heartbeat,http"},
		{name: "tcp private host from the worker", make: mkTCP, in: in(monitors.UpdateInput{TCP: &monitors.TCPSettings{Host: ptr("10.0.0.1")}}), err: "tcp.host"},
		{name: "tcp same values", make: mkTCP, in: in(monitors.UpdateInput{TCP: &monitors.TCPSettings{Host: ptr("example.com"), Port: ptr(22), FailureThreshold: ptr(2)}}), want: ""},

		// heartbeat
		{name: "heartbeat interval and grace", make: mkHB, in: in(monitors.UpdateInput{Heartbeat: &monitors.HeartbeatSettings{ExpectedIntervalSeconds: ptr(600), GraceSeconds: ptr(5)}}),
			want: "heartbeat.expectedIntervalSeconds,heartbeat.graceSeconds"},
		{name: "heartbeat out of range", make: mkHB, in: in(monitors.UpdateInput{Heartbeat: &monitors.HeartbeatSettings{ExpectedIntervalSeconds: ptr(1), GraceSeconds: ptr(-1)}}),
			err: "heartbeat.expectedIntervalSeconds,heartbeat.graceSeconds"},
		{name: "everything else on a heartbeat", make: mkHB, in: in(monitors.UpdateInput{Location: &monitors.Location{}, HTTP: &monitors.HTTPSettings{}, TCP: &monitors.TCPSettings{}}),
			err: "location.speculaId,http,tcp"},
		{name: "heartbeat same values", make: mkHB, in: in(monitors.UpdateInput{Heartbeat: &monitors.HeartbeatSettings{ExpectedIntervalSeconds: ptr(300), GraceSeconds: ptr(60)}}), want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.make(w)
			a := w.owner
			if c.a != nil {
				a = c.a(w)
			}
			before, err := w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.svc.Update(w.ctx, a, m.ID, c.in(w, m))
			if c.err != "" {
				if f := fieldsOf(err); f != c.err {
					t.Fatalf("fields = %q (%v), want %q", f, err, c.err)
				}
				after, _ := w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{})
				if after.Name != before.Name || !after.UpdatedAt.Equal(before.UpdatedAt) {
					t.Fatal("a refused update changed the monitor")
				}
				if _, n := w.audit(monitors.ActionUpdated, m.ID); n != 0 {
					t.Fatalf("audit rows after a refusal: %d", n)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			meta, n := w.audit(monitors.ActionUpdated, m.ID)
			if c.want == "" {
				if n != 0 || !got.UpdatedAt.Equal(before.UpdatedAt) {
					t.Fatalf("no change expected: audit %d %v", n, meta)
				}
				return
			}
			if f := w.changed(m.ID); n != 1 || f != c.want || len(meta) != 1 {
				t.Fatalf("changed = %q (%d rows, meta %v), want %q", f, n, meta, c.want)
			}
		})
	}
}

func TestIntegrationUpdateResultsOfChanges(t *testing.T) {
	w := newWorld(t, "mon_update_results")
	m := w.http("web")
	got := w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Name: ptr("  renamed ")}))
	if got.Name != "renamed" || got.URL != publicURL || got.ExpectedStatus != "2xx" || got.FailureThreshold != 2 {
		t.Fatalf("rename: %+v", got)
	}
	// A rename keeps the state; a new URL restarts it, clears the certificate and ends an open
	// incident without a notification.
	w.setUp(m.ID)
	pgtest.MustExec(t, w.admin, `UPDATE monitor_state SET tls_expires_at = now() WHERE monitor_id = '`+m.ID.String()+`'`)
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Name: ptr("again")}))
	if w.status(m.ID) != "up" {
		t.Fatalf("status after rename = %s", w.status(m.ID))
	}
	incID := uuid.New().String()
	pgtest.MustExec(t, w.admin,
		`INSERT INTO incidents (id, tenant_id, monitor_id, started_at) VALUES ('`+incID+`', '`+tenantA+`', '`+m.ID.String()+`', now())`,
		`UPDATE monitor_state SET current_incident_id = '`+incID+`', status = 'down' WHERE monitor_id = '`+m.ID.String()+`'`)
	got = w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{URL: ptr(otherURL)}}))
	if got.URL != otherURL || got.Status != monitors.StatusUnknown || got.TLSExpiresAt != nil || got.CurrentIncidentID != nil {
		t.Fatalf("after a new URL: %+v", got)
	}
	var reason string
	if err := w.admin.QueryRow(w.ctx, `SELECT resolve_reason FROM incidents WHERE id = $1`, incID).Scan(&reason); err != nil || reason != "url_changed" {
		t.Fatalf("incident: %q %v", reason, err)
	}
	// Threshold and expected status do not restart anything.
	w.setUp(m.ID)
	got = w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{ExpectedStatus: ptr("204"), FailureThreshold: ptr(3)}}))
	if got.ExpectedStatus != "204" || got.FailureThreshold != 3 || w.status(m.ID) != "up" {
		t.Fatalf("settings: %+v %s", got, w.status(m.ID))
	}
	// A tcp host or port restarts it; the threshold does not.
	tm := w.tcp("ssh")
	w.setUp(tm.ID)
	w.must(w.svc.Update(w.ctx, w.owner, tm.ID, monitors.UpdateInput{TCP: &monitors.TCPSettings{FailureThreshold: ptr(3)}}))
	if w.status(tm.ID) != "up" {
		t.Fatal("threshold restarted the monitor")
	}
	got = w.must(w.svc.Update(w.ctx, w.owner, tm.ID, monitors.UpdateInput{TCP: &monitors.TCPSettings{Port: ptr(23)}}))
	if got.Port != 23 || got.Host != "example.com" || got.FailureThreshold != 3 || w.status(tm.ID) != "unknown" {
		t.Fatalf("port change: %+v %s", got, w.status(tm.ID))
	}
	// A new expect rule changes what the check means, so it restarts too (an executor that cannot
	// run it would otherwise leave a stale "up").
	w.setUp(tm.ID)
	w.must(w.svc.Update(w.ctx, w.owner, tm.ID, monitors.UpdateInput{TCP: &monitors.TCPSettings{Expect: &monitors.TCPExpect{Mode: monitors.ExpectContains, Value: "ok"}}}))
	if w.status(tm.ID) != "unknown" {
		t.Fatal("a new expect rule did not restart the monitor")
	}
	// A heartbeat's interval and grace are stored; the failure threshold stays 1.
	hb := w.heartbeat("cron")
	got = w.must(w.svc.Update(w.ctx, w.owner, hb.ID, monitors.UpdateInput{Heartbeat: &monitors.HeartbeatSettings{ExpectedIntervalSeconds: ptr(600), GraceSeconds: ptr(30)}}))
	if got.IntervalSeconds != 600 || got.GraceSeconds != 30 || got.FailureThreshold != 1 {
		t.Fatalf("heartbeat: %+v", got)
	}
}

func TestIntegrationUpdateRequestSecrets(t *testing.T) {
	w := newWorld(t, "mon_update_secrets")
	m := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: "post", HTTP: &monitors.HTTPSettings{
		URL: ptr(publicURL), Method: ptr("POST"), Body: ptr("b1"), Headers: &[]monitors.HeaderInput{{Name: "X-Token", Value: ptr("s1")}},
	}}))
	// A header sent without a value keeps the stored one; nothing changes.
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Headers: &[]monitors.HeaderInput{{Name: "X-Token"}}}}))
	if _, n := w.audit(monitors.ActionUpdated, m.ID); n != 0 {
		t.Fatalf("keeping a header was a change (%d)", n)
	}
	// A new value is a change; the audit names the field, never the value.
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Headers: &[]monitors.HeaderInput{{Name: "X-Token", Value: ptr("s2")}}}}))
	meta, _ := w.audit(monitors.ActionUpdated, m.ID)
	raw, _ := json.Marshal(meta)
	if w.changed(m.ID) != "http.headers" || strings.Contains(string(raw), "s2") {
		t.Fatalf("audit = %s", raw)
	}
	// An empty body removes it.
	got := w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Body: ptr("")}}))
	if got.BodySet || len(got.HeaderNames) != 1 || w.changed(m.ID) != "http.body" {
		t.Fatalf("body removed: %+v %s", got, w.changed(m.ID))
	}
	// The stored header values and body are not carried to another host.
	_, err := w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{URL: ptr(otherURL)}})
	if fieldsOf(err) != "http.headers" {
		t.Fatalf("host moved with stored headers: %v", err)
	}
	_, err = w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{URL: ptr(otherURL), Headers: &[]monitors.HeaderInput{{Name: "X-Token"}}}})
	if fieldsOf(err) != "http.headers[0].value" {
		t.Fatalf("host moved, header without a value: %v", err)
	}
	got = w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{URL: ptr(otherURL), Headers: &[]monitors.HeaderInput{}}}))
	if got.URL != otherURL || len(got.HeaderNames) != 0 {
		t.Fatalf("host moved, headers removed: %+v", got)
	}
	if w.changed(m.ID) != "http.url,http.headers" {
		t.Fatalf("changed = %q", w.changed(m.ID))
	}
	// Without the keyring headers cannot be added.
	pm := w.must(w.plain.Create(w.ctx, w.owner, monitors.CreateInput{Name: "plain", HTTP: &monitors.HTTPSettings{URL: ptr(publicURL)}}))
	if _, err := w.plain.Update(w.ctx, w.owner, pm.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Headers: &[]monitors.HeaderInput{{Name: "X-A", Value: ptr("1")}}}}); fieldsOf(err) != "http.headers" {
		t.Fatalf("headers without keys: %v", err)
	}
}

func TestIntegrationUpdateAccess(t *testing.T) {
	w := newWorld(t, "mon_update_access")
	m := w.http("web")
	otherMon := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: "elsewhere", ProjectID: &w.other, HTTP: &monitors.HTTPSettings{URL: ptr(publicURL)}}))
	if _, err := w.svc.Update(w.ctx, w.owner, uuid.New(), monitors.UpdateInput{}); !errors.Is(err, monitors.ErrNotFound) || err != monitors.ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}
	foreign := w.owner
	foreign.TenantID = uuid.MustParse(tenantB)
	if _, err := w.svc.Update(w.ctx, foreign, m.ID, monitors.UpdateInput{Name: ptr("x")}); err != monitors.ErrNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := w.svc.Update(w.ctx, w.projMaint, otherMon.ID, monitors.UpdateInput{Name: ptr("x")}); err != monitors.ErrNotFound {
		t.Fatalf("project not seen: %v", err)
	}
	if _, err := w.svc.Update(w.ctx, w.projViewer, m.ID, monitors.UpdateInput{Name: ptr("x")}); err != authz.ErrProjectForbidden {
		t.Fatalf("viewer: %v", err)
	}
	if got := w.must(w.svc.Update(w.ctx, w.projMaint, m.ID, monitors.UpdateInput{Name: ptr("by maintainer")})); got.Name != "by maintainer" {
		t.Fatalf("maintainer: %+v", got)
	}
	// A deleted monitor is gone for updates too.
	pgtest.MustExec(t, w.admin, `UPDATE monitors SET deleted_at = now() WHERE id = '`+m.ID.String()+`'`)
	if _, err := w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Name: ptr("x")}); err != monitors.ErrNotFound {
		t.Fatalf("deleted: %v", err)
	}
}

func TestIntegrationUpdateMoveAndChannels(t *testing.T) {
	w := newWorld(t, "mon_update_move")
	m := w.http("web")
	// Channels: a selection of the monitor's project; the same selection again is no change.
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{ChannelIDs: &[]uuid.UUID{w.chDef}}))
	if w.changed(m.ID) != "channelIds" {
		t.Fatalf("changed = %q", w.changed(m.ID))
	}
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{ChannelIDs: &[]uuid.UUID{w.chDef}}))
	if _, n := w.audit(monitors.ActionUpdated, m.ID); n != 1 {
		t.Fatalf("same selection wrote an audit row (%d)", n)
	}
	// A channel of another project is refused and the rest of the request rolls back.
	_, err := w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Name: ptr("lost"), ChannelIDs: &[]uuid.UUID{w.chOther}})
	if fieldsOf(err) != "channelIds" {
		t.Fatalf("foreign channel: %v", err)
	}
	if got := w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{})); got.Name != "web" {
		t.Fatalf("name = %q", got.Name)
	}

	// Moving: the channels of the old project are dropped, and the audit row tells.
	got := w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{ProjectID: &w.other, Name: ptr("moved")}))
	if got.ProjectID != w.other || got.Name != "moved" {
		t.Fatalf("move: %+v", got)
	}
	meta, _ := w.audit(monitors.ActionUpdated, m.ID)
	mv, _ := meta["move"].(map[string]any)
	if w.changed(m.ID) != "name,projectId" || mv["from"] != w.def.String() || mv["to"] != w.other.String() || mv["channelsDetached"] != float64(1) ||
		mv["statusPageComponentsRemoved"] != float64(0) || len(mv) != 4 || len(meta) != 2 {
		t.Fatalf("move audit = %v", meta)
	}
	if n := w.count(`SELECT count(*) FROM monitor_channels WHERE monitor_id = $1`, m.ID); n != 0 {
		t.Fatalf("%d channel links after the move", n)
	}
	// Moving to the project it is in is nothing; the move is applied before the channel selection.
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{ProjectID: &w.other}))
	if _, n := w.audit(monitors.ActionUpdated, m.ID); n != 2 {
		t.Fatalf("same project wrote an audit row (%d)", n)
	}
	got = w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{ProjectID: &w.def, ChannelIDs: &[]uuid.UUID{w.chDef}}))
	if got.ProjectID != w.def || w.changed(m.ID) != "projectId,channelIds" {
		t.Fatalf("move with channels: %+v %q", got, w.changed(m.ID))
	}

	// Refusals.
	missing := uuid.New()
	for _, c := range []struct {
		name string
		a    monitors.Actor
		in   monitors.UpdateInput
		want string
	}{
		{"unknown project", w.owner, monitors.UpdateInput{ProjectID: &missing}, "projectId"},
		{"project not seen", w.projMaint, monitors.UpdateInput{ProjectID: &w.other}, "projectId"},
		// The name is checked before the project: the validation error comes first.
		{"bad name and unknown project", w.owner, monitors.UpdateInput{Name: ptr(" "), ProjectID: &missing}, "name"},
	} {
		if _, err := w.svc.Update(w.ctx, c.a, m.ID, c.in); fieldsOf(err) != c.want {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	// A viewer of the target project may not move a monitor into it.
	pgtest.MustExec(t, w.admin, `UPDATE monitors SET project_id = '`+w.other.String()+`' WHERE id = '`+m.ID.String()+`'`)
	viewerOfDef := w.projViewer
	viewerOfDef.Access.Projects = map[uuid.UUID]authz.Role{w.def: authz.Viewer, w.other: authz.Maintainer}
	if _, err := w.svc.Update(w.ctx, viewerOfDef, m.ID, monitors.UpdateInput{ProjectID: &w.def}); err != authz.ErrProjectForbidden {
		t.Fatalf("move into a project the actor only views: %v", err)
	}
	if got := w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{})); got.ProjectID != w.other {
		t.Fatalf("project after refusals: %+v", got)
	}
}

func TestIntegrationUpdateLocation(t *testing.T) {
	w := newWorld(t, "mon_update_location")
	m := w.http("web")
	// Nothing sent, or the current place sent: no change.
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Location: &monitors.Location{}}))
	if _, n := w.audit(monitors.ActionUpdated, m.ID); n != 0 {
		t.Fatal("same location was a change")
	}
	// The stored public target must satisfy the rules of the new place: moving to a Specula is
	// fine (private rules are looser), moving back with a private target is refused.
	w.setUp(m.ID)
	got := w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Location: &monitors.Location{SpeculaID: &w.specAll}}))
	if w.locationCode(m.ID) != "specula-"+stripDash(w.specAll) || w.changed(m.ID) != "location.speculaId" || got.Status != monitors.StatusUnknown {
		t.Fatalf("to specula: %+v %q %q", got, w.locationCode(m.ID), w.changed(m.ID))
	}
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{URL: ptr(privateURL)}}))
	if _, err := w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Location: &monitors.Location{}}); fieldsOf(err) != "http.url" {
		t.Fatalf("private target back to the worker: %v", err)
	}
	if w.locationCode(m.ID) != "specula-"+stripDash(w.specAll) {
		t.Fatal("a refused move changed the location")
	}
	// Both together: the new URL and the move to the worker, validated by the new place's rules.
	w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Location: &monitors.Location{}, HTTP: &monitors.HTTPSettings{URL: ptr(publicURL)}}))
	if w.locationCode(m.ID) != "default" || w.changed(m.ID) != "http.url,location.speculaId" {
		t.Fatalf("back to the worker: %q %q", w.locationCode(m.ID), w.changed(m.ID))
	}
	// A Specula the monitor's project may not use, and a Specula that does not exist.
	for _, id := range []uuid.UUID{w.specOther, uuid.New()} {
		_, err := w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{Name: ptr("lost"), Location: &monitors.Location{SpeculaID: &id}})
		if fieldsOf(err) != "location.speculaId" {
			t.Fatalf("specula %s: %v", id, err)
		}
	}
	if got := w.must(w.svc.Update(w.ctx, w.owner, m.ID, monitors.UpdateInput{})); got.Name != "web" || w.locationCode(m.ID) != "default" {
		t.Fatalf("rollback: %+v", got)
	}
	// A tcp monitor is checked the same way, by its host.
	tm := w.tcp("ssh")
	w.must(w.svc.Update(w.ctx, w.owner, tm.ID, monitors.UpdateInput{Location: &monitors.Location{SpeculaID: &w.specAll}, TCP: &monitors.TCPSettings{Host: ptr("10.0.0.9")}}))
	if _, err := w.svc.Update(w.ctx, w.owner, tm.ID, monitors.UpdateInput{Location: &monitors.Location{}}); fieldsOf(err) != "tcp.host" {
		t.Fatalf("private host back to the worker: %v", err)
	}
	// A project move must keep the Specula usable: "other" is not served by a Specula of the default
	// project only, here the Specula serves every project, so the move works; one shared with
	// "other" only does not serve the default project.
	pgtest.MustExec(t, w.admin, `UPDATE specula_agents SET all_projects = false WHERE id = '`+w.specAll.String()+`'`)
	_, err := w.svc.Update(w.ctx, w.owner, tm.ID, monitors.UpdateInput{ProjectID: &w.other})
	if fieldsOf(err) != "location.speculaId" {
		t.Fatalf("move away from the Specula's projects: %v", err)
	}
	if got := w.must(w.svc.Update(w.ctx, w.owner, tm.ID, monitors.UpdateInput{})); got.ProjectID != w.def {
		t.Fatalf("project after the refused move: %+v", got)
	}
	// A heartbeat has no location.
	hb := w.heartbeat("cron")
	if _, err := w.svc.Update(w.ctx, w.owner, hb.ID, monitors.UpdateInput{Location: &monitors.Location{SpeculaID: &w.specAll}}); fieldsOf(err) != "location.speculaId" {
		t.Fatalf("heartbeat location: %v", err)
	}
}
