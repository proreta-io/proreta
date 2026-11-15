package executor

import (
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

var bufMonitor = uuid.New()

func rep(slot int64) wire.Report {
	return wire.Report{MonitorID: bufMonitor, Epoch: 1, Slot: slot, OK: true, StatusCode: 200, LatencyMs: 7}
}

func openTestBuffer(t *testing.T, dir string, cfg bufferConfig, now time.Time) *buffer {
	t.Helper()
	st := newStore(dir)
	_ = st.load() // an unusable directory is the executor's problem to log, not a failure
	b := openBuffer(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	t.Cleanup(b.Close)
	return b
}

func slotsOf(b *buffer) []int64 {
	var out []int64
	for _, r := range b.Peek(1 << 30).Reports {
		out = append(out, r.Slot)
	}
	return out
}

func seq(from, n int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = from + int64(i)
	}
	return out
}

func segFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "buffer", "seg-*.log"))
	return m
}

var small = bufferConfig{SegmentBytes: 2048}

func TestBufferKeepsOrderAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	base := t0.Unix()
	b := openTestBuffer(t, dir, small, t0)
	for i := range int64(250) {
		b.Append(rep(base + i))
	}
	if len(segFiles(t, dir)) < 3 {
		t.Fatalf("expected several segments, got %d", len(segFiles(t, dir)))
	}
	batch := b.Peek(100)
	if len(batch.Reports) != 100 || batch.Reports[0].Slot != base || batch.Reports[99].Slot != base+99 {
		t.Fatalf("batch %d", len(batch.Reports))
	}
	b.Ack(batch)
	if b.Depth() != 150 {
		t.Fatalf("depth %d", b.Depth())
	}
	b.Close()
	b2 := openTestBuffer(t, dir, small, t0)
	if got := slotsOf(b2); !eq(got, seq(base+100, 150)...) || b2.Depth() != 150 {
		t.Fatalf("after the restart: %d results, depth %d, first %v", len(got), b2.Depth(), got[:1])
	}
	b2.Append(rep(base + 250))
	if got := slotsOf(b2); len(got) != 151 || got[150] != base+250 {
		t.Fatal("append after the restart")
	}
}

func TestBufferGivesBackWhatWasNotAcknowledged(t *testing.T) {
	// The process dies after the gateway stored a batch and before the acknowledgement: the batch
	// comes back (the gateway answers duplicate) and nothing else is lost.
	dir := t.TempDir()
	b := openTestBuffer(t, dir, small, t0)
	for i := range int64(30) {
		b.Append(rep(t0.Unix() + i))
	}
	_ = b.Peek(10)
	b.Close()
	b2 := openTestBuffer(t, dir, small, t0)
	if got := slotsOf(b2); !eq(got, seq(t0.Unix(), 30)...) {
		t.Fatalf("slots %v", got)
	}
}

func TestBufferCutsATornLastLine(t *testing.T) {
	dir := t.TempDir()
	b := openTestBuffer(t, dir, bufferConfig{}, t0)
	for i := range int64(5) {
		b.Append(rep(t0.Unix() + i))
	}
	b.Close()
	f := segFiles(t, dir)[0]
	st, _ := os.Stat(f)
	if err := os.Truncate(f, st.Size()-10); err != nil {
		t.Fatal(err)
	}
	b2 := openTestBuffer(t, dir, bufferConfig{}, t0)
	if got := slotsOf(b2); !eq(got, seq(t0.Unix(), 4)...) {
		t.Fatalf("slots %v", got)
	}
	b2.Append(rep(t0.Unix() + 9))
	b2.Close()
	b3 := openTestBuffer(t, dir, bufferConfig{}, t0)
	if got := slotsOf(b3); len(got) != 5 || got[4] != t0.Unix()+9 {
		t.Fatalf("the line after the cut is damaged: %v", got)
	}
}

func TestBufferSkipsADamagedRecordAndCountsIt(t *testing.T) {
	dir := t.TempDir()
	b := openTestBuffer(t, dir, bufferConfig{}, t0)
	for i := range int64(5) {
		b.Append(rep(t0.Unix() + i))
	}
	b.Close()
	f := segFiles(t, dir)[0]
	data, _ := os.ReadFile(f)
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(f, data, 0o600); err != nil {
		t.Fatal(err)
	}
	b2 := openTestBuffer(t, dir, bufferConfig{}, t0)
	got := slotsOf(b2)
	if len(got) != 4 || b2.Depth() != 4 || b2.Stats().Corrupt < 1 {
		t.Fatalf("got %v depth %d corrupt %d", got, b2.Depth(), b2.Stats().Corrupt)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("order %v", got)
		}
	}
}

func TestBufferSetsAnUnreadableSegmentAsideAndGoesOn(t *testing.T) {
	dir := t.TempDir()
	b := openTestBuffer(t, dir, small, t0)
	for i := range int64(100) {
		b.Append(rep(t0.Unix() + i))
	}
	b.Close()
	files := segFiles(t, dir)
	if err := os.Chmod(files[1], 0); err != nil {
		t.Fatal(err)
	}
	b2 := openTestBuffer(t, dir, small, t0)
	if b2.Stats().Quarantined != 1 {
		t.Fatalf("quarantined %d", b2.Stats().Quarantined)
	}
	if _, err := os.Stat(files[1] + ".bad"); err != nil {
		t.Fatalf("not renamed: %v", err)
	}
	got := slotsOf(b2)
	if len(got) == 0 || len(got) >= 100 || got[0] != t0.Unix() {
		t.Fatalf("got %d results", len(got))
	}
	b2.Append(rep(t0.Unix() + 500)) // still writable
}

func TestBufferDropsTheOldestSegmentAtTheSizeLimit(t *testing.T) {
	dir := t.TempDir()
	b := openTestBuffer(t, dir, bufferConfig{SegmentBytes: 1024, MaxBytes: 8 * 1024}, t0)
	for i := range int64(500) {
		b.Append(rep(t0.Unix() + i))
	}
	st := b.Stats()
	if st.Drops[dropSize] == 0 || st.Bytes > 8*1024+1024 {
		t.Fatalf("drops %v bytes %d", st.Drops, st.Bytes)
	}
	got := slotsOf(b)
	if got[len(got)-1] != t0.Unix()+499 || int64(len(got))+st.Drops[dropSize] != 500 || b.Depth() != len(got) {
		t.Fatalf("kept %d dropped %d depth %d", len(got), st.Drops[dropSize], b.Depth())
	}
	if got[0] != t0.Unix()+st.Drops[dropSize] {
		t.Fatal("not the oldest that were dropped")
	}
}

func TestBufferDropsResultsOlderThanTheAgeLimit(t *testing.T) {
	dir := t.TempDir()
	now := t0.Add(30 * time.Hour)
	b := openTestBuffer(t, dir, small, now)
	for i := range int64(60) { // 30 hours old
		b.Append(rep(t0.Unix() + i))
	}
	for i := range int64(10) {
		b.Append(rep(now.Unix() - 100 + i))
	}
	b.Enforce()
	got := slotsOf(b)
	if b.Stats().Drops[dropAge] == 0 || got[len(got)-1] != now.Unix()-91 || b.Depth() != len(got) {
		t.Fatalf("drops %v kept %d depth %d", b.Stats().Drops, len(got), b.Depth())
	}
	for _, s := range got {
		if s < now.Unix()-int64(DefaultBufferAge/time.Second)-int64(len(got)) && s < t0.Unix()+60 && s >= t0.Unix() {
			continue
		}
	}
}

func TestBufferWithoutAStateDirKeepsAMemoryRing(t *testing.T) {
	b := openTestBuffer(t, "", bufferConfig{MemoryMax: 10}, t0)
	if !b.Degraded() {
		t.Fatal("memory only must be reported as degraded")
	}
	for i := range int64(25) {
		b.Append(rep(t0.Unix() + i))
	}
	got := slotsOf(b)
	if !eq(got, seq(t0.Unix()+15, 10)...) || b.Stats().Drops[dropMemory] != 15 || b.Depth() != 10 {
		t.Fatalf("got %v drops %v depth %d", got, b.Stats().Drops, b.Depth())
	}
	b.Ack(b.Peek(4))
	if b.Depth() != 6 || len(slotsOf(b)) != 6 {
		t.Fatalf("depth %d", b.Depth())
	}
}

func TestBufferDegradesWhenTheDirectoryCannotBeUsed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"parent is a file": filepath.Join(file, "state"), "read only": ro(t)} {
		t.Run(name, func(t *testing.T) {
			b := openTestBuffer(t, dir, bufferConfig{}, t0)
			if !b.Degraded() {
				t.Fatal("not degraded")
			}
			b.Append(rep(t0.Unix()))
			if len(slotsOf(b)) != 1 {
				t.Fatal("the result was not kept in memory")
			}
		})
	}
}

func ro(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d, 0o700) })
	return filepath.Join(d, "state")
}

func TestBufferCreatesAMissingStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	b := openTestBuffer(t, dir, bufferConfig{}, t0)
	if b.Degraded() {
		t.Fatal("degraded")
	}
	b.Append(rep(t0.Unix()))
	if len(segFiles(t, dir)) != 1 {
		t.Fatal("no segment")
	}
}

func TestBufferFallsBackToMemoryWhenTheDiskFailsAndComesBack(t *testing.T) {
	dir := t.TempDir()
	now := t0
	st := newStore(dir)
	b := openBuffer(small, st, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	defer b.Close()
	for i := range int64(10) {
		b.Append(rep(t0.Unix() + i))
	}
	_ = b.active.Close() // the next write fails, like a full disk
	for i := int64(10); i < 20; i++ {
		b.Append(rep(t0.Unix() + i))
	}
	if !b.Degraded() {
		t.Fatal("not degraded")
	}
	if got := slotsOf(b); !eq(got, seq(t0.Unix(), 20)...) {
		t.Fatalf("order across disk and ring: %v", got)
	}
	now = now.Add(time.Minute)
	b.Enforce() // the disk works again
	if b.Degraded() {
		t.Fatal("did not recover")
	}
	b.Append(rep(t0.Unix() + 20))
	if got := slotsOf(b); !eq(got, seq(t0.Unix(), 21)...) || b.Depth() != 21 {
		t.Fatalf("after recovery: %v depth %d", got, b.Depth())
	}
	b.Close()
	b2 := openTestBuffer(t, dir, small, now)
	if got := slotsOf(b2); !eq(got, seq(t0.Unix(), 21)...) {
		t.Fatalf("after a restart: %v", got)
	}
}

func TestDecodeLineRoundTripAndRejects(t *testing.T) {
	r := rep(1234)
	got, ok := decodeLine(encodeLine(r)[:len(encodeLine(r))-1])
	if !ok || got.Slot != 1234 || got.MonitorID != bufMonitor {
		t.Fatalf("%+v %v", got, ok)
	}
	for _, bad := range []string{"", "short", "zzzzzzzz {}", "00000000 {}", "00000000{}"} {
		if _, ok := decodeLine([]byte(bad)); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func FuzzDecodeLine(f *testing.F) {
	f.Add(encodeLine(rep(1)))
	f.Add([]byte("00000000 {}"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = decodeLine(b) })
}

// A random mix of appends, reads, acknowledgements, restarts and damage to the files: the buffer
// must never panic or stall and what it returns must stay in strictly ascending order.
func TestBufferSurvivesRandomFaults(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		rng := rand.New(rand.NewPCG(seed, 99))
		dir := t.TempDir()
		b := openTestBuffer(t, dir, bufferConfig{SegmentBytes: 700, MaxBytes: 6000}, t0)
		next := t0.Unix()
		for range 400 {
			switch rng.IntN(10) {
			case 0, 1, 2, 3, 4:
				b.Append(rep(next))
				next++
			case 5:
				b.Ack(b.Peek(1 + rng.IntN(20)))
			case 6:
				b.Enforce()
			case 7:
				b.Close()
				b = openTestBuffer(t, dir, bufferConfig{SegmentBytes: 700, MaxBytes: 6000}, t0)
			default:
				files := segFiles(t, dir)
				if len(files) == 0 {
					continue
				}
				f := files[rng.IntN(len(files))]
				data, err := os.ReadFile(f)
				if err != nil || len(data) == 0 {
					continue
				}
				if rng.IntN(2) == 0 {
					data[rng.IntN(len(data))] ^= byte(1 + rng.IntN(255))
					_ = os.WriteFile(f, data, 0o600)
				} else {
					_ = os.Truncate(f, int64(rng.IntN(len(data))))
				}
			}
			got := slotsOf(b)
			for i := 1; i < len(got); i++ {
				if got[i] <= got[i-1] {
					t.Fatalf("seed %d: not ascending: %v", seed, got)
				}
			}
		}
		b.Close()
	}
}
