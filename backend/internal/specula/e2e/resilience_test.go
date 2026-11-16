package e2e_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

var twoMonitors = []monSpec{
	{name: "api", interval: 60, threshold: 1},
	{name: "db", interval: 60, threshold: 1, tcp: true},
}

func (e *env) degradedAnnounced() bool {
	return e.count(`SELECT count(*) FROM specula_agents WHERE id = $1 AND buffer_degraded`, e.spec.ID) == 1
}

func lastOf(s []int64) int64 { return s[len(s)-1] }

// Without its state directory the executor keeps checking from memory, loses no slot while the
// process lives, and says in its hello that the buffer is degraded.
func TestStateDirectoryGone(t *testing.T) {
	for _, c := range []struct {
		name string
		// A deleted directory is made again when the buffer next writes, so it is not degraded for
		// long; one replaced by a file stays unusable.
		degraded bool
		remove   func(t *testing.T, dir string)
	}{
		{"deleted while running", false, func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced by a file while running", true, func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "e2estatedir", 3*time.Hour, twoMonitors)
			dir := filepath.Join(t.TempDir(), "state")
			e.start(e.clk, dir)
			e.ready()
			e.run(5*time.Minute, time.Minute)
			e.link.cut.Store(true)
			e.run(2*time.Minute, time.Minute)
			cutAt := e.clk.Now().Add(-2 * time.Minute)
			gone := e.clk.Now()
			c.remove(t, dir)
			e.run(10*time.Minute, time.Minute)
			e.link.cut.Store(false)
			e.until("the slots after the removal are stored", 30*time.Minute, 5*time.Second, func() bool {
				got := e.stored()
				for _, m := range e.mons {
					if !slices.Contains(got[m.ID], lastOf(m.slots(e.t0, e.clk.Now()))) {
						return false
					}
				}
				return true
			})
			e.run(time.Minute, time.Minute)
			// Known limit: what was waiting in the segment file when the directory went, and what was
			// appended to that file before the buffer noticed, is lost: the segment is read by path,
			// not through the file the process holds. Everything after that is kept in memory.
			lost := e.wantCoveredExcept(e.t0, e.clk.Now(), cutAt, gone.Add(2*time.Minute))
			t.Logf("results lost with the directory: %d", lost)
			if c.degraded {
				e.until("the hello says the buffer is degraded", 5*time.Minute, 10*time.Second, e.degradedAnnounced)
			}
		})
	}
}

// An executor that cannot use its state directory from the start (read-only file system, no
// volume) checks from memory and says so.
func TestStateDirectoryUnusableFromTheStart(t *testing.T) {
	e := newEnv(t, "e2enostate", 3*time.Hour, twoMonitors)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.start(e.clk, filepath.Join(file, "state")) // a directory below a file
	e.ready()
	e.run(3*time.Minute, time.Minute)
	e.link.cut.Store(true)
	e.run(8*time.Minute, time.Minute)
	e.link.cut.Store(false)
	e.until("every slot is stored", 30*time.Minute, 5*time.Second, func() bool { return e.allStored(e.t0) })
	e.wantCovered(e.t0, e.clk.Now())
	e.until("the hello says the buffer is degraded", 5*time.Minute, 10*time.Second, e.degradedAnnounced)
}

// An executor whose clock is minutes off still runs the server's slots, because it corrects its
// clock by what hello and the schedule say; on its own clock every slot would be refused (too far in
// the future) or checked at the wrong time.
func TestClockSkew(t *testing.T) {
	for _, skew := range []time.Duration{7 * time.Minute, -7 * time.Minute} {
		t.Run(skew.String(), func(t *testing.T) {
			e := newEnv(t, "e2eskew", 3*time.Hour, twoMonitors)
			e.start(skewed{e.clk, skew}, t.TempDir())
			e.ready()
			e.run(10*time.Minute, time.Minute)
			e.until("every slot is stored", 10*time.Minute, 5*time.Second, func() bool { return e.allStored(e.t0) })
			e.wantCovered(e.t0, e.clk.Now())
		})
	}
}

// The executor is restarted in the middle of an outage, its clock off by minutes: it checks from the
// saved schedule and the saved clock correction before it has reached Proreta, and the results of the
// first run, kept on disk, are sent with the others when the link is back.
func TestRestartDuringOutage(t *testing.T) {
	e := newEnv(t, "e2erestart", 3*time.Hour, twoMonitors)
	view := skewed{e.clk, 7 * time.Minute}
	dir := t.TempDir()
	first := e.start(view, dir)
	e.ready()
	e.run(5*time.Minute, time.Minute)
	e.link.cut.Store(true)
	e.run(5*time.Minute, time.Minute)
	if err := first.stop(t, e.clk); err != nil {
		t.Fatal(err)
	}
	hellosBefore := e.link.hit(wire.PathHello)
	e.start(view, dir)
	e.clk.quiet(100 * time.Millisecond) // it starts checking from the cache, with no connection
	e.run(5*time.Minute, time.Minute)
	if got := e.link.hit(wire.PathHello); got != hellosBefore {
		t.Fatalf("the link is cut, but %d hellos got through", got-hellosBefore)
	}
	e.link.cut.Store(false)
	e.until("every slot is stored", 30*time.Minute, 5*time.Second, func() bool { return e.allStored(e.t0) })
	e.wantCovered(e.t0, e.clk.Now())
}
