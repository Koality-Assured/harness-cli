package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Claim represents a registered git worktree claim.
type Claim struct {
	Slug         string   `json:"slug"`
	Branch       string   `json:"branch"`
	Areas        []string `json:"areas"`
	Agent        string   `json:"agent"`
	CreatedAt    string   `json:"created_at"`
	Path         string   `json:"path"`
	ExistsOnDisk bool     `json:"exists_on_disk"`
	Error        string   `json:"error,omitempty"`
}

// GetWorktreesDir returns the path to scratch/worktrees under the primary repository root.
func GetWorktreesDir(primaryRoot string) string {
	return filepath.Join(primaryRoot, "scratch", "worktrees")
}

// LoadClaims reads all *.claim.json files in scratch/worktrees and checks if their folders exist.
func LoadClaims(primaryRoot string) []Claim {
	wtDir := GetWorktreesDir(primaryRoot)
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return []Claim{}
	}

	var claims []Claim
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || !filepath.HasPrefix(entry.Name(), "") {
			continue
		}
		if !hasClaimSuffix(entry.Name()) {
			continue
		}

		fullPath := filepath.Join(wtDir, entry.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var c Claim
		if err := json.Unmarshal(data, &c); err != nil {
			slug := entry.Name()[:len(entry.Name())-len(".claim.json")]
			claims = append(claims, Claim{
				Slug:         slug,
				Error:        "invalid json",
				Path:         fullPath,
				ExistsOnDisk: false,
			})
			continue
		}

		targetPath := c.Path
		if targetPath == "" {
			targetPath = filepath.Join(wtDir, c.Slug)
		}
		if _, statErr := os.Stat(targetPath); statErr == nil {
			c.ExistsOnDisk = true
		} else {
			c.ExistsOnDisk = false
		}
		claims = append(claims, c)
	}

	sort.Slice(claims, func(i, j int) bool {
		return claims[i].Slug < claims[j].Slug
	})
	return claims
}

func hasClaimSuffix(name string) bool {
	return len(name) > len(".claim.json") && name[len(name)-len(".claim.json"):] == ".claim.json"
}

// Overlapping returns all active claims that share one or more areas with wantAreas.
func Overlapping(wantAreas []string, claims []Claim, ignoreSlug string) []Claim {
	wantMap := make(map[string]bool)
	for _, a := range wantAreas {
		wantMap[a] = true
	}

	var hits []Claim
	for _, c := range claims {
		if ignoreSlug != "" && c.Slug == ignoreSlug {
			continue
		}
		for _, area := range c.Areas {
			if wantMap[area] {
				hits = append(hits, c)
				break
			}
		}
	}
	return hits
}
