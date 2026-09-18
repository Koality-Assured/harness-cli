package git

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ClaimLock represents an atomic cross-process lock protecting worktree claim operations.
type ClaimLock struct {
	lockFilePath string
	file         *os.File
}

// AcquireClaimLock attempts to acquire the atomic lockfile .claims.lock in scratch/worktrees.
func AcquireClaimLock(worktreesDir string, timeout time.Duration) (*ClaimLock, error) {
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktrees dir: %w", err)
	}

	lockFile := filepath.Join(worktreesDir, ".claims.lock")
	start := time.Now()
	pollInterval := 50 * time.Millisecond

	for {
		// Attempt atomic create with O_EXCL
		fd, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			return &ClaimLock{
				lockFilePath: lockFile,
				file:         fd,
			}, nil
		}

		// Check if timed out
		if time.Since(start) >= timeout {
			// Check if existing lockfile is stale (> 60 seconds old)
			if fi, statErr := os.Stat(lockFile); statErr == nil {
				if time.Since(fi.ModTime()) > 60*time.Second {
					_ = os.Remove(lockFile)
					continue
				}
			}
			return nil, fmt.Errorf("timed out after %v waiting for claim lock: %s", timeout, lockFile)
		}

		time.Sleep(pollInterval)
	}
}

// Release closes and removes the lockfile.
func (l *ClaimLock) Release() error {
	var closeErr, removeErr error
	if l.file != nil {
		closeErr = l.file.Close()
	}
	removeErr = os.Remove(l.lockFilePath)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
