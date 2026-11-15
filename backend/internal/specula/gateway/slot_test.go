package gateway

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

func TestSlotOutcomeTable(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	from := now.Add(-48 * time.Hour)
	until := now.Add(-time.Hour)
	anchor := now.Add(-47*time.Hour).Unix() + 7
	open := epoch{Number: 1, Anchor: anchor, Interval: 60, From: from}
	closed := open
	closed.Until = &until
	// on returns the n-th slot of the epoch.
	on := func(n int64) int64 { return anchor + n*60 }
	nowSlot := on((now.Unix() - anchor) / 60) // last slot not after now
	cases := []struct {
		name string
		e    epoch
		slot int64
		want string
	}{
		{"on the grid, recent", open, nowSlot, wire.OutcomeRecorded},
		{"anchor itself, expired", open, anchor, wire.OutcomeExpired},
		{"one second off the grid", open, nowSlot - 1, wire.OutcomeRejected},
		{"before the anchor", open, anchor - 60, wire.OutcomeRejected},
		{"before the epoch started", open, from.Unix() - 60 + (anchor-from.Unix())%60, wire.OutcomeSuperseded},
		{"exactly 24h old", epoch{Number: 1, Anchor: now.Unix() - 24*3600 - 60, Interval: 60, From: from}, now.Unix() - 24*3600, wire.OutcomeRecorded},
		{"one second over 24h old", epoch{Number: 1, Anchor: now.Unix() - 24*3600 - 60, Interval: 1, From: from}, now.Unix() - 24*3600 - 1, wire.OutcomeExpired},
		{"future within slack", epoch{Anchor: now.Unix() + 20, Interval: 60, From: from, Number: 1}, now.Unix() + 20, wire.OutcomeRecorded},
		{"future beyond slack", epoch{Anchor: now.Unix() + 31, Interval: 60, From: from, Number: 1}, now.Unix() + 31, wire.OutcomeRejected},
		{"closed epoch, slot inside", closed, on((until.Unix() - 120 - anchor) / 60), wire.OutcomeRecorded},
		{"closed epoch, slot at its end", epoch{Number: 1, Anchor: until.Unix(), Interval: 60, From: from, Until: &until}, until.Unix(), wire.OutcomeSuperseded},
		{"closed epoch, slot after", closed, nowSlot, wire.OutcomeSuperseded},
		{"zero", open, 0, wire.OutcomeRejected},
		{"negative", open, -60, wire.OutcomeRejected},
		{"huge", open, 1 << 62, wire.OutcomeRejected},
		{"max int", open, 1<<63 - 1, wire.OutcomeRejected},
		{"min int", open, -1 << 63, wire.OutcomeRejected},
	}
	for _, c := range cases {
		if got := slotOutcome(c.e, c.slot, now); got != c.want {
			t.Errorf("%s: slot %d: got %s, want %s", c.name, c.slot, got, c.want)
		}
	}
}

// Whatever the epoch and slot, a recorded slot is exactly a point of anchor + n*interval inside
// the period and inside [now-24h, now+30s]; no other slot is recorded.
func TestSlotOutcomeProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	now := time.Unix(2_000_000_000, 0)
	for range 200_000 {
		interval := int64(rng.IntN(86400*2) + 1)
		if rng.IntN(3) == 0 {
			interval = int64(rng.IntN(120) + 1)
		}
		from := now.Add(-time.Duration(rng.Int64N(int64(60*time.Hour))) * time.Second / 1)
		e := epoch{Number: 1, Anchor: from.Unix() + rng.Int64N(interval), Interval: interval, From: from}
		if rng.IntN(2) == 0 {
			u := from.Add(time.Duration(rng.Int64N(int64(50*time.Hour)/int64(time.Second))) * time.Second)
			e.Until = &u
		}
		var slot int64
		switch rng.IntN(4) {
		case 0: // a real grid point near now
			slot = e.Anchor + ((now.Unix()-e.Anchor)/interval-rng.Int64N(3000/interval+1))*interval
		case 1:
			slot = now.Unix() + rng.Int64N(7200) - 3600
		case 2:
			slot = rng.Int64()
		default:
			slot = e.Anchor + rng.Int64N(200000)*interval + rng.Int64N(2)
		}
		got := slotOutcome(e, slot, now)
		onGrid := slot >= e.Anchor && (slot-e.Anchor)%interval == 0
		inPeriod := !time.Unix(slot, 0).Before(e.From) && (e.Until == nil || time.Unix(slot, 0).Before(*e.Until))
		inWindow := slot <= now.Unix()+30 && slot >= now.Unix()-24*3600
		want := onGrid && inPeriod && inWindow && slot > 0 && slot <= maxSlot
		if (got == wire.OutcomeRecorded) != want {
			t.Fatalf("epoch %+v slot %d: got %s, recordable=%v (grid %v period %v window %v)", e, slot, got, want, onGrid, inPeriod, inWindow)
		}
	}
}
