package domains

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)

func days(n int) time.Duration  { return time.Duration(n) * 24 * time.Hour }
func hours(n int) time.Duration { return time.Duration(n) * time.Hour }

func TestStep(t *testing.T) {
	both := Facts{Present, Present}
	txtOnly := Facts{TXT: Present, CNAME: Missing}
	cnameOnly := Facts{TXT: Missing, CNAME: Present}
	none := Facts{Missing, Missing}
	dnsDown := Facts{Unknown, Unknown}

	tests := []struct {
		name     string
		in       Domain
		facts    Facts
		at       time.Time // now; default t0
		state    State
		events   []Event
		failures int
		proof    time.Time // expected LastProofAt
	}{
		// pending
		{"pending, TXT seen", Domain{State: Pending, CreatedAt: t0.Add(-hours(1))}, txtOnly, time.Time{}, Verified, []Event{EventVerified}, 0, t0},
		{"pending, TXT and CNAME seen", Domain{State: Pending, CreatedAt: t0.Add(-hours(1))}, both, time.Time{}, Active, []Event{EventVerified, EventActivated}, 0, t0},
		{"pending, CNAME alone never verifies", Domain{State: Pending, CreatedAt: t0.Add(-hours(1))}, cnameOnly, time.Time{}, Pending, nil, 0, time.Time{}},
		{"pending, nothing seen", Domain{State: Pending, CreatedAt: t0.Add(-days(3))}, none, time.Time{}, Pending, nil, 0, time.Time{}},
		{"pending, expires at 7 days", Domain{State: Pending, CreatedAt: t0.Add(-days(7))}, none, time.Time{}, Pending, []Event{EventExpired}, 0, time.Time{}},
		{"pending, one second short of 7 days", Domain{State: Pending, CreatedAt: t0.Add(-days(7) + time.Second)}, none, time.Time{}, Pending, nil, 0, time.Time{}},
		{"pending, DNS error after 7 days does not expire", Domain{State: Pending, CreatedAt: t0.Add(-days(9))}, dnsDown, time.Time{}, Pending, nil, 0, time.Time{}},
		{"pending, TXT seen after 7 days still verifies", Domain{State: Pending, CreatedAt: t0.Add(-days(9))}, txtOnly, time.Time{}, Verified, []Event{EventVerified}, 0, t0},
		{"pending, CNAME alone after 7 days with TXT missing expires", Domain{State: Pending, CreatedAt: t0.Add(-days(8))}, cnameOnly, time.Time{}, Pending, []Event{EventExpired}, 0, time.Time{}},

		// verified
		{"verified, both seen becomes active", Domain{State: Verified, LastProofAt: t0.Add(-hours(6)), CheckFailures: 2}, both, time.Time{}, Active, []Event{EventActivated}, 0, t0},
		{"verified, CNAME only becomes active", Domain{State: Verified, LastProofAt: t0.Add(-hours(6))}, cnameOnly, time.Time{}, Active, []Event{EventActivated}, 0, t0},
		{"verified, TXT only stays verified and refreshes", Domain{State: Verified, LastProofAt: t0.Add(-hours(6)), CheckFailures: 1}, txtOnly, time.Time{}, Verified, nil, 0, t0},
		{"verified, one failed check", Domain{State: Verified, LastProofAt: t0.Add(-hours(6))}, none, time.Time{}, Verified, nil, 1, t0.Add(-hours(6))},
		{"verified, DNS error is not a failure", Domain{State: Verified, LastProofAt: t0.Add(-days(8)), CheckFailures: 5}, dnsDown, time.Time{}, Verified, nil, 5, t0.Add(-days(8))},
		{"verified, TXT missing and CNAME lookup failed is not a failure", Domain{State: Verified, LastProofAt: t0.Add(-days(8)), CheckFailures: 5}, Facts{Missing, Unknown}, time.Time{}, Verified, nil, 5, t0.Add(-days(8))},
		{"verified, third failure but under 7 days", Domain{State: Verified, LastProofAt: t0.Add(-days(2)), CheckFailures: 2}, none, time.Time{}, Verified, nil, 3, t0.Add(-days(2))},
		{"verified, 7 days but only the second failure", Domain{State: Verified, LastProofAt: t0.Add(-days(7)), CheckFailures: 1}, none, time.Time{}, Verified, nil, 2, t0.Add(-days(7))},
		{"verified, third failure after 7 days fails", Domain{State: Verified, LastProofAt: t0.Add(-days(7)), CheckFailures: 2}, none, time.Time{}, Failed, []Event{EventFailed}, 3, t0.Add(-days(7))},
		{"verified without a proof time counts from creation", Domain{State: Verified, CreatedAt: t0.Add(-days(10)), CheckFailures: 2}, none, time.Time{}, Failed, []Event{EventFailed}, 3, time.Time{}},

		// active
		{"active, both seen", Domain{State: Active, LastProofAt: t0.Add(-hours(6))}, both, time.Time{}, Active, nil, 0, t0},
		{"active, TXT deleted but CNAME stays", Domain{State: Active, LastProofAt: t0.Add(-hours(6)), CheckFailures: 1}, cnameOnly, time.Time{}, Active, nil, 0, t0},
		{"active, CNAME gone but TXT stays: still active", Domain{State: Active, LastProofAt: t0.Add(-hours(6))}, txtOnly, time.Time{}, Active, nil, 0, t0},
		{"active, both gone for 7 days and 3 checks fails", Domain{State: Active, LastProofAt: t0.Add(-days(7) - hours(1)), CheckFailures: 2}, none, time.Time{}, Failed, []Event{EventFailed}, 3, t0.Add(-days(7) - hours(1))},
		{"active, DNS outage for 20 days never fails", Domain{State: Active, LastProofAt: t0.Add(-days(20)), CheckFailures: 0}, dnsDown, time.Time{}, Active, nil, 0, t0.Add(-days(20))},

		// failed
		{"failed, nothing", Domain{State: Failed, LastProofAt: t0.Add(-days(9)), CheckFailures: 30}, none, time.Time{}, Failed, nil, 30, t0.Add(-days(9))},
		{"failed, DNS error", Domain{State: Failed, LastProofAt: t0.Add(-days(9)), CheckFailures: 30}, dnsDown, time.Time{}, Failed, nil, 30, t0.Add(-days(9))},
		{"failed, TXT returns", Domain{State: Failed, LastProofAt: t0.Add(-days(9)), CheckFailures: 30}, txtOnly, time.Time{}, Verified, []Event{EventRecovered}, 0, t0},
		{"failed, CNAME returns", Domain{State: Failed, LastProofAt: t0.Add(-days(9)), CheckFailures: 30}, cnameOnly, time.Time{}, Verified, []Event{EventRecovered}, 0, t0},
		{"failed, both return goes to verified first", Domain{State: Failed, LastProofAt: t0.Add(-days(9))}, both, time.Time{}, Verified, []Event{EventRecovered}, 0, t0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := tc.at
			if now.IsZero() {
				now = t0
			}
			got := Step(tc.in, tc.facts, now)
			if got.Domain.State != tc.state || got.Domain.CheckFailures != tc.failures || !got.Domain.LastProofAt.Equal(tc.proof) {
				t.Fatalf("got state %s failures %d proof %v; want %s %d %v", got.Domain.State, got.Domain.CheckFailures, got.Domain.LastProofAt, tc.state, tc.failures, tc.proof)
			}
			if len(got.Events) != len(tc.events) {
				t.Fatalf("events %v, want %v", got.Events, tc.events)
			}
			for i := range tc.events {
				if got.Events[i] != tc.events[i] {
					t.Fatalf("events %v, want %v", got.Events, tc.events)
				}
			}
			if got.Domain.CreatedAt != tc.in.CreatedAt {
				t.Fatal("CreatedAt changed")
			}
		})
	}
}

// A run of checks drives a domain through its whole life.
func TestStepLifecycle(t *testing.T) {
	d := Domain{State: Pending, CreatedAt: t0}
	now := t0
	step := func(f Facts, after time.Duration) Outcome {
		now = now.Add(after)
		o := Step(d, f, now)
		d = o.Domain
		return o
	}
	step(Facts{Missing, Missing}, hours(1))
	if d.State != Pending {
		t.Fatal(d.State)
	}
	step(Facts{Present, Missing}, hours(1))
	if d.State != Verified {
		t.Fatal(d.State)
	}
	step(Facts{Present, Present}, hours(6))
	if d.State != Active {
		t.Fatal(d.State)
	}
	// The customer removes everything: a check every 6 hours for 7 days.
	for i := 0; i < 27; i++ {
		step(Facts{Missing, Missing}, hours(6))
		if d.State != Active {
			t.Fatalf("failed too early at check %d", i+1)
		}
	}
	if o := step(Facts{Missing, Missing}, hours(6)); d.State != Failed || len(o.Events) != 1 {
		t.Fatalf("state %s, events %v", d.State, o.Events)
	}
	step(Facts{Present, Missing}, hours(6))
	if d.State != Verified || d.CheckFailures != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestServing(t *testing.T) {
	for s, want := range map[State]bool{Pending: false, Verified: true, Active: true, Failed: false} {
		if s.Serving() != want {
			t.Fatalf("%s.Serving() != %v", s, want)
		}
	}
}
