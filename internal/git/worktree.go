package git

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// AddWorktree creates a git worktree under scratch/worktrees/<slug> and writes <slug>.claim.json.
func AddWorktree(primaryRoot, slug, branch string, areas []string, agent string, dryRun bool) (*Claim, error) {
	wtDir := GetWorktreesDir(primaryRoot)
	targetPath := filepath.Join(wtDir, slug)
	claimFilePath := filepath.Join(wtDir, slug+".claim.json")

	if _, err := os.Stat(targetPath); err == nil {
		return nil, fmt.Errorf("worktree directory already exists: %s", targetPath)
	}

	nowISO := time.Now().UTC().Format(time.RFC3339)
	claim := &Claim{
		Slug:         slug,
		Branch:       branch,
		Areas:        areas,
		Agent:        agent,
		CreatedAt:    nowISO,
		Path:         targetPath,
		ExistsOnDisk: true,
	}

	if dryRun {
		return claim, nil
	}

	// Acquire claim lock
	lock, err := AcquireClaimLock(wtDir, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire claim lock: %w", err)
	}
	defer func() {
		_ = lock.Release()
	}()

	// Execute git worktree add
	relTarget, err := filepath.Rel(primaryRoot, targetPath)
	if err != nil {
		relTarget = targetPath
	}

	// Check if branch already exists in repo
	branchesOut, _ := RunGit(primaryRoot, "branch", "--list", branch)
	var gitArgs []string
	if branchesOut != "" {
		gitArgs = []string{"worktree", "add", relTarget, branch}
	} else {
		gitArgs = []string{"worktree", "add", "-b", branch, relTarget}
	}

	if _, err := RunGit(primaryRoot, gitArgs...); err != nil {
		return nil, fmt.Errorf("git worktree add failed: %w", err)
	}

	// Write claim JSON
	data, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to serialize claim json: %w", err)
	}
	if err := os.WriteFile(claimFilePath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write claim file: %w", err)
	}

	return claim, nil
}

// worktreeRemovalOps keeps the destructive fallback injectable for deterministic tests.
type worktreeRemovalOps struct {
	runGit    func(cwd string, args ...string) (string, error)
	removeAll func(path string) error
	remove    func(path string) error
}

// RemoveWorktree removes a worktree and deletes its claim JSON only after removal is verified.
func RemoveWorktree(primaryRoot, slug string, force, dryRun bool) error {
	return removeWorktreeWithOps(primaryRoot, slug, force, dryRun, worktreeRemovalOps{
		runGit:    RunGit,
		removeAll: os.RemoveAll,
		remove:    os.Remove,
	})
}

func removeWorktreeWithOps(primaryRoot, slug string, force, dryRun bool, ops worktreeRemovalOps) error {
	wtDir := GetWorktreesDir(primaryRoot)
	targetPath := filepath.Join(wtDir, slug)
	claimFilePath := filepath.Join(wtDir, slug+".claim.json")

	_, statErr := os.Lstat(targetPath)
	targetExists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to inspect worktree: %w", statErr)
	}

	// Dry runs still report whether an unforced removal would be blocked.
	if dryRun {
		if targetExists && !force {
			reason, _, err := inspectWorktreeDeleteGate(targetPath, "main")
			if err != nil {
				return err
			}
			if reason != "" {
				return fmt.Errorf("%s (pass --force to override)", reason)
			}
		}
		return nil
	}

	// Acquire claim lock before inspecting and removing the worktree.
	lock, err := AcquireClaimLock(wtDir, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to acquire claim lock: %w", err)
	}
	defer func() {
		_ = lock.Release()
	}()

	// Recheck existence and inspect the deletion gate under the claim lock.
	_, statErr = os.Lstat(targetPath)
	targetExists = statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to inspect worktree: %w", statErr)
	}
	if targetExists {
		hasUntracked := false
		if !force {
			reason, untracked, err := inspectWorktreeDeleteGate(targetPath, "main")
			if err != nil {
				return err
			}
			if reason != "" {
				return fmt.Errorf("%s (pass --force to override)", reason)
			}
			hasUntracked = untracked
		}

		args := []string{"worktree", "remove", targetPath}
		// Git requires --force to remove untracked-only worktrees. This is safe
		// after the gate has confirmed there are no tracked edits or base commits.
		if force || hasUntracked {
			args = append(args, "--force")
		}
		if _, err := ops.runGit(primaryRoot, args...); err != nil {
			if !force {
				return fmt.Errorf("failed to remove git worktree: %w", err)
			}
			// The explicit --force fallback must succeed completely before its
			// claim can be removed. Keep the claim on either fallback failure.
			removeErr := ops.removeAll(targetPath)
			if removeErr != nil {
				return fmt.Errorf("forced worktree removal fallback failed after git error (%v): %w", err, removeErr)
			}
			if _, targetRemoveErr := ops.runGit(primaryRoot, "worktree", "remove", "--force", targetPath); targetRemoveErr != nil {
				if verifyErr := verifyWorktreeRemoved(primaryRoot, targetPath, ops.runGit); verifyErr != nil {
					return fmt.Errorf("forced worktree removal fallback failed after git error (%v): %w; verification: %v", err, targetRemoveErr, verifyErr)
				}
			}
		}
	} else {
		// Do not prune repository-wide: only remove this registration if Git still
		// lists the missing target. A missing path with no registration is an
		// ordinary stale claim and needs no Git mutation.
		registrations, err := ops.runGit(primaryRoot, "worktree", "list", "--porcelain")
		if err != nil {
			return fmt.Errorf("could not inspect Git worktree registrations: %w", err)
		}
		if worktreeListContainsPath(registrations, targetPath) {
			if _, err := ops.runGit(primaryRoot, "worktree", "remove", "--force", targetPath); err != nil {
				// Git may have completed the removal while returning an error. Accept
				// that only if the target path and registration are both gone.
				if verifyErr := verifyWorktreeRemoved(primaryRoot, targetPath, ops.runGit); verifyErr != nil {
					return fmt.Errorf("failed to remove missing Git worktree registration: %w; verification: %v", err, verifyErr)
				}
			}
		}
	}
	if err := verifyWorktreeRemoved(primaryRoot, targetPath, ops.runGit); err != nil {
		return err
	}

	// A claim is deleted only after the path and Git registration are both gone.
	if err := ops.remove(claimFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove claim file: %w", err)
	}
	return nil
}

func verifyWorktreeRemoved(primaryRoot, targetPath string, runGit func(cwd string, args ...string) (string, error)) error {
	if _, err := os.Lstat(targetPath); err == nil {
		return fmt.Errorf("worktree path still exists after removal: %s", targetPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("could not verify worktree path removal: %w", err)
	}

	registrations, err := runGit(primaryRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("could not verify Git worktree removal: %w", err)
	}
	if worktreeListContainsPath(registrations, targetPath) {
		return fmt.Errorf("Git still registers worktree after removal: %s", targetPath)
	}
	return nil
}

func worktreeListContainsPath(output, wantedPath string) bool {
	wanted := normalizedWorktreePath(wantedPath)
	for _, line := range strings.Split(output, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if ok && sameWorktreePath(normalizedWorktreePath(path), wanted) {
			return true
		}
	}
	return false
}

func normalizedWorktreePath(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func sameWorktreePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
