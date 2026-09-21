package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
)

func TestClaimTimestampParsingAndStaleDetection(t *testing.T) {
	// 1. Valid RFC3339 recent claim
	recent := git.Claim{
		Slug:         "active-feature",
		CreatedAt:    time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339),
		ExistsOnDisk: true,
	}
	dur, ok := recent.Age()
	if !ok {
		t.Fatalf("expected age calculation to succeed for recent claim")
	}
	if dur.Hours() < 1.9 || dur.Hours() > 2.1 {
		t.Errorf("expected ~2 hours age, got %v", dur)
	}
	stale, _ := recent.CheckStale(24.0)
	if stale {
		t.Errorf("recent claim (<2h) should not be stale with 24h threshold")
	}

	// 2. Stale claim (> 24 hours old)
	staleClaim := git.Claim{
		Slug:         "abandoned-feature",
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
		ExistsOnDisk: true,
	}
	stale, reason := staleClaim.CheckStale(24.0)
	if !stale {
		t.Errorf("claim older than 48h should be flagged stale")
	}
	if reason == "" {
		t.Errorf("expected stale reason for stale lock")
	}

	// 3. Claim missing on disk
	missingClaim := git.Claim{
		Slug:         "missing-dir",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		ExistsOnDisk: false,
	}
	stale, _ = missingClaim.CheckStale(24.0)
	if !stale {
		t.Errorf("claim with missing worktree on disk should always be stale")
	}
}

func TestLoadClaimsDualDirectories(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-claims-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create scratch/worktrees and .git/worktree-claims
	scratchDir := filepath.Join(tmpDir, "scratch", "worktrees")
	gitClaimsDir := filepath.Join(tmpDir, ".git", "worktree-claims")
	_ = os.MkdirAll(scratchDir, 0755)
	_ = os.MkdirAll(gitClaimsDir, 0755)

	// 1. Claim in scratch/worktrees
	scratchClaim := git.Claim{
		Slug:      "feat-scratch",
		Branch:    "feat/scratch",
		Areas:     []string{"routing"},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data1, _ := json.Marshal(scratchClaim)
	_ = os.WriteFile(filepath.Join(scratchDir, "feat-scratch.claim.json"), data1, 0644)
	_ = os.MkdirAll(filepath.Join(scratchDir, "feat-scratch"), 0755)

	// 2. Claim in .git/worktree-claims
	gitClaim := git.Claim{
		Slug:      "feat-git-claim",
		Branch:    "feat/git-claim",
		Areas:     []string{"scripts"},
		CreatedAt: time.Now().UTC().Add(-30 * time.Hour).Format(time.RFC3339),
	}
	data2, _ := json.Marshal(gitClaim)
	_ = os.WriteFile(filepath.Join(gitClaimsDir, "feat-git-claim.json"), data2, 0644)
	_ = os.MkdirAll(filepath.Join(scratchDir, "feat-git-claim"), 0755)

	claims := git.LoadClaims(tmpDir)
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims loaded across dual directories, got %d", len(claims))
	}

	claimMap := make(map[string]git.Claim)
	for _, c := range claims {
		claimMap[c.Slug] = c
	}

	c1, ok1 := claimMap["feat-scratch"]
	if !ok1 || c1.IsStale {
		t.Errorf("feat-scratch should exist and not be stale")
	}

	c2, ok2 := claimMap["feat-git-claim"]
	if !ok2 || !c2.IsStale {
		t.Errorf("feat-git-claim should exist and be flagged stale (> 30h old)")
	}
}
