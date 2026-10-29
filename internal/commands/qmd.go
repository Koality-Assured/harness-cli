package commands

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/Koality-Assured/harness-cli/internal/registry"
	"github.com/spf13/cobra"
)

var (
	qmdCollection string
	qmdMinScore   float64
	qmdLimit      int
	qmdIntent     string
	qmdLex        string
	qmdVec        string
	qmdHyde       string
	qmdNoRerank   bool
	qmdMaxLines   int
	qmdMaxBytes   int
	qmdFormat     string
	qmdForce      bool
	qmdTimeout    int
)

var qmdCmd = &cobra.Command{
	Use:   "qmd",
	Short: "Query, search, and maintain repository Markdown knowledge bases via QMD",
	Long: `Query Markdown Documents (QMD) control plane integration.
Provides unified BM25 search, dense vector similarity (vsearch), structured hybrid queries,
batch retrieval (multi-get), and index lifecycle maintenance.`,
}

var qmdSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "BM25 keyword search (no LLM, fast)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}

		qmdArgs := []string{"search"}
		if JSONOutput {
			qmdArgs = append(qmdArgs, "--format", "json")
		}
		if qmdMinScore > 0 {
			qmdArgs = append(qmdArgs, "--min-score", fmt.Sprintf("%g", qmdMinScore))
		}
		if qmdLimit > 0 {
			qmdArgs = append(qmdArgs, "-n", strconv.Itoa(qmdLimit))
		}
		if qmdCollection != "" {
			qmdArgs = append(qmdArgs, "-c", qmdCollection)
		}
		qmdArgs = append(qmdArgs, query)

		out, err := runQMD(dir, qmdArgs...)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdVSearchCmd = &cobra.Command{
	Use:   "vsearch <query>",
	Short: "Dense vector similarity search (embeddings-only, no LLM rerank)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}

		qmdArgs := []string{"vsearch"}
		if JSONOutput {
			qmdArgs = append(qmdArgs, "--format", "json")
		}
		if qmdMinScore > 0 {
			qmdArgs = append(qmdArgs, "--min-score", fmt.Sprintf("%g", qmdMinScore))
		}
		if qmdLimit > 0 {
			qmdArgs = append(qmdArgs, "-n", strconv.Itoa(qmdLimit))
		}
		if qmdCollection != "" {
			qmdArgs = append(qmdArgs, "-c", qmdCollection)
		}
		qmdArgs = append(qmdArgs, query)

		out, err := runQMD(dir, qmdArgs...)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdQueryCmd = &cobra.Command{
	Use:   "query [prompt]",
	Short: "Hybrid search with auto-expansion + reranking or structured query document",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}

		qmdArgs := []string{"query"}
		if JSONOutput {
			qmdArgs = append(qmdArgs, "--format", "json")
		}
		if qmdMinScore > 0 {
			qmdArgs = append(qmdArgs, "--min-score", fmt.Sprintf("%g", qmdMinScore))
		}
		if qmdLimit > 0 {
			qmdArgs = append(qmdArgs, "-n", strconv.Itoa(qmdLimit))
		}
		if qmdNoRerank {
			qmdArgs = append(qmdArgs, "--no-rerank")
		}
		if qmdCollection != "" {
			qmdArgs = append(qmdArgs, "-c", qmdCollection)
		}

		// Structured document if intent/lex/vec/hyde specified
		if qmdIntent != "" || qmdLex != "" || qmdVec != "" || qmdHyde != "" {
			var lines []string
			if qmdIntent != "" {
				lines = append(lines, fmt.Sprintf("intent: %s", qmdIntent))
			}
			if qmdLex != "" {
				lines = append(lines, fmt.Sprintf("lex: %s", qmdLex))
			}
			if qmdVec != "" {
				lines = append(lines, fmt.Sprintf("vec: %s", qmdVec))
			}
			if qmdHyde != "" {
				lines = append(lines, fmt.Sprintf("hyde: %s", qmdHyde))
			}
			qmdArgs = append(qmdArgs, strings.Join(lines, "\n"))
		} else if len(args) > 0 {
			qmdArgs = append(qmdArgs, args[0])
		} else {
			return fmt.Errorf("must provide a prompt string or structured flags (--intent, --lex, --vec, --hyde)")
		}

		out, err := runQMD(dir, qmdArgs...)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdGetCmd = &cobra.Command{
	Use:   "get <docid-or-path>",
	Short: "Fetch document content by docid or path",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := args[0]
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}

		qmdArgs := []string{"get", target}
		out, err := runQMD(dir, qmdArgs...)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdMultiGetCmd = &cobra.Command{
	Use:   "multi-get <targets-or-glob>",
	Short: "Batch retrieve documents by comma-separated docids or glob patterns",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targets := args[0]
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}

		qmdArgs := []string{"multi-get", targets}
		if JSONOutput {
			qmdArgs = append(qmdArgs, "--format", "json")
		} else if qmdFormat != "" {
			qmdArgs = append(qmdArgs, "--format", qmdFormat)
		}
		if qmdMaxLines > 0 {
			qmdArgs = append(qmdArgs, "-l", strconv.Itoa(qmdMaxLines))
		}
		if qmdMaxBytes > 0 {
			qmdArgs = append(qmdArgs, "--max-bytes", strconv.Itoa(qmdMaxBytes))
		}

		out, err := runQMD(dir, qmdArgs...)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Re-index collections incrementally (routine freshness)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}
		out, err := runQMD(dir, "update")
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdEmbedCmd = &cobra.Command{
	Use:   "embed",
	Short: "Generate or refresh vector embeddings",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}

		qmdArgs := []string{"embed"}
		if qmdCollection != "" {
			qmdArgs = append(qmdArgs, "-c", qmdCollection)
		}
		if qmdForce {
			qmdArgs = append(qmdArgs, "-f")
		}
		if qmdTimeout > 0 {
			qmdArgs = append(qmdArgs, "--timeout", strconv.Itoa(qmdTimeout))
		}

		out, err := runQMD(dir, qmdArgs...)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Drop inactive docs/orphans, compact FTS, vacuum SQLite",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}

		qmdArgs := []string{"cleanup"}
		if DryRun {
			qmdArgs = append(qmdArgs, "--dry-run")
		}

		out, err := runQMD(dir, qmdArgs...)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

var qmdStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "View index and collection health",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := resolveQMDDir(HarnessArg)
		if err != nil {
			return err
		}
		out, err := runQMD(dir, "status")
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	},
}

func init() {
	// Flags for search / vsearch / query
	qmdSearchCmd.Flags().StringVarP(&qmdCollection, "collection", "c", "", "Filter search to specific collection")
	qmdSearchCmd.Flags().Float64Var(&qmdMinScore, "min-score", 0.5, "Minimum similarity score floor")
	qmdSearchCmd.Flags().IntVarP(&qmdLimit, "limit", "n", 5, "Maximum number of results to return")

	qmdVSearchCmd.Flags().StringVarP(&qmdCollection, "collection", "c", "", "Filter search to specific collection")
	qmdVSearchCmd.Flags().Float64Var(&qmdMinScore, "min-score", 0.4, "Minimum vector similarity score floor")
	qmdVSearchCmd.Flags().IntVarP(&qmdLimit, "limit", "n", 5, "Maximum number of results to return")

	qmdQueryCmd.Flags().StringVarP(&qmdCollection, "collection", "c", "", "Filter search to specific collection")
	qmdQueryCmd.Flags().Float64Var(&qmdMinScore, "min-score", 0.5, "Minimum similarity score floor")
	qmdQueryCmd.Flags().IntVarP(&qmdLimit, "limit", "n", 5, "Maximum number of results to return")
	qmdQueryCmd.Flags().StringVar(&qmdIntent, "intent", "", "Structured intent: what to retrieve and what to reject")
	qmdQueryCmd.Flags().StringVar(&qmdLex, "lex", "", "Structured lex: exact keywords, symbols, titles")
	qmdQueryCmd.Flags().StringVar(&qmdVec, "vec", "", "Structured vec: natural language paraphrasing")
	qmdQueryCmd.Flags().StringVar(&qmdHyde, "hyde", "", "Structured hyde: hypothetical answer passage")
	qmdQueryCmd.Flags().BoolVar(&qmdNoRerank, "no-rerank", false, "Skip LLM reranking (fast RRF mode)")

	qmdMultiGetCmd.Flags().IntVarP(&qmdMaxLines, "max-lines", "l", 0, "Maximum lines per retrieved file")
	qmdMultiGetCmd.Flags().IntVar(&qmdMaxBytes, "max-bytes", 65536, "Skip files larger than N bytes")
	qmdMultiGetCmd.Flags().StringVar(&qmdFormat, "format", "md", "Output format (md, json, cli)")

	qmdEmbedCmd.Flags().StringVarP(&qmdCollection, "collection", "c", "", "Filter embedding to specific collection")
	qmdEmbedCmd.Flags().BoolVarP(&qmdForce, "force", "f", false, "Force re-embedding of existing documents")
	qmdEmbedCmd.Flags().IntVar(&qmdTimeout, "timeout", 10, "Embedding session timeout in minutes")

	qmdCmd.AddCommand(qmdSearchCmd)
	qmdCmd.AddCommand(qmdVSearchCmd)
	qmdCmd.AddCommand(qmdQueryCmd)
	qmdCmd.AddCommand(qmdGetCmd)
	qmdCmd.AddCommand(qmdMultiGetCmd)
	qmdCmd.AddCommand(qmdUpdateCmd)
	qmdCmd.AddCommand(qmdEmbedCmd)
	qmdCmd.AddCommand(qmdCleanupCmd)
	qmdCmd.AddCommand(qmdStatusCmd)
}

func resolveQMDBinary() (string, error) {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("qmd.cmd"); err == nil {
			return p, nil
		}
		if p, err := exec.LookPath("qmd.exe"); err == nil {
			return p, nil
		}
	}
	return exec.LookPath("qmd")
}

func resolveQMDDir(targetHarness string) (string, error) {
	var targetDir string
	if targetHarness != "" {
		if fi, err := os.Stat(targetHarness); err == nil && fi.IsDir() {
			targetDir = targetHarness
		} else {
			var err error
			targetDir, err = registry.ResolveHarnessRoot(targetHarness, "")
			if err != nil {
				return "", err
			}
		}
	}
	primaryRoot, err := git.GetPrimaryRepoRoot(targetDir)
	if err != nil {
		return "", err
	}

	// Prefer scratch/qmd-main if present (canonical detached retrieval source)
	qmdMain := filepath.Join(primaryRoot, "scratch", "qmd-main")
	if fi, err := os.Stat(qmdMain); err == nil && fi.IsDir() {
		return qmdMain, nil
	}
	return primaryRoot, nil
}

func runQMD(qmdDir string, args ...string) (string, error) {
	bin, err := resolveQMDBinary()
	if err != nil {
		return "", fmt.Errorf("qmd binary not found on PATH: %w", err)
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = qmdDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("qmd %s failed: %w (stderr: %s)", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}
