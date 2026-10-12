package auth

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringVault_Basic(t *testing.T) {
	keyring.MockInit()
	kv := NewKeyringVault("test_service")

	if !kv.IsAvailable() {
		t.Errorf("Expected IsAvailable to be true with MockInit")
	}

	cred := &Credential{
		AccessToken: "token123",
		TokenType:   "Bearer",
	}

	err := kv.SetCredential("my_provider", cred)
	if err != nil {
		t.Fatalf("SetCredential failed: %v", err)
	}

	ret, err := kv.GetCredential("my_provider")
	if err != nil {
		t.Fatalf("GetCredential failed: %v", err)
	}
	if ret.AccessToken != cred.AccessToken {
		t.Errorf("Expected %s, got %s", cred.AccessToken, ret.AccessToken)
	}

	providers := kv.ListProviders()
	if len(providers) != 1 || providers[0] != "my_provider" {
		t.Errorf("ListProviders returned unexpected: %v", providers)
	}

	deleted, err := kv.DeleteCredential("my_provider")
	if err != nil || !deleted {
		t.Errorf("DeleteCredential failed or returned false: %v", err)
	}

	providers = kv.ListProviders()
	if len(providers) != 0 {
		t.Errorf("Expected 0 providers, got %v", providers)
	}
}
