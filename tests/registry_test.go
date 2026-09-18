package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/registry"
)

func TestSlugifyID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ai-router", "ai-router"},
		{"AI_ROUTER", "ai-router"},
		{"My Test Repo!", "my-test-repo"},
		{"con", "harness-con"},
		{"nul", "harness-nul"},
		{"prn", "harness-prn"},
		{"aux", "harness-aux"},
		{"", "harness"},
	}

	for _, tc := range tests {
		got := registry.SlugifyID(tc.input)
		if got != tc.expected {
			t.Errorf("SlugifyID(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestHarnessRegistryLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "harness-reg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "config.json")
	reg := registry.NewHarnessRegistry(cfgPath)

	// Create fake repo dir
	fakeRepo := filepath.Join(tmpDir, "fake-spoke")
	if err := os.MkdirAll(filepath.Join(fakeRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create fake repo: %v", err)
	}

	// 1. Register
	rec, err := reg.Register(fakeRepo, "Fake Spoke", "Test Domain", true, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if rec.ID != "fake-spoke" {
		t.Errorf("expected ID 'fake-spoke', got %q", rec.ID)
	}

	// 2. Verify Active
	active, ok := reg.GetActiveHarness()
	if !ok || active == nil {
		t.Fatal("expected active harness to be set")
	}
	if active.ID != "fake-spoke" {
		t.Errorf("expected active ID 'fake-spoke', got %q", active.ID)
	}

	// 3. Register second repo
	fakeRepo2 := filepath.Join(tmpDir, "second-spoke")
	if err := os.MkdirAll(filepath.Join(fakeRepo2, ".git"), 0755); err != nil {
		t.Fatalf("failed to create fake repo 2: %v", err)
	}
	rec2, err := reg.Register(fakeRepo2, "Second Spoke", "Domain 2", false, false)
	if err != nil {
		t.Fatalf("Register second repo failed: %v", err)
	}

	// 4. Switch
	switched, err := reg.Switch(rec2.ID)
	if err != nil {
		t.Fatalf("Switch failed: %v", err)
	}
	if switched.ID != rec2.ID {
		t.Errorf("expected switched ID %q, got %q", rec2.ID, switched.ID)
	}

	activeNow, _ := reg.GetActiveHarness()
	if activeNow.ID != rec2.ID {
		t.Errorf("expected active ID %q after switch, got %q", rec2.ID, activeNow.ID)
	}

	// 5. List
	list := reg.ListHarnesses()
	if len(list) != 2 {
		t.Errorf("expected 2 harnesses in list, got %d", len(list))
	}

	// 6. Deregister
	ok = reg.Deregister("fake-spoke")
	if !ok {
		t.Error("expected Deregister to return true")
	}

	listAfter := reg.ListHarnesses()
	if len(listAfter) != 1 {
		t.Errorf("expected 1 harness after deregister, got %d", len(listAfter))
	}
}
