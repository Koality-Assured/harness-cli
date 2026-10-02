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
		// Global --dry-run must not mutate the catalog; suppress auto-register.
		autoRegister := !scanNoRegister && !DryRun

		discovered, err := reg.ScanSiblings(targetDir, autoRegister)
		if err != nil {
			return err
		}

		registerPlan := buildScanRegisterPlan(reg, discovered, !scanNoRegister)

		if JSONOutput {
			payload := map[string]interface{}{
				"scanned_count": len(discovered),
				"discovered":    discovered,
			}
			if DryRun {
				payload["dry_run"] = true
				payload["auto_register_planned"] = !scanNoRegister
				payload["register_plan"] = registerPlan
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		if DryRun {
			fmt.Println("[dry-run] Simulating scan; no registry mutations.")
			if scanNoRegister {
				fmt.Println("[dry-run] Discovery only (--no-register); nothing would be registered.")
			} else if len(registerPlan) == 0 {
				fmt.Println("[dry-run] Would auto-register: (none — all discovered harnesses already cataloged or none found).")
			} else {
				fmt.Printf("[dry-run] Would auto-register %d harness(es):\n", len(registerPlan))
				for _, name := range registerPlan {
					fmt.Printf("  - %s\n", name)
				}
			}
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
			} else if DryRun && !scanNoRegister {
				if _, ok := reg.GetHarness(d.Path); ok {
					statusReg = "already-cataloged"
				} else {
					statusReg = "would-register"
				}
			}
			fmt.Printf("  [%s] %-24s (%s) -> %s\n", strings.ToUpper(statusReg), d.Name, d.Branch, d.Path)
			fmt.Printf("         Domain: %s\n", d.Domain)
		}

		return nil
	},
}

// buildScanRegisterPlan lists discovered harness names that auto-register would add
// (paths not already present in the catalog). Empty when auto-register is disabled.
func buildScanRegisterPlan(reg *registry.HarnessRegistry, discovered []registry.ScanResult, autoRegisterPlanned bool) []string {
	if !autoRegisterPlanned {
		return []string{}
	}
	plan := make([]string, 0)
	for _, d := range discovered {
		if _, ok := reg.GetHarness(d.Path); ok {
			continue
		}
		plan = append(plan, d.Name)
	}
	return plan
}

func init() {
	scanCmd.Flags().BoolVar(&scanNoRegister, "no-register", false, "Discover without auto-registering")
}
