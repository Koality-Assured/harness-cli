package commands

import (
	"encoding/json"
	"fmt"

	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	regName   string
	regDomain string
	regActive bool
)

var registerCmd = &cobra.Command{
	Use:   "register <path>",
	Short: "Register a local repository checkout as a domain harness",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetPath := args[0]
		reg := registry.GetRegistry()

		record, err := reg.Register(targetPath, regName, regDomain, regActive, Force)
		if err != nil {
			return err
		}

		if JSONOutput {
			data, err := json.MarshalIndent(record, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Printf("Registered harness '%s' successfully.\n", record.ID)
		fmt.Printf("  Name:   %s\n", record.Name)
		fmt.Printf("  Domain: %s\n", record.Domain)
		fmt.Printf("  Path:   %s\n", record.Path)
		if regActive {
			fmt.Println("  Status: Active harness")
		}
		return nil
	},
}

func init() {
	registerCmd.Flags().StringVar(&regName, "name", "", "Friendly name for the harness")
	registerCmd.Flags().StringVar(&regDomain, "domain", "", "Explicit domain description")
	registerCmd.Flags().BoolVar(&regActive, "active", false, "Set as active harness immediately")
}
