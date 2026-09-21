package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	claimAllHarnesses bool
	claimStaleHours   float64
)

var claimCmd = &cobra.Command{
	Use:   "claim",
	Short: "Inspect and manage active worktree claims and concurrency locks",
}

type ClaimReportItem struct {
	HarnessID    string   `json:"harness_id"`
	HarnessPath  string   `json:"harness_path"`
	Slug         string   `json:"slug"`
	Branch       string   `json:"branch"`
	Areas        []string `json:"areas"`
	Agent        string   `json:"agent"`
	CreatedAt    string   `json:"created_at"`
	AgeHuman     string   `json:"age_human"`
	AgeHours     float64  `json:"age_hours"`
	ExistsOnDisk bool     `json:"exists_on_disk"`
	IsStale      bool     `json:"is_stale"`
	StaleReason  string   `json:"stale_reason,omitempty"`
}

type ClaimInspectReport struct {
	TotalClaims int               `json:"total_claims"`
	StaleClaims int               `json:"stale_claims"`
	ThresholdH  float64           `json:"stale_threshold_hours"`
	Claims      []ClaimReportItem `json:"claims"`
	Warnings    []string          `json:"warnings"`
}

var claimInspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Inspect active worktree claims and detect stale concurrency locks",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg := registry.GetRegistry()

		var targets []registry.HarnessRecord
		if claimAllHarnesses {
			entries := reg.ListHarnesses()
			for _, e := range entries {
				targets = append(targets, e.HarnessRecord)
			}
		} else if HarnessArg != "" {
			targetDir, err := registry.ResolveHarnessRoot(HarnessArg, "")
			if err != nil {
				return err
			}
			hRec, found := reg.GetHarness(HarnessArg)
			if found {
				targets = append(targets, *hRec)
			} else {
				targets = append(targets, registry.HarnessRecord{
					ID:   "target",
					Path: targetDir,
				})
			}
		} else {
			// Use current or active harness
			targetDir, err := registry.ResolveHarnessRoot("", "")
			if err != nil {
				return err
			}
			active, found := reg.GetActiveHarness()
			if found && active.Path == targetDir {
				targets = append(targets, *active)
			} else {
				targets = append(targets, registry.HarnessRecord{
					ID:   "current",
					Path: targetDir,
				})
			}
		}

		threshold := claimStaleHours
		if threshold <= 0 {
			threshold = 24.0
		}

		var reportItems []ClaimReportItem
		var warnings []string
		staleCount := 0

		for _, h := range targets {
			primaryRoot, err := git.GetPrimaryRepoRoot(h.Path)
			if err != nil {
				continue
			}

			claims := git.LoadClaims(primaryRoot)
			for _, c := range claims {
				stale, reason := c.CheckStale(threshold)
				c.IsStale = stale
				c.StaleReason = reason

				ageHuman := "-"
				if dur, ok := c.Age(); ok {
					ageHuman = formatDuration(dur)
					c.AgeHours = dur.Hours()
				}

				item := ClaimReportItem{
					HarnessID:    h.ID,
					HarnessPath:  h.Path,
					Slug:         c.Slug,
					Branch:       c.Branch,
					Areas:        c.Areas,
					Agent:        c.Agent,
					CreatedAt:    c.CreatedAt,
					AgeHuman:     ageHuman,
					AgeHours:     c.AgeHours,
					ExistsOnDisk: c.ExistsOnDisk,
					IsStale:      c.IsStale,
					StaleReason:  c.StaleReason,
				}

				if c.IsStale {
					staleCount++
					warn := fmt.Sprintf("Stale worktree claim detected (> %.0fh old): '%s' in %s (created %s ago by agent '%s')",
						threshold, c.Slug, h.ID, ageHuman, c.Agent)
					warnings = append(warnings, warn)
				}

				reportItems = append(reportItems, item)
			}
		}

		report := ClaimInspectReport{
			TotalClaims: len(reportItems),
			StaleClaims: staleCount,
			ThresholdH:  threshold,
			Claims:      reportItems,
			Warnings:    warnings,
		}

		if JSONOutput {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Println("=== Active Worktree Claims & Lock Inspector ===")
		fmt.Printf("Stale Threshold: %.0f hours | Total Claims: %d | Stale Claims: %d\n\n",
			threshold, len(reportItems), staleCount)

		if len(warnings) > 0 {
			for _, w := range warnings {
				fmt.Fprintf(os.Stderr, "[WARNING] %s\n", w)
			}
			fmt.Println()
		}

		if len(reportItems) == 0 {
			fmt.Println("  (no active claims or concurrency locks found)")
			return nil
		}

		fmt.Printf("%-14s %-20s %-20s %-12s %-12s %-8s %s\n",
			"Harness", "Slug", "Branch", "Agent", "Age", "Disk?", "Status")
		fmt.Println(strings.Repeat("-", 96))
		for _, item := range reportItems {
			diskStr := "yes"
			if !item.ExistsOnDisk {
				diskStr = "MISSING"
			}
			statusStr := "ACTIVE"
			if item.IsStale {
				statusStr = fmt.Sprintf("STALE (%s)", item.StaleReason)
			}
			fmt.Printf("%-14s %-20s %-20s %-12s %-12s %-8s %s\n",
				item.HarnessID, item.Slug, item.Branch, item.Agent, item.AgeHuman, diskStr, statusStr)
		}

		return nil
	},
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	if hours < 24 {
		mins := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	days := hours / 24
	remHours := hours % 24
	return fmt.Sprintf("%dd%dh", days, remHours)
}

func init() {
	claimInspectCmd.Flags().BoolVar(&claimAllHarnesses, "all", false, "Inspect claims across all registered domain harnesses")
	claimInspectCmd.Flags().Float64Var(&claimStaleHours, "stale-hours", 24.0, "Threshold in hours before a lock claim is considered stale")

	claimCmd.AddCommand(claimInspectCmd)
}
