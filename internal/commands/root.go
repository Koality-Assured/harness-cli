package commands

import (
	"os"

	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/Koality-Assured/harness-cli/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	JSONOutput bool
	DryRun     bool
	Force      bool
	HarnessArg string
)

// RootCmd is the top-level CLI command for harness.
var RootCmd = &cobra.Command{
	Use:   "harness",
	Short: "Unified Harness CLI Control Plane for domain harnesses and spokes",
	Long: `A high-performance, cross-platform compiled binary control plane for human operators
and autonomous AI agents to discover, interact with, authenticate, and coordinate domain harnesses.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return tui.RunSwitcher(registry.GetRegistry())
		}
		if JSONOutput {
			return runList(cmd, args)
		}
		return cmd.Help()
	},
}

func init() {
	RootCmd.PersistentFlags().BoolVar(&JSONOutput, "json", false, "Format output as machine-readable JSON")
	RootCmd.PersistentFlags().BoolVar(&DryRun, "dry-run", false, "Simulate operation without mutating state")
	RootCmd.PersistentFlags().BoolVar(&Force, "force", false, "Override safety checks")
	RootCmd.PersistentFlags().StringVar(&HarnessArg, "harness", "", "Target specific registered domain harness by ID or path")

	RootCmd.AddCommand(statusCmd)
	RootCmd.AddCommand(listCmd)
	RootCmd.AddCommand(switchCmd)
	RootCmd.AddCommand(branchCmd)
	RootCmd.AddCommand(cleanCmd)
	RootCmd.AddCommand(agentCmd)
	RootCmd.AddCommand(prCmd)
	RootCmd.AddCommand(authCmd)
	RootCmd.AddCommand(registerCmd)
	RootCmd.AddCommand(deregisterCmd)
	RootCmd.AddCommand(scanCmd)
	RootCmd.AddCommand(tuiCmd)
}

// Execute runs the root command.
func Execute() error {
	return RootCmd.Execute()
}
