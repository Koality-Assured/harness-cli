package commands

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/auth"
	"github.com/Koality-Assured/harness-cli/internal/registry"
)

func withExecGlobals(t *testing.T, dryRun, force bool, harness string) func() {
	t.Helper()
	oldDryRun, oldForce, oldHarness, oldAgent := DryRun, Force, HarnessArg, execAgent
	oldVault := getExecVault
	DryRun, Force, HarnessArg, execAgent = dryRun, force, harness, ""
	return func() {
		DryRun, Force, HarnessArg, execAgent = oldDryRun, oldForce, oldHarness, oldAgent
		getExecVault = oldVault
	}
}

func emptyTestVault(t *testing.T) *auth.UniversalVault {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "harness-exec-empty-vault-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	return auth.NewUniversalVault(tmpDir, "testpass", true)
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String(), runErr
}

func TestExecSecretInjection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-exec-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Configure test vault
	vault := auth.NewUniversalVault(tmpDir, "testpass", true)
	_ = vault.SetCredential("anthropic", &auth.Credential{
		AccessToken: "sk-ant-test-key-12345",
		TokenType:   "bearer",
	})
	_ = vault.SetCredential("openai", &auth.Credential{
		AccessToken: "sk-test-openai-67890",
		TokenType:   "bearer",
	})

	cred, err := vault.GetCredential("anthropic")
	if err != nil || cred == nil || cred.AccessToken != "sk-ant-test-key-12345" {
		t.Fatalf("vault credential retrieval failed: %v", err)
	}

	credOAI, err := vault.GetCredential("openai")
	if err != nil || credOAI == nil || credOAI.AccessToken != "sk-test-openai-67890" {
		t.Fatalf("vault openai credential retrieval failed: %v", err)
	}
}

func TestExecFailClosedWithoutVaultCredentials(t *testing.T) {
	defer withExecGlobals(t, false, false, "")()
	getExecVault = func() *auth.UniversalVault { return emptyTestVault(t) }

	err := execCmd.RunE(execCmd, []string{"echo", "should-not-run"})
	if err == nil {
		t.Fatal("expected fail-closed error when vault has no credentials")
	}
	if !strings.Contains(err.Error(), "no vault credentials") {
		t.Fatalf("expected no-credentials error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected error to mention --force override, got: %v", err)
	}
}

func TestExecForceOverridesMissingCredentials(t *testing.T) {
	defer withExecGlobals(t, false, true, "")()
	getExecVault = func() *auth.UniversalVault { return emptyTestVault(t) }

	// On Windows use cmd /c echo; on Unix use true (or echo).
	args := []string{"cmd", "/c", "echo", "ok"}
	if filepath.Separator == '/' {
		args = []string{"true"}
	}

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stderr = w
	runErr := execCmd.RunE(execCmd, args)
	_ = w.Close()
	os.Stderr = oldStderr
	var stderrBuf bytes.Buffer
	_, _ = io.Copy(&stderrBuf, r)
	_ = r.Close()

	if runErr != nil {
		t.Fatalf("--force should allow exec without credentials, got: %v", runErr)
	}
	if !strings.Contains(stderrBuf.String(), "zero injected vault credentials") {
		t.Fatalf("expected stderr warning about zero credentials, got: %q", stderrBuf.String())
	}
}

func TestExecDryRunDoesNotStartChild(t *testing.T) {
	defer withExecGlobals(t, true, false, "")()
	getExecVault = func() *auth.UniversalVault { return emptyTestVault(t) }

	out, err := captureStdout(t, func() error {
		// Would fail hard if started: binary does not exist.
		return execCmd.RunE(execCmd, []string{"nonexistent-harness-exec-binary-xyzzy", "marker"})
	})
	if err != nil {
		t.Fatalf("dry-run must not start child or return error: %v", err)
	}
	if !strings.Contains(out, "[dry-run]") {
		t.Fatalf("expected dry-run simulation output, got: %q", out)
	}
	if !strings.Contains(out, "Would exec:") {
		t.Fatalf("expected would-exec line, got: %q", out)
	}
	if !strings.Contains(out, "Would refuse: no vault credentials") {
		t.Fatalf("expected dry-run to report refuse-without-creds, got: %q", out)
	}
}

func TestExecHarnessArgResolvesWorkDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-exec-workdir-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("HARNESS_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	record, err := registry.GetRegistry().Register(tmpDir, "exec-test", "", false, true)
	if err != nil {
		t.Fatalf("register test harness: %v", err)
	}
	defer withExecGlobals(t, true, false, record.ID)()
	getExecVault = func() *auth.UniversalVault { return emptyTestVault(t) }

	out, runErr := captureStdout(t, func() error {
		return execCmd.RunE(execCmd, []string{"echo", "scoped"})
	})
	if runErr != nil {
		t.Fatalf("--harness dry-run should resolve existing dir: %v", runErr)
	}
	abs, _ := filepath.Abs(tmpDir)
	if !strings.Contains(out, "Working directory:") {
		t.Fatalf("expected working directory in dry-run output, got: %q", out)
	}
	if !strings.Contains(out, abs) && !strings.Contains(out, tmpDir) {
		t.Fatalf("expected resolved harness path in output, got: %q", out)
	}
}

func TestExecHarnessArgRejectsMissingTarget(t *testing.T) {
	defer withExecGlobals(t, false, true, `C:\nonexistent-harness-exec-target-xyzzy`)()
	getExecVault = func() *auth.UniversalVault { return emptyTestVault(t) }

	err := execCmd.RunE(execCmd, []string{"echo", "nope"})
	if err == nil {
		t.Fatal("expected error for unresolvable --harness target")
	}
	if !strings.Contains(err.Error(), "not registered") && !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected harness resolution error, got: %v", err)
	}
}

func TestExecForceDoesNotBypassHarnessResolution(t *testing.T) {
	defer withExecGlobals(t, false, true, `C:\nonexistent-harness-exec-target-xyzzy`)()
	getExecVault = func() *auth.UniversalVault { return emptyTestVault(t) }

	err := execCmd.RunE(execCmd, []string{"echo", "nope"})
	if err == nil {
		t.Fatal("--force must not bypass --harness resolution failure")
	}
}

func TestResolveExecWorkDirEmpty(t *testing.T) {
	defer withExecGlobals(t, false, false, "")()
	dir, err := resolveExecWorkDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "" {
		t.Fatalf("expected empty workdir when --harness unset, got %q", dir)
	}
}
