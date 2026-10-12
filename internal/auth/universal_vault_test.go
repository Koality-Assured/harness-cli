package auth

import (
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestUniversalVault_Basic(t *testing.T) {
	keyring.MockInit()
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "univ_vault.enc")

	uv := NewUniversalVault(vaultPath, "supersecret", false)

	cred := &Credential{
		AccessToken: "univ_token",
		TokenType:   "Bearer",
	}

	err := uv.SetCredential("univ_prov", cred)
	if err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}

	ret, err := uv.GetCredential("univ_prov")
	if err != nil {
		t.Fatalf("GetCredential failed: %v", err)
	}
	if ret == nil || ret.AccessToken != cred.AccessToken {
		t.Errorf("Expected token %s, got %v", cred.AccessToken, ret)
	}

	providers := uv.ListProviders()
	if len(providers) != 1 || providers[0] != "univ_prov" {
		t.Errorf("ListProviders returned unexpected: %v", providers)
	}

	deleted, err := uv.DeleteCredential("univ_prov")
	if err != nil || !deleted {
		t.Errorf("DeleteCredential failed or returned false: %v", err)
	}

	providers = uv.ListProviders()
	if len(providers) != 0 {
		t.Errorf("Expected 0 providers after delete, got %v", providers)
	}
}

func TestUniversalVault_ForceFile(t *testing.T) {
	keyring.MockInit()
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "univ_vault_force.enc")

	uv := NewUniversalVault(vaultPath, "supersecret", true)

	if uv.ActiveBackendName() != "encrypted_file" {
		t.Errorf("Expected active backend to be encrypted_file, got %s", uv.ActiveBackendName())
	}
}
