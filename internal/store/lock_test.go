package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestLockProject_UT_28 proves UT-28 (DD-28 "Lock plumbing"): mutual
// exclusion across two store handles on one file, independence of project
// ids, the in-memory fallback, and release for the next waiter.
func TestLockProject_UT_28(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	race := func(s1, s2 *Store, p1, p2 string) (overlap bool) {
		var mu sync.Mutex
		var inside int
		var maxInside int
		var wg sync.WaitGroup
		for _, pair := range []struct {
			s *Store
			p string
		}{{s1, p1}, {s2, p2}} {
			wg.Add(1)
			go func(s *Store, p string) {
				defer wg.Done()
				unlock, err := s.LockProject(p)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				unlock()
			}(pair.s, pair.p)
		}
		wg.Wait()
		return maxInside > 1
	}

	// Same project across two handles: serialized.
	if race(a, b, "p1", "p1") {
		t.Fatal("same-project locks overlapped across store handles")
	}
	// Different projects: independent (may overlap).
	if !race(a, b, "p1", "p2") {
		t.Fatal("different projects must not block each other")
	}

	// In-memory fallback serializes too.
	m1, _ := Open(":memory:")
	defer m1.Close()
	m2, _ := Open(":memory:")
	defer m2.Close()
	if race(m1, m2, "px", "px") {
		t.Fatal("in-memory fallback did not serialize")
	}

	// Unlock releases for the next waiter (would deadlock otherwise).
	unlock, err := a.LockProject("p3")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock2, err := b.LockProject("p3")
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
}
