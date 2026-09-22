package git

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Claim represents a registered git worktree claim.
type Claim struct {
	Slug         string   `json:"slug"`
	Branch       string   `json:"branch"`
	Areas        []string `json:"areas"`
	Agent        string   `json:"agent"`
	CreatedAt    string   `json:"created_at"`
	TTLHours     float64  `json:"ttl_hours,omitempty"`
	ExpiresAt    string   `json:"expires_at,omitempty"`
	Path         string   `json:"path"`
	ExistsOnDisk bool     `json:"exists_on_disk"`
	Error        string   `json:"error,omitempty"`
	HarnessID    string   `json:"harness_id,omitempty"`
	HarnessPath  string   `json:"harness_path,omitempty"`
	AgeHours     float64  `json:"age_hours,omitempty"`
	IsStale      bool     `json:"is_stale,omitempty"`
	StaleReason  string   `json:"stale_reason,omitempty"`
}

// GetWorktreesDir returns the path to scratch/worktrees under the primary repository root.
func GetWorktreesDir(primaryRoot string) string {
	return filepath.Join(primaryRoot, "scratch", "worktrees")
}

// GetGitClaimsDir returns the path to .git/worktree-claims under the primary repository root.
func GetGitClaimsDir(primaryRoot string) string {
	return filepath.Join(primaryRoot, ".git", "worktree-claims")
}

// ParseCreatedAt attempts to parse the CreatedAt string into a time.Time.
func (c Claim) ParseCreatedAt() (time.Time, error) {
	if c.CreatedAt == "" {
		return time.Time{}, os.ErrNotExist
	}
	// Try RFC3339Nano then RFC3339
	t, err := time.Parse(time.RFC3339Nano, c.CreatedAt)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse(time.RFC3339, c.CreatedAt)
	if err == nil {
		return t, nil
	}
	// Try ISO 8601 without timezone
	return time.Parse("2006-01-02T15:04:05", c.CreatedAt)
}

// ParseExpiresAt attempts to parse the ExpiresAt string into a time.Time.
func (c Claim) ParseExpiresAt() (time.Time, error) {
	if c.ExpiresAt == "" {
		return time.Time{}, os.ErrNotExist
	}
	t, err := time.Parse(time.RFC3339Nano, c.ExpiresAt)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse(time.RFC3339, c.ExpiresAt)
	if err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05", c.ExpiresAt)
}

// Age returns the duration elapsed since the claim was created.
func (c Claim) Age() (time.Duration, bool) {
	t, err := c.ParseCreatedAt()
	if err != nil {
		return 0, false
	}
	return time.Since(t), true
}

// CheckStale evaluates whether a claim is considered stale given a duration threshold.
func (c Claim) CheckStale(thresholdHours float64) (bool, string) {
	if !c.ExistsOnDisk {
		return true, "worktree directory does not exist on disk"
	}
	if exp, err := c.ParseExpiresAt(); err == nil {
		if time.Now().UTC().After(exp) {
			return true, "claim lease TTL expired"
		}
	} else if c.TTLHours > 0 {
		dur, ok := c.Age()
		if ok && dur.Hours() >= c.TTLHours {
			return true, fmt.Sprintf("claim lock duration exceeds TTL (%.1fh)", c.TTLHours)
		}
	}
	if thresholdHours > 0 {
		dur, ok := c.Age()
		if ok && dur.Hours() >= thresholdHours {
			return true, "claim lock duration exceeds threshold"
		}
	}
	return false, ""
}

// LoadClaims reads claim files from scratch/worktrees and .git/worktree-claims.
func LoadClaims(primaryRoot string) []Claim {
	wtDir := GetWorktreesDir(primaryRoot)
	gitClaimsDir := GetGitClaimsDir(primaryRoot)

	seenSlugs := make(map[string]bool)
	var claims []Claim

	readDir := func(dir string, isGitClaims bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			if !isGitClaims && !hasClaimSuffix(entry.Name()) {
				continue
			}

			fullPath := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}

			var c Claim
			if err := json.Unmarshal(data, &c); err != nil {
				slug := entry.Name()
				if strings.HasSuffix(slug, ".claim.json") {
					slug = slug[:len(slug)-len(".claim.json")]
				} else if strings.HasSuffix(slug, ".json") {
					slug = slug[:len(slug)-len(".json")]
				}
				if seenSlugs[slug] {
					continue
				}
				seenSlugs[slug] = true
				claims = append(claims, Claim{
					Slug:         slug,
					Error:        "invalid json",
					Path:         fullPath,
					ExistsOnDisk: false,
					IsStale:      true,
					StaleReason:  "corrupted claim file",
				})
				continue
			}

			if c.CreatedAt == "" {
				var raw map[string]interface{}
				if err := json.Unmarshal(data, &raw); err == nil {
					if cr, ok := raw["created"].(string); ok && cr != "" {
						c.CreatedAt = cr
					}
				}
			}

			if c.Slug == "" {
				name := entry.Name()
				if strings.HasSuffix(name, ".claim.json") {
					c.Slug = name[:len(name)-len(".claim.json")]
				} else if strings.HasSuffix(name, ".json") {
					c.Slug = name[:len(name)-len(".json")]
				}
			}

			if seenSlugs[c.Slug] {
				continue
			}
			seenSlugs[c.Slug] = true

			targetPath := c.Path
			if targetPath == "" {
				targetPath = filepath.Join(wtDir, c.Slug)
			}
			if _, statErr := os.Stat(targetPath); statErr == nil {
				c.ExistsOnDisk = true
			} else {
				c.ExistsOnDisk = false
			}

			if dur, ok := c.Age(); ok {
				c.AgeHours = dur.Hours()
			}
			stale, reason := c.CheckStale(24.0)
			c.IsStale = stale
			c.StaleReason = reason

			claims = append(claims, c)
		}
	}

	readDir(wtDir, false)
	readDir(gitClaimsDir, true)

	sort.Slice(claims, func(i, j int) bool {
		return claims[i].Slug < claims[j].Slug
	})
	return claims
}

func hasClaimSuffix(name string) bool {
	return (len(name) > len(".claim.json") && name[len(name)-len(".claim.json"):] == ".claim.json") ||
		(len(name) > len(".json") && name[len(name)-len(".json"):] == ".json")
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
