package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetConfigDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	expected := filepath.Join(home, ".harness")
	if got := GetConfigDir(); got != expected {
		t.Errorf("GetConfigDir() = %v, want %v", got, expected)
	}
}

func TestGetConfigPath(t *testing.T) {
	// Test without env
	os.Unsetenv("HARNESS_CONFIG_PATH")
	expected := filepath.Join(GetConfigDir(), "config.json")
	if got := GetConfigPath(); got != expected {
		t.Errorf("GetConfigPath() without env = %v, want %v", got, expected)
	}

	// Test with env
	os.Setenv("HARNESS_CONFIG_PATH", "custom/config.json")
	expectedEnv := filepath.Clean("custom/config.json")
	if got := GetConfigPath(); got != expectedEnv {
		t.Errorf("GetConfigPath() with env = %v, want %v", got, expectedEnv)
	}
	os.Unsetenv("HARNESS_CONFIG_PATH")
}

func TestGetVaultPath(t *testing.T) {
	// Test without env
	os.Unsetenv("HARNESS_VAULT_PATH")
	expected := filepath.Join(GetConfigDir(), "credentials.enc")
	if got := GetVaultPath(); got != expected {
		t.Errorf("GetVaultPath() without env = %v, want %v", got, expected)
	}

	// Test with env
	os.Setenv("HARNESS_VAULT_PATH", "custom/vault.enc")
	expectedEnv := filepath.Clean("custom/vault.enc")
	if got := GetVaultPath(); got != expectedEnv {
		t.Errorf("GetVaultPath() with env = %v, want %v", got, expectedEnv)
	}
	os.Unsetenv("HARNESS_VAULT_PATH")
}

func TestGetKeyringService(t *testing.T) {
	os.Unsetenv("HARNESS_KEYRING_SERVICE")
	if got := GetKeyringService(); got != DefaultKeyringService {
		t.Errorf("GetKeyringService() without env = %v, want %v", got, DefaultKeyringService)
	}

	os.Setenv("HARNESS_KEYRING_SERVICE", "custom-keyring")
	if got := GetKeyringService(); got != "custom-keyring" {
		t.Errorf("GetKeyringService() with env = %v, want %v", got, "custom-keyring")
	}
	os.Unsetenv("HARNESS_KEYRING_SERVICE")
}

func TestEnforcePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	
	dirPath := filepath.Join(tmpDir, "testdir")
	err := os.Mkdir(dirPath, 0755)
	if err != nil {
		t.Fatal(err)
	}
	EnforcePrivateDirPermissions(dirPath)

	filePath := filepath.Join(tmpDir, "testfile.txt")
	err = os.WriteFile(filePath, []byte("test"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	EnforcePrivatePermissions(filePath)
}
