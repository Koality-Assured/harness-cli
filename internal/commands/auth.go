package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/auth"
	"github.com/spf13/cobra"
)

var (
	authNoBrowser  bool
	authDeviceCode bool
	authAPIKey     string
	authAll        bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication and secure credential vaults",
}

var authLoginCmd = &cobra.Command{
	Use:   "login <provider>",
	Short: "Authenticate with an AI model provider",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		raw := args[0]
		prov, ok := auth.NormalizeProvider(raw)
		if !ok {
			return fmt.Errorf("unsupported provider '%s'. Supported: %s", raw, strings.Join(auth.SupportedProviders, ", "))
		}

		if authAPIKey != "" {
			fmt.Fprintln(os.Stderr, "warning: providing secrets via the '--api-key' CLI argument can expose credentials to process table inspection (ps, Task Manager, /proc). Consider using interactive masked input instead.")
		}

		switch prov {
		case "anthropic":
			return auth.LoginAnthropic(authNoBrowser, authAPIKey)
		case "cursor":
			return auth.LoginCursor(authNoBrowser, authAPIKey)
		case "gemini":
			return auth.LoginGemini(authNoBrowser, authDeviceCode, authAPIKey)
		case "openai":
			return auth.LoginOpenAI(authAPIKey, authNoBrowser, authDeviceCode)
		default:
			return fmt.Errorf("provider '%s' handler not implemented", prov)
		}
	},
}

type ProviderStatus struct {
	Provider      string `json:"provider"`
	Authenticated bool   `json:"authenticated"`
	Status        string `json:"status"`
	Profile       string `json:"profile"`
	Expires       string `json:"expires"`
	TokenMasked   string `json:"token_masked"`
	TokenType     string `json:"token_type"`
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Inspect token validity, expiration, and active credential profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		vault := auth.GetVault()
		backend := vault.ActiveBackendName()

		var statuses []ProviderStatus
		for _, p := range auth.SupportedProviders {
			cred, err := vault.GetCredential(p)
			if err == nil && cred != nil {
				expired := auth.IsTokenExpired(cred.ExpiresAt, 60)
				statusStr := "VALID"
				if expired {
					statusStr = "EXPIRED"
				}

				expStr := "Never (API Key)"
				if cred.ExpiresAt != nil {
					switch v := cred.ExpiresAt.(type) {
					case float64:
						expStr = time.Unix(int64(v), 0).UTC().Format("2006-01-02 15:04:05 UTC")
					case int64:
						expStr = time.Unix(v, 0).UTC().Format("2006-01-02 15:04:05 UTC")
					case string:
						expStr = v
					}
				}

				profile := cred.Profile
				if profile == "" {
					profile = "-"
				}

				statuses = append(statuses, ProviderStatus{
					Provider:      p,
					Authenticated: true,
					Status:        statusStr,
					Profile:       profile,
					Expires:       expStr,
					TokenMasked:   auth.MaskToken(cred.AccessToken),
					TokenType:     cred.TokenType,
				})
			} else {
				statuses = append(statuses, ProviderStatus{
					Provider:      p,
					Authenticated: false,
					Status:        "NOT AUTHENTICATED",
					Profile:       "-",
					Expires:       "-",
					TokenMasked:   "-",
					TokenType:     "-",
				})
			}
		}

		if JSONOutput {
			payload := map[string]interface{}{
				"active_vault_backend": backend,
				"providers":            statuses,
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Println("=== Harness Credential Vault Status ===")
		fmt.Printf("Active Vault Backend: %s\n\n", backend)
		fmt.Printf("%-14s %-18s %-16s %-24s %s\n", "Provider", "Status", "Profile", "Expires", "Token Masked")
		fmt.Println(strings.Repeat("-", 92))
		for _, s := range statuses {
			fmt.Printf("%-14s %-18s %-16s %-24s %s\n", s.Provider, s.Status, s.Profile, s.Expires, s.TokenMasked)
		}

		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout [provider]",
	Short: "Safely delete credentials from OS vault and encrypted fallback",
	RunE: func(cmd *cobra.Command, args []string) error {
		vault := auth.GetVault()

		if authAll {
			providers := vault.ListProviders()
			if len(providers) == 0 {
				fmt.Println("No stored credentials found in vault.")
				return nil
			}
			for _, p := range providers {
				_, _ = vault.DeleteCredential(p)
			}
			fmt.Printf("Successfully purged all credentials from vault (%d provider(s)).\n", len(providers))
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("specify a provider to logout or pass --all to clear all credentials")
		}

		prov, ok := auth.NormalizeProvider(args[0])
		if !ok {
			return fmt.Errorf("unsupported provider '%s'. Supported: %s", args[0], strings.Join(auth.SupportedProviders, ", "))
		}

		deleted, err := vault.DeleteCredential(prov)
		if err != nil {
			return err
		}
		if deleted {
			fmt.Printf("Logged out from '%s'. Credential purged from vault.\n", prov)
		} else {
			fmt.Printf("No stored credentials found for '%s'.\n", prov)
		}
		return nil
	},
}

func init() {
	authLoginCmd.Flags().BoolVar(&authNoBrowser, "no-browser", false, "Use terminal/manual code entry instead of launching browser")
	authLoginCmd.Flags().BoolVar(&authDeviceCode, "device-code", false, "Use RFC 8628 Device Authorization Grant")
	authLoginCmd.Flags().StringVar(&authAPIKey, "api-key", "", "Direct API key onboarding into secure vault")

	authLogoutCmd.Flags().BoolVar(&authAll, "all", false, "Purge all credentials across all providers")

	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)
}
