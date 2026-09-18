package commands

import (
	"encoding/json"
	"fmt"

	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var switchCmd = &cobra.Command{
	Use:   "switch <id_or_path>",
	Short: "Switch active harness to target ID or path",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := args[0]
		reg := registry.GetRegistry()

		record, err := reg.Switch(target)
		if err != nil {
			return err
		}

		if JSONOutput {
			payload := map[string]interface{}{
				"active_harness": record.ID,
				"record":         record,
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Printf("Switched active harness to '%s'.\n", record.ID)
		fmt.Printf("  Path:   %s\n", record.Path)
		fmt.Printf("  Domain: %s\n", record.Domain)
		return nil
	},
}
