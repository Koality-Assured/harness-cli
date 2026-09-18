package commands

import (
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/Koality-Assured/harness-cli/internal/tui"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:     "tui",
	Aliases: []string{"switcher"},
	Short:   "Launch interactive terminal UI switcher",
	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.RunSwitcher(registry.GetRegistry())
	},
}
