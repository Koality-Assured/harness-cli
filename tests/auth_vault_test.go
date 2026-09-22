package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/auth"
)

func TestEncryptedFileVaultRoundtrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-vault-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	vaultPath := filepath.Join(tmpDir, "credentials.enc")
	passphrase := "super-secure-master-key-12345"
	vault := auth.NewEncryptedFileVault(vaultPath, passphrase)

	// 1. Initial empty
	list := vault.ListProviders()
	if len(list) != 0 {
		t.Errorf("expected 0 providers, got %d", len(list))
	}

	// 2. Set credential
	cred := &auth.Credential{
		AccessToken:  "sk-test-secret-token-123",
		RefreshToken: "rt-refresh-token-456",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		Profile:      "test-profile",
	}

	if err := vault.SetCredential("openai", cred); err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}

	// 3. Get credential
	retrieved, err := vault.GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected retrieved credential to not be nil")
	}
	if retrieved.AccessToken != cred.AccessToken {
		t.Errorf("expected token %q, got %q", cred.AccessToken, retrieved.AccessToken)
	}
	if retrieved.Profile != cred.Profile {
		t.Errorf("expected profile %q, got %q", cred.Profile, retrieved.Profile)
	}

	// 4. Delete credential
	deleted, err := vault.DeleteCredential("openai")
	if err != nil {
		t.Fatalf("DeleteCredential failed: %v", err)
	}
	if !deleted {
		t.Error("expected delete to return true")
	}

	retrievedAfter, _ := vault.GetCredential("openai")
	if retrievedAfter != nil {
		t.Error("expected credential to be deleted")
	}
}

func TestEncryptedVaultTamperDetection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-tamper-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	vaultPath := filepath.Join(tmpDir, "credentials.enc")
	passphrase := "master-key"
	vault := auth.NewEncryptedFileVault(vaultPath, passphrase)

	cred := &auth.Credential{
		AccessToken: "sensitive-token",
		TokenType:   "Bearer",
	}
	if err := vault.SetCredential("anthropic", cred); err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}

	// Read raw encrypted file
	raw, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatalf("failed to read vault file: %v", err)
	}

	// Tamper with ciphertext byte near end (AEAD tag)
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	tampered[len(tampered)-5] ^= 0xFF

	if err := os.WriteFile(vaultPath, tampered, 0600); err != nil {
		t.Fatalf("failed to write tampered vault: %v", err)
	}

	// Decryption must fail with tamper error
	_, err = vault.GetCredential("anthropic")
	if err == nil {
		t.Fatal("expected GetCredential to fail on tampered vault ciphertext, but it succeeded")
	}
}

func TestSilentRefreshTokenRotation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-refresh-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	vaultPath := filepath.Join(tmpDir, "credentials.enc")
	vault := auth.NewEncryptedFileVault(vaultPath, "master-key")

	// 1. Expiring credential with refresh token
	expiring := time.Now().Add(2 * time.Minute).Unix() // within 15-minute buffer
	cred := &auth.Credential{
		AccessToken:  "sk-ant-old-access-token",
		RefreshToken: "rt-initial-refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    expiring,
		Profile:      "claude-user",
	}
	if err := vault.SetCredential("anthropic", cred); err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}

	// 2. Call EnsureFreshToken with 15m buffer -> should trigger RefreshToken
	refreshed, err := auth.EnsureFreshToken(vault, "anthropic", 15*time.Minute)
	if err != nil {
		t.Fatalf("EnsureFreshToken failed: %v", err)
	}
	if refreshed == nil {
		t.Fatal("expected refreshed credential to not be nil")
	}
	if refreshed.AccessToken == "sk-ant-old-access-token" {
		t.Errorf("expected access token to rotate, got old token %q", refreshed.AccessToken)
	}
	if refreshed.RefreshToken == "rt-initial-refresh-token" {
		t.Errorf("expected refresh token to rotate, got old refresh token %q", refreshed.RefreshToken)
	}
	if !strings.HasPrefix(refreshed.AccessToken, "sk-ant-oauth-rot-") {
		t.Errorf("expected rotated token prefix, got %q", refreshed.AccessToken)
	}

	// Verify persistence in vault
	persisted, err := vault.GetCredential("anthropic")
	if err != nil {
		t.Fatalf("GetCredential failed: %v", err)
	}
	if persisted.AccessToken != refreshed.AccessToken {
		t.Errorf("expected persisted token %q, got %q", refreshed.AccessToken, persisted.AccessToken)
	}

	// 3. Token that is fresh (expires in 2 hours) -> EnsureFreshToken should NOT rotate
	freshExp := time.Now().Add(2 * time.Hour).Unix()
	persisted.ExpiresAt = freshExp
	if err := vault.SetCredential("anthropic", persisted); err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}

	notRefreshed, err := auth.EnsureFreshToken(vault, "anthropic", 15*time.Minute)
	if err != nil {
		t.Fatalf("EnsureFreshToken failed: %v", err)
	}
	if notRefreshed.AccessToken != persisted.AccessToken {
		t.Errorf("expected fresh token to not rotate, but got %q", notRefreshed.AccessToken)
	}

	// 4. API Key without refresh token -> EnsureFreshToken returns as-is
	apiKeyCred := &auth.Credential{
		AccessToken: "sk-openai-static-api-key",
		TokenType:   "ApiKey",
		ExpiresAt:   nil,
	}
	if err := vault.SetCredential("openai", apiKeyCred); err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}
	apiKeyRes, err := auth.EnsureFreshToken(vault, "openai", 15*time.Minute)
	if err != nil {
		t.Fatalf("EnsureFreshToken on ApiKey failed: %v", err)
	}
	if apiKeyRes.AccessToken != "sk-openai-static-api-key" {
		t.Errorf("expected static API key to remain unchanged, got %q", apiKeyRes.AccessToken)
	}
}
