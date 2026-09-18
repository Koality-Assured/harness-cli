package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var scanNoRegister bool

var scanCmd = &cobra.Command{
	Use:   "scan [directory]",
	Short: "Auto-discover sibling harness repositories",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var targetDir string
		if len(args) > 0 {
			targetDir = args[0]
		}

		reg := registry.GetRegistry()
		autoRegister := !scanNoRegister

		discovered, err := reg.ScanSiblings(targetDir, autoRegister)
		if err != nil {
			return err
		}

		if JSONOutput {
			payload := map[string]interface{}{
				"scanned_count": len(discovered),
				"discovered":    discovered,
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Printf("=== Sibling Harness Auto-Discovery (%d found) ===\n", len(discovered))
		if len(discovered) == 0 {
			fmt.Println("  No sibling domain harnesses found matching router markers.")
			return nil
		}

		for _, d := range discovered {
			statusReg := "found"
			if d.Registered {
				statusReg = "registered"
			}
			fmt.Printf("  [%s] %-24s (%s) -> %s\n", strings.ToUpper(statusReg), d.Name, d.Branch, d.Path)
			fmt.Printf("         Domain: %s\n", d.Domain)
		}

		return nil
	},
}

func init() {
	scanCmd.Flags().BoolVar(&scanNoRegister, "no-register", false, "Discover without auto-registering")
}
