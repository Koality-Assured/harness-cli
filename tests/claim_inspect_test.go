package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/commands"
	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
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

	// 4. Claim with TTL lease expiry in past
	expiredLeaseClaim := git.Claim{
		Slug:         "expired-lease",
		CreatedAt:    time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339),
		ExpiresAt:    time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
		ExistsOnDisk: true,
	}
	stale, reason = expiredLeaseClaim.CheckStale(24.0)
	if !stale || !strings.Contains(reason, "TTL expired") {
		t.Errorf("claim with past expires_at should be stale with TTL expired reason, got %v (%s)", stale, reason)
	}

	// 5. Claim with TTL lease in future
	futureLeaseClaim := git.Claim{
		Slug:         "future-lease",
		CreatedAt:    time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339),
		ExpiresAt:    time.Now().UTC().Add(5 * time.Hour).Format(time.RFC3339),
		ExistsOnDisk: true,
	}
	stale, _ = futureLeaseClaim.CheckStale(24.0)
	if stale {
		t.Errorf("claim with future expires_at should not be stale")
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

func TestClaimWatcherLoopAndCancellation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-watcher-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "config.json")
	reg := registry.NewHarnessRegistry(cfgPath)

	fakeRepo := filepath.Join(tmpDir, "test-repo")
	_ = os.MkdirAll(filepath.Join(fakeRepo, ".git"), 0755)
	wtDir := filepath.Join(fakeRepo, "scratch", "worktrees")
	_ = os.MkdirAll(filepath.Join(wtDir, "stale-worker"), 0755)

	staleClaim := git.Claim{
		Slug:      "stale-worker",
		Branch:    "feat/stale",
		Areas:     []string{"core"},
		Agent:     "test-agent",
		CreatedAt: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
	}
	claimData, _ := json.Marshal(staleClaim)
	_ = os.WriteFile(filepath.Join(wtDir, "stale-worker.claim.json"), claimData, 0644)

	_, err = reg.Register(fakeRepo, "Test Repo", "Test Domain", true, false)
	if err != nil {
		t.Fatalf("failed to register repo: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var outBuf bytes.Buffer

	doneChan := make(chan error, 1)
	go func() {
		doneChan <- commands.RunClaimWatcher(ctx, 20*time.Millisecond, 24.0, reg, &outBuf)
	}()

	// Allow initial check and at least one tick
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-doneChan:
		if err != nil && err != context.Canceled {
			t.Errorf("unexpected error from RunClaimWatcher: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RunClaimWatcher failed to stop after context cancellation")
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "[WARNING] Stale worktree claim detected") {
		t.Errorf("expected stale claim warning in watcher output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "stale-worker") {
		t.Errorf("expected slug 'stale-worker' in watcher output, got: %s", outStr)
	}
}

func TestClaimWatcherAutoClean(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-claim-autoclean-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "harness.config.json")
	reg := registry.NewHarnessRegistry(configPath)

	fakeRepo := filepath.Join(tmpDir, "domain-repo")
	_ = os.MkdirAll(filepath.Join(fakeRepo, ".git"), 0755)
	wtDir := filepath.Join(fakeRepo, "scratch", "worktrees")
	_ = os.MkdirAll(wtDir, 0755)

	// Write stale claim
	staleClaim := git.Claim{
		Slug:         "orphaned-tree",
		Branch:       "feat/orphaned",
		Areas:        []string{"core"},
		Agent:        "test-agent",
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
		ExistsOnDisk: false,
	}
	claimData, _ := json.Marshal(staleClaim)
	claimFile := filepath.Join(wtDir, "orphaned-tree.claim.json")
	_ = os.WriteFile(claimFile, claimData, 0644)

	_, err = reg.Register(fakeRepo, "AutoClean Repo", "Test Domain", true, false)
	if err != nil {
		t.Fatalf("failed to register repo: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var outBuf bytes.Buffer

	doneChan := make(chan error, 1)
	go func() {
		// Run with autoClean=true
		doneChan <- commands.RunClaimWatcher(ctx, 20*time.Millisecond, 24.0, reg, &outBuf, true)
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-doneChan:
		if err != nil && err != context.Canceled {
			t.Errorf("unexpected error from RunClaimWatcher: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RunClaimWatcher failed to stop after context cancellation")
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "[AUTO-CLEAN]") {
		t.Errorf("expected [AUTO-CLEAN] in watcher output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "orphaned-tree") {
		t.Errorf("expected slug 'orphaned-tree' in watcher output, got: %s", outStr)
	}

	// Verify claim file was pruned
	if _, err := os.Stat(claimFile); !os.IsNotExist(err) {
		t.Errorf("expected claim file %s to be removed by auto-clean", claimFile)
	}
}
