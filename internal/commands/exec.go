package commands

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/auth"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	execAgent string
	// getExecVault is overridable in tests so gates can run against an isolated empty vault.
	getExecVault = auth.GetVault
)

var execCmd = &cobra.Command{
	Use:   "exec [flags] -- <command...>",
	Short: "Execute a command with provider API keys injected from the secure vault",
	Long: `Execute a subprocess with foundation model credentials (ANTHROPIC_API_KEY,
OPENAI_API_KEY, GEMINI_API_KEY, etc.) automatically retrieved from the native OS
keyring or encrypted vault and injected into the child process environment.

Secrets are held in memory only and never written to disk or echoed to output.

Without vault credentials, exec fails closed unless --force is passed to run
without injected credentials. Global --harness scopes the child working
directory to a resolved harness root. Global --dry-run simulates without
starting the child.`,
	DisableFlagParsing: false,
	Args:               cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("no command provided to exec. Usage: harness exec -- <command> [args...]")
		}

		workDir, err := resolveExecWorkDir()
		if err != nil {
			return err
		}

		vault := getExecVault()

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

		// Inject foundation model credentials with silent RFC 6749 refresh rotation
		providers := []struct {
			provider string
			envVar   string
		}{
			{"anthropic", "ANTHROPIC_API_KEY"},
			{"openai", "OPENAI_API_KEY"},
			{"gemini", "GEMINI_API_KEY"},
			{"cursor", "CURSOR_API_KEY"},
		}

		for _, p := range providers {
			cred, err := auth.EnsureFreshToken(vault, p.provider, 15*time.Minute)
			if err == nil && cred != nil && cred.AccessToken != "" {
				envMap[p.envVar] = cred.AccessToken
				injectedCount++
			}
		}

		if execAgent != "" {
			envMap["HARNESS_AGENT_ID"] = execAgent
		}

		if DryRun {
			fmt.Printf("[dry-run] Would exec: %s\n", strings.Join(args, " "))
			if workDir != "" {
				fmt.Printf("[dry-run] Working directory: %s\n", workDir)
			}
			fmt.Printf("[dry-run] Would inject %d provider credential(s)\n", injectedCount)
			if injectedCount == 0 && !Force {
				fmt.Println("[dry-run] Would refuse: no vault credentials (pass --force to override)")
			} else if injectedCount == 0 && Force {
				fmt.Println("[dry-run] Would run with --force and zero injected vault credentials")
			}
			return nil
		}

		if injectedCount == 0 && !Force {
			return fmt.Errorf("no vault credentials available for provider injection; authenticate first or pass --force to run without injected credentials")
		}
		if injectedCount == 0 && Force {
			fmt.Fprintln(os.Stderr, "warning: running exec with --force and zero injected vault credentials")
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
		if workDir != "" {
			subCmd.Dir = workDir
		}

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

// resolveExecWorkDir resolves --harness via the registry when set.
// Force does not bypass resolution failures.
func resolveExecWorkDir() (string, error) {
	if HarnessArg == "" {
		return "", nil
	}
	return registry.ResolveHarnessRoot(HarnessArg, "")
}

func init() {
	execCmd.Flags().StringVar(&execAgent, "agent", "", "Agent identity for profile tracking")
}
