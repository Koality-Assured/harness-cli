package auth

import (
	"os"
	"sort"
	"strings"
	"sync"
)

// UniversalVault manages credentials with automatic fallback between OS Keyring and Encrypted File Vault.
type UniversalVault struct {
	keyringVault   *KeyringVault
	fileVault      *EncryptedFileVault
	forceFileVault bool
	mu             sync.RWMutex
}

var (
	globalVault *UniversalVault
	vaultMu     sync.Mutex
)

// GetVault returns the global UniversalVault instance.
func GetVault() *UniversalVault {
	vaultMu.Lock()
	defer vaultMu.Unlock()

	forceFile := strings.ToLower(os.Getenv("HARNESS_FORCE_FILE_VAULT"))
	isForce := forceFile == "1" || forceFile == "true"

	if globalVault == nil || globalVault.forceFileVault != isForce {
		globalVault = &UniversalVault{
			keyringVault:   NewKeyringVault(""),
			fileVault:      NewEncryptedFileVault("", ""),
			forceFileVault: isForce,
		}
	}
	return globalVault
}

// NewUniversalVault creates a custom vault instance (used in tests).
func NewUniversalVault(customFileVaultPath, passphrase string, forceFile bool) *UniversalVault {
	return &UniversalVault{
		keyringVault:   NewKeyringVault(""),
		fileVault:      NewEncryptedFileVault(customFileVaultPath, passphrase),
		forceFileVault: forceFile,
	}
}

// ActiveBackendName reports the currently active credential backend.
func (uv *UniversalVault) ActiveBackendName() string {
	if uv.forceFileVault {
		return "encrypted_file"
	}
	if uv.keyringVault.IsAvailable() {
		return "keyring:OSKeyring"
	}
	return "encrypted_file"
}

// GetCredential retrieves credential from Keyring, falling back to File Vault.
func (uv *UniversalVault) GetCredential(provider string) (*Credential, error) {
	if !uv.forceFileVault && uv.keyringVault.IsAvailable() {
		cred, err := uv.keyringVault.GetCredential(provider)
		if err == nil && cred != nil {
			return cred, nil
		}
	}
	return uv.fileVault.GetCredential(provider)
}

// SetCredential writes credential to Keyring, falling back to File Vault.
func (uv *UniversalVault) SetCredential(provider string, cred *Credential) error {
	if !uv.forceFileVault && uv.keyringVault.IsAvailable() {
		err := uv.keyringVault.SetCredential(provider, cred)
		if err == nil {
			return nil
		}
	}
	return uv.fileVault.SetCredential(provider, cred)
}

// DeleteCredential purges credential across both Keyring and File Vault.
func (uv *UniversalVault) DeleteCredential(provider string) (bool, error) {
	delKeyring := false
	if !uv.forceFileVault && uv.keyringVault.IsAvailable() {
		delKeyring, _ = uv.keyringVault.DeleteCredential(provider)
	}
	delFile, err := uv.fileVault.DeleteCredential(provider)
	return delKeyring || delFile, err
}

// ListProviders returns all registered providers across both vaults.
func (uv *UniversalVault) ListProviders() []string {
	providerSet := make(map[string]bool)

	if !uv.forceFileVault && uv.keyringVault.IsAvailable() {
		for _, p := range uv.keyringVault.ListProviders() {
			providerSet[p] = true
		}
	}
	for _, p := range uv.fileVault.ListProviders() {
		providerSet[p] = true
	}

	var list []string
	for p := range providerSet {
		list = append(list, p)
	}
	sort.Strings(list)
	return list
}
