package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEncryptedFileVault_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault.enc")
	
	ev := NewEncryptedFileVault(vaultPath, "supersecret")
	
	cred := &Credential{
		AccessToken: "token_abc",
		TokenType:   "Bearer",
	}

	err := ev.SetCredential("test_prov", cred)
	if err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}

	// Verify file permissions 0600
	info, err := os.Stat(vaultPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Errorf("expected file mode 0600 or tighter, got %v", info.Mode().Perm())
	}

	// Test Nonce uniqueness
	raw1, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatalf("Read raw failed: %v", err)
	}
	
	err = ev.SetCredential("test_prov2", cred)
	if err != nil {
		t.Fatalf("SetCredential 2 failed: %v", err)
	}
	
	raw2, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatalf("Read raw 2 failed: %v", err)
	}
	
	nonce1 := raw1[20:32]
	nonce2 := raw2[20:32]
	if bytes.Equal(nonce1, nonce2) {
		t.Errorf("Nonces are not unique across writes")
	}

	// Test wrong passphrase
	evWrong := NewEncryptedFileVault(vaultPath, "wrongpass")
	_, err = evWrong.GetCredential("test_prov")
	if err == nil {
		t.Errorf("Expected error with wrong passphrase, got nil")
	} else if err.Error() != "authentication tag verification failed: vault ciphertext has been tampered with or passphrase is incorrect" {
		t.Errorf("Unexpected error message for wrong passphrase: %v", err)
	}

	// Wait, if it fails to load, SetCredential shouldn't overwrite unless we want it to?
	// The problem statement says: "Wrong passphrase handling for encrypted vault."
	// Let's check how SetCredential behaves with wrong passphrase.
	err = evWrong.SetCredential("test_prov3", cred)
	if err == nil {
		t.Errorf("SetCredential with wrong passphrase should return error, got nil")
	} else if err.Error() != "authentication tag verification failed: vault ciphertext has been tampered with or passphrase is incorrect" {
		t.Errorf("Unexpected error for SetCredential with wrong passphrase: %v", err)
	}
}
