package tests

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
)

func TestCLIIntegrationSubcommands(t *testing.T) {
	// Build or locate binary
	binaryPath := "../harness.exe"

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
}
