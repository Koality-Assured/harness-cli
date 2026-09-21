package tests

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func getBinaryPath(t *testing.T) string {
	binName := "harness-test-bin"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	tmpBin := filepath.Join(t.TempDir(), binName)
	cmd := exec.Command("go", "build", "-o", tmpBin, "../cmd/harness")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test harness binary: %s (%v)", string(out), err)
	}
	return tmpBin
}

func TestCLIIntegrationSubcommands(t *testing.T) {
	binaryPath := getBinaryPath(t)

	// 1. Test status --json
	cmd := exec.Command(binaryPath, "status", "--json")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("status --json failed: %v", err)
	}

	var statusMap map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &statusMap); err != nil {
		t.Fatalf("status --json did not return valid JSON: %v", err)
	}

	for _, key := range []string{"primary_root", "checkout_root", "branch", "is_clean", "active_claims"} {
		if _, ok := statusMap[key]; !ok {
			t.Errorf("status JSON missing expected key %q", key)
		}
	}

	// 2. Test list --json
	stdout.Reset()
	cmd = exec.Command(binaryPath, "list", "--json")
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("list --json failed: %v", err)
	}

	var listMap map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &listMap); err != nil {
		t.Fatalf("list --json did not return valid JSON: %v", err)
	}

	if _, ok := listMap["count"]; !ok {
		t.Error("list JSON missing 'count'")
	}
	if _, ok := listMap["harnesses"]; !ok {
		t.Error("list JSON missing 'harnesses'")
	}

	// 3. Test auth status --json
	stdout.Reset()
	cmd = exec.Command(binaryPath, "auth", "status", "--json")
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("auth status --json failed: %v", err)
	}

	var authMap map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &authMap); err != nil {
		t.Fatalf("auth status --json did not return valid JSON: %v", err)
	}

	if _, ok := authMap["active_vault_backend"]; !ok {
		t.Error("auth status JSON missing 'active_vault_backend'")
	}
	if _, ok := authMap["providers"]; !ok {
		t.Error("auth status JSON missing 'providers'")
	}

	// 4. Test claim inspect --json
	stdout.Reset()
	cmd = exec.Command(binaryPath, "claim", "inspect", "--json")
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("claim inspect --json failed: %v", err)
	}

	var claimMap map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &claimMap); err != nil {
		t.Fatalf("claim inspect --json did not return valid JSON: %v", err)
	}
	if _, ok := claimMap["stale_threshold_hours"]; !ok {
		t.Error("claim inspect JSON missing 'stale_threshold_hours'")
	}

	// 5. Test clean --dry-run
	stdout.Reset()
	cmd = exec.Command(binaryPath, "clean", "--stale-hours", "24", "--dry-run")
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("clean --stale-hours --dry-run failed: %v", err)
	}
}
