package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Koality-Assured/harness-cli/internal/auth"
	"github.com/Koality-Assured/harness-cli/internal/chat"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var chatOptions chatOptionsValue

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "One-shot or interactive agent conversation",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runChat(cmd, chatOptions, nil, defaultChatCredential)
	},
}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List, rename, or search conversation sessions",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runSessionAction(cmd, "list", nil) },
}

var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent sessions",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runSessionAction(cmd, "list", nil) },
}

var sessionsRenameCmd = &cobra.Command{
	Use:   "rename <session-id> <title>",
	Short: "Rename a session",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionAction(cmd, "rename", args)
	},
}

var sessionsSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search message content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionAction(cmd, "search", args)
	},
}

var commandsCmd = &cobra.Command{
	Use:   "commands",
	Short: "List slash commands available in harness chat",
	RunE: func(cmd *cobra.Command, _ []string) error {
		commands := builtinCommands
		if JSONOutput {
			return writeJSON(cmd.OutOrStdout(), map[string]any{"commands": commands})
		}
		for _, item := range commands {
			fmt.Fprintf(cmd.OutOrStdout(), "/%-12s %s\n", item.Name, item.Summary)
		}
		return nil
	},
}

type chatOptionsValue struct {
	query           string
	queryFile       string
	resume          string
	continueSession bool
	provider        string
	model           string
	title           string
	noStream        bool
}

type chatCommandDef struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

var builtinCommands = []chatCommandDef{
	{"help", "List available slash commands"},
	{"status", "Show active session provider, model, and cwd"},
	{"model", "Show or set the session model (usage: /model <id>)"},
	{"sessions", "List recent sessions"},
	{"compact", "Summarize middle turns into a new session"},
	{"title", "Rename the active session (usage: /title <text>)"},
	{"busy", "Show or set busy input mode: interrupt, queue, or steer"},
	{"quit", "Exit the interactive REPL"},
}

func init() {
	chatCmd.Flags().StringVarP(&chatOptions.query, "query", "q", "", "One-shot user message")
	chatCmd.Flags().StringVar(&chatOptions.queryFile, "query-file", "", "Read one-shot user message from file")
	chatCmd.Flags().StringVar(&chatOptions.resume, "resume", "", "Resume session by id")
	chatCmd.Flags().BoolVar(&chatOptions.continueSession, "continue", false, "Resume latest session")
	chatCmd.Flags().StringVar(&chatOptions.model, "model", "", "Model id (required for openai/gemini)")
	chatCmd.Flags().StringVar(&chatOptions.provider, "provider", "", "Model provider (default: anthropic)")
	chatCmd.Flags().StringVar(&chatOptions.title, "title", "", "Session title")
	chatCmd.Flags().BoolVar(&chatOptions.noStream, "no-stream", false, "Wait for the complete response and print it once")
	sessionsCmd.AddCommand(sessionsListCmd, sessionsRenameCmd, sessionsSearchCmd)
}

func registerConversationCommands() {
	RootCmd.AddCommand(chatCmd, sessionsCmd, commandsCmd, gatewayCmd, acpCmd)
}

func defaultChatCredential(provider string) (string, error) {
	credential, err := auth.EnsureFreshToken(auth.GetVault(), provider, 60*time.Second)
	if err != nil {
		return "", err
	}
	if credential == nil || credential.AccessToken == "" {
		return "", fmt.Errorf("no credentials for '%s'. Run: harness auth login %s", provider, provider)
	}
	return credential.AccessToken, nil
}

func runChat(cmd *cobra.Command, options chatOptionsValue, client *chat.ProviderClient, credentialResolver chat.CredentialResolver) error {
	store, err := chat.OpenSessionStore("")
	if err != nil {
		return err
	}
	defer store.Close()
	session, code, err := openOrResumeSession(store, options)
	if err != nil {
		return err
	}
	if session == nil {
		return exitCodeError(code)
	}
	provider := session.Provider
	if provider == "" {
		provider = "anthropic"
	}
	if options.provider != "" {
		resolved, ok := chat.NormalizeProvider(options.provider)
		if !ok {
			return fmt.Errorf("unsupported provider '%s'. Supported: anthropic, cursor, gemini, openai", options.provider)
		}
		provider = resolved
	}
	model := options.model
	if model == "" {
		model = session.Model
	}
	if provider == "anthropic" && model == "" {
		model = chat.AnthropicDefaultModel
	}
	if (provider == "openai" || provider == "gemini") && model == "" {
		return fmt.Errorf("provider '%s' requires --model (no safe default confirmed from official docs)", provider)
	}
	if client == nil {
		client = &chat.ProviderClient{}
	}
	if credentialResolver == nil {
		credentialResolver = defaultChatCredential
	}
	apiKey, err := credentialResolver(provider)
	if err != nil {
		return err
	}
	if options.title != "" && session.Title != options.title {
		updated, err := store.RenameSession(session.ID, options.title)
		if err != nil {
			return err
		}
		if updated != nil {
			session = updated
		}
	}
	query, querySet, err := loadChatQuery(options)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("query") && options.queryFile == "" {
		querySet = true
	}
	if querySet {
		stream := !options.noStream
		turnOptions := chat.TurnOptions{Stream: stream, ToolRegistry: chat.NewToolRegistry()}
		if stream {
			turnOptions.OnDelta = func(delta string) { _, _ = io.WriteString(cmd.OutOrStdout(), delta) }
		}
		assistant, err := chat.RunTurn(context.Background(), store, session.ID, query, provider, model, apiKey, client, turnOptions)
		if err != nil {
			if stream {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return err
		}
		if stream {
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), assistant)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), assistant)
		}
		status, err := chat.GetSessionStatus(store, session.ID)
		if err == nil {
			fmt.Fprintln(cmd.ErrOrStderr(), formatChatStatus(status, model))
		}
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("interactive chat requires a TTY; pass -q/--query or --query-file")
	}
	return runInteractiveChat(cmd, store, session, provider, model, apiKey, client, options.noStream)
}

func openOrResumeSession(store *chat.SessionStore, options chatOptionsValue) (*chat.Session, int, error) {
	if options.resume != "" && options.continueSession {
		return nil, 2, errors.New("pass either --resume or --continue, not both")
	}
	if options.resume != "" {
		session, err := store.GetSession(options.resume)
		if err != nil {
			return nil, 1, err
		}
		if session == nil {
			return nil, 1, fmt.Errorf("session not found: %s", options.resume)
		}
		return session, 0, nil
	}
	if options.continueSession {
		session, err := store.LatestSession()
		if err != nil {
			return nil, 1, err
		}
		if session == nil {
			return nil, 1, errors.New("no sessions to continue")
		}
		return session, 0, nil
	}
	rawProvider := options.provider
	if rawProvider == "" {
		rawProvider = "anthropic"
	}
	provider, ok := chat.NormalizeProvider(rawProvider)
	if !ok {
		return nil, 1, fmt.Errorf("unsupported provider '%s'. Supported: anthropic, cursor, gemini, openai", rawProvider)
	}
	model := options.model
	if provider == "anthropic" && model == "" {
		model = chat.AnthropicDefaultModel
	}
	if (provider == "openai" || provider == "gemini") && model == "" {
		return nil, 1, fmt.Errorf("provider '%s' requires --model (no safe default confirmed from official docs)", provider)
	}
	cwd, _ := os.Getwd()
	returnValue, err := store.CreateSession(chat.Session{Title: options.title, CWD: cwd, Provider: provider, Model: model})
	if err != nil {
		return nil, 1, err
	}
	return &returnValue, 0, nil
}

func exitCodeError(code int) error { return fmt.Errorf("command exited with code %d", code) }

func loadChatQuery(options chatOptionsValue) (string, bool, error) {
	if options.query != "" && options.queryFile != "" {
		return "", false, errors.New("pass either -q/--query or --query-file, not both")
	}
	if options.queryFile != "" {
		contents, err := os.ReadFile(options.queryFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", false, fmt.Errorf("query file not found: %s", options.queryFile)
			}
			return "", false, err
		}
		if !utf8.Valid(contents) {
			return "", false, errors.New("query file must be UTF-8 text")
		}
		return string(contents), true, nil
	}
	if options.query != "" {
		return options.query, true, nil
	}
	return "", false, nil
}

func runInteractiveChat(cmd *cobra.Command, store *chat.SessionStore, session *chat.Session, provider, model, apiKey string, client *chat.ProviderClient, noStream bool) error {
	status, err := chat.GetSessionStatus(store, session.ID)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), formatChatStatus(status, model))
	fmt.Fprintln(cmd.OutOrStdout(), "Type /help for commands, /quit to exit.")
	pump := newChatLinePump(os.Stdin)
	defer pump.close()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	var pending []string
	for {
		var line string
		if len(pending) > 0 {
			line, pending = pending[0], pending[1:]
		} else {
			fmt.Fprint(cmd.OutOrStdout(), "> ")
			value, open := pump.get()
			if !open {
				fmt.Fprintln(cmd.OutOrStdout())
				return nil
			}
			line = value
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if handled, exit := handleChatSlash(cmd, store, session, &model, provider, apiKey, client, line); handled {
			if exit {
				return nil
			}
			continue
		}
		steered := []string{}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct {
			text string
			err  error
		}, 1)
		turnOptions := chat.TurnOptions{Stream: !noStream, ToolRegistry: chat.NewToolRegistry()}
		if !noStream {
			turnOptions.OnDelta = func(delta string) { _, _ = io.WriteString(cmd.OutOrStdout(), delta) }
		}
		turnOptions.OnToolBatch = func() string {
			if session.BusyMode != "steer" || len(steered) > 0 {
				return ""
			}
			waiting := pump.takeAvailable(1)
			if len(waiting) > 0 && strings.TrimSpace(waiting[0]) != "" {
				steered = append(steered, waiting[0])
				return waiting[0]
			}
			return ""
		}
		go func() {
			text, err := chat.RunTurn(ctx, store, session.ID, line, provider, model, apiKey, client, turnOptions)
			done <- struct {
				text string
				err  error
			}{text, err}
		}()
		var turn struct {
			text string
			err  error
		}
		interrupted := false
		select {
		case turn = <-done:
		case <-signals:
			cancel()
			turn = <-done
			interrupted = true
		}
		cancel()
		if interrupted || errors.Is(turn.err, chat.TurnCancelled) {
			if !noStream {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Turn interrupted.")
			pending = append(steered, pending...)
			continue
		}
		if turn.err != nil {
			if !noStream {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "error:", turn.err)
			pending = append(steered, pending...)
			continue
		}
		if noStream {
			fmt.Fprintln(cmd.OutOrStdout(), turn.text)
		} else {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		busyMode := session.BusyMode
		if busyMode == "queue" {
			pending = append(pending, pump.takeAvailable(-1)...)
		} else if busyMode == "steer" && len(steered) == 0 {
			pending = append(pending, pump.takeAvailable(1)...)
		}
	}
}

func handleChatSlash(cmd *cobra.Command, store *chat.SessionStore, session *chat.Session, model *string, provider, apiKey string, client *chat.ProviderClient, line string) (bool, bool) {
	text := strings.TrimSpace(line)
	if !strings.HasPrefix(text, "/") {
		return false, false
	}
	body := strings.TrimSpace(strings.TrimPrefix(text, "/"))
	name, argument := body, ""
	if index := strings.IndexByte(body, ' '); index >= 0 {
		name, argument = body[:index], strings.TrimSpace(body[index+1:])
	}
	name = strings.ToLower(name)
	switch name {
	case "quit", "exit", "q":
		return true, true
	case "", "help":
		for _, item := range builtinCommands {
			fmt.Fprintf(cmd.OutOrStdout(), "  /%-12s %s\n", item.Name, item.Summary)
		}
	case "status":
		status, err := chat.GetSessionStatus(store, session.ID)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			break
		}
		fmt.Fprintln(cmd.OutOrStdout(), formatChatStatus(status, *model))
	case "model":
		if argument != "" {
			*model = argument
			_ = store.TouchSession(session.ID, nil, model)
			fmt.Fprintf(cmd.OutOrStdout(), "model set to %s\n", argument)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "model=%s\n", firstChatValue(*model, session.Model, "(unset)"))
		}
	case "sessions":
		sessions, err := store.ListSessions(20)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			break
		}
		for _, item := range sessions {
			title := item.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s  %s\n", item.ID, item.UpdatedAt, title)
		}
	case "compact":
		if argument != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "usage: /compact")
			break
		}
		result, err := chat.CompactSession(context.Background(), store, session.ID, provider, *model, apiKey, client)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "error: context compression failed:", err)
			break
		}
		if result == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "nothing to compact: need at least three complete user-led turns")
			break
		}
		oldID := session.ID
		*session = result.Session
		*model = session.Model
		fmt.Fprintf(cmd.OutOrStdout(), "compacted %d middle turn(s) from %s; active session=%s\n", result.CompactedTurns, oldID, session.ID)
	case "title":
		if argument == "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "usage: /title <text>")
			break
		}
		updated, err := store.RenameSession(session.ID, argument)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			break
		}
		if updated != nil {
			*session = *updated
		}
		fmt.Fprintf(cmd.OutOrStdout(), "title set to %s\n", argument)
	case "busy":
		if argument == "" || strings.EqualFold(argument, "status") {
			fmt.Fprintf(cmd.OutOrStdout(), "busy=%s\n", firstChatValue(session.BusyMode, "interrupt"))
			break
		}
		if strings.EqualFold(argument, "help") {
			fmt.Fprintln(cmd.OutOrStdout(), busyHelp)
			break
		}
		mode := strings.ToLower(argument)
		if mode != "interrupt" && mode != "queue" && mode != "steer" {
			fmt.Fprintln(cmd.ErrOrStderr(), busyHelp)
			break
		}
		updated, err := store.SetBusyMode(session.ID, mode)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			break
		}
		if updated != nil {
			*session = *updated
		}
		fmt.Fprintf(cmd.OutOrStdout(), "busy=%s\n", mode)
	default:
		fmt.Fprintf(cmd.ErrOrStderr(), "unknown command: /%s (try /help)\n", name)
	}
	return true, false
}

const busyHelp = "usage: /busy [status|interrupt|queue|steer|help]\ninterrupt (default) aborts a turn with Ctrl+C; queue sends waiting lines in order; steer sends one waiting line after the current answer. True steer-at-tool-boundary waits for tool batches."

func formatChatStatus(status chat.SessionStatus, model string) string {
	input, output := "n/a", "n/a"
	if value, ok := status.InputTokens.(int64); ok {
		input = fmt.Sprint(value)
	}
	if value, ok := status.InputTokens.(int); ok {
		input = fmt.Sprint(value)
	}
	if value, ok := status.OutputTokens.(int64); ok {
		output = fmt.Sprint(value)
	}
	if value, ok := status.OutputTokens.(int); ok {
		output = fmt.Sprint(value)
	}
	if model == "" {
		model = status.Model
	}
	return fmt.Sprintf("session=%s provider=%s model=%s messages=%d input_tokens=%s output_tokens=%s cost=n/a", status.SessionID, status.Provider, model, status.MessageCount, input, output)
}

func firstChatValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func runSessionAction(cmd *cobra.Command, action string, args []string) error {
	store, err := chat.OpenSessionStore("")
	if err != nil {
		return err
	}
	defer store.Close()
	switch action {
	case "list":
		rows, err := store.ListSessions(50)
		if err != nil {
			return err
		}
		if JSONOutput {
			return writeJSON(cmd.OutOrStdout(), map[string]any{"sessions": rows})
		}
		if len(rows) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "(no sessions)")
		}
		for _, row := range rows {
			title := row.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s/%s  %s\n", row.ID, row.UpdatedAt, row.Provider, row.Model, title)
		}
	case "rename":
		row, err := store.RenameSession(args[0], args[1])
		if err != nil {
			return err
		}
		if row == nil {
			return fmt.Errorf("session not found: %s", args[0])
		}
		if JSONOutput {
			return writeJSON(cmd.OutOrStdout(), row)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "renamed %s -> %s\n", args[0], args[1])
	case "search":
		rows, err := store.SearchMessages(args[0], 50)
		if err != nil {
			return err
		}
		if JSONOutput {
			return writeJSON(cmd.OutOrStdout(), map[string]any{"matches": rows})
		}
		if len(rows) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "(no matches)")
		}
		for _, row := range rows {
			snippet := strings.ReplaceAll(row.Content, "\n", " ")
			if len(snippet) > 100 {
				snippet = snippet[:97] + "..."
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", row.SessionID, row.Role, snippet)
		}
	}
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type chatLinePump struct {
	lines   chan string
	done    chan struct{}
	stopped chan struct{}
}

func newChatLinePump(reader io.Reader) *chatLinePump {
	pump := &chatLinePump{lines: make(chan string, 128), done: make(chan struct{}), stopped: make(chan struct{})}
	go func() {
		defer close(pump.done)
		defer close(pump.lines)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1_048_576)
		for scanner.Scan() {
			select {
			case pump.lines <- scanner.Text():
			case <-pump.stopped:
				return
			}
		}
	}()
	return pump
}

func (p *chatLinePump) get() (string, bool) { line, ok := <-p.lines; return line, ok }
func (p *chatLinePump) takeAvailable(limit int) []string {
	result := []string{}
	for limit < 0 || len(result) < limit {
		select {
		case line, ok := <-p.lines:
			if !ok {
				return result
			}
			result = append(result, line)
		default:
			return result
		}
	}
	return result
}
func (p *chatLinePump) close() {
	select {
	case <-p.stopped:
	default:
		close(p.stopped)
	}
}

var gatewayOptions struct {
	host     string
	port     int
	provider string
	model    string
}
var gatewayCmd = &cobra.Command{
	Use: "gateway", Short: "Serve the text-only OpenAI-compatible chat completions subset",
	RunE: func(cmd *cobra.Command, _ []string) error { return runGateway(cmd, gatewayOptions) },
}

type gatewayOptionsValue struct {
	host     string
	port     int
	provider string
	model    string
}

func init() {
	gatewayCmd.Flags().StringVar(&gatewayOptions.host, "host", "127.0.0.1", "Bind address (default: loopback only)")
	gatewayCmd.Flags().IntVar(&gatewayOptions.port, "port", 8642, "Listen port (default: 8642)")
	gatewayCmd.Flags().StringVar(&gatewayOptions.provider, "provider", "anthropic", "Provider used by the shared conversation loop")
	gatewayCmd.Flags().StringVar(&gatewayOptions.model, "model", "", "Default model for the configured provider")
}

func runGateway(cmd *cobra.Command, options gatewayOptionsValue) error {
	secret := os.Getenv("HARNESS_GATEWAY_API_KEY")
	if secret == "" {
		return errors.New("set HARNESS_GATEWAY_API_KEY before starting the gateway")
	}
	runtime, err := chat.NewAdapterRuntime(chat.AdapterRuntimeConfig{Provider: options.provider, Model: options.model})
	if err != nil {
		return err
	}
	address := fmt.Sprintf("%s:%d", options.host, options.port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: chat.NewGatewayHandler(runtime, secret), ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(cmd.OutOrStdout(), "Harness gateway listening on http://%s%s\n", listener.Addr(), chat.GatewayRoute)
	return server.Serve(listener)
}

var acpOptions struct {
	provider string
	model    string
}
var acpCmd = &cobra.Command{
	Use: "acp", Short: "Run the ACP v1 stdio JSON-RPC adapter",
	RunE: func(cmd *cobra.Command, _ []string) error { return runACP(acpOptions) },
}

type acpOptionsValue struct {
	provider string
	model    string
}

func init() {
	acpCmd.Flags().StringVar(&acpOptions.provider, "provider", "anthropic", "Provider used by the shared conversation loop")
	acpCmd.Flags().StringVar(&acpOptions.model, "model", "", "Model for the configured provider")
}

func runACP(options acpOptionsValue) error {
	runtime, err := chat.NewAdapterRuntime(chat.AdapterRuntimeConfig{Provider: options.provider, Model: options.model})
	if err != nil {
		return err
	}
	return chat.RunACPStdio(runtime, os.Stdin, os.Stdout)
}
