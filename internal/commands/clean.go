package commands

import (
	"fmt"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	cleanSlug   string
	cleanMerged bool
	cleanStale  bool
	cleanAll    bool
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Prune merged worktrees and delete stale claims",
	RunE: func(cmd *cobra.Command, args []string) error {
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

		claims := git.LoadClaims(primaryRoot)

		// Get merged branches
		mergedOut, _ := git.RunGit(primaryRoot, "branch", "--merged", "main")
		mergedMap := make(map[string]bool)
		for _, b := range strings.Split(mergedOut, "\n") {
			cleanB := strings.TrimSpace(strings.TrimLeft(b, "*+ "))
			if cleanB != "" {
				mergedMap[cleanB] = true
			}
		}

		if cleanSlug == "" && !cleanMerged && !cleanStale && !cleanAll {
			fmt.Println("=== Worktree Cleanup Candidates ===")
			if len(claims) == 0 {
				fmt.Println("  (no active worktrees or claims)")
				return nil
			}
			for _, c := range claims {
				isMerged := mergedMap[c.Branch]
				var tags []string
				if !c.ExistsOnDisk {
					tags = append(tags, "STALE")
				}
				if isMerged {
					tags = append(tags, "MERGED")
				}
				if len(tags) == 0 {
					tags = append(tags, "ACTIVE")
				}
				fmt.Printf("  [%s] %s (%s)\n", strings.Join(tags, "/"), c.Slug, c.Branch)
			}
			fmt.Println("\nSpecify --merged, --stale, --slug <slug>, or --all to clean.")
			return nil
		}

		var targets []string
		for _, c := range claims {
			isMerged := mergedMap[c.Branch]
			isStale := !c.ExistsOnDisk

			if cleanSlug != "" && c.Slug == cleanSlug {
				targets = append(targets, c.Slug)
			} else if cleanAll {
				targets = append(targets, c.Slug)
			} else if cleanMerged && isMerged {
				targets = append(targets, c.Slug)
			} else if cleanStale && isStale {
				targets = append(targets, c.Slug)
			}
		}

		if len(targets) == 0 {
			fmt.Println("No matching worktrees found to clean.")
			return nil
		}

		fmt.Printf("Cleaning %d worktree(s): %s\n", len(targets), strings.Join(targets, ", "))
		for _, slug := range targets {
			if err := git.RemoveWorktree(primaryRoot, slug, Force, DryRun); err != nil {
				fmt.Printf("warning: failed to remove worktree '%s': %v\n", slug, err)
			}
		}

		return nil
	},
}

func init() {
	cleanCmd.Flags().StringVar(&cleanSlug, "slug", "", "Specific worktree slug to clean")
	cleanCmd.Flags().BoolVar(&cleanMerged, "merged", false, "Clean all merged worktrees")
	cleanCmd.Flags().BoolVar(&cleanStale, "stale", false, "Clean all stale claims")
	cleanCmd.Flags().BoolVar(&cleanAll, "all", false, "Clean all worktrees and claims")
}
