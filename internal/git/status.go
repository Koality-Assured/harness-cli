package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	SlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	ConventionalCommitPattern = regexp.MustCompile(
		`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-zA-Z0-9_\-\./]+\))?(!)?:\s*.+`,
	)
)

// RunGit executes a git command in the specified directory and returns stdout as string.
func RunGit(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("git %s failed: %w (stderr: %s)", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// GetPrimaryRepoRoot resolves the primary repository root where .git and scratch/worktrees live.
func GetPrimaryRepoRoot(cwd string) (string, error) {
	targetCwd := cwd
	if targetCwd == "" {
		var err error
		targetCwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	targetCwd = filepath.Clean(targetCwd)

	// Fast path: check if targetCwd/.git exists
	gitEntry := filepath.Join(targetCwd, ".git")
	if fi, err := os.Stat(gitEntry); err == nil {
		if fi.IsDir() {
			return targetCwd, nil
		}
		// It's a worktree pointer file
		if data, err := os.ReadFile(gitEntry); err == nil {
			line := strings.TrimSpace(string(data))
			if strings.HasPrefix(line, "gitdir:") {
				gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(targetCwd, gitDir)
				}
				commonFile := filepath.Join(gitDir, "commondir")
				if cdata, err := os.ReadFile(commonFile); err == nil {
					relCommon := strings.TrimSpace(string(cdata))
					commonGit := filepath.Clean(filepath.Join(gitDir, relCommon))
					return filepath.Dir(commonGit), nil
				}
			}
		}
	}

	// Try git rev-parse --git-common-dir
	commonDir, err := RunGit(targetCwd, "rev-parse", "--git-common-dir")
	if err == nil && commonDir != "" {
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(targetCwd, commonDir)
		}
		commonDir = filepath.Clean(commonDir)
		return filepath.Dir(commonDir), nil
	}

	// Fallback to show-toplevel
	topLevel, err := RunGit(targetCwd, "rev-parse", "--show-toplevel")
	if err == nil && topLevel != "" {
		return filepath.Clean(topLevel), nil
	}

	return targetCwd, nil
}

// GetCheckoutRoot returns the current working tree top-level checkout directory.
func GetCheckoutRoot(cwd string) (string, error) {
	targetCwd := cwd
	if targetCwd == "" {
		var err error
		targetCwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	targetCwd = filepath.Clean(targetCwd)

	// Fast path: check if targetCwd/.git exists
	gitEntry := filepath.Join(targetCwd, ".git")
	if _, err := os.Stat(gitEntry); err == nil {
		return targetCwd, nil
	}

	topLevel, err := RunGit(targetCwd, "rev-parse", "--show-toplevel")
	if err == nil && topLevel != "" {
		return filepath.Clean(topLevel), nil
	}
	return targetCwd, nil
}

// GetCurrentBranch returns the active git branch or detached commit short hash.
func GetCurrentBranch(checkoutRoot string) string {
	// Fast path: read .git/HEAD directly
	gitEntry := filepath.Join(checkoutRoot, ".git")
	headPath := filepath.Join(gitEntry, "HEAD")

	if fi, err := os.Stat(gitEntry); err == nil && !fi.IsDir() {
		// Worktree pointer file
		if data, err := os.ReadFile(gitEntry); err == nil {
			line := strings.TrimSpace(string(data))
			if strings.HasPrefix(line, "gitdir:") {
				gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(checkoutRoot, gitDir)
				}
				headPath = filepath.Join(gitDir, "HEAD")
			}
		}
	}

	if data, err := os.ReadFile(headPath); err == nil {
		head := strings.TrimSpace(string(data))
		if strings.HasPrefix(head, "ref: refs/heads/") {
			return strings.TrimPrefix(head, "ref: refs/heads/")
		}
		if len(head) >= 7 {
			return fmt.Sprintf("detached-at-%s", head[:7])
		}
	}

	branch, err := RunGit(checkoutRoot, "branch", "--show-current")
	if err == nil && branch != "" {
		return branch
	}
	// Check detached HEAD
	headCommit, err := RunGit(checkoutRoot, "rev-parse", "--short", "HEAD")
	if err == nil && headCommit != "" {
		return fmt.Sprintf("detached-at-%s", headCommit)
	}
	return "unknown"
}

// GetStatusPorcelain returns the porcelain status lines and cleanliness flag.
func GetStatusPorcelain(checkoutRoot string) ([]string, bool) {
	out, err := RunGit(checkoutRoot, "status", "--porcelain")
	if err != nil || out == "" {
		return []string{}, err == nil
	}
	lines := strings.Split(out, "\n")
	var result []string
	for _, l := range lines {
		trimmed := strings.TrimRight(l, "\r\n")
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result, len(result) == 0
}

// IsConventionalCommit checks if the given commit subject conforms to Conventional Commits.
func IsConventionalCommit(subject string) bool {
	return ConventionalCommitPattern.MatchString(strings.TrimSpace(subject))
}
