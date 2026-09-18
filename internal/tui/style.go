package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Styles provides centralized Lip Gloss styles respecting NO_COLOR and TTY presence.
type Styles struct {
	Bold    lipgloss.Style
	Green   lipgloss.Style
	Yellow  lipgloss.Style
	Cyan    lipgloss.Style
	Magenta lipgloss.Style
	Red     lipgloss.Style
	Dim     lipgloss.Style
}

// GetStyles returns initialized styles.
func GetStyles() Styles {
	noColor := os.Getenv("NO_COLOR") != ""
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))

	if noColor || !isTTY {
		return Styles{
			Bold:    lipgloss.NewStyle(),
			Green:   lipgloss.NewStyle(),
			Yellow:  lipgloss.NewStyle(),
			Cyan:    lipgloss.NewStyle(),
			Magenta: lipgloss.NewStyle(),
			Red:     lipgloss.NewStyle(),
			Dim:     lipgloss.NewStyle(),
		}
	}

	return Styles{
		Bold:    lipgloss.NewStyle().Bold(true),
		Green:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Yellow:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Cyan:    lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		Magenta: lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		Red:     lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Dim:     lipgloss.NewStyle().Faint(true),
	}
}
