package commands

import (
	"encoding/json"
	"fmt"

	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/Koality-Assured/harness-cli/internal/tui"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all registered domain harnesses",
	RunE:    runList,
}

func runList(cmd *cobra.Command, args []string) error {
	reg := registry.GetRegistry()
	harnesses := reg.ListHarnesses()

	if JSONOutput {
		payload := map[string]interface{}{
			"count":     len(harnesses),
			"harnesses": harnesses,
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	fmt.Print(tui.FormatHarnessesTable(harnesses))
	return nil
}
