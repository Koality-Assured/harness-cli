package commands

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/Koality-Assured/harness-cli/internal/auth"
	"github.com/spf13/cobra"
)

var (
	execAgent string
)

var execCmd = &cobra.Command{
	Use:   "exec [flags] -- <command...>",
	Short: "Execute a command with provider API keys injected from the secure vault",
	Long: `Execute a subprocess with foundation model credentials (ANTHROPIC_API_KEY,
OPENAI_API_KEY, GEMINI_API_KEY, etc.) automatically retrieved from the native OS
keyring or encrypted vault and injected into the child process environment.

Secrets are held in memory only and never written to disk or echoed to output.`,
	DisableFlagParsing: false,
	Args:               cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("no command provided to exec. Usage: harness exec -- <command> [args...]")
		}

		vault := auth.GetVault()

		// Prepare environment with existing env
		envMap := make(map[string]string)
		for _, env := range os.Environ() {
			for i := 0; i < len(env); i++ {
				if env[i] == '=' {
					envMap[env[:i]] = env[i+1:]
					break
				}
			}
		}

		injectedCount := 0

		// Check and inject Anthropic
		if cred, err := vault.GetCredential("anthropic"); err == nil && cred != nil && cred.AccessToken != "" {
			envMap["ANTHROPIC_API_KEY"] = cred.AccessToken
			injectedCount++
		}

		// Check and inject OpenAI
		if cred, err := vault.GetCredential("openai"); err == nil && cred != nil && cred.AccessToken != "" {
			envMap["OPENAI_API_KEY"] = cred.AccessToken
			injectedCount++
		}

		// Check and inject Gemini
		if cred, err := vault.GetCredential("gemini"); err == nil && cred != nil && cred.AccessToken != "" {
			envMap["GEMINI_API_KEY"] = cred.AccessToken
			injectedCount++
		}

		// Check and inject Cursor
		if cred, err := vault.GetCredential("cursor"); err == nil && cred != nil && cred.AccessToken != "" {
			envMap["CURSOR_API_KEY"] = cred.AccessToken
			injectedCount++
		}

		if execAgent != "" {
			envMap["HARNESS_AGENT_ID"] = execAgent
		}

		childEnv := make([]string, 0, len(envMap))
		for k, v := range envMap {
			childEnv = append(childEnv, fmt.Sprintf("%s=%s", k, v))
		}

		subCmd := exec.Command(args[0], args[1:]...)
		subCmd.Env = childEnv
		subCmd.Stdin = os.Stdin
		subCmd.Stdout = os.Stdout
		subCmd.Stderr = os.Stderr

		// Handle signals to gracefully propagate interrupts to child process
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			for sig := range sigChan {
				if subCmd.Process != nil {
					_ = subCmd.Process.Signal(sig)
				}
			}
		}()

		runErr := subCmd.Run()
		signal.Stop(sigChan)
		close(sigChan)

		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			return runErr
		}

		return nil
	},
}

func init() {
	execCmd.Flags().StringVar(&execAgent, "agent", "", "Agent identity for profile tracking")
}
