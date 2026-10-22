package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestParseDotEnv(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env")
	content := `# comment
ANTHROPIC_API_KEY="sk-ant-test-123"
OPENAI_API_KEY='sk-proj-test-456'
BIFROST_API_KEY=sk-bf-test-789
EMPTY_VAL=
`
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test .env: %v", err)
	}

	parsed := ParseDotEnv(envPath)
	if parsed["ANTHROPIC_API_KEY"] != "sk-ant-test-123" {
		t.Errorf("expected sk-ant-test-123, got %q", parsed["ANTHROPIC_API_KEY"])
	}
	if parsed["OPENAI_API_KEY"] != "sk-proj-test-456" {
		t.Errorf("expected sk-proj-test-456, got %q", parsed["OPENAI_API_KEY"])
	}
	if parsed["BIFROST_API_KEY"] != "sk-bf-test-789" {
		t.Errorf("expected sk-bf-test-789, got %q", parsed["BIFROST_API_KEY"])
	}
	if _, ok := parsed["EMPTY_VAL"]; ok {
		t.Errorf("empty val should not be set")
	}
}

func TestAutoDiscoverAPIKey_EnvVar(t *testing.T) {
	keyring.MockInit()
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-auto-discovered-123")

	key, err := AutoDiscoverAPIKey("anthropic", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "sk-ant-auto-discovered-123" {
		t.Errorf("expected sk-ant-auto-discovered-123, got %q", key)
	}

	// Verify it was stored in the vault
	vault := GetVault()
	cred, err := vault.GetCredential("anthropic")
	if err != nil || cred == nil {
		t.Fatalf("credential not found in vault: %v", err)
	}
	if cred.AccessToken != "sk-ant-auto-discovered-123" {
		t.Errorf("vault credential token mismatch: %q", cred.AccessToken)
	}
}

func TestBifrostProviderNormalization(t *testing.T) {
	cases := []struct {
		input    string
		expected string
		valid    bool
	}{
		{"bifrost", "bifrost", true},
		{"BIFROST", "bifrost", true},
		{"maxim", "bifrost", true},
		{"MAXIM", "bifrost", true},
		{"unknown-gw", "", false},
	}
	for _, tc := range cases {
		prov, ok := NormalizeProvider(tc.input)
		if ok != tc.valid {
			t.Errorf("NormalizeProvider(%q) valid = %v, want %v", tc.input, ok, tc.valid)
		}
		if prov != tc.expected {
			t.Errorf("NormalizeProvider(%q) = %q, want %q", tc.input, prov, tc.expected)
		}
	}
}

func TestLoginBifrost_WithAPIKey(t *testing.T) {
	keyring.MockInit()
	err := LoginBifrost("sk-bf-explicit-key")
	if err != nil {
		t.Fatalf("LoginBifrost failed: %v", err)
	}

	cred, err := GetVault().GetCredential("bifrost")
	if err != nil || cred == nil {
		t.Fatalf("GetCredential failed: %v", err)
	}
	if cred.AccessToken != "sk-bf-explicit-key" {
		t.Errorf("expected sk-bf-explicit-key, got %q", cred.AccessToken)
	}
}
