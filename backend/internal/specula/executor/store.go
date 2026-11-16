package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	scheduleFile = "schedule.json"
	stateFile    = "state.json"
)

// state is what survives a restart besides the cached schedule and the buffer.
type state struct {
	// ClockOffsetNanos is the server's time minus the local time at the last observation.
	ClockOffsetNanos int64 `json:"clockOffsetNanos"`
	// ScheduleValidUntil extends the cached schedule's validUntil after a 304.
	ScheduleValidUntil *time.Time `json:"scheduleValidUntil,omitempty"`
	// InstanceID identifies this executor across restarts; the gateway counts distinct ids per
	// token to notice two executors that share one.
	InstanceID string `json:"instanceId,omitempty"`
	// Cursor is the oldest result of the buffer that was not acknowledged.
	Cursor *cursor `json:"cursor,omitempty"`
}

// store keeps the executor's files in a state directory. An empty directory means memory only:
// every operation then succeeds without writing. A failing directory never stops the executor.
type store struct {
	dir string
	mu  sync.Mutex
	st  state
}

func newStore(dir string) *store { return &store{dir: dir} }

func (s *store) enabled() bool { return s.dir != "" }

// readFile reads a file of the state directory; a missing file is (nil, nil).
func (s *store) readFile(name string) ([]byte, error) {
	if !s.enabled() {
		return nil, nil
	}
	b, err := os.ReadFile(filepath.Join(s.dir, name)) //nolint:gosec // name is a constant of this package
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// writeAtomic writes the file so that a reader sees the old or the new content, never a part.
func (s *store) writeAtomic(name string, data []byte) error {
	if !s.enabled() {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, name+".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(s.dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if d, err := os.Open(s.dir); err == nil { // make the rename itself durable
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// load reads state.json; an unreadable file is treated as empty (the executor then starts
// from the server's clock and the schedule it fetches).
func (s *store) load() error {
	b, err := s.readFile(stateFile)
	if err != nil || b == nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.Unmarshal(b, &s.st); err != nil {
		s.st = state{}
		return fmt.Errorf("state.json is unreadable: %w", err)
	}
	return nil
}

func (s *store) snapshot() state {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// update changes the state and writes it.
func (s *store) update(fn func(*state)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
	b, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	return s.writeAtomic(stateFile, b)
}
