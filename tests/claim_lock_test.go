package tests

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
)

func TestClaimLockMutualExclusion(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-lock-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	worktreesDir := filepath.Join(tmpDir, "worktrees")
	concurrency := 10
	var wg sync.WaitGroup
	var activeHolders int32
	var maxObservedHolders int32
	var successCount int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			lock, err := git.AcquireClaimLock(worktreesDir, 5*time.Second)
			if err != nil {
				t.Errorf("AcquireClaimLock failed: %v", err)
				return
			}
			defer func() {
				_ = lock.Release()
			}()

			current := atomic.AddInt32(&activeHolders, 1)
			for {
				old := atomic.LoadInt32(&maxObservedHolders)
				if current <= old || atomic.CompareAndSwapInt32(&maxObservedHolders, old, current) {
					break
				}
			}

			// Simulate work inside critical section
			time.Sleep(20 * time.Millisecond)

			atomic.AddInt32(&activeHolders, -1)
			atomic.AddInt32(&successCount, 1)
		}()
	}

	wg.Wait()

	if maxObservedHolders > 1 {
		t.Errorf("mutual exclusion violated: maximum concurrent lock holders observed was %d, expected 1", maxObservedHolders)
	}
	if successCount != int32(concurrency) {
		t.Errorf("expected %d successful acquisitions, got %d", concurrency, successCount)
	}
}
