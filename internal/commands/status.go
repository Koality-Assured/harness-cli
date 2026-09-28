package commands

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Inspect git branch, cleanliness, and active worktree claims",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Always resolve via registry: active harness when --harness is omitted,
		// otherwise the explicit --harness target. Matches claim inspect context.
		targetDir, err := registry.ResolveHarnessRoot(HarnessArg, "")
		if err != nil {
			return err
		}

		primaryRoot, err := git.GetPrimaryRepoRoot(targetDir)
		if err != nil {
			return err
		}
		checkoutRoot, err := git.GetCheckoutRoot(targetDir)
		if err != nil {
			return err
		}

		currentBranch := git.GetCurrentBranch(checkoutRoot)
		dirtyLines, isClean := git.GetStatusPorcelain(checkoutRoot)
		if dirtyLines == nil {
			dirtyLines = []string{}
		}
		claims := git.LoadClaims(primaryRoot)
		if claims == nil {
			claims = []git.Claim{}
		}

		reg := registry.GetRegistry()
		activeHarness, hasActive := reg.GetActiveHarness()

		scopedViaHarnessFlag := HarnessArg != ""
		scopeNote := ""
		if scopedViaHarnessFlag && hasActive && activeHarness != nil {
			if !samePath(activeHarness.Path, primaryRoot) {
				scopeNote = "--harness scopes git and claims reporting below; active_harness remains the global registry selection"
			}
		}

		if JSONOutput {
			payload := map[string]interface{}{
				"primary_root":      primaryRoot,
				"checkout_root":     checkoutRoot,
				"branch":            currentBranch,
				"is_clean":          isClean,
				"uncommitted_files": dirtyLines,
				"active_claims":     claims,
			}
			if hasActive && activeHarness != nil {
				payload["active_harness"] = activeHarness
			}
			if scopedViaHarnessFlag {
				payload["status_scope"] = map[string]interface{}{
					"harness_arg": HarnessArg,
					"path":        primaryRoot,
					"note":        "git and claims fields describe this path; active_harness is unchanged by --harness",
				}
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		repoTitle := filepath.Base(primaryRoot)
		if scopedRec, ok := reg.GetHarness(HarnessArg); ok && scopedViaHarnessFlag {
			repoTitle = scopedRec.ID
		} else if !scopedViaHarnessFlag && hasActive && activeHarness != nil {
			repoTitle = activeHarness.ID
		}
		fmt.Printf("=== %s Harness Status ===\n", repoTitle)
		if hasActive && activeHarness != nil {
			fmt.Printf("Active Harness: %s (%s)\n", activeHarness.ID, activeHarness.Path)
		}
		if scopedViaHarnessFlag {
			fmt.Printf("Status Scope:   %s (%s) [--harness]\n", repoTitle, primaryRoot)
			if scopeNote != "" {
				fmt.Printf("Note:           %s\n", scopeNote)
			}
		}
		fmt.Printf("Primary Root:   %s\n", primaryRoot)
		fmt.Printf("Checkout Root:  %s\n", checkoutRoot)
		fmt.Printf("Current Branch: %s\n", currentBranch)

		cleanStatus := "CLEAN"
		if !isClean {
			cleanStatus = fmt.Sprintf("DIRTY (%d uncommitted file(s))", len(dirtyLines))
		}
		fmt.Printf("Working Tree:   %s\n", cleanStatus)
		if !isClean {
			for idx, f := range dirtyLines {
				if idx >= 10 {
					fmt.Printf("  ... and %d more\n", len(dirtyLines)-10)
					break
				}
				fmt.Printf("  %s\n", f)
			}
		}

		fmt.Println("\nActive Worktree Claims:")
		if len(claims) == 0 {
			fmt.Println("  (no active claims)")
		} else {
			for _, c := range claims {
				statusTag := "OK"
				if !c.ExistsOnDisk {
					statusTag = "STALE (missing folder)"
				}
				fmt.Printf("  [%s] %s\n", statusTag, c.Slug)
				fmt.Printf("         branch: %s\n", c.Branch)
				fmt.Printf("         areas:  %s\n", strings.Join(c.Areas, ","))
				fmt.Printf("         agent:  %s\n", c.Agent)
				fmt.Printf("         path:   %s\n", c.Path)
			}
		}

		return nil
	},
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
}
