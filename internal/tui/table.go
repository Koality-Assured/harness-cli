package tui

import (
	"fmt"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/registry"
)

// FormatHarnessesTable renders an ANSI formatted table of domain harnesses.
func FormatHarnessesTable(harnesses []registry.HarnessListEntry) string {
	styles := GetStyles()
	var lines []string

	header := fmt.Sprintf("=== Registered AI Domain Harnesses (%d) ===", len(harnesses))
	lines = append(lines, styles.Bold.Render(header))

	if len(harnesses) == 0 {
		lines = append(lines, styles.Yellow.Render("\n  (No harnesses registered yet. Run 'harness scan' or 'harness register <path>').\n"))
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "")
	hdrLine := fmt.Sprintf("  %-4s %-5s %-22s %-20s %-8s %-7s %s",
		"#", "Act", "Harness ID", "Branch", "Status", "WkTrs", "Domain")
	lines = append(lines, styles.Bold.Render(hdrLine))
	lines = append(lines, "  "+strings.Repeat("-", 88))

	for idx, h := range harnesses {
		idxStr := fmt.Sprintf("[%d]", idx+1)

		activeMark := styles.Dim.Render("  no")
		hidDisplay := h.ID
		if h.IsActive {
			activeMark = styles.Green.Render("* YES")
			hidDisplay = styles.Bold.Render(styles.Cyan.Render(h.ID))
		}

		branch := h.Live.Branch
		if len(branch) > 18 {
			branch = branch[:16] + ".."
		}

		var statusDisplay string
		switch h.Live.Status {
		case "CLEAN":
			statusDisplay = styles.Green.Render("CLEAN")
		case "DIRTY":
			statusDisplay = styles.Yellow.Render("DIRTY")
		case "MISSING":
			statusDisplay = styles.Red.Render("MISSING")
		default:
			statusDisplay = styles.Dim.Render(h.Live.Status)
		}

		wts := fmt.Sprintf("%d", h.Live.WorktreesCount)
		domain := h.Domain
		if len(domain) > 35 {
			domain = domain[:33] + ".."
		}

		row := fmt.Sprintf("  %-4s %-14s %-31s %-20s %-17s %-7s %s",
			idxStr, activeMark, hidDisplay, branch, statusDisplay, wts, domain)
		lines = append(lines, row)

		pathStr := styles.Dim.Render(fmt.Sprintf("       -> %s", h.Path))
		lines = append(lines, pathStr)
	}

	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
