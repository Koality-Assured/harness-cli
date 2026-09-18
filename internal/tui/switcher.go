package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/registry"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

type model struct {
	reg       *registry.HarnessRegistry
	harnesses []registry.HarnessListEntry
	message   string
	quitting  bool
}

func initialModel(reg *registry.HarnessRegistry) model {
	return model{
		reg:       reg,
		harnesses: reg.ListHarnesses(),
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		case "r":
			m.harnesses = m.reg.ListHarnesses()
			m.message = "Refreshed harness list."
			return m, nil
		case "s":
			discovered, err := m.reg.ScanSiblings("", true)
			if err != nil {
				m.message = fmt.Sprintf("Scan failed: %v", err)
			} else {
				m.harnesses = m.reg.ListHarnesses()
				m.message = fmt.Sprintf("Scanned and discovered %d harnesses.", len(discovered))
			}
			return m, nil
		default:
			// Check if digit selection (1-9)
			if len(msg.String()) == 1 && msg.String()[0] >= '1' && msg.String()[0] <= '9' {
				idx, _ := strconv.Atoi(msg.String())
				idx--
				if idx >= 0 && idx < len(m.harnesses) {
					target := m.harnesses[idx]
					rec, err := m.reg.Switch(target.ID)
					if err != nil {
						m.message = fmt.Sprintf("Failed to switch: %v", err)
					} else {
						m.harnesses = m.reg.ListHarnesses()
						m.message = fmt.Sprintf("Switched active harness to '%s'.", rec.ID)
					}
					return m, nil
				}
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	styles := GetStyles()
	var b strings.Builder

	b.WriteString(FormatHarnessesTable(m.harnesses))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString(styles.Green.Render(fmt.Sprintf("  => %s\n\n", m.message)))
	}

	b.WriteString(styles.Bold.Render("Actions:\n"))
	if len(m.harnesses) > 0 {
		maxIdx := len(m.harnesses)
		if maxIdx > 9 {
			maxIdx = 9
		}
		b.WriteString(fmt.Sprintf("  %s Switch active harness\n", styles.Cyan.Render(fmt.Sprintf("[1-%d]", maxIdx))))
	}
	b.WriteString(fmt.Sprintf("  %s     Scan sibling repositories for harnesses\n", styles.Cyan.Render("[s]")))
	b.WriteString(fmt.Sprintf("  %s     Refresh view\n", styles.Cyan.Render("[r]")))
	b.WriteString(fmt.Sprintf("  %s     Quit switcher\n\n", styles.Cyan.Render("[q]")))

	return b.String()
}

// RunSwitcher launches the interactive terminal switcher loop.
func RunSwitcher(reg *registry.HarnessRegistry) error {
	// If non-interactive stdin, render table and return immediately
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		harnesses := reg.ListHarnesses()
		fmt.Println(FormatHarnessesTable(harnesses))
		return nil
	}

	p := tea.NewProgram(initialModel(reg))
	_, err := p.Run()
	return err
}
