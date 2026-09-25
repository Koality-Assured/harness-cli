package git

import (
	"fmt"
	"strconv"
	"strings"
)

// worktreeDeleteBlocked returns the shared Slice 11 refusal reason, or an empty
// string when the status is safe to remove.
func worktreeDeleteBlocked(statusPorcelain string, commitsNotInBase int) string {
	validStatusCodes := " MADRCUT?!"
	for _, line := range strings.Split(statusPorcelain, "\n") {
		if strings.TrimSpace(line) == "" || len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		if !strings.ContainsRune(validStatusCodes, rune(x)) || !strings.ContainsRune(validStatusCodes, rune(y)) {
			continue
		}
		if (x == '?' && y == '?') || (x == '!' && y == '!') || (x == ' ' && y == ' ') {
			continue
		}
		return "worktree has uncommitted tracked changes"
	}
	if commitsNotInBase > 0 {
		return fmt.Sprintf("worktree has %d commit(s) not contained in the base branch", commitsNotInBase)
	}
	return ""
}

func inspectWorktreeDeleteGate(worktreePath, base string) (reason string, hasUntracked bool, err error) {
	status, err := RunGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return "", false, fmt.Errorf("failed to inspect worktree status: %w", err)
	}
	commitsRaw, err := RunGit(worktreePath, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return "", false, fmt.Errorf("failed to compare worktree with base branch %q: %w", base, err)
	}
	commitsNotInBase, err := strconv.Atoi(strings.TrimSpace(commitsRaw))
	if err != nil {
		return "", false, fmt.Errorf("git returned an invalid commit count: %q", commitsRaw)
	}
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "??") {
			hasUntracked = true
			break
		}
	}
	return worktreeDeleteBlocked(status, commitsNotInBase), hasUntracked, nil
}
