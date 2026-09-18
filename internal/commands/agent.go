package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent [agent_id]",
	Short: "Inspect available agents or view detailed agent spec",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var targetDir string
		if HarnessArg != "" {
			var err error
			targetDir, err = registry.ResolveHarnessRoot(HarnessArg, "")
			if err != nil {
				return err
			}
		}

		primaryRoot, err := git.GetPrimaryRepoRoot(targetDir)
		if err != nil {
			return err
		}

		if len(args) == 0 {
			agents, err := registry.LoadAgents(primaryRoot)
			if err != nil {
				return err
			}

			if JSONOutput {
				data, err := json.MarshalIndent(agents, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("=== Available Agents (%d) ===\n\n", len(agents))
			fmt.Printf("%-26s %-12s %-22s %s\n", "Agent ID", "Model Tier", "Ownership Areas", "Allowed Tools")
			fmt.Println(strings.Repeat("-", 80))
			for _, a := range agents {
				areas := strings.Join(a.OwnershipAreas, ",")
				if areas == "" {
					areas = "-"
				}
				tools := strings.Join(a.AllowedTools, ", ")
				if len(a.AllowedTools) > 3 {
					tools = fmt.Sprintf("%s (+%d)", strings.Join(a.AllowedTools[:3], ", "), len(a.AllowedTools)-3)
				}
				fmt.Printf("%-26s %-12s %-22s %s\n", a.AgentID, a.ModelTier, areas, tools)
			}
			fmt.Println("\nRun 'harness agent <agent_id>' for detailed inspection.")
			return nil
		}

		agentID := args[0]
		agent, err := registry.LoadAgent(primaryRoot, agentID)
		if err != nil {
			return fmt.Errorf("agent '%s' not found: %w", agentID, err)
		}

		if JSONOutput {
			data, err := json.MarshalIndent(agent, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Printf("=== Agent Specification: %s (%s) ===\n", agent.Name, agent.AgentID)
		fmt.Printf("Model Tier:        %s\n", agent.ModelTier)
		fmt.Printf("Ownership Areas:   %s\n", strings.Join(agent.OwnershipAreas, ", "))
		fmt.Printf("Description:       %s\n", agent.Description)
		if agent.Body != "" {
			fmt.Println("\nPrompt Overview:")
			lines := strings.Split(agent.Body, "\n")
			for i, l := range lines {
				if i >= 8 {
					fmt.Println("  ...")
					break
				}
				if strings.TrimSpace(l) != "" {
					fmt.Printf("  %s\n", l)
				}
			}
		}
		return nil
	},
}
