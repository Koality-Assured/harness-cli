package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeDeleteBlockedSlice11Cases(t *testing.T) {
	trackedReason := "worktree has uncommitted tracked changes"
	cases := []struct {
		name, status string
		commits      int
		want         string
	}{
		{name: "empty"},
		{name: "untracked only", status: "?? untracked.txt\n"},
		{name: "ignored only", status: "!! ignored.txt\n"},
		{name: "unrelated output", status: "main\n"},
		{name: "unstaged tracked change", status: " M tracked.txt\n", want: trackedReason},
		{name: "staged tracked change", status: "M  tracked.txt\n", want: trackedReason},
		{name: "unmerged commits", commits: 3, want: "worktree has 3 commit(s) not contained in the base branch"},
		{name: "tracked change takes precedence", status: " M tracked.txt\n", commits: 3, want: trackedReason},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := worktreeDeleteBlocked(tc.status, tc.commits); got != tc.want {
				t.Fatalf("worktreeDeleteBlocked(%q, %d) = %q, want %q", tc.status, tc.commits, got, tc.want)
			}
		})
	}
}

func TestRemoveWorktreeDeleteGateAndForce(t *testing.T) {
	root := setupDeleteGateRepo(t)

	// An untracked-only worktree is allowed by the application gate. Git still
	// needs --force for this case, which RemoveWorktree supplies after inspection.
	untrackedPath := addDeleteGateWorktree(t, root, "untracked-only", "slice16-untracked")
	if err := os.WriteFile(filepath.Join(untrackedPath, "extra.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(root, "untracked-only", false, false); err != nil {
		t.Fatalf("untracked-only worktree should be removed: %v", err)
	}
	if _, err := os.Stat(untrackedPath); !os.IsNotExist(err) {
		t.Fatalf("untracked-only worktree still exists (stat error %v)", err)
	}

	// A tracked edit is refused unless the caller explicitly forces removal.
	trackedPath := addDeleteGateWorktree(t, root, "tracked-edit", "slice16-tracked")
	if err := os.WriteFile(filepath.Join(trackedPath, "tracked.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(root, "tracked-edit", false, false); err == nil || err.Error() != "worktree has uncommitted tracked changes (pass --force to override)" {
		t.Fatalf("tracked edit removal error = %v, want gate refusal", err)
	}
	if _, err := os.Stat(trackedPath); err != nil {
		t.Fatalf("refused worktree should remain: %v", err)
	}
	if err := RemoveWorktree(root, "tracked-edit", true, false); err != nil {
		t.Fatalf("forced tracked edit removal failed: %v", err)
	}

	// A clean worktree with a commit ahead of main is also refused, then force
	// removes it as the Slice 11 override specifies.
	commitPath := addDeleteGateWorktree(t, root, "ahead-commit", "slice16-ahead")
	if err := os.WriteFile(filepath.Join(commitPath, "tracked.txt"), []byte("ahead\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, commitPath, "add", "tracked.txt")
	gitTest(t, commitPath, "commit", "-m", "feat: add ahead commit")
	if err := RemoveWorktree(root, "ahead-commit", false, false); err == nil || err.Error() != "worktree has 1 commit(s) not contained in the base branch (pass --force to override)" {
		t.Fatalf("ahead commit removal error = %v, want gate refusal", err)
	}
	if _, err := os.Stat(commitPath); err != nil {
		t.Fatalf("refused worktree should remain: %v", err)
	}
	if err := RemoveWorktree(root, "ahead-commit", true, false); err != nil {
		t.Fatalf("forced ahead-commit removal failed: %v", err)
	}
}

func TestRemoveWorktreeBlocksAheadCommitWhenBaseIsMissing(t *testing.T) {
	root := setupDeleteGateRepo(t)
	worktreePath := addDeleteGateWorktree(t, root, "missing-base", "slice16-missing-base")
	if err := os.WriteFile(filepath.Join(worktreePath, "tracked.txt"), []byte("ahead\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktreePath, "add", "tracked.txt")
	gitTest(t, worktreePath, "commit", "-m", "feat: add ahead commit")
	gitTest(t, root, "switch", "-c", "test-primary")
	gitTest(t, root, "branch", "-D", "main")

	err := RemoveWorktree(root, "missing-base", false, false)
	if err == nil || !strings.Contains(err.Error(), `base branch "main"`) {
		t.Fatalf("missing base error = %v, want clear base-resolution failure", err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("ahead worktree must remain when its base cannot be resolved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(GetWorktreesDir(root), "missing-base.claim.json")); err != nil {
		t.Fatalf("claim must remain when its base cannot be resolved: %v", err)
	}
}

func TestRemoveWorktreeMissingPathDoesNotPruneUnrelatedRegistrations(t *testing.T) {
	root := setupDeleteGateRepo(t)
	worktreePath := addDeleteGateWorktree(t, root, "missing-path", "slice16-missing-path")
	claimPath := filepath.Join(GetWorktreesDir(root), "missing-path.claim.json")
	unrelatedPath := addDeleteGateWorktree(t, root, "unrelated-missing", "slice16-unrelated-missing")
	unrelatedClaimPath := filepath.Join(GetWorktreesDir(root), "unrelated-missing.claim.json")
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatalf("remove target worktree directory: %v", err)
	}
	if err := os.RemoveAll(unrelatedPath); err != nil {
		t.Fatalf("remove unrelated worktree directory: %v", err)
	}

	registrations, err := RunGit(root, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("list worktrees before target removal: %v", err)
	}
	if !worktreeListContainsPathForTest(t, registrations, worktreePath) || !worktreeListContainsPathForTest(t, registrations, unrelatedPath) {
		t.Fatalf("expected Git to retain registrations for both missing paths, got:\n%s", registrations)
	}

	globalPruneCalled := false
	runGit := func(cwd string, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "worktree" && args[1] == "prune" {
			globalPruneCalled = true
			return "", errors.New("repository-wide prune must not run")
		}
		return RunGit(cwd, args...)
	}
	err = removeWorktreeWithOps(root, "missing-path", false, false, worktreeRemovalOps{
		runGit: runGit, removeAll: os.RemoveAll, remove: os.Remove,
	})
	if globalPruneCalled {
		t.Fatal("cleanup invoked repository-wide Git worktree prune")
	}
	registrations, listErr := RunGit(root, "worktree", "list", "--porcelain")
	if listErr != nil {
		t.Fatalf("list worktrees after target cleanup: %v", listErr)
	}
	targetStillRegistered := worktreeListContainsPathForTest(t, registrations, worktreePath)
	t.Logf("target-specific removal left registration=%t; cleanup error=%v", targetStillRegistered, err)
	if targetStillRegistered {
		if err == nil {
			t.Fatal("cleanup succeeded while Git still registers the missing target")
		}
		if _, statErr := os.Stat(claimPath); statErr != nil {
			t.Fatalf("claim must remain when target registration remains: %v", statErr)
		}
	} else {
		if err != nil {
			t.Fatalf("cleanup returned error after target registration disappeared: %v", err)
		}
		if _, statErr := os.Stat(claimPath); !os.IsNotExist(statErr) {
			t.Fatalf("claim was not removed after target registration disappeared: %v", statErr)
		}
	}
	if !worktreeListContainsPathForTest(t, registrations, unrelatedPath) {
		t.Fatalf("cleanup changed the unrelated missing worktree registration:\n%s", registrations)
	}
	if _, statErr := os.Stat(unrelatedClaimPath); statErr != nil {
		t.Fatalf("cleanup changed the unrelated worktree claim: %v", statErr)
	}
}

func TestWorktreeListContainsPathResolvesSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real-root")
	aliasRoot := filepath.Join(root, "alias-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	const slug = "missing-worktree"
	physicalPath := filepath.Join(realRoot, slug)
	symlinkedPath := filepath.Join(aliasRoot, slug)
	if err := os.RemoveAll(physicalPath); err != nil {
		t.Fatal(err)
	}

	gitOutput := "worktree " + symlinkedPath + "\n"
	if !worktreeListContainsPathForTest(t, gitOutput, physicalPath) {
		t.Fatalf("Git path %q did not match missing worktree path through symlink ancestor %q", symlinkedPath, physicalPath)
	}
	gitOutput = "worktree " + physicalPath + "\n"
	if !worktreeListContainsPathForTest(t, gitOutput, symlinkedPath) {
		t.Fatalf("physical Git path %q did not match missing worktree path through symlink ancestor %q", physicalPath, symlinkedPath)
	}
}

func TestWorktreeListContainsPathFailsClosedForDanglingSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	resolvedRoot := filepath.Join(root, "resolved-root")
	danglingRoot := filepath.Join(root, "dangling-root")
	if err := os.Symlink(resolvedRoot, danglingRoot); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	const slug = "missing-worktree"
	registrationAtResolvedTarget := filepath.Join(resolvedRoot, slug)
	wantedThroughDanglingAncestor := filepath.Join(danglingRoot, slug)
	contains, err := worktreeListContainsPath("worktree "+registrationAtResolvedTarget+"\n", wantedThroughDanglingAncestor)
	if err == nil {
		t.Fatalf("path comparison = (%t, nil), want a resolution error for dangling symlink ancestor", contains)
	}
	if contains {
		t.Fatalf("path comparison reported a match despite unresolved path identity")
	}
}

func TestVerifyWorktreeRemovedFailsClosedForDanglingSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	resolvedRoot := filepath.Join(root, "resolved-root")
	danglingRoot := filepath.Join(root, "dangling-root")
	if err := os.Symlink(resolvedRoot, danglingRoot); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	targetPath := filepath.Join(danglingRoot, "missing-worktree")
	registeredAtResolvedTarget := filepath.Join(resolvedRoot, "missing-worktree")
	runGit := func(cwd string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" {
			return "worktree " + registeredAtResolvedTarget + "\n", nil
		}
		return "", fmt.Errorf("unexpected Git command in verify fail-closed test: %v", args)
	}

	err := verifyWorktreeRemoved(root, targetPath, runGit)
	if err == nil || !strings.Contains(err.Error(), "could not verify Git worktree path identity") {
		t.Fatalf("verification error = %v, want fail-closed path identity error", err)
	}
}

func TestRemoveWorktreeDanglingRegistrationAliasPreservesClaim(t *testing.T) {
	root := setupDeleteGateRepo(t)
	worktreePath := filepath.Join(GetWorktreesDir(root), "dangling-target")
	claimPath := filepath.Join(GetWorktreesDir(root), "dangling-target.claim.json")
	if err := os.WriteFile(claimPath, []byte(`{"slug":"dangling-target"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registrationAlias := filepath.Join(GetWorktreesDir(root), "dangling-alias")
	if err := os.Symlink(worktreePath, registrationAlias); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	runGit := func(cwd string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" {
			// The alias is dangling, but its intended physical destination is the
			// requested worktree path. Include the physical spelling too: cleanup
			// must not delete the claim while either path identity is unresolved.
			return "worktree " + registrationAlias + "\nworktree " + worktreePath + "\n", nil
		}
		return "", fmt.Errorf("unexpected Git command in fail-closed test: %v", args)
	}

	err := removeWorktreeWithOps(root, "dangling-target", false, false, worktreeRemovalOps{
		runGit: runGit, removeAll: os.RemoveAll, remove: os.Remove,
	})
	if err == nil || !strings.Contains(err.Error(), "could not compare Git worktree registrations") {
		t.Fatalf("cleanup error = %v, want fail-closed path identity error", err)
	}
	if _, err := os.Stat(claimPath); err != nil {
		t.Fatalf("claim must remain when a Git registration path cannot be resolved: %v", err)
	}
}

func worktreeListContainsPathForTest(t *testing.T, output, wantedPath string) bool {
	t.Helper()
	contains, err := worktreeListContainsPath(output, wantedPath)
	if err != nil {
		t.Fatalf("worktree path comparison failed: %v", err)
	}
	return contains
}

func TestForcedRemovalFallbackFailuresPreserveClaim(t *testing.T) {
	tests := []struct {
		name            string
		removeAll       func(string) error
		listErr         error
		leaveRegistered bool
		wantError       string
	}{
		{name: "remove all fails", removeAll: func(string) error { return os.ErrPermission }, wantError: "permission denied"},
		{name: "target-specific remove fails", removeAll: os.RemoveAll, wantError: "still registers worktree"},
		{name: "verification command fails", removeAll: os.RemoveAll, listErr: errors.New("injected list failure"), wantError: "could not verify Git worktree removal"},
		{name: "registration remains", removeAll: os.RemoveAll, leaveRegistered: true, wantError: "still registers worktree"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := setupDeleteGateRepo(t)
			worktreePath := addDeleteGateWorktree(t, root, "fallback", "slice16-fallback")
			claimPath := filepath.Join(GetWorktreesDir(root), "fallback.claim.json")
			gitRemoveErr := errors.New("injected git worktree remove failure")
			globalPruneCalled := false
			runGit := func(cwd string, args ...string) (string, error) {
				if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
					return "", gitRemoveErr
				}
				if len(args) >= 2 && args[0] == "worktree" && args[1] == "prune" {
					globalPruneCalled = true
					return "", errors.New("repository-wide prune must not run")
				}
				if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" {
					if tc.listErr != nil {
						return "", tc.listErr
					}
					if tc.leaveRegistered {
						return fmt.Sprintf("worktree %s\n", worktreePath), nil
					}
				}
				return RunGit(cwd, args...)
			}
			err := removeWorktreeWithOps(root, "fallback", true, false, worktreeRemovalOps{
				runGit: runGit, removeAll: tc.removeAll, remove: os.Remove,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("forced fallback error = %v, want text containing %q", err, tc.wantError)
			}
			if globalPruneCalled {
				t.Fatal("forced fallback invoked repository-wide Git worktree prune")
			}
			if _, err := os.Stat(claimPath); err != nil {
				t.Fatalf("claim must be preserved on fallback failure: %v", err)
			}
		})
	}
}

func setupDeleteGateRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "--initial-branch=main")
	gitTest(t, root, "config", "user.name", "Harness Test")
	gitTest(t, root, "config", "user.email", "harness-test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/scratch/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", ".gitignore", "tracked.txt")
	gitTest(t, root, "commit", "-m", "chore: initialize test repository")
	if err := os.MkdirAll(GetWorktreesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func addDeleteGateWorktree(t *testing.T, root, slug, branch string) string {
	t.Helper()
	claim, err := AddWorktree(root, slug, branch, nil, "test-agent", false)
	if err != nil {
		t.Fatalf("AddWorktree(%q) failed: %v", slug, err)
	}
	return claim.Path
}

func gitTest(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	output, err := RunGit(cwd, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return output
}
