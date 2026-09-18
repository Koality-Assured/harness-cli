package commands

import (
	"encoding/json"
	"fmt"

	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var deregisterCmd = &cobra.Command{
	Use:     "deregister <id_or_path>",
	Aliases: []string{"rm", "remove"},
	Short:   "Deregister a harness from the local catalog",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := args[0]
		reg := registry.GetRegistry()

		deleted := reg.Deregister(target)
		if !deleted {
			return fmt.Errorf("harness '%s' not found in registry", target)
		}

		if JSONOutput {
			payload := map[string]interface{}{
				"deregistered": target,
				"success":      true,
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Printf("Deregistered harness '%s' successfully.\n", target)
		return nil
	},
}
