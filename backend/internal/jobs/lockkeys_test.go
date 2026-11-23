package jobs

import "testing"

// Every housekeeping task and every other advisory lock of the project has its own key: a shared
// key makes two tasks skip each other.
func TestLockKeysAreUnique(t *testing.T) {
	tasks := append(DefaultTasks(nil, nil, nil), StatusPageTask(nil, nil), CheckStatusPageDomains(StatusDomainsConfig{}, nil))
	seen := map[int64]string{
		// Locks of other packages, copied here because they are unexported: auth bootstrap admin,
		// database bootstrap, test database setup.
		0x7072_6f72_6574_6161: "auth bootstrap admin",
		0x7072_6f72_6574_6162: "database bootstrap",
		0x7072_6f72_6574_6174: "test setup",
	}
	for _, task := range tasks {
		if task.LockKey == 0 {
			t.Errorf("task %s has no lock key", task.Name)
		}
		if other, dup := seen[task.LockKey]; dup {
			t.Errorf("lock key %#x is used by %s and %s", task.LockKey, other, task.Name)
		}
		seen[task.LockKey] = task.Name
	}
}
