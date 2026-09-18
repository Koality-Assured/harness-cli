package tests

import (
	"os"
	"path/filepath"
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
