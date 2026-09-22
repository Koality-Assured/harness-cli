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
