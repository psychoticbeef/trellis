package store

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

// memLocks backs LockProject for in-memory stores, where no lock file can
// exist. Keyed globally: several :memory: stores in one process (tests)
// still serialize per project id.
var (
	memLocksMu sync.Mutex
	memLocks   = map[string]*sync.Mutex{}
)

// LockProject acquires the exclusive cross-process mutation lock for one
// project and returns the unlock func. File-backed stores use flock on a
// sibling lock file, so every process mutating the same database serializes;
// in-memory stores fall back to an in-process mutex.
func (s *Store) LockProject(projectID string) (func(), error) {
	if s.path == "" || s.path == ":memory:" {
		memLocksMu.Lock()
		mu, ok := memLocks[projectID]
		if !ok {
			mu = &sync.Mutex{}
			memLocks[projectID] = mu
		}
		memLocksMu.Unlock()
		mu.Lock()
		return mu.Unlock, nil
	}
	f, err := os.OpenFile(fmt.Sprintf("%s.%s.lock", s.path, projectID), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("project lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("project lock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
