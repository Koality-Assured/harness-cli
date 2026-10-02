package commands

import (
	"fmt"
	"os"
	"strings"

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
		// --json without a subcommand must not dump the registry (owen-02).
		if JSONOutput {
			return fmt.Errorf("a command is required")
		}
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return tui.RunSwitcher(registry.GetRegistry())
		}
		return cmd.Help()
	},
}

func init() {
	RootCmd.PersistentFlags().BoolVar(&JSONOutput, "json", false, "Format output as machine-readable JSON")
	RootCmd.PersistentFlags().BoolVar(&DryRun, "dry-run", false, "Simulate operation without mutating state")
	RootCmd.PersistentFlags().BoolVar(&Force, "force", false, "Override safety checks")
	RootCmd.PersistentFlags().StringVar(&HarnessArg, "harness", "", "Target specific registered domain harness by ID or path")

	// Unknown help topics must exit non-zero (owen-04). Cobra's default help
	// prints "Unknown help topic" then Usage() and still returns success.
	RootCmd.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Long: `Help provides help for any command in the application.
Simply type harness help [path to command] for full details.`,
		RunE: runRootHelp,
	})

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
	RootCmd.AddCommand(spokeCmd)
	RootCmd.AddCommand(claimCmd)
	RootCmd.AddCommand(execCmd)
	registerConversationCommands()
}

// runRootHelp shows help for a known command path, or fails closed on unknown topics.
func runRootHelp(cmd *cobra.Command, args []string) error {
	target, _, err := cmd.Root().Find(args)
	if target == nil || err != nil {
		return fmt.Errorf("unknown help topic %q", strings.Join(args, " "))
	}
	target.InitDefaultHelpFlag()
	target.InitDefaultVersionFlag()
	return target.Help()
}

// Execute runs the root command.
func Execute() error {
	return RootCmd.Execute()
}
