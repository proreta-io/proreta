package executor

import (
	"strings"
	"testing"
)

// serverClock subtracts local readings, so they must carry the monotonic clock: a wall-clock step
// between two hellos would otherwise move the corrected time.
func TestRealClockKeepsTheMonotonicReading(t *testing.T) {
	if !strings.Contains(realClock{}.Now().String(), "m=") {
		t.Fatal("realClock.Now dropped the monotonic reading")
	}
}
