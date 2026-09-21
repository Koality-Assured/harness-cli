package commands

import (
	"os"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/auth"
)

func TestExecSecretInjection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-exec-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Configure test vault
	vault := auth.NewUniversalVault(tmpDir, "testpass", true)
	_ = vault.SetCredential("anthropic", &auth.Credential{
		AccessToken: "sk-ant-test-key-12345",
		TokenType:   "bearer",
	})
	_ = vault.SetCredential("openai", &auth.Credential{
		AccessToken: "sk-test-openai-67890",
		TokenType:   "bearer",
	})

	cred, err := vault.GetCredential("anthropic")
	if err != nil || cred == nil || cred.AccessToken != "sk-ant-test-key-12345" {
		t.Fatalf("vault credential retrieval failed: %v", err)
	}

	credOAI, err := vault.GetCredential("openai")
	if err != nil || credOAI == nil || credOAI.AccessToken != "sk-test-openai-67890" {
		t.Fatalf("vault openai credential retrieval failed: %v", err)
	}
}
