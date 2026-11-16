package e2e_test

import (
	"strings"
	"testing"
	"time"
)

// failing are the slots of m inside [from, to).
func (e *env) failing(m mon, from, to time.Time) []int64 {
	return m.slots(from, to.Add(-time.Second))
}

// Incidents that happened while the link was down are created with their real start and end. One
// that is over when Proreta hears of it sends a single late "recovered" and no "down"; one that is
// still going on sends a late "down", and its end later is an ordinary message.
func TestIncidentsDuringAnOutage(t *testing.T) {
	e := newEnv(t, "e2eincidents", 4*time.Hour, []monSpec{
		{name: "over", interval: 60, threshold: 1},
		{name: "going", interval: 60, threshold: 1},
		{name: "calm", interval: 60, threshold: 1},
	})
	over, going := e.monitor("over"), e.monitor("going")
	at := func(d time.Duration) time.Time { return e.t0.Add(d) }
	e.prober.failDuring(over.Key, at(15*time.Minute), at(25*time.Minute))
	e.prober.failDuring(going.Key, at(35*time.Minute), at(70*time.Minute))

	e.start(e.clk, t.TempDir())
	e.ready()
	e.run(10*time.Minute, time.Minute)
	e.link.cut.Store(true)
	e.run(40*time.Minute, time.Minute) // 00:10 to 00:50
	if n := e.count(`SELECT count(*) FROM incidents`); n != 0 {
		t.Fatalf("%d incidents while the link was down", n)
	}
	e.link.cut.Store(false)
	e.run(2*time.Minute, time.Minute)
	e.until("every slot is stored", time.Hour, 5*time.Second, func() bool { return e.allStored(e.t0) })

	incident := func(m mon) (started, resolved *time.Time) {
		t.Helper()
		if n := e.count(`SELECT count(*) FROM incidents WHERE monitor_id = $1`, m.ID); n != 1 {
			t.Fatalf("%s has %d incidents, want 1", m.Name, n)
		}
		if err := e.admin.QueryRow(e.ctx, `SELECT started_at, resolved_at FROM incidents WHERE monitor_id = $1`, m.ID).Scan(&started, &resolved); err != nil {
			t.Fatal(err)
		}
		return started, resolved
	}
	// The first failing slot starts the incident, the first good one after the window ends it.
	failed := e.failing(over, at(15*time.Minute), at(25*time.Minute))
	started, resolved := incident(over)
	if resolved == nil || started.Unix() != failed[0] || resolved.Unix() != over.slots(at(25*time.Minute), at(26*time.Minute))[0] {
		t.Errorf("over: incident %v to %v, want %d to the first slot after 00:25", started, resolved, failed[0])
	}
	started, resolved = incident(going)
	if resolved != nil || started.Unix() != e.failing(going, at(35*time.Minute), at(70*time.Minute))[0] {
		t.Errorf("going: incident %v to %v, want one that began at its first failing slot and is open", started, resolved)
	}
	if n := e.count(`SELECT count(*) FROM incidents WHERE monitor_id = $1`, e.monitor("calm").ID); n != 0 {
		t.Errorf("calm has %d incidents", n)
	}

	e.deliverAll()
	row := func(m mon, kind string) (status, errKind string) {
		t.Helper()
		var last *string
		if err := e.admin.QueryRow(e.ctx, `SELECT status, last_error FROM notification_deliveries WHERE monitor_id = $1 AND kind = $2`, m.ID, kind).Scan(&status, &last); err != nil {
			t.Fatalf("%s %s: %v", m.Name, kind, err)
		}
		if last != nil {
			errKind = *last
		}
		return status, errKind
	}
	if s, why := row(over, "opened"); s != "failed" || why != "late_incident_resolved" {
		t.Errorf("over: the late 'down' is %q (%q), want it dropped because the incident is over", s, why)
	}
	if s, _ := row(over, "resolved"); s != "sent" {
		t.Errorf("over: the 'recovered' is %q, want sent", s)
	}
	if texts := e.textsAbout("over"); len(texts) != 1 || !strings.Contains(texts[0], "back up") || !strings.Contains(texts[0], "reported late") || !strings.Contains(texts[0], "Was down") {
		t.Errorf("over: messages %q, want one late 'back up' that says when it was down", texts)
	}
	if texts := e.textsAbout("going"); len(texts) != 1 || !strings.Contains(texts[0], "is down (reported late)") {
		t.Errorf("going: messages %q, want one late 'down'", texts)
	}

	// The target of "going" recovers at 01:10, live: an ordinary message.
	e.run(25*time.Minute, time.Minute)
	e.until("every slot is stored", time.Hour, 5*time.Second, func() bool { return e.allStored(e.t0) })
	_, resolved = incident(going)
	if resolved == nil || resolved.Unix() != going.slots(at(70*time.Minute), at(71*time.Minute))[0] {
		t.Errorf("going: resolved %v, want the first slot after 01:10", resolved)
	}
	e.deliverAll()
	if texts := e.textsAbout("going"); len(texts) != 2 || !strings.Contains(texts[1], "back up") || strings.Contains(texts[1], "late") {
		t.Errorf("going: messages %q, want the late 'down' and an ordinary 'back up'", texts)
	}
	if n := e.count(`SELECT count(*) FROM notification_deliveries`); n != 4 {
		t.Errorf("%d deliveries, want 4 (over: down dropped, recovered; going: down, recovered)", n)
	}
	e.wantCovered(e.t0, e.clk.Now())
}
