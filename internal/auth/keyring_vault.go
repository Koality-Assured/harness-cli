package auth

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/config"
	"github.com/zalando/go-keyring"
)

const (
	HarnessProvidersKey = "__harness_providers__"
)

// Credential represents stored authentication data for a provider.
type Credential struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token,omitempty"`
	TokenType    string      `json:"token_type"`
	ExpiresAt    interface{} `json:"expires_at,omitempty"`
	Profile      string      `json:"profile,omitempty"`
	UpdatedAt    string      `json:"updated_at"`
}

// KeyringVault manages native OS credentials via the go-keyring library.
type KeyringVault struct {
	service string
	mu      sync.RWMutex
}

// NewKeyringVault creates a new KeyringVault instance.
func NewKeyringVault(service string) *KeyringVault {
	if service == "" {
		service = config.GetKeyringService()
	}
	return &KeyringVault{service: service}
}

// IsAvailable checks if the OS keyring can be accessed.
func (kv *KeyringVault) IsAvailable() bool {
	// Attempt a dry read on a sentinel key
	_, err := keyring.Get(kv.service, "__probe__")
	if err == keyring.ErrNotFound {
		return true
	}
	return err == nil
}

func (kv *KeyringVault) getProvidersIndex() []string {
	raw, err := keyring.Get(kv.service, HarnessProvidersKey)
	if err != nil || raw == "" {
		return []string{}
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []string{}
	}
	return list
}

func (kv *KeyringVault) setProvidersIndex(providers []string) {
	unique := make(map[string]bool)
	var clean []string
	for _, p := range providers {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" && !unique[p] {
			unique[p] = true
			clean = append(clean, p)
		}
	}
	sort.Strings(clean)
	data, err := json.Marshal(clean)
	if err == nil {
		_ = keyring.Set(kv.service, HarnessProvidersKey, string(data))
	}
}

// GetCredential retrieves stored credential for provider.
func (kv *KeyringVault) GetCredential(provider string) (*Credential, error) {
	kv.mu.RLock()
	defer kv.mu.RUnlock()

	norm := strings.ToLower(strings.TrimSpace(provider))
	raw, err := keyring.Get(kv.service, norm)
	if err != nil {
		if err == keyring.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("keyring get failed for provider '%s': %w", provider, err)
	}

	var cred Credential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return nil, fmt.Errorf("failed to parse credential json for '%s': %w", provider, err)
	}
	return &cred, nil
}

// SetCredential stores credential for provider.
func (kv *KeyringVault) SetCredential(provider string, cred *Credential) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	norm := strings.ToLower(strings.TrimSpace(provider))
	if cred.TokenType == "" {
		cred.TokenType = "Bearer"
	}
	cred.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(cred)
	if err != nil {
		return err
	}

	if err := keyring.Set(kv.service, norm, string(data)); err != nil {
		return fmt.Errorf("keyring set failed for provider '%s': %w", provider, err)
	}

	// Update provider index
	providers := kv.getProvidersIndex()
	found := false
	for _, p := range providers {
		if p == norm {
			found = true
			break
		}
	}
	if !found {
		providers = append(providers, norm)
		kv.setProvidersIndex(providers)
	}

	return nil
}

// DeleteCredential purges credentials for provider.
func (kv *KeyringVault) DeleteCredential(provider string) (bool, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	norm := strings.ToLower(strings.TrimSpace(provider))
	err := keyring.Delete(kv.service, norm)
	if err != nil {
		if err == keyring.ErrNotFound {
			return false, nil
		}
		return false, err
	}

	providers := kv.getProvidersIndex()
	var updated []string
	for _, p := range providers {
		if p != norm {
			updated = append(updated, p)
		}
	}
	kv.setProvidersIndex(updated)
	return true, nil
}

// ListProviders returns all registered credential providers in keyring.
func (kv *KeyringVault) ListProviders() []string {
	kv.mu.RLock()
	defer kv.mu.RUnlock()
	return kv.getProvidersIndex()
}
