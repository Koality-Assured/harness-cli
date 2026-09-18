package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/config"
	"golang.org/x/crypto/pbkdf2"
)

var (
	MagicHeader = []byte("HNC1") // Harness Encrypted Credentials v1
)

// EncryptedFileVault manages credentials in an AES-256-GCM encrypted file.
type EncryptedFileVault struct {
	path       string
	passphrase string
	mu         sync.RWMutex
}

// NewEncryptedFileVault creates a new EncryptedFileVault instance.
func NewEncryptedFileVault(customPath, passphrase string) *EncryptedFileVault {
	if customPath == "" {
		customPath = config.GetVaultPath()
	}
	return &EncryptedFileVault{
		path:       customPath,
		passphrase: passphrase,
	}
}

func (ev *EncryptedFileVault) getPassphrase() string {
	if ev.passphrase != "" {
		return ev.passphrase
	}
	if env := os.Getenv("HARNESS_VAULT_PASSPHRASE"); env != "" {
		return env
	}

	// Machine/user bound fallback for automated headless runs
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "agent"
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return fmt.Sprintf("harness-vault:%s:%s", user, home)
}

func (ev *EncryptedFileVault) deriveKey(salt []byte) []byte {
	return pbkdf2.Key([]byte(ev.getPassphrase()), salt, 100000, 32, sha256.New)
}

func (ev *EncryptedFileVault) loadAll() (map[string]Credential, error) {
	if _, err := os.Stat(ev.path); os.IsNotExist(err) {
		return make(map[string]Credential), nil
	}

	raw, err := os.ReadFile(ev.path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return make(map[string]Credential), nil
	}

	minLen := len(MagicHeader) + 16 + 12 + 16
	if len(raw) < minLen || !bytes.HasPrefix(raw, MagicHeader) {
		return nil, errors.New("invalid or corrupted vault file header: missing MAGIC_HEADER or file truncated")
	}

	salt := raw[4:20]
	nonce := raw[20:32]
	ciphertext := raw[32:]

	key := ev.deriveKey(salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, MagicHeader)
	if err != nil {
		return nil, errors.New("authentication tag verification failed: vault ciphertext has been tampered with or passphrase is incorrect")
	}

	var data map[string]Credential
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return nil, fmt.Errorf("failed to decode decrypted json: %w", err)
	}

	return data, nil
}

func (ev *EncryptedFileVault) saveAll(data map[string]Credential) error {
	dir := filepath.Dir(ev.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	config.EnforcePrivateDirPermissions(dir)

	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	if _, err := rand.Read(nonce); err != nil {
		return err
	}

	key := ev.deriveKey(salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	ciphertext := aesgcm.Seal(nil, nonce, jsonBytes, MagicHeader)

	var payload bytes.Buffer
	payload.Write(MagicHeader)
	payload.Write(salt)
	payload.Write(nonce)
	payload.Write(ciphertext)

	tmpPath := fmt.Sprintf("%s.tmp.%d", ev.path, os.Getpid())
	if err := os.WriteFile(tmpPath, payload.Bytes(), 0600); err != nil {
		return err
	}
	config.EnforcePrivatePermissions(tmpPath)

	if err := os.Rename(tmpPath, ev.path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	config.EnforcePrivatePermissions(ev.path)
	return nil
}

// GetCredential retrieves a credential from the encrypted vault.
func (ev *EncryptedFileVault) GetCredential(provider string) (*Credential, error) {
	ev.mu.RLock()
	defer ev.mu.RUnlock()

	data, err := ev.loadAll()
	if err != nil {
		return nil, err
	}
	norm := strings.ToLower(strings.TrimSpace(provider))
	if cred, ok := data[norm]; ok {
		return &cred, nil
	}
	return nil, nil
}

// SetCredential stores a credential in the encrypted vault.
func (ev *EncryptedFileVault) SetCredential(provider string, cred *Credential) error {
	ev.mu.Lock()
	defer ev.mu.Unlock()

	data, err := ev.loadAll()
	if err != nil {
		// Corrupted recovery backup
		_ = ev.recoverCorrupted()
		data = make(map[string]Credential)
	}

	norm := strings.ToLower(strings.TrimSpace(provider))
	if cred.TokenType == "" {
		cred.TokenType = "Bearer"
	}
	cred.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data[norm] = *cred

	return ev.saveAll(data)
}

// DeleteCredential deletes a credential from the encrypted vault.
func (ev *EncryptedFileVault) DeleteCredential(provider string) (bool, error) {
	ev.mu.Lock()
	defer ev.mu.Unlock()

	data, err := ev.loadAll()
	if err != nil {
		return false, err
	}

	norm := strings.ToLower(strings.TrimSpace(provider))
	if _, ok := data[norm]; ok {
		delete(data, norm)
		err := ev.saveAll(data)
		return true, err
	}
	return false, nil
}

// ListProviders returns all provider keys in the encrypted vault.
func (ev *EncryptedFileVault) ListProviders() []string {
	ev.mu.RLock()
	defer ev.mu.RUnlock()

	data, err := ev.loadAll()
	if err != nil {
		return []string{}
	}

	var list []string
	for k := range data {
		list = append(list, k)
	}
	sort.Strings(list)
	return list
}

func (ev *EncryptedFileVault) recoverCorrupted() string {
	if _, err := os.Stat(ev.path); os.IsNotExist(err) {
		return ""
	}
	ts := time.Now().UTC().Format("20060102_150405")
	backupPath := fmt.Sprintf("%s.corrupted.%s", ev.path, ts)
	_ = os.Rename(ev.path, backupPath)
	return backupPath
}
