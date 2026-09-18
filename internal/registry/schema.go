package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentRecord holds parsed agent metadata from ai-tooling/agents/*/AGENT.md.
type AgentRecord struct {
	AgentID           string   `json:"agent_id"`
	Name              string   `json:"name"`
	ModelTier         string   `json:"model_tier"`
	OwnershipAreas    []string `json:"ownership_areas"`
	AllowedTools      []string `json:"allowed_tools"`
	DelegationTargets []string `json:"delegation_targets"`
	IsolationModes    []string `json:"isolation_modes"`
	Description       string   `json:"description"`
	Path              string   `json:"path"`
	Body              string   `json:"body,omitempty"`
}

// ResolveHarnessRoot resolves the target directory for harness commands based on --harness flag or active registry.
func ResolveHarnessRoot(harnessArg, fallbackCwd string) (string, error) {
	reg := GetRegistry()

	if harnessArg != "" {
		if rec, ok := reg.GetHarness(harnessArg); ok {
			return rec.Path, nil
		}
		// Check if direct directory path
		abs, err := filepath.Abs(harnessArg)
		if err == nil {
			if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
				return abs, nil
			}
		}
		return "", fmt.Errorf("target harness '%s' is not registered and does not exist as a directory", harnessArg)
	}

	if active, ok := reg.GetActiveHarness(); ok {
		if fi, err := os.Stat(active.Path); err == nil && fi.IsDir() {
			return active.Path, nil
		}
	}

	if fallbackCwd != "" {
		return filepath.Abs(fallbackCwd)
	}
	return os.Getwd()
}

// LoadAreasYaml extracts valid area IDs from routing/areas.yaml.
func LoadAreasYaml(repoRoot string) ([]string, error) {
	areasPath := filepath.Join(repoRoot, "routing", "areas.yaml")
	data, err := os.ReadFile(areasPath)
	if err != nil {
		return nil, err
	}

	var areas []string
	lines := strings.Split(string(data), "\n")
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "- id:") {
			id := strings.TrimSpace(strings.TrimPrefix(trimmed, "- id:"))
			id = strings.Trim(id, `"'`)
			if id != "" {
				areas = append(areas, id)
			}
		}
	}
	return areas, nil
}

// LoadAgents discovers all agents in ai-tooling/agents/*/AGENT.md.
func LoadAgents(repoRoot string) ([]AgentRecord, error) {
	agentsDir := filepath.Join(repoRoot, "ai-tooling", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return []AgentRecord{}, nil
	}

	var agents []AgentRecord
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		rec, err := LoadAgent(repoRoot, e.Name())
		if err == nil && rec != nil {
			agents = append(agents, *rec)
		}
	}

	sort.Slice(agents, func(i, j int) bool {
		return agents[i].AgentID < agents[j].AgentID
	})
	return agents, nil
}

// LoadAgent reads and parses a specific agent specification.
func LoadAgent(repoRoot, agentID string) (*AgentRecord, error) {
	specPath := filepath.Join(repoRoot, "ai-tooling", "agents", agentID, "AGENT.md")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}

	content := string(data)
	rec := &AgentRecord{
		AgentID:           agentID,
		Name:              agentID,
		ModelTier:         "standard",
		OwnershipAreas:    []string{},
		AllowedTools:      []string{},
		DelegationTargets: []string{},
		IsolationModes:    []string{},
		Path:              specPath,
	}

	// Parse frontmatter
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	var bodyLines []string

	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if idx == 0 {
				inFrontmatter = true
				continue
			}
			if inFrontmatter {
				inFrontmatter = false
				bodyLines = lines[idx+1:]
				break
			}
		}

		if inFrontmatter {
			if strings.HasPrefix(trimmed, "name:") {
				rec.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
			} else if strings.HasPrefix(trimmed, "model_tier:") {
				rec.ModelTier = strings.TrimSpace(strings.TrimPrefix(trimmed, "model_tier:"))
			} else if strings.HasPrefix(trimmed, "description:") {
				rec.Description = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			}
		}
	}

	rec.Body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	return rec, nil
}
