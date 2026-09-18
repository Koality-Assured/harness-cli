package git

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// RemoveWorktree prunes a worktree and deletes its claim JSON.
func RemoveWorktree(primaryRoot, slug string, force, dryRun bool) error {
	wtDir := GetWorktreesDir(primaryRoot)
	targetPath := filepath.Join(wtDir, slug)
	claimFilePath := filepath.Join(wtDir, slug+".claim.json")

	if dryRun {
		return nil
	}

	// Acquire claim lock
	lock, err := AcquireClaimLock(wtDir, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to acquire claim lock: %w", err)
	}
	defer func() {
		_ = lock.Release()
	}()

	// Run git worktree remove if directory exists
	if _, err := os.Stat(targetPath); err == nil {
		args := []string{"worktree", "remove", targetPath}
		if force {
			args = append(args, "--force")
		}
		if _, err := RunGit(primaryRoot, args...); err != nil {
			// If git fails but force is set, remove folder manually
			if force {
				_ = os.RemoveAll(targetPath)
				_, _ = RunGit(primaryRoot, "worktree", "prune")
			} else {
				return fmt.Errorf("failed to remove git worktree: %w", err)
			}
		}
	}

	// Delete claim file
	if err := os.Remove(claimFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove claim file: %w", err)
	}

	return nil
}
