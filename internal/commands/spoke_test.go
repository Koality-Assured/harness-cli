package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsAllowlistedCorePath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-spoke-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Root files must be allowlisted
	allow, reason := isAllowlistedCorePath("AGENTS.md", tmpDir)
	if !allow {
		t.Errorf("AGENTS.md should be allowlisted, got reason: %s", reason)
	}

	allow, _ = isAllowlistedCorePath("harness.ps1", tmpDir)
	if !allow {
		t.Errorf("harness.ps1 should be allowlisted")
	}

	// 2. Domain markers and overlays must be rejected
	allow, _ = isAllowlistedCorePath(".harness/domain.json", tmpDir)
	if allow {
		t.Errorf(".harness/domain.json should NOT be allowlisted")
	}

	allow, _ = isAllowlistedCorePath("docs/standards/legal-overlay.md", tmpDir)
	if allow {
		t.Errorf("domain overlay should NOT be allowlisted")
	}

	// 3. Instance leak markers must be rejected
	allow, _ = isAllowlistedCorePath("references/owasp/top10.md", tmpDir)
	if allow {
		t.Errorf("references/owasp must NOT be allowlisted")
	}

	allow, _ = isAllowlistedCorePath("ai-tooling/skills/aws/deploy.md", tmpDir)
	if allow {
		t.Errorf("vendor skill family aws must NOT be allowlisted")
	}

	// 4. routing/areas.yaml: MUST NOT overwrite existing domain taxonomy in spoke!
	routingDir := filepath.Join(tmpDir, "routing")
	_ = os.MkdirAll(routingDir, 0755)
	_ = os.WriteFile(filepath.Join(routingDir, "areas.yaml"), []byte("domain: legal\n"), 0644)

	allow, reason = isAllowlistedCorePath("routing/areas.yaml", tmpDir)
	if allow {
		t.Errorf("routing/areas.yaml MUST NOT be allowlisted when it exists in spoke!")
	}
	if reason != "preserved-spoke-domain-taxonomy" {
		t.Errorf("expected reason preserved-spoke-domain-taxonomy, got %s", reason)
	}

	// 5. Core tooling scripts must be allowlisted
	allow, _ = isAllowlistedCorePath("scripts/cli/harness.py", tmpDir)
	if !allow {
		t.Errorf("scripts/cli tooling should be allowlisted")
	}

	allow, _ = isAllowlistedCorePath("scripts/sync/pull_harness_core.py", tmpDir)
	if !allow {
		t.Errorf("scripts/sync tooling should be allowlisted")
	}
}

func TestResolvePython(t *testing.T) {
	py := resolvePython()
	if py == "" {
		t.Errorf("expected resolvePython to return a non-empty binary")
	}
}
