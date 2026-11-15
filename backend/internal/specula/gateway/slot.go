package gateway

import (
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

const (
	// maxBacklog is how old a slot may be and still be stored.
	maxBacklog = 24 * time.Hour
	// clockSlack is how far into the future a slot may lie.
	clockSlack = 30 * time.Second
	// maxSlot keeps slot arithmetic far from int64 overflow; year 36812.
	maxSlot = int64(1) << 40
)

// epoch is the part of a monitor_schedule_epochs row a slot is checked against. The server chose
// every field; nothing in a report can change them.
type epoch struct {
	Number   int
	Anchor   int64
	Interval int64 // seconds, > 0 (table CHECK)
	From     time.Time
	Until    *time.Time // nil while open
}

// slotOutcome decides whether slot may be stored for e at now. It is the whole validation: the
// caller adds only the lookup of e (a missing monitor or a missing epoch row is decided there).
// The result is wire.OutcomeRecorded when the slot is acceptable.
func slotOutcome(e epoch, slot int64, now time.Time) string {
	if slot <= 0 || slot > maxSlot {
		return wire.OutcomeRejected
	}
	at := time.Unix(slot, 0)
	if at.Before(e.From) || (e.Until != nil && !at.Before(*e.Until)) {
		return wire.OutcomeSuperseded
	}
	if slot < e.Anchor || (slot-e.Anchor)%e.Interval != 0 {
		return wire.OutcomeRejected
	}
	switch {
	case at.After(now.Add(clockSlack)):
		return wire.OutcomeRejected
	case at.Before(now.Add(-maxBacklog)):
		return wire.OutcomeExpired
	}
	return wire.OutcomeRecorded
}
