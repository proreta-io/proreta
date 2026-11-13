package domains

import "time"

// State is the lifecycle state of a domain. A released domain is not a state: it has released_at.
type State string

// States of a domain.
const (
	// Pending: token issued, not seen in DNS. Not served.
	Pending State = "pending"
	// Verified: the TXT proof was seen. Served and eligible for a certificate; waiting for the CNAME.
	Verified State = "verified"
	// Active: verified and the CNAME ends at the configured target.
	Active State = "active"
	// Failed: no proof for the grace period. Not served; recovers to Verified when a proof returns.
	Failed State = "failed"
)

// Serving reports whether a host in state s is served and eligible for a certificate.
func (s State) Serving() bool { return s == Verified || s == Active }

// Timing rules of the lifecycle.
const (
	// PendingTTL is how long a pending domain waits for its TXT record before it expires.
	PendingTTL = 7 * 24 * time.Hour
	// FailureGrace is how long both proofs must be gone before a serving domain fails.
	FailureGrace = 7 * 24 * time.Hour
	// MinFailedChecks is the least number of consecutive checks without any proof before a
	// serving domain fails, whatever time has passed (a stopped job must not fail everything).
	MinFailedChecks = 3
)

// Proof is what one DNS lookup said about one record.
type Proof uint8

// Proof values. The zero value is Unknown, so a lookup that was not made or failed for a reason
// other than "no such record" can never count as a missing record.
const (
	// Unknown: timeout, SERVFAIL, loop or any other error. Never advances toward Failed.
	Unknown Proof = iota
	// Missing: the resolver answered and the record is not there (or does not match).
	Missing
	// Present: the TXT record carries the domain's token, or the CNAME chain ends at the target.
	Present
)

// Facts are the results of one verification check.
type Facts struct {
	TXT   Proof
	CNAME Proof
}

func (f Facts) anyPresent() bool { return f.TXT == Present || f.CNAME == Present }

// Domain is the part of a domain row the state machine reads and writes.
type Domain struct {
	State     State
	CreatedAt time.Time
	// LastProofAt is the last time the TXT or the CNAME was seen; zero if never.
	LastProofAt time.Time
	// CheckFailures counts consecutive checks in which neither record was there.
	CheckFailures int
}

// Event is something worth an audit row or a notice that a check caused.
type Event string

// Events of a check.
const (
	EventVerified  Event = "verified"
	EventActivated Event = "activated"
	EventFailed    Event = "failed"
	EventRecovered Event = "recovered"
	// EventExpired: a pending domain went seven days without its TXT record; the caller deletes it.
	EventExpired Event = "expired"
)

// Outcome is the result of Step.
type Outcome struct {
	Domain Domain
	Events []Event
}

// Step applies one check to d at time now and returns the new row values and the events. It is
// the reference for the SQL function status_page_domain_record_check, which a test compares
// with it. Rules:
//   - pending: a TXT proof makes it verified (and active when the CNAME is also right); a TXT
//     that is missing at 7 days of age expires it. A CNAME alone never verifies.
//   - verified: any proof refreshes it; the CNAME makes it active.
//   - active: any proof refreshes it. It does not step back to verified when only the CNAME is
//     gone; the host is served either way.
//   - verified or active with both records missing: counts a failure; after MinFailedChecks in a
//     row and FailureGrace since the last proof it fails.
//   - failed: any proof returns it to verified.
//   - a lookup that failed (Unknown) never counts as a missing record.
func Step(d Domain, f Facts, now time.Time) Outcome {
	out := Outcome{Domain: d}
	switch d.State {
	case Pending:
		switch {
		case f.TXT == Present:
			out.Domain.State, out.Domain.LastProofAt, out.Domain.CheckFailures = Verified, now, 0
			out.Events = append(out.Events, EventVerified)
			if f.CNAME == Present {
				out.Domain.State = Active
				out.Events = append(out.Events, EventActivated)
			}
		case f.TXT == Missing && now.Sub(d.CreatedAt) >= PendingTTL:
			out.Events = append(out.Events, EventExpired)
		}
	case Verified, Active:
		switch {
		case f.anyPresent():
			out.Domain.LastProofAt, out.Domain.CheckFailures = now, 0
			if d.State == Verified && f.CNAME == Present {
				out.Domain.State = Active
				out.Events = append(out.Events, EventActivated)
			}
		case f.TXT == Missing && f.CNAME == Missing:
			out.Domain.CheckFailures++
			since := d.LastProofAt
			if since.IsZero() {
				since = d.CreatedAt
			}
			if out.Domain.CheckFailures >= MinFailedChecks && now.Sub(since) >= FailureGrace {
				out.Domain.State = Failed
				out.Events = append(out.Events, EventFailed)
			}
		}
	case Failed:
		if f.anyPresent() {
			out.Domain.State, out.Domain.LastProofAt, out.Domain.CheckFailures = Verified, now, 0
			out.Events = append(out.Events, EventRecovered)
		}
	}
	return out
}
