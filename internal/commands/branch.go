package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	branchAreas      string
	branchAgent      string
	branchType       string
	branchName       string
	branchAllowDirty bool
)

var branchCmd = &cobra.Command{
	Use:   "branch <slug>",
	Short: "Create isolated git worktree and area claim",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := strings.TrimSpace(args[0])
		if !git.SlugPattern.MatchString(slug) {
			return fmt.Errorf("slug must be kebab-case [a-z0-9-], got '%s'", slug)
		}

		var targetDir string
		if HarnessArg != "" {
			var err error
			targetDir, err = registry.ResolveHarnessRoot(HarnessArg, "")
			if err != nil {
				return err
			}
		}

		primaryRoot, err := git.GetPrimaryRepoRoot(targetDir)
		if err != nil {
			return err
		}

		// 1. Verify primary tree cleanliness
		_, isClean := git.GetStatusPorcelain(primaryRoot)
		if !isClean && !branchAllowDirty && !Force {
			return fmt.Errorf("primary repository has uncommitted changes. Stash or commit before branching (or pass --allow-dirty / --force)")
		}

		// 2. Parse and validate areas
		var areasList []string
		if branchAreas != "" {
			parts := strings.Split(branchAreas, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					areasList = append(areasList, p)
				}
			}
		}

		if len(areasList) == 0 {
			return fmt.Errorf("provide --areas (comma-separated top-level folders)")
		}

		validAreas, err := registry.LoadAreasYaml(primaryRoot)
		if err == nil && len(validAreas) > 0 {
			validMap := make(map[string]bool)
			for _, a := range validAreas {
				validMap[a] = true
			}
			var unknown []string
			for _, a := range areasList {
				if !validMap[a] {
					unknown = append(unknown, a)
				}
			}
			if len(unknown) > 0 {
				return fmt.Errorf("unknown areas: %v (from routing/areas.yaml)", unknown)
			}
		}

		// 3. Check for claim collisions
		activeClaims := git.LoadClaims(primaryRoot)
		hits := git.Overlapping(areasList, activeClaims, slug)
		if len(hits) > 0 && !Force {
			var details []string
			for _, h := range hits {
				details = append(details, fmt.Sprintf("  %s areas=%v agent=%s", h.Slug, h.Areas, h.Agent))
			}
			return fmt.Errorf("overlapping areas with active claims (pass --force to override):\n%s", strings.Join(details, "\n"))
		}

		// 4. Generate branch name
		bName := branchName
		if bName == "" {
			if branchType == "feat" {
				bName = fmt.Sprintf("feat/%s", slug)
			} else {
				today := time.Now().Format("2006-01-02")
				bName = fmt.Sprintf("agent/%s-%s", today, slug)
			}
		}

		// 5. Create worktree
		claim, err := git.AddWorktree(primaryRoot, slug, bName, areasList, branchAgent, DryRun)
		if err != nil {
			return err
		}

		if JSONOutput {
			data, err := json.MarshalIndent(claim, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		if DryRun {
			fmt.Printf("[dry-run] Would create worktree '%s' on branch '%s'\n", claim.Slug, claim.Branch)
			return nil
		}

		fmt.Printf("Created isolated worktree '%s' successfully.\n", claim.Slug)
		fmt.Printf("  Branch: %s\n", claim.Branch)
		fmt.Printf("  Areas:  %s\n", strings.Join(claim.Areas, ", "))
		fmt.Printf("  Agent:  %s\n", claim.Agent)
		fmt.Printf("  Path:   %s\n", claim.Path)
		return nil
	},
}

func init() {
	branchCmd.Flags().StringVar(&branchAreas, "areas", "", "Comma-separated top-level directory claims")
	branchCmd.Flags().StringVar(&branchAgent, "agent", "harness-operator", "Owner agent ID")
	branchCmd.Flags().StringVar(&branchType, "type", "agent", "Branch prefix type (agent or feat)")
	branchCmd.Flags().StringVar(&branchName, "branch", "", "Explicit branch name override")
	branchCmd.Flags().BoolVar(&branchAllowDirty, "allow-dirty", false, "Allow branching even if primary repository has uncommitted changes")
}
