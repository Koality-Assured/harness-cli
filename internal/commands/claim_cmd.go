package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	claimAllHarnesses    bool
	claimStaleHours      float64
	claimWatchInterval   string
	claimWatchStaleHours float64
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

// RunClaimWatcher polls registered domain harnesses at the specified interval and writes stale claim warnings to out.
// If autoClean is enabled, it automatically prunes stale or merged worktrees non-interactively.
// It stops when ctx is cancelled.
func RunClaimWatcher(ctx context.Context, interval time.Duration, staleHours float64, reg *registry.HarnessRegistry, out io.Writer, autoClean ...bool) error {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if staleHours <= 0 {
		staleHours = 24.0
	}
	if out == nil {
		out = os.Stderr
	}
	doAutoClean := len(autoClean) > 0 && autoClean[0]

	checkClaims := func() {
		entries := reg.ListHarnesses()
		for _, e := range entries {
			primaryRoot, err := git.GetPrimaryRepoRoot(e.Path)
			if err != nil {
				continue
			}

			mergedMap := make(map[string]bool)
			if mergedOut, err := git.RunGit(primaryRoot, "branch", "--merged", "main"); err == nil && mergedOut != "" {
				for _, b := range strings.Split(mergedOut, "\n") {
					cleanB := strings.TrimSpace(strings.TrimLeft(b, "*+ "))
					if cleanB != "" {
						mergedMap[cleanB] = true
					}
				}
			}

			claims := git.LoadClaims(primaryRoot)
			for _, c := range claims {
				stale, reason := c.CheckStale(staleHours)
				isMerged := mergedMap[c.Branch]

				if doAutoClean && (stale || isMerged) {
					pruneReason := reason
					if isMerged {
						pruneReason = "branch merged into main"
					}
					if err := git.RemoveWorktree(primaryRoot, c.Slug, true, false); err == nil {
						fmt.Fprintf(out, "[AUTO-CLEAN] Pruned worktree '%s' in %s (%s)\n", c.Slug, e.ID, pruneReason)
						continue
					}
				}

				if stale {
					ageHuman := "-"
					if dur, ok := c.Age(); ok {
						ageHuman = formatDuration(dur)
					}
					reasonStr := ""
					if reason != "" {
						reasonStr = fmt.Sprintf(" (%s)", reason)
					}
					fmt.Fprintf(out, "[WARNING] Stale worktree claim detected (> %.0fh old%s): '%s' in %s (created %s ago by agent '%s')\n",
						staleHours, reasonStr, c.Slug, e.ID, ageHuman, c.Agent)
				}
			}
		}
	}

	// Run initial inspection immediately
	checkClaims()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			checkClaims()
		}
	}
}

var (
	claimWatchAutoClean bool
)

var claimWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Continuously monitor registered domain harnesses for stale worktree claims",
	RunE: func(cmd *cobra.Command, args []string) error {
		intervalDur, err := time.ParseDuration(claimWatchInterval)
		if err != nil {
			return fmt.Errorf("invalid interval duration %q: %w", claimWatchInterval, err)
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		fmt.Printf("Starting claim watcher daemon (polling every %v, stale threshold: %.0fh, auto-clean: %v)...\n",
			intervalDur, claimWatchStaleHours, claimWatchAutoClean)
		fmt.Println("Press Ctrl+C to terminate gracefully.")

		reg := registry.GetRegistry()
		err = RunClaimWatcher(ctx, intervalDur, claimWatchStaleHours, reg, os.Stderr, claimWatchAutoClean)
		if err != nil && err != context.Canceled {
			return err
		}

		fmt.Println("\nGracefully shutting down claim watcher daemon.")
		return nil
	},
}

func init() {
	claimInspectCmd.Flags().BoolVar(&claimAllHarnesses, "all", false, "Inspect claims across all registered domain harnesses")
	claimInspectCmd.Flags().Float64Var(&claimStaleHours, "stale-hours", 24.0, "Threshold in hours before a lock claim is considered stale")

	claimWatchCmd.Flags().StringVar(&claimWatchInterval, "interval", "5m", "Polling interval for claim status check (e.g. 30s, 5m, 1h)")
	claimWatchCmd.Flags().Float64Var(&claimWatchStaleHours, "stale-hours", 24.0, "Threshold in hours before a lock claim is considered stale")
	claimWatchCmd.Flags().BoolVar(&claimWatchAutoClean, "auto-clean", false, "Automatically prune expired or merged worktrees")

	claimCmd.AddCommand(claimInspectCmd)
	claimCmd.AddCommand(claimWatchCmd)
}
