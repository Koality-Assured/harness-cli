package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/git"
)

var KnownDomains = map[string]string{
	"ai-router":                  "General Orchestrator & AI Router",
	"ai-harness-core":            "Domain-Agnostic Core Baseline Engine",
	"art-router":                 "Digital Art & Creative Asset Generation",
	"legal-router":               "Legal Document Analysis & Regulatory Compliance",
	"ui-ux-router":               "UI/UX Design Systems & Interface Synthesis",
	"financial-advisement-router": "Quantitative Finance & Advisement Engine",
	"game-dev-router":            "Game Systems Simulation & Logic Generation",
	"knockoutbeauty":             "E-Commerce Brand & Catalog Optimization",
}

// LiveInspection contains live git and claim metrics for a repository checkout.
type LiveInspection struct {
	Path           string `json:"path"`
	Exists         bool   `json:"exists"`
	IsGit          bool   `json:"is_git"`
	Branch         string `json:"branch"`
	Status         string `json:"status"`
	WorktreesCount int    `json:"worktrees_count"`
	ClaimsCount    int    `json:"claims_count"`
	AreasCount     int    `json:"areas_count"`
	Domain         string `json:"domain"`
}

// ScanResult holds discovery metrics from a scan pass.
type ScanResult struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Domain     string `json:"domain"`
	Branch     string `json:"branch"`
	Status     string `json:"status"`
	Registered bool   `json:"registered"`
}

// InferDomain infers a friendly domain description from repository markers.
func InferDomain(repoPath string) string {
	base := filepath.Base(repoPath)
	lower := strings.ToLower(base)
	if d, ok := KnownDomains[lower]; ok {
		return d
	}

	cfgPath := filepath.Join(repoPath, "config", "harness.config.json")
	if data, err := os.ReadFile(cfgPath); err == nil {
		var cfg map[string]interface{}
		if err := json.Unmarshal(data, &cfg); err == nil {
			if d, ok := cfg["domain"].(string); ok && d != "" {
				return d
			}
			if desc, ok := cfg["description"].(string); ok && desc != "" {
				return desc
			}
		}
	}

	agentsPath := filepath.Join(repoPath, "AGENTS.md")
	if data, err := os.ReadFile(agentsPath); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, l := range lines[:min(5, len(lines))] {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "# ") {
				return strings.TrimSpace(trimmed[2:])
			}
		}
	}

	parts := strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, " ")
}

// InspectHarness inspects a local repository directory and extracts live git and claim metrics.
func InspectHarness(repoPath string) LiveInspection {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		abs = repoPath
	}

	info := LiveInspection{
		Path:           abs,
		Exists:         false,
		IsGit:          false,
		Branch:         "(unknown)",
		Status:         "MISSING",
		WorktreesCount: 0,
		ClaimsCount:    0,
		AreasCount:     0,
		Domain:         "Unknown",
	}

	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return info
	}

	info.Exists = true
	info.Status = "UNKNOWN"
	info.Domain = InferDomain(abs)

	gitDir := filepath.Join(abs, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		info.IsGit = true
		info.Branch = git.GetCurrentBranch(abs)
		_, isClean := git.GetStatusPorcelain(abs)
		if isClean {
			info.Status = "CLEAN"
		} else {
			info.Status = "DIRTY"
		}
	}

	// Count claims & worktrees
	claims := git.LoadClaims(abs)
	info.ClaimsCount = len(claims)

	wtDir := git.GetWorktreesDir(abs)
	if entries, err := os.ReadDir(wtDir); err == nil {
		wts := 0
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				wts++
			}
		}
		info.WorktreesCount = wts
	}

	// Count areas if areas.yaml exists
	areas, _ := LoadAreasYaml(abs)
	info.AreasCount = len(areas)

	return info
}

// ScanSiblings scans the parent directory for repositories matching harness markers.
func (r *HarnessRegistry) ScanSiblings(parentDir string, autoRegister bool) ([]ScanResult, error) {
	var scanRoot string
	if parentDir != "" {
		var err error
		scanRoot, err = filepath.Abs(parentDir)
		if err != nil {
			return nil, err
		}
	} else {
		// Default to parent of cwd
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		scanRoot = filepath.Dir(cwd)
	}

	entries, err := os.ReadDir(scanRoot)
	if err != nil {
		return nil, err
	}

	var results []ScanResult
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		itemPath := filepath.Join(scanRoot, e.Name())

		// Check harness markers
		isGit := false
		if _, err := os.Stat(filepath.Join(itemPath, ".git")); err == nil {
			isGit = true
		}
		hasAreas := false
		if _, err := os.Stat(filepath.Join(itemPath, "routing", "areas.yaml")); err == nil {
			hasAreas = true
		}
		hasConfig := false
		if _, err := os.Stat(filepath.Join(itemPath, "config", "harness.config.json")); err == nil {
			hasConfig = true
		}
		hasAgents := false
		if _, err := os.Stat(filepath.Join(itemPath, "AGENTS.md")); err == nil {
			hasAgents = true
		}

		if isGit && (hasAreas || hasConfig || hasAgents) {
			meta := InspectHarness(itemPath)
			res := ScanResult{
				Name:   e.Name(),
				Path:   itemPath,
				Domain: meta.Domain,
				Branch: meta.Branch,
				Status: meta.Status,
			}

			if autoRegister {
				rec, err := r.Register(itemPath, e.Name(), meta.Domain, false, false)
				if err == nil {
					res.ID = rec.ID
					res.Registered = true
				} else {
					res.ID = SlugifyID(e.Name())
					res.Registered = false
				}
			} else {
				res.ID = SlugifyID(e.Name())
				res.Registered = false
			}

			results = append(results, res)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})
	return results, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
