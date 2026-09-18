package commands

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/spf13/cobra"
)

var (
	prBase  string
	prTitle string
	prBody  string
	prDraft bool
)

var prCmd = &cobra.Command{
	Use:   "pr",
	Short: "Run preflight validations, verify commits, and open PR",
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := exec.LookPath("gh"); err != nil {
			return fmt.Errorf("GitHub CLI ('gh') is not installed or not found in PATH")
		}

		checkoutRoot, err := git.GetCheckoutRoot("")
		if err != nil {
			return err
		}

		baseBranch := prBase
		if baseBranch == "" {
			baseBranch = "main"
		}

		currentBranch := git.GetCurrentBranch(checkoutRoot)
		if currentBranch == "" || strings.HasPrefix(currentBranch, "detached-at-") {
			return fmt.Errorf("cannot create PR from detached HEAD. Please checkout a named branch first")
		}
		if currentBranch == baseBranch {
			return fmt.Errorf("cannot create PR from base branch '%s'. Checkout a feature or agent branch", baseBranch)
		}

		fmt.Printf("Preparing PR for branch '%s' targeting '%s'...\n", currentBranch, baseBranch)

		// Verify Conventional Commits
		logOut, err := git.RunGit(checkoutRoot, "log", fmt.Sprintf("%s..HEAD", baseBranch), "--format=%s")
		if err != nil || strings.TrimSpace(logOut) == "" {
			return fmt.Errorf("no commits found on branch '%s' relative to '%s'", currentBranch, baseBranch)
		}

		var commits []string
		var nonConforming []string
		for _, line := range strings.Split(logOut, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				commits = append(commits, trimmed)
				if !git.IsConventionalCommit(trimmed) {
					nonConforming = append(nonConforming, trimmed)
				}
			}
		}

		if len(nonConforming) > 0 {
			fmt.Println("error: commits do not conform to Conventional Commits:")
			for _, m := range nonConforming {
				fmt.Printf("  - '%s'\n", m)
			}
			return fmt.Errorf("allowed types: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert")
		}

		fmt.Printf("Conventional Commits verified (%d commit(s)).\n", len(commits))

		title := prTitle
		if title == "" {
			title = commits[0]
		}

		body := prBody
		if body == "" {
			var bullets []string
			for _, m := range commits {
				bullets = append(bullets, fmt.Sprintf("- %s", m))
			}
			body = fmt.Sprintf("## Summary\n%s\n\n## Verification Checklist\n- [x] Conventional Commits verified across branch history\n", strings.Join(bullets, "\n"))
		}

		ghArgs := []string{"pr", "create", "--base", baseBranch, "--title", title, "--body", body}
		if prDraft {
			ghArgs = append(ghArgs, "--draft")
		}

		if DryRun {
			fmt.Println("\n[dry-run] Would execute:")
			fmt.Printf("gh %s\n", strings.Join(ghArgs, " "))
			fmt.Printf("\n[dry-run] PR Title: %s\n", title)
			fmt.Printf("[dry-run] PR Body:\n%s\n", body)
			return nil
		}

		fmt.Println("\nExecuting gh pr create...")
		out, err := exec.Command("gh", ghArgs...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("gh pr create failed: %w\n%s", err, string(out))
		}

		fmt.Println(strings.TrimSpace(string(out)))
		return nil
	},
}

func init() {
	prCmd.Flags().StringVar(&prBase, "base", "main", "Target base branch (default: main)")
	prCmd.Flags().StringVar(&prTitle, "title", "", "PR title (defaults to last commit message)")
	prCmd.Flags().StringVar(&prBody, "body", "", "PR description markdown")
	prCmd.Flags().BoolVar(&prDraft, "draft", false, "Create as a draft PR")
}
