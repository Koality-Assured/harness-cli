package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlugifyID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "harness"},
		{"  ", "harness"},
		{"My Project", "my-project"},
		{"NUL", "harness-nul"},
		{"con", "harness-con"},
		{"some_under_score", "some-under-score"},
		{"multiple---dashes", "multiple-dashes"},
		{"-leading-and-trailing-", "leading-and-trailing"},
		{"special@#$chars", "special-chars"},
	}

	for _, tt := range tests {
		if got := SlugifyID(tt.input); got != tt.expected {
			t.Errorf("SlugifyID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestHarnessRegistry_Register_And_Get(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "config.json")
	reg := NewHarnessRegistry(regPath)

	repoPath := filepath.Join(tmpDir, "repo1")
	if err := os.Mkdir(repoPath, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	// Attempt to register empty dir without force
	_, err := reg.Register(repoPath, "Repo 1", "test.com", true, false)
	if err == nil {
		t.Errorf("Expected error registering non-harness without force")
	}

	// Register with force
	rec, err := reg.Register(repoPath, "Repo 1", "test.com", true, true)
	if err != nil {
		t.Fatalf("Failed to register: %v", err)
	}

	if rec.ID != "repo-1" {
		t.Errorf("Expected ID 'repo-1', got %q", rec.ID)
	}

	got, ok := reg.GetHarness(rec.ID)
	if !ok || got.ID != rec.ID {
		t.Errorf("GetHarness failed")
	}

	active, ok := reg.GetActiveHarness()
	if !ok || active.ID != rec.ID {
		t.Errorf("GetActiveHarness failed")
	}

	// Register another repo to test collision resolution and ID generation
	repoPath2 := filepath.Join(tmpDir, "repo-1")
	if err := os.Mkdir(repoPath2, 0755); err != nil {
		t.Fatalf("failed to create repo2 dir: %v", err)
	}
	rec2, err := reg.Register(repoPath2, "", "test2.com", false, true)
	if err != nil {
		t.Fatalf("Failed to register: %v", err)
	}
	if rec2.ID != "repo-1-2" {
		t.Errorf("Expected ID 'repo-1-2', got %q", rec2.ID)
	}
}

func TestHarnessRegistry_Switch_And_Deregister(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "config.json")
	reg := NewHarnessRegistry(regPath)

	repoPath1 := filepath.Join(tmpDir, "repo1")
	if err := os.Mkdir(repoPath1, 0755); err != nil {
		t.Fatalf("failed to create repo1 dir: %v", err)
	}
	repoPath2 := filepath.Join(tmpDir, "repo2")
	if err := os.Mkdir(repoPath2, 0755); err != nil {
		t.Fatalf("failed to create repo2 dir: %v", err)
	}

	rec1, _ := reg.Register(repoPath1, "Repo 1", "", true, true)
	rec2, _ := reg.Register(repoPath2, "Repo 2", "", false, true)

	// Switch to rec2
	switched, err := reg.Switch(rec2.ID)
	if err != nil {
		t.Fatalf("Switch failed: %v", err)
	}
	if switched.ID != rec2.ID {
		t.Errorf("Switch returned wrong record")
	}

	active, _ := reg.GetActiveHarness()
	if active.ID != rec2.ID {
		t.Errorf("Expected active to be rec2")
	}

	// Deregister rec2
	if ok := reg.Deregister(rec2.ID); !ok {
		t.Errorf("Deregister failed")
	}

	// Verify it was removed
	_, ok := reg.GetHarness(rec2.ID)
	if ok {
		t.Errorf("Harness should be removed")
	}

	// Active harness should fall back to rec1
	active, ok = reg.GetActiveHarness()
	if !ok || active.ID != rec1.ID {
		t.Errorf("Active harness not fell back to rec1")
	}

	// Deregister rec1
	reg.Deregister(rec1.ID)
	_, ok = reg.GetActiveHarness()
	if ok {
		t.Errorf("Active harness should be nil")
	}
}

func TestHarnessRegistry_CorruptFile(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "config.json")
	
	err := os.WriteFile(regPath, []byte("invalid json"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewHarnessRegistry(regPath)
	// load should recover
	rd := reg.load()
	if rd.Version != "1.0.0" {
		t.Errorf("Expected default registry due to corrupt file")
	}
}

func TestHarnessRegistry_ListHarnesses(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "config.json")
	reg := NewHarnessRegistry(regPath)

	repoPath1 := filepath.Join(tmpDir, "repo1")
	if err := os.Mkdir(repoPath1, 0755); err != nil {
		t.Fatalf("failed to create repo1 dir: %v", err)
	}
	if _, err := reg.Register(repoPath1, "Repo 1", "", true, true); err != nil {
		t.Fatalf("failed to register repo: %v", err)
	}

	list := reg.ListHarnesses()
	if len(list) != 1 {
		t.Fatalf("Expected 1 harness, got %d", len(list))
	}
	if list[0].ID != "repo-1" || !list[0].IsActive {
		t.Errorf("List data mismatch")
	}
}
