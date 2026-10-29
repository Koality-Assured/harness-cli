package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQMDSubcommandsRegistered(t *testing.T) {
	subcommands := []string{"search", "vsearch", "query", "get", "multi-get", "update", "embed", "cleanup", "status"}
	for _, sub := range subcommands {
		cmd, _, err := RootCmd.Find([]string{"qmd", sub})
		if err != nil || cmd == nil {
			t.Fatalf("expected 'harness qmd %s' to be registered, got error: %v", sub, err)
		}
		if cmd.Name() != sub {
			t.Errorf("expected subcommand name '%s', got '%s'", sub, cmd.Name())
		}
	}
}

func TestResolveQMDDirPrefersScratchQMDMain(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-qmd-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	qmdMain := filepath.Join(tmpDir, "scratch", "qmd-main")
	_ = os.MkdirAll(qmdMain, 0755)

	resolved, err := resolveQMDDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveQMDDir failed: %v", err)
	}

	if resolved != qmdMain {
		t.Errorf("expected resolved dir %q, got %q", qmdMain, resolved)
	}
}

func TestResolveQMDDirFallsBackToPrimaryRoot(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-qmd-fallback-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)

	resolved, err := resolveQMDDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveQMDDir failed: %v", err)
	}

	if resolved != tmpDir {
		t.Errorf("expected resolved dir %q, got %q", tmpDir, resolved)
	}
}
