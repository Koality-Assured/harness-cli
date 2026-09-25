package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
)

func TestCleanAutoTargetSelection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-clean-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	wtDir := filepath.Join(tmpDir, "scratch", "worktrees")
	_ = os.MkdirAll(wtDir, 0755)

	// Create 3 claims:
	// 1: merged into main
	// 2: stale (missing on disk)
	// 3: active and not merged (should be preserved!)

	_ = os.MkdirAll(filepath.Join(wtDir, "merged-feature"), 0755)
	cMerged := git.Claim{
		Slug:      "merged-feature",
		Branch:    "feat/merged",
		CreatedAt: time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
	}
	d1, _ := json.Marshal(cMerged)
	_ = os.WriteFile(filepath.Join(wtDir, "merged-feature.claim.json"), d1, 0644)

	cStale := git.Claim{
		Slug:      "stale-missing",
		Branch:    "feat/stale",
		CreatedAt: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
	}
	d2, _ := json.Marshal(cStale)
	_ = os.WriteFile(filepath.Join(wtDir, "stale-missing.claim.json"), d2, 0644)

	_ = os.MkdirAll(filepath.Join(wtDir, "active-feature"), 0755)
	cActive := git.Claim{
		Slug:      "active-feature",
		Branch:    "feat/active",
		CreatedAt: time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339),
	}
	d3, _ := json.Marshal(cActive)
	_ = os.WriteFile(filepath.Join(wtDir, "active-feature.claim.json"), d3, 0644)

	claims := git.LoadClaims(tmpDir)
	if len(claims) != 3 {
		t.Fatalf("expected 3 loaded claims, got %d", len(claims))
	}

	mergedMap := map[string]bool{
		"feat/merged": true,
	}

	autoTargets := make([]string, 0)
	staleThreshold := 24.0
	for _, c := range claims {
		isMerged := mergedMap[c.Branch]
		isStale := !c.ExistsOnDisk
		if dur, ok := c.Age(); ok && dur.Hours() >= staleThreshold {
			isStale = true
		}
		if isMerged || isStale {
			autoTargets = append(autoTargets, c.Slug)
		}
	}

	if len(autoTargets) != 2 {
		t.Errorf("expected 2 auto-clean targets (merged & stale), got %d: %v", len(autoTargets), autoTargets)
	}

	hasMerged := false
	hasStale := false
	for _, tgt := range autoTargets {
		if tgt == "merged-feature" {
			hasMerged = true
		}
		if tgt == "stale-missing" {
			hasStale = true
		}
		if tgt == "active-feature" {
			t.Errorf("active-feature must NOT be marked for auto-cleaning!")
		}
	}

	if !hasMerged || !hasStale {
		t.Errorf("missing expected targets: merged=%v, stale=%v", hasMerged, hasStale)
	}
}

func TestCleanCommandReturnsErrorForBlockedWorktreeAndForceOverrides(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.name", "Harness Test"},
		{"config", "user.email", "harness-test@example.invalid"},
	} {
		if _, err := git.RunGit(root, args...); err != nil {
			t.Fatalf("git %v failed: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/scratch/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunGit(root, "add", ".gitignore", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunGit(root, "commit", "-m", "chore: initialize test repository"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(git.GetWorktreesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	claim, err := git.AddWorktree(root, "command-gate", "slice16-command-gate", nil, "test-agent", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claim.Path, "tracked.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldCwd)
	})
	oldCleanSlug, oldForce := cleanSlug, Force
	oldMerged, oldStale, oldAll, oldAuto, oldStaleHours := cleanMerged, cleanStale, cleanAll, cleanAuto, cleanStaleHours
	oldDryRun, oldHarness := DryRun, HarnessArg
	t.Cleanup(func() {
		cleanSlug, Force = oldCleanSlug, oldForce
		cleanMerged, cleanStale, cleanAll, cleanAuto, cleanStaleHours = oldMerged, oldStale, oldAll, oldAuto, oldStaleHours
		DryRun, HarnessArg = oldDryRun, oldHarness
	})
	cleanSlug, Force = "command-gate", false
	cleanMerged, cleanStale, cleanAll, cleanAuto, cleanStaleHours = false, false, false, false, 0
	DryRun, HarnessArg = false, ""

	if err := cleanCmd.RunE(cleanCmd, nil); err == nil {
		t.Fatal("clean should return an error when the deletion gate blocks a worktree")
	}
	if _, err := os.Stat(claim.Path); err != nil {
		t.Fatalf("blocked worktree should remain: %v", err)
	}

	Force = true
	if err := cleanCmd.RunE(cleanCmd, nil); err != nil {
		t.Fatalf("forced clean failed: %v", err)
	}
	if _, err := os.Stat(claim.Path); !os.IsNotExist(err) {
		t.Fatalf("forced worktree should be removed (stat error %v)", err)
	}
}
