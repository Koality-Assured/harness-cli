package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	spokeSyncAll     bool
	spokeSyncRef     string
	spokeSyncNoFetch bool
)

var spokeCmd = &cobra.Command{
	Use:   "spoke",
	Short: "Coordinate domain spoke harnesses and synchronize baseline core updates",
}

type SpokeSyncResult struct {
	OK                  bool     `json:"ok"`
	Spoke               string   `json:"spoke"`
	SpokeID             string   `json:"spoke_id"`
	Ref                 string   `json:"ref"`
	DryRun              bool     `json:"dry_run"`
	Branch              string   `json:"branch,omitempty"`
	BaseBranch          string   `json:"base_branch,omitempty"`
	Updates             []string `json:"updates"`
	SkippedDomain       []string `json:"skipped_domain"`
	RegeneratedIndexes  []string `json:"regenerated_indexes"`
	Warnings            []string `json:"warnings,omitempty"`
	Error               string   `json:"error,omitempty"`
}

var (
	coreRemoteName = "harness-core"

	// Files that are allowlisted root or core checkout files
	allowlistedRootFiles = map[string]bool{
		"AGENTS.md":                          true,
		"CLAUDE.md":                          true,
		"GEMINI.md":                          true,
		".cursorignore":                      true,
		".cursorindexingignore":              true,
		"naming-conventions.md":              true,
		".gitignore":                         true,
		".markdownlint-cli2.jsonc":           true,
		"sgconfig.yml":                       true,
		"harness.cmd":                        true,
		"harness.ps1":                        true,
		"harness.sh":                         true,
		"README.md":                          true,
		"LICENSE":                            true,
		".editorconfig":                      true,
		".github/workflows/ci.yml":           true,
		"routing/skill-dispatch.md":          true,
		"routing/area-map.md":                true,
		"routing/agent-dispatch.md":          true,
		"routing/by-task.md":                 true,
		"routing/AGENTS.md":                  true,
		"scripts/script-index.md":            true,
		"docs/standards/AGENTS.md":           true,
		"references/AGENTS.md":               true,
		"projects/project-prompts/README.md": true,
	}

	// Drop families that represent instance or vendor-only plugins
	dropSkillFamilies = map[string]bool{
		"admin":      true,
		"aws":        true,
		"azure":      true,
		"community":  true,
		"confluence": true,
		"discovery":  true,
		"gcp":        true,
		"google":     true,
		"iac":        true,
		"security":   true,
		"slack":      true,
	}

	instanceLeakMarkers = []string{
		"references/owasp",
		"references/nist-csf",
		"references/cwe",
		"docs/standards/identity-and-access.md",
		"ai-tooling/skills/aws",
		"ai-tooling/skills/slack",
		"ai-tooling/skills/confluence",
		"ai-tooling/skills/google",
		"projects/secpanic-idler",
		"research/secpanic-idler",
	}
)

func posixRel(path string) string {
	clean := strings.ReplaceAll(path, "\\", "/")
	clean = strings.TrimPrefix(clean, "./")
	return strings.Trim(clean, "/")
}

// isAllowlistedCorePath implements the allowlist protocol from _harness_core_protocol.py.
func isAllowlistedCorePath(rel string, spokeRoot string) (bool, string) {
	posix := posixRel(rel)

	// 1. Check root files
	if allowlistedRootFiles[posix] {
		return true, "root-allowlist"
	}

	// 2. Check for domain overlays and markers
	if strings.HasPrefix(posix, ".harness/") {
		return false, "spoke-domain-marker"
	}
	if strings.HasPrefix(posix, "docs/standards/") && strings.HasSuffix(posix, "-overlay.md") {
		return false, "spoke-domain-overlay"
	}

	// 3. Check for instance leak markers
	for _, marker := range instanceLeakMarkers {
		if posix == marker || strings.HasPrefix(posix, marker+"/") {
			return false, "instance-leak-marker"
		}
	}

	parts := strings.Split(posix, "/")

	// 4. routing/areas.yaml: MUST NOT overwrite existing domain taxonomy
	if posix == "routing/areas.yaml" {
		if _, err := os.Stat(filepath.Join(spokeRoot, "routing", "areas.yaml")); err == nil {
			return false, "preserved-spoke-domain-taxonomy"
		}
		return true, "core-default-areas"
	}

	// 5. Scripts directory: allow core tooling
	if len(parts) >= 2 && parts[0] == "scripts" {
		if parts[1] == "_lib" || parts[1] == "cli" || parts[1] == "sync" || parts[1] == "routing" || parts[1] == "docs" || parts[1] == "cost-layers" {
			return true, "core-script-tooling"
		}
	}

	// 6. ai-tooling/skills: allow generic skills, filter drop families
	if len(parts) >= 3 && parts[0] == "ai-tooling" && parts[1] == "skills" {
		fam := parts[2]
		if dropSkillFamilies[fam] {
			return false, "instance-skill-family"
		}
		return true, "core-skill"
	}

	// 7. ai-tooling/agents: filter non-template agents
	if len(parts) >= 3 && parts[0] == "ai-tooling" && parts[1] == "agents" {
		if parts[2] == "AGENTS.md" || parts[2] == "model-tiers.md" || parts[2] == "README.md" {
			return true, "core-agent-docs"
		}
		// Generic baseline operators
		agentID := parts[2]
		if agentID == "router" || agentID == "meta" || agentID == "harness-operator" || agentID == "document-operator" || agentID == "research-operator" {
			return true, "core-agent"
		}
		return false, "domain-agent"
	}

	// 8. Instance memory and research
	if len(parts) >= 3 && parts[0] == "ai-tooling" && parts[1] == "memory" && parts[2] == "user" {
		return false, "instance-user-memory"
	}
	if len(parts) >= 2 && parts[0] == "projects" && parts[1] != "notes" && parts[1] != "project-prompts" && parts[1] != "AGENTS.md" {
		return false, "instance-project"
	}
	if len(parts) >= 2 && parts[0] == "research" && parts[1] != "AGENTS.md" && parts[1] != "README.md" {
		return false, "instance-research"
	}

	return false, "unclassified-domain-file"
}

func syncSingleSpoke(spokePath, spokeID, ref string, dryRun, noFetch bool) SpokeSyncResult {
	res := SpokeSyncResult{
		OK:      true,
		Spoke:   spokePath,
		SpokeID: spokeID,
		Ref:     fmt.Sprintf("%s/%s", coreRemoteName, ref),
		DryRun:  dryRun,
		Updates: []string{},
		SkippedDomain: []string{},
		RegeneratedIndexes: []string{},
	}

	primaryRoot, err := git.GetPrimaryRepoRoot(spokePath)
	if err != nil {
		res.OK = false
		res.Error = fmt.Sprintf("invalid spoke git repository at '%s': %v", spokePath, err)
		return res
	}

	// 1. Verify remote exists
	remotesOut, err := git.RunGit(primaryRoot, "remote", "-v")
	if err != nil || !strings.Contains(remotesOut, coreRemoteName) {
		res.OK = false
		res.Error = fmt.Sprintf("spoke repository at '%s' is missing remote '%s'; scaffold with scripts/sync/scaffold_harness.py", spokePath, coreRemoteName)
		return res
	}

	// Get base branch
	baseBranch, _ := git.RunGit(primaryRoot, "rev-parse", "--abbrev-ref", "HEAD")
	res.BaseBranch = strings.TrimSpace(baseBranch)

	// 2. Fetch harness-core unless noFetch or dryRun
	if !noFetch && !dryRun {
		_, err := git.RunGit(primaryRoot, "fetch", coreRemoteName, ref)
		if err != nil {
			res.OK = false
			res.Error = fmt.Sprintf("git fetch %s %s failed: %v", coreRemoteName, ref, err)
			return res
		}
	}

	coreRef := fmt.Sprintf("%s/%s", coreRemoteName, ref)
	// Verify core ref is available
	_, err = git.RunGit(primaryRoot, "rev-parse", "--verify", coreRef)
	if err != nil {
		if dryRun {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s is not available locally; run 'git fetch %s' or omit --dry-run", coreRef, coreRemoteName))
			return res
		}
		res.OK = false
		res.Error = fmt.Sprintf("core ref '%s' not found in repository", coreRef)
		return res
	}

	// 3. Diff analysis
	diffOut, err := git.RunGit(primaryRoot, "diff", "--name-only", "--diff-filter=d", "HEAD", coreRef)
	if err != nil {
		// Fallback for unborn branch / no commits
		diffOut, err = git.RunGit(primaryRoot, "ls-tree", "-r", "--name-only", coreRef)
		if err != nil {
			res.OK = false
			res.Error = fmt.Sprintf("failed to diff against %s: %v", coreRef, err)
			return res
		}
		res.Warnings = append(res.Warnings, "spoke has no commits yet; comparing against tree of core ref")
	}

	var candidateFiles []string
	for _, line := range strings.Split(diffOut, "\n") {
		clean := strings.TrimSpace(line)
		if clean != "" {
			candidateFiles = append(candidateFiles, clean)
		}
	}

	// 4. Allowlist Filtering
	for _, rel := range candidateFiles {
		allow, reason := isAllowlistedCorePath(rel, primaryRoot)
		if allow {
			res.Updates = append(res.Updates, rel)
		} else {
			res.SkippedDomain = append(res.SkippedDomain, fmt.Sprintf("%s (%s)", rel, reason))
		}
	}

	if dryRun || len(res.Updates) == 0 {
		return res
	}

	// 5. Checkout onto new branch
	stamp := time.Now().UTC().Format("20060102T150405Z")
	branchName := fmt.Sprintf("chore/pull-harness-core-%s", stamp)
	res.Branch = branchName

	if _, err := git.RunGit(primaryRoot, "checkout", "-b", branchName); err != nil {
		res.OK = false
		res.Error = fmt.Sprintf("failed to create branch '%s': %v", branchName, err)
		return res
	}

	checkoutArgs := append([]string{"checkout", coreRef, "--"}, res.Updates...)
	if _, err := git.RunGit(primaryRoot, checkoutArgs...); err != nil {
		res.OK = false
		res.Error = fmt.Sprintf("failed to checkout allowlisted paths: %v", err)
		return res
	}

	// 6. Run Index Generators if present
	pyBin := resolvePython()
	routingGen := filepath.Join(primaryRoot, "scripts", "routing", "generate_routing_index.py")
	if _, err := os.Stat(routingGen); err == nil {
		cmd := exec.Command(pyBin, routingGen)
		cmd.Dir = primaryRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("routing index regeneration warning: %s (%v)", string(out), err))
		} else {
			res.RegeneratedIndexes = append(res.RegeneratedIndexes, "routing/area-map.md", "routing/skill-dispatch.md", "routing/agent-dispatch.md")
		}
	}

	scriptGen := filepath.Join(primaryRoot, "scripts", "routing", "generate_script_index.py")
	if _, err := os.Stat(scriptGen); err == nil {
		cmd := exec.Command(pyBin, scriptGen)
		cmd.Dir = primaryRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("script index regeneration warning: %s (%v)", string(out), err))
		} else {
			res.RegeneratedIndexes = append(res.RegeneratedIndexes, "scripts/script-index.md")
		}
	}

	return res
}

// resolvePython dynamically resolves python3 then python for cross-platform execution.
func resolvePython() string {
	if p, err := exec.LookPath("python3"); err == nil {
		return p
	}
	if p, err := exec.LookPath("python"); err == nil {
		return p
	}
	return "python"
}

var spokeSyncCmd = &cobra.Command{
	Use:   "sync [spoke-id|--all]",
	Short: "Synchronize baseline core updates into domain spoke harnesses",
	Long: `Pulls allowlisted files from the upstream ai-harness-core template into domain spoke harnesses.
Strictly preserves spoke-specific domain taxonomy in routing/areas.yaml and isolates updates on a feature branch.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		reg := registry.GetRegistry()
		ref := spokeSyncRef
		if ref == "" {
			ref = "main"
		}

		type Target struct {
			ID   string
			Path string
		}
		var targets []Target

		if spokeSyncAll {
			entries := reg.ListHarnesses()
			for _, e := range entries {
				// Don't sync the core repo or ai-router orchestrator if registered
				if strings.Contains(strings.ToLower(e.Name), "core") || strings.Contains(strings.ToLower(e.ID), "core") ||
					strings.EqualFold(e.ID, "ai-router") || strings.EqualFold(e.Name, "ai-router") {
					continue
				}
				targets = append(targets, Target{ID: e.ID, Path: e.Path})
			}
			if len(targets) == 0 {
				return fmt.Errorf("no domain spoke harnesses found in registry to sync")
			}
		} else if len(args) > 0 {
			spokeID := args[0]
			targetPath, err := registry.ResolveHarnessRoot(spokeID, "")
			if err != nil {
				return err
			}
			targets = append(targets, Target{ID: spokeID, Path: targetPath})
		} else if HarnessArg != "" {
			targetPath, err := registry.ResolveHarnessRoot(HarnessArg, "")
			if err != nil {
				return err
			}
			targets = append(targets, Target{ID: HarnessArg, Path: targetPath})
		} else {
			// Current repo
			targetPath, err := registry.ResolveHarnessRoot("", "")
			if err != nil {
				return err
			}
			id := filepath.Base(targetPath)
			if active, ok := reg.GetActiveHarness(); ok && active.Path == targetPath {
				id = active.ID
			}
			targets = append(targets, Target{ID: id, Path: targetPath})
		}

		var results []SpokeSyncResult
		for _, t := range targets {
			res := syncSingleSpoke(t.Path, t.ID, ref, DryRun, spokeSyncNoFetch)
			results = append(results, res)
		}

		if JSONOutput {
			data, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		modeStr := "LIVE"
		if DryRun {
			modeStr = "DRY-RUN"
		}

		fmt.Printf("=== Spoke Core Synchronization (%s) ===\n\n", modeStr)
		for _, r := range results {
			statusStr := "SUCCESS"
			if !r.OK {
				statusStr = "FAILED"
			}
			fmt.Printf("Spoke: %s (%s) -> [%s]\n", r.SpokeID, r.Spoke, statusStr)
			fmt.Printf("  Ref:        %s\n", r.Ref)
			if r.Branch != "" {
				fmt.Printf("  Branch:     %s (from %s)\n", r.Branch, r.BaseBranch)
			}
			if r.Error != "" {
				fmt.Printf("  Error:      %s\n", r.Error)
			}
			if len(r.Warnings) > 0 {
				for _, w := range r.Warnings {
					fmt.Printf("  Warning:    %s\n", w)
				}
			}
			fmt.Printf("  Updates (%d):\n", len(r.Updates))
			if len(r.Updates) == 0 {
				fmt.Println("    (no allowlisted updates)")
			} else {
				for _, u := range r.Updates {
					fmt.Printf("    + %s\n", u)
				}
			}
			if len(r.SkippedDomain) > 0 {
				fmt.Printf("  Preserved Domain Items (%d):\n", len(r.SkippedDomain))
				for _, s := range r.SkippedDomain {
					fmt.Printf("    - %s\n", s)
				}
			}
			if len(r.RegeneratedIndexes) > 0 {
				fmt.Printf("  Regenerated Indexes (%d):\n", len(r.RegeneratedIndexes))
				for _, re := range r.RegeneratedIndexes {
					fmt.Printf("    * %s\n", re)
				}
			}
			fmt.Println()
		}

		return nil
	},
}

func init() {
	spokeSyncCmd.Flags().BoolVar(&spokeSyncAll, "all", false, "Synchronize all registered domain spoke harnesses")
	spokeSyncCmd.Flags().StringVar(&spokeSyncRef, "ref", "main", "Upstream harness-core branch or git ref to pull")
	spokeSyncCmd.Flags().BoolVar(&spokeSyncNoFetch, "no-fetch", false, "Skip git fetch and use existing local remote-tracking ref")

	spokeCmd.AddCommand(spokeSyncCmd)
}
