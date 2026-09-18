package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	DefaultKeyringService = "harness-control-plane"
)

// GetConfigDir returns the default ~/.harness directory or an override.
func GetConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".harness")
}

// GetConfigPath returns the active configuration file path.
func GetConfigPath() string {
	if env := os.Getenv("HARNESS_CONFIG_PATH"); env != "" {
		return filepath.Clean(env)
	}
	return filepath.Join(GetConfigDir(), "config.json")
}

// GetVaultPath returns the encrypted credentials file path.
func GetVaultPath() string {
	if env := os.Getenv("HARNESS_VAULT_PATH"); env != "" {
		return filepath.Clean(env)
	}
	return filepath.Join(GetConfigDir(), "credentials.enc")
}

// GetKeyringService returns the OS keyring service identifier.
func GetKeyringService() string {
	if env := os.Getenv("HARNESS_KEYRING_SERVICE"); env != "" {
		return env
	}
	return DefaultKeyringService
}

// EnforcePrivatePermissions sets user-only permissions (0600 on POSIX, restricted ACL on Windows).
func EnforcePrivatePermissions(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	if runtime.GOOS == "windows" {
		username := os.Getenv("USERNAME")
		if username != "" {
			_ = exec.Command("icacls", path, "/inheritance:r", "/grant:r", username+":(R,W)").Run()
		}
	} else {
		_ = os.Chmod(path, 0600)
	}
}

// EnforcePrivateDirPermissions sets user-only permissions on a directory (0700 on POSIX).
func EnforcePrivateDirPermissions(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0700)
	}
}
