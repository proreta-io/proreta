package executor

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// Defaults of the buffer limits.
const (
	DefaultBufferBytes = 256 << 20
	DefaultBufferAge   = 24 * time.Hour
	defaultSegBytes    = 4 << 20
	defaultMemoryMax   = 2000
	recoverEvery       = 30 * time.Second
	syncEvery          = time.Second
	syncAfter          = 100
	readChunk          = 64 << 10
)

// Reasons for which results are dropped (the metric label).
const (
	dropSize   = "size"
	dropAge    = "age"
	dropMemory = "memory"
	dropBad    = "unreadable"
	// dropRefused is a result the gateway refused as a whole; it is final.
	dropRefused = "refused"
)

// bufferConfig are the limits of a buffer; zero values take the defaults.
type bufferConfig struct {
	MaxBytes     int64
	MaxAge       time.Duration
	SegmentBytes int64
	MemoryMax    int
}

// cursor is the position of the oldest result that was not acknowledged: a segment and an offset.
type cursor struct {
	Seg int   `json:"seg"`
	Off int64 `json:"off"`
}

type segment struct {
	n        int
	size     int64
	records  int   // valid results not yet acknowledged
	lastSlot int64 // slot of the newest record, for the age limit
}

type ringItem struct {
	seq uint64
	r   wire.Report
}

// buffer holds finished slots until the gateway has answered for them. Results are appended to
// segment files of CRC-framed JSON lines ("<crc32 hex> <json>\n") and read back from there, so
// only what reached the disk survives a restart. When the state directory is missing or cannot
// be written the buffer keeps a small ring in memory instead and says it is degraded. It never
// stops the executor: a corrupt record is skipped, a corrupt segment is set aside.
//
// One goroutine appends (the checks), one reads (the reporter); the mutex covers both.
type buffer struct {
	cfg bufferConfig
	st  *store
	log *slog.Logger
	now func() time.Time

	mu          sync.Mutex
	dir         string
	segs        []*segment // ascending; the last is the active one
	nextN       int        // number of the next segment; never reused, so order follows the numbers
	active      *os.File
	cur         cursor
	depth       int
	bytes       int64
	ring        []ringItem
	nextSeq     uint64
	degraded    bool
	unsynced    int
	lastSync    time.Time
	lastRecover time.Time
	drops       map[string]int64
	corrupt     int64
	quarantined int64
}

// Batch is a read-ahead of the buffer: Reports in order and what Ack needs to remove them.
type Batch struct {
	Reports []wire.Report

	end             cursor
	consumed        int // disk records including skipped bad ones
	lastSegConsumed int
	ringEnd         uint64 // ring items with seq < ringEnd
	ringCount       int
}

func openBuffer(cfg bufferConfig, st *store, log *slog.Logger, now func() time.Time) *buffer {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultBufferBytes
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = DefaultBufferAge
	}
	if cfg.SegmentBytes <= 0 {
		cfg.SegmentBytes = defaultSegBytes
	}
	if cfg.MemoryMax <= 0 {
		cfg.MemoryMax = defaultMemoryMax
	}
	b := &buffer{cfg: cfg, st: st, log: log, now: now, drops: map[string]int64{}, lastSync: now()}
	if !st.enabled() {
		b.degraded = true
		log.Warn("no state directory: results are buffered in memory only")
		return b
	}
	b.dir = filepath.Join(st.dir, "buffer")
	if err := b.openDisk(); err != nil {
		b.degraded = true
		b.lastRecover = now()
		log.Error("cannot use the state directory; buffering in memory until it works", "error", err)
	}
	return b
}

func segName(n int) string { return fmt.Sprintf("seg-%08d.log", n) }

func (b *buffer) segPath(n int) string { return filepath.Join(b.dir, segName(n)) }

// openDisk scans the segments left by an earlier run: torn tails are cut off, bad records
// counted, unreadable segments set aside, acknowledged segments deleted.
func (b *buffer) openDisk() error {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return err
	}
	var nums []int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".log")); err == nil {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	saved := b.st.snapshot().Cursor
	b.nextN = 1
	if len(nums) > 0 {
		b.nextN = nums[len(nums)-1] + 1
	}
	if saved != nil {
		b.nextN = max(b.nextN, saved.Seg+1)
	}
	b.segs, b.depth, b.bytes = nil, 0, 0
	for _, n := range nums {
		if saved != nil && n < saved.Seg {
			_ = os.Remove(b.segPath(n)) // acknowledged completely
			continue
		}
		var from int64
		if saved != nil && n == saved.Seg {
			from = saved.Off
		}
		sc, err := scanSegment(b.segPath(n), from)
		if err != nil {
			b.quarantine(n, err)
			continue
		}
		b.corrupt += int64(sc.corrupt)
		if sc.corrupt > 0 {
			b.log.Warn("skipped damaged records in a buffer segment", "segment", n, "records", sc.corrupt)
		}
		if sc.torn {
			if err := os.Truncate(b.segPath(n), sc.goodSize); err != nil {
				b.quarantine(n, err)
				continue
			}
		}
		b.segs = append(b.segs, &segment{n: n, size: sc.goodSize, records: sc.valid, lastSlot: sc.lastSlot})
		b.depth += sc.valid
		b.bytes += sc.goodSize
	}
	switch {
	case len(b.segs) == 0:
		b.cur = cursor{}
	case saved != nil && saved.Seg == b.segs[0].n:
		b.cur = *saved
	default:
		b.cur = cursor{Seg: b.segs[0].n}
	}
	if len(b.segs) > 0 && b.cur.Off > b.segs[0].size {
		b.cur.Off = b.segs[0].size
	}
	if last := len(b.segs) - 1; last >= 0 && b.segs[last].size < b.cfg.SegmentBytes {
		f, err := os.OpenFile(b.segPath(b.segs[last].n), os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		b.active = f
		return nil
	}
	return b.newSegment()
}

func (b *buffer) quarantine(n int, cause error) {
	b.quarantined++
	b.log.Error("a buffer segment is unreadable and was set aside", "segment", n, "error", cause)
	_ = os.Rename(b.segPath(n), b.segPath(n)+".bad")
}

// newSegment closes the active segment and starts the next one.
func (b *buffer) newSegment() error {
	n := b.nextN
	f, err := os.OpenFile(b.segPath(n), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if b.active != nil {
		_ = b.active.Sync()
		_ = b.active.Close()
	}
	b.nextN = n + 1
	b.active = f
	b.unsynced = 0
	if len(b.segs) == 0 {
		b.cur = cursor{Seg: n}
	}
	b.segs = append(b.segs, &segment{n: n})
	return nil
}

type scan struct {
	valid, corrupt int
	lastSlot       int64
	goodSize       int64
	torn           bool
}

// scanSegment checks every line of a segment; valid counts the good records at or after from.
func scanSegment(path string, from int64) (scan, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a segment of the state directory
	if err != nil {
		return scan{}, err
	}
	var sc scan
	for pos := 0; pos < len(data); {
		nl := indexByte(data[pos:], '\n')
		if nl < 0 {
			sc.torn = true // a crash in the middle of a write
			break
		}
		end := pos + nl + 1
		if r, ok := decodeLine(data[pos : end-1]); ok {
			if int64(pos) >= from {
				sc.valid++
			}
			sc.lastSlot = max(sc.lastSlot, r.Slot)
		} else {
			sc.corrupt++
		}
		pos = end
		sc.goodSize = int64(end)
	}
	return sc, nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func encodeLine(r wire.Report) []byte {
	j, _ := json.Marshal(r)
	return fmt.Appendf(nil, "%08x %s\n", crc32.ChecksumIEEE(j), j)
}

func decodeLine(line []byte) (wire.Report, bool) {
	var r wire.Report
	if len(line) < 10 || line[8] != ' ' {
		return r, false
	}
	sum, err := strconv.ParseUint(string(line[:8]), 16, 32)
	if err != nil || uint32(sum) != crc32.ChecksumIEEE(line[9:]) {
		return r, false
	}
	return r, json.Unmarshal(line[9:], &r) == nil
}

// Append adds a finished slot. It never fails: when the disk does, the result goes to the ring.
func (b *buffer) Append(r wire.Report) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appendLocked(r)
}

func (b *buffer) appendLocked(r wire.Report) {
	b.depth++
	if b.degraded || b.active == nil || len(b.ring) > 0 { // the ring is always newer than the disk
		b.toRing(r)
		return
	}
	line := encodeLine(r)
	seg := b.segs[len(b.segs)-1]
	if _, err := b.active.Write(line); err != nil {
		b.fail(err, seg)
		b.toRing(r)
		return
	}
	seg.size += int64(len(line))
	seg.records++
	seg.lastSlot = max(seg.lastSlot, r.Slot)
	b.bytes += int64(len(line))
	b.unsynced++
	if now := b.now(); b.unsynced >= syncAfter || now.Sub(b.lastSync) >= syncEvery {
		b.syncLocked(now)
	}
	if seg.size >= b.cfg.SegmentBytes {
		if err := b.newSegment(); err != nil {
			b.fail(err, nil)
			return
		}
		b.enforceLocked()
	}
}

func (b *buffer) syncLocked(now time.Time) {
	if b.active != nil && b.unsynced > 0 {
		if err := b.active.Sync(); err != nil {
			b.fail(err, nil)
		}
	}
	b.unsynced, b.lastSync = 0, now
}

// fail switches to memory after a disk error; seg, when given, is cut back to its last good size.
func (b *buffer) fail(err error, seg *segment) {
	b.log.Error("cannot write the result buffer; buffering in memory until the disk works", "error", err)
	b.degraded = true
	b.lastRecover = b.now()
	if b.active != nil {
		if seg != nil {
			_ = b.active.Truncate(seg.size)
		}
		_ = b.active.Close()
		b.active = nil
	}
}

func (b *buffer) toRing(r wire.Report) {
	b.nextSeq++
	b.ring = append(b.ring, ringItem{seq: b.nextSeq, r: r})
	if over := len(b.ring) - b.cfg.MemoryMax; over > 0 {
		b.ring = b.ring[over:]
		b.depth -= over
		b.drops[dropMemory] += int64(over)
		b.log.Warn("the result buffer is in memory and full; the oldest results were dropped", "dropped", over)
	}
}

// Peek returns up to max of the oldest results without removing them.
func (b *buffer) Peek(max int) Batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out Batch
	idx := b.headIndex()
	off := b.cur.Off
	for idx >= 0 && idx < len(b.segs) && len(out.Reports) < max {
		seg := b.segs[idx]
		reports, end, consumed, err := b.readSegment(seg, off, max-len(out.Reports))
		if err != nil {
			b.dropSegment(idx, dropBad)
			off = 0
			if idx < len(b.segs) && b.cur.Seg == b.segs[idx].n {
				off = b.cur.Off
			}
			out = Batch{} // positions moved; read again from the new head
			idx = b.headIndex()
			continue
		}
		out.Reports = append(out.Reports, reports...)
		out.consumed += consumed
		out.lastSegConsumed += consumed
		off = end
		out.end = cursor{Seg: seg.n, Off: off}
		if off < seg.size || idx == len(b.segs)-1 {
			break
		}
		idx++
		off = 0
		out.end = cursor{Seg: b.segs[idx].n}
		out.lastSegConsumed = 0
	}
	if len(out.Reports) < max {
		for _, it := range b.ring {
			if len(out.Reports) == max {
				break
			}
			out.Reports = append(out.Reports, it.r)
			out.ringEnd = it.seq + 1
			out.ringCount++
		}
	}
	return out
}

func (b *buffer) headIndex() int {
	for i, s := range b.segs {
		if s.n == b.cur.Seg {
			return i
		}
	}
	if len(b.segs) > 0 {
		b.cur = cursor{Seg: b.segs[0].n}
		return 0
	}
	return -1
}

// readSegment reads up to max results of a segment from off; consumed counts bad lines too.
func (b *buffer) readSegment(seg *segment, off int64, max int) (out []wire.Report, end int64, consumed int, err error) {
	f, err := os.Open(b.segPath(seg.n))
	if err != nil {
		return nil, off, 0, err
	}
	defer f.Close()
	end = off
	var carry []byte
	for len(out) < max && off < seg.size {
		chunk := make([]byte, min(int64(readChunk), seg.size-off))
		n, rerr := f.ReadAt(chunk, off)
		if n == 0 && rerr != nil {
			return out, end, consumed, rerr
		}
		off += int64(n)
		carry = append(carry, chunk[:n]...)
		for len(out) < max {
			nl := indexByte(carry, '\n')
			if nl < 0 {
				break
			}
			end += int64(nl + 1)
			consumed++
			if r, ok := decodeLine(carry[:nl]); ok {
				out = append(out, r)
			} else {
				b.corrupt++
			}
			carry = carry[nl+1:]
		}
	}
	return out, end, consumed, nil
}

// Ack removes a batch the gateway has answered for. A fully read segment is deleted.
func (b *buffer) Ack(batch Batch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if batch.consumed > 0 {
		b.depth -= batch.consumed
		for len(b.segs) > 1 && b.segs[0].n < batch.end.Seg {
			b.removeSegment(0)
		}
		if len(b.segs) > 0 && b.segs[0].n == batch.end.Seg {
			b.segs[0].records = max(0, b.segs[0].records-batch.lastSegConsumed)
			b.cur = batch.end
			if len(b.segs) > 1 && b.cur.Off >= b.segs[0].size {
				b.removeSegment(0)
				b.cur = cursor{Seg: b.segs[0].n}
			}
		}
		c := b.cur
		if err := b.st.update(func(s *state) { s.Cursor = &c }); err != nil {
			// The next start may send a few results again; the gateway answers duplicate.
			b.log.Warn("cannot save the buffer position", "error", err)
		}
	}
	if batch.ringCount > 0 {
		keep := b.ring[:0]
		for _, it := range b.ring {
			if it.seq >= batch.ringEnd {
				keep = append(keep, it)
			}
		}
		b.ring = keep
		b.depth -= batch.ringCount
	}
	b.depth = max(b.depth, 0)
}

func (b *buffer) removeSegment(i int) {
	s := b.segs[i]
	_ = os.Remove(b.segPath(s.n))
	b.bytes -= s.size
	b.segs = append(b.segs[:i], b.segs[i+1:]...)
}

// dropSegment throws away a segment with its unsent results and counts them.
func (b *buffer) dropSegment(i int, reason string) {
	s := b.segs[i]
	b.drops[reason] += int64(s.records)
	b.depth -= s.records
	b.log.Warn("results were dropped from the buffer", "reason", reason, "results", s.records, "segment", s.n)
	if i == len(b.segs)-1 && b.active != nil {
		_ = b.active.Close()
		b.active = nil
	}
	if reason == dropBad {
		b.quarantined++
		_ = os.Rename(b.segPath(s.n), b.segPath(s.n)+".bad")
		b.bytes -= s.size
		b.segs = append(b.segs[:i], b.segs[i+1:]...)
	} else {
		b.removeSegment(i)
	}
	if len(b.segs) > 0 && b.cur.Seg == s.n {
		b.cur = cursor{Seg: b.segs[0].n}
	}
	if len(b.segs) == 0 {
		b.cur = cursor{}
	}
	if b.active == nil && !b.degraded {
		if err := b.newSegment(); err != nil {
			b.fail(err, nil)
		}
	}
}

// Enforce applies the size and age limits, flushes to disk and tries to leave memory mode. The
// reporter calls it between batches.
func (b *buffer) Enforce() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncLocked(b.now())
	b.enforceLocked()
	if b.degraded && b.dir != "" && b.now().Sub(b.lastRecover) >= recoverEvery {
		b.recoverLocked()
	}
}

func (b *buffer) enforceLocked() {
	if b.active == nil {
		return
	}
	for len(b.segs) > 0 && b.bytes > b.cfg.MaxBytes {
		if len(b.segs) == 1 {
			if b.newSegment() != nil {
				return
			}
		}
		b.dropSegment(0, dropSize)
	}
	cutoff := b.now().Add(-b.cfg.MaxAge).Unix()
	for len(b.segs) > 0 && b.segs[0].lastSlot > 0 && b.segs[0].lastSlot < cutoff {
		if len(b.segs) == 1 {
			if b.newSegment() != nil {
				return
			}
		}
		b.dropSegment(0, dropAge)
	}
}

// recoverLocked tries the disk again; the ring then moves to it, behind everything already there.
func (b *buffer) recoverLocked() {
	b.lastRecover = b.now()
	if err := b.openDiskKeepingRing(); err != nil {
		return
	}
	b.degraded = false
	items := b.ring
	b.ring = nil
	b.depth -= len(items)
	for _, it := range items {
		b.appendLocked(it.r)
	}
	b.log.Info("the result buffer is on disk again", "moved", len(items))
}

func (b *buffer) openDiskKeepingRing() error {
	ring, depth := b.ring, b.depth
	if err := b.openDisk(); err != nil {
		b.ring, b.depth = ring, depth
		return err
	}
	b.ring, b.depth = ring, b.depth+len(ring)
	return nil
}

// Close flushes and closes the active segment.
func (b *buffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncLocked(b.now())
	if b.active != nil {
		_ = b.active.Close()
		b.active = nil
	}
}

// Depth is the number of results waiting to be sent.
func (b *buffer) Depth() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.depth
}

// Degraded reports that results are only in memory.
func (b *buffer) Degraded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.degraded
}

// OldestSlot is the slot of the oldest waiting result, or 0.
func (b *buffer) OldestSlot() int64 {
	if p := b.Peek(1); len(p.Reports) > 0 {
		return p.Reports[0].Slot
	}
	return 0
}

// BufferStats is what the metrics need.
type BufferStats struct {
	Depth       int
	Bytes       int64
	Degraded    bool
	Drops       map[string]int64
	Corrupt     int64
	Quarantined int64
}

func (b *buffer) Stats() BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := make(map[string]int64, len(b.drops))
	for k, v := range b.drops {
		d[k] = v
	}
	return BufferStats{Depth: b.depth, Bytes: b.bytes, Degraded: b.degraded, Drops: d, Corrupt: b.corrupt, Quarantined: b.quarantined}
}
