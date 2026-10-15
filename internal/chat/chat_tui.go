package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/config"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// BuiltinSlashCommands lists the default slash commands available in the chat session.
var BuiltinSlashCommands = []string{
	"help", "status", "model", "sessions", "compact",
	"context", "thinking", "effort", "title", "busy",
	"preset", "system", "mcp", "permissions", "quit",
}

// FormatStatusBar formats the session status bar in the canonical layout:
// [harness] [provider:model] [thinking: level] [ctx: X%] [$X.XXXX] [mcp: N] [mode: ask]
func FormatStatusBar(provider, model string, thinking string, contextPct float64, cost float64, mcpCount int, permissionMode string) string {
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "none"
	}
	if thinking == "" {
		thinking = "off"
	}
	if permissionMode == "" {
		permissionMode = "ask"
	}

	var ctxStr string
	if contextPct == float64(int(contextPct)) {
		ctxStr = fmt.Sprintf("%d%%", int(contextPct))
	} else {
		ctxStr = fmt.Sprintf("%.1f%%", contextPct)
	}

	return fmt.Sprintf("[harness] [%s:%s] [thinking: %s] [ctx: %s] [$%.4f] [mcp: %d] [mode: %s]",
		provider, model, thinking, ctxStr, cost, mcpCount, permissionMode)
}

// CompleteSlashCommand completes a slash command against the default builtin commands.
func CompleteSlashCommand(prefix string) []string {
	return CompleteSlashCommands(prefix, BuiltinSlashCommands)
}

// CompleteSlashCommands completes a command against a specified list of command names.
func CompleteSlashCommands(prefix string, commands []string) []string {
	hasSlash := strings.HasPrefix(prefix, "/")
	cleanPrefix := strings.ToLower(strings.TrimPrefix(prefix, "/"))

	var matches []string
	for _, cmd := range commands {
		cleanCmd := strings.ToLower(strings.TrimPrefix(cmd, "/"))
		if strings.HasPrefix(cleanCmd, cleanPrefix) {
			if hasSlash {
				matches = append(matches, "/"+cleanCmd)
			} else {
				matches = append(matches, cleanCmd)
			}
		}
	}
	sort.Strings(matches)
	return matches
}

// FilePathCompleter indexes files within a workspace root with a TTL and bounds.
type FilePathCompleter struct {
	workspaceRoot string
	ttl           time.Duration
	maxVisited    int
	maxMatches    int

	mu          sync.Mutex
	cachedFiles []string
	cachedAt    time.Time
}

// NewFilePathCompleter creates a completer for the specified workspace root.
func NewFilePathCompleter(workspaceRoot string) *FilePathCompleter {
	return &FilePathCompleter{
		workspaceRoot: workspaceRoot,
		ttl:           20 * time.Second,
		maxVisited:    1000,
		maxMatches:    50,
	}
}

// InvalidateCache invalidates the in-memory cached file index.
func (c *FilePathCompleter) InvalidateCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedFiles = nil
	c.cachedAt = time.Time{}
}

// Complete completes file paths starting with `@`. Non-@ tokens return nil.
func (c *FilePathCompleter) Complete(token string) []string {
	if !strings.HasPrefix(token, "@") {
		return nil
	}
	prefix := strings.TrimPrefix(token, "@")
	prefix = filepath.ToSlash(prefix)
	prefixLower := strings.ToLower(prefix)

	files := c.getOrBuildFileIndex()

	var matches []string
	for _, f := range files {
		fLower := strings.ToLower(f)
		if strings.HasPrefix(fLower, prefixLower) {
			matches = append(matches, "@"+f)
			if len(matches) >= c.maxMatches {
				break
			}
		}
	}
	return matches
}

func (c *FilePathCompleter) getOrBuildFileIndex() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedFiles != nil && time.Since(c.cachedAt) < c.ttl {
		return c.cachedFiles
	}

	c.cachedFiles = c.scanWorkspaceFiles()
	c.cachedAt = time.Now()
	return c.cachedFiles
}

func (c *FilePathCompleter) scanWorkspaceFiles() []string {
	root := c.workspaceRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		realRoot = root
	}

	var files []string
	visitedCount := 0

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}

		visitedCount++
		if visitedCount > c.maxVisited {
			return filepath.SkipAll
		}

		name := d.Name()
		isDir := d.IsDir()

		// Ignore hidden dirs, .git, node_modules, __pycache__
		if isDir {
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__" {
				return filepath.SkipDir
			}
		} else {
			if strings.HasPrefix(name, ".") {
				return nil
			}
		}

		isSymlink := d.Type()&os.ModeSymlink != 0
		if isSymlink {
			// Strictly exclude out-of-bounds symlinks using filepath.EvalSymlinks
			resolvedPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				if isDir {
					return filepath.SkipDir
				}
				return nil
			}
			resolvedPath, err = filepath.Abs(resolvedPath)
			if err != nil {
				if isDir {
					return filepath.SkipDir
				}
				return nil
			}

			// Check containment in realRoot
			relToReal, err := filepath.Rel(realRoot, resolvedPath)
			if err != nil || strings.HasPrefix(relToReal, "..") || filepath.IsAbs(relToReal) {
				if isDir {
					return filepath.SkipDir
				}
				return nil
			}

			fi, err := os.Stat(resolvedPath)
			if err != nil {
				if isDir {
					return filepath.SkipDir
				}
				return nil
			}
			if fi.IsDir() {
				base := filepath.Base(resolvedPath)
				if strings.HasPrefix(base, ".") || base == "node_modules" || base == "__pycache__" {
					return filepath.SkipDir
				}
				return nil
			}
		} else if isDir {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		files = append(files, relPath)

		return nil
	})

	sort.Strings(files)
	return files
}

// CompleteFilePath is a helper function to complete file paths in a workspace root.
func CompleteFilePath(workspaceRoot string, token string) []string {
	completer := NewFilePathCompleter(workspaceRoot)
	return completer.Complete(token)
}

// RenderDiff colorizes unified diffs (green +, red -, cyan @@), guards against
// empty/nil text, and caps at 150 lines with a truncation footer.
func RenderDiff(diffText string, writer io.Writer) {
	if writer == nil || diffText == "" {
		return
	}
	lines := strings.Split(diffText, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	const maxLines = 150
	limit := len(lines)
	truncated := 0
	if limit > maxLines {
		truncated = limit - maxLines
		limit = maxLines
	}

	noColor := os.Getenv("NO_COLOR") != ""

	for i := 0; i < limit; i++ {
		line := lines[i]
		if noColor {
			fmt.Fprintln(writer, line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			fmt.Fprintf(writer, "\x1b[36m%s\x1b[0m\n", line) // cyan
		case strings.HasPrefix(line, "+"):
			fmt.Fprintf(writer, "\x1b[32m%s\x1b[0m\n", line) // green
		case strings.HasPrefix(line, "-"):
			fmt.Fprintf(writer, "\x1b[31m%s\x1b[0m\n", line) // red
		default:
			fmt.Fprintln(writer, line)
		}
	}

	if truncated > 0 {
		fmt.Fprintf(writer, "... (%d lines truncated)\n", truncated)
	}
}

// RenderToolCall renders a visual box displaying the tool name and indented arguments.
func RenderToolCall(name string, arguments map[string]any, writer io.Writer) {
	if writer == nil {
		return
	}
	var argsText string
	if len(arguments) > 0 {
		data, err := json.MarshalIndent(arguments, "  ", "  ")
		if err == nil {
			argsText = string(data)
		} else {
			argsText = fmt.Sprintf("%v", arguments)
		}
	} else {
		argsText = "{}"
	}

	width := 60
	topBorder := fmt.Sprintf("┌─ [Tool Call] %s ", name)
	if len(topBorder) < width {
		topBorder += strings.Repeat("─", width-len(topBorder))
	}
	fmt.Fprintln(writer, topBorder)
	fmt.Fprintf(writer, "│ Arguments:\n")
	for _, line := range strings.Split(argsText, "\n") {
		fmt.Fprintf(writer, "│   %s\n", line)
	}
	fmt.Fprintln(writer, "└"+strings.Repeat("─", width-1))
}

// RenderToolOutcome renders a visual badge [OK] or [FAIL] with outcome details.
// Multi-line outcome summaries are capped at 10 lines and 500 characters with a truncation note.
func RenderToolOutcome(name string, outcomeSummary string, isError bool, writer io.Writer) {
	if writer == nil {
		return
	}
	noColor := os.Getenv("NO_COLOR") != ""
	badge := "[OK]"
	if isError {
		badge = "[FAIL]"
		if !noColor {
			badge = "\x1b[31m" + badge + "\x1b[0m"
		}
	} else {
		if !noColor {
			badge = "\x1b[32m" + badge + "\x1b[0m"
		}
	}

	summary := strings.TrimSpace(outcomeSummary)
	if summary != "" {
		lines := strings.Split(summary, "\n")
		truncated := false
		if len(lines) > 10 {
			lines = lines[:10]
			truncated = true
		}
		summary = strings.Join(lines, "\n")
		if len(summary) > 500 {
			summary = summary[:480]
			truncated = true
		}
		if truncated {
			summary = strings.TrimRight(summary, ". ") + " ... (truncated)"
		}
		fmt.Fprintf(writer, "%s %s: %s\n", badge, name, summary)
	} else {
		fmt.Fprintf(writer, "%s %s\n", badge, name)
	}
}

// DefaultChatHistoryPath returns ~/.harness/chat_history.
func DefaultChatHistoryPath() string {
	return filepath.Join(config.GetConfigDir(), "chat_history")
}

// AppendChatHistory writes a line to ~/.harness/chat_history with 0600 permissions.
func AppendChatHistory(historyPath string, line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if historyPath == "" {
		historyPath = DefaultChatHistoryPath()
	}
	dir := filepath.Dir(historyPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	config.EnforcePrivateDirPermissions(dir)

	f, err := os.OpenFile(historyPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, line); err != nil {
		return err
	}
	config.EnforcePrivatePermissions(historyPath)
	return nil
}

// LoadChatHistory reads history lines from ~/.harness/chat_history.
func LoadChatHistory(historyPath string, maxEntries int) ([]string, error) {
	if historyPath == "" {
		historyPath = DefaultChatHistoryPath()
	}
	f, err := os.Open(historyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		text := scanner.Text()
		if strings.TrimSpace(text) != "" {
			lines = append(lines, text)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if maxEntries > 0 && len(lines) > maxEntries {
		lines = lines[len(lines)-maxEntries:]
	}
	return lines, nil
}

// ChatTUISessionConfig configures the interactive TUI session.
type ChatTUISessionConfig struct {
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	WorkspaceRoot string
	HistoryPath   string
	PlainMode     bool
}

// ChatTUISession manages an interactive terminal session with status bar,
// autocompletion, visual tool banners, and persistent history.
type ChatTUISession struct {
	in            io.Reader
	out           io.Writer
	err           io.Writer
	bufioReader   *bufio.Reader
	workspaceRoot string
	historyPath   string
	plainMode     bool
	completer     *FilePathCompleter
	slashCommands []string

	mu           sync.Mutex
	history      []string
	historyIndex int

	// Status bar state
	provider       string
	model          string
	thinking       string
	contextPct     float64
	cost           float64
	mcpCount       int
	permissionMode string
}

// NewChatTUISession initializes a new ChatTUISession instance.
func NewChatTUISession(cfg ChatTUISessionConfig) *ChatTUISession {
	in := cfg.In
	if in == nil {
		in = os.Stdin
	}
	out := cfg.Out
	if out == nil {
		out = os.Stdout
	}
	errWriter := cfg.Err
	if errWriter == nil {
		errWriter = os.Stderr
	}
	root := cfg.WorkspaceRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	histPath := cfg.HistoryPath
	if histPath == "" {
		histPath = DefaultChatHistoryPath()
	}

	session := &ChatTUISession{
		in:             in,
		out:            out,
		err:            errWriter,
		bufioReader:    bufio.NewReader(in),
		workspaceRoot:  root,
		historyPath:    histPath,
		plainMode:      cfg.PlainMode,
		completer:      NewFilePathCompleter(root),
		slashCommands:  BuiltinSlashCommands,
		permissionMode: "ask",
		thinking:       "off",
	}

	if hist, err := LoadChatHistory(histPath, 500); err == nil {
		session.history = hist
		session.historyIndex = len(hist)
	}

	return session
}

// SetPlainMode updates whether plain readline fallback is active.
func (s *ChatTUISession) SetPlainMode(plain bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plainMode = plain
}

// IsPlainMode returns whether plain readline mode is active.
func (s *ChatTUISession) IsPlainMode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plainMode
}

// UpdateStatusBar updates the status values displayed in the anchored status bar.
func (s *ChatTUISession) UpdateStatusBar(provider, model, thinking string, contextPct, cost float64, mcpCount int, permissionMode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provider = provider
	s.model = model
	s.thinking = thinking
	s.contextPct = contextPct
	s.cost = cost
	s.mcpCount = mcpCount
	s.permissionMode = permissionMode
}

// RenderStatusBar returns the formatted status bar text.
func (s *ChatTUISession) RenderStatusBar() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return FormatStatusBar(s.provider, s.model, s.thinking, s.contextPct, s.cost, s.mcpCount, s.permissionMode)
}

// PrintStatusBar prints the styled status bar if not in plain mode.
func (s *ChatTUISession) PrintStatusBar() {
	if s.plainMode {
		return
	}
	bar := s.RenderStatusBar()
	if os.Getenv("NO_COLOR") == "" {
		bar = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Render(bar)
	}
	fmt.Fprintln(s.out, bar)
}

// AddHistory adds an entry to memory and persists to ~/.harness/chat_history.
func (s *ChatTUISession) AddHistory(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	s.mu.Lock()
	s.history = append(s.history, line)
	s.historyIndex = len(s.history)
	s.mu.Unlock()
	_ = AppendChatHistory(s.historyPath, line)
}

// History returns a copy of loaded history lines.
func (s *ChatTUISession) History() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.history))
	copy(cp, s.history)
	return cp
}

// CompleteToken completes a single token starting with `/` or `@`.
func (s *ChatTUISession) CompleteToken(token string) []string {
	if strings.HasPrefix(token, "/") {
		return CompleteSlashCommands(token, s.slashCommands)
	}
	if strings.HasPrefix(token, "@") {
		return s.completer.Complete(token)
	}
	return nil
}

// CompleteLine completes the last whitespace-delimited token on a line.
func (s *ChatTUISession) CompleteLine(line string) []string {
	token := lastToken(line)
	return s.CompleteToken(token)
}

func lastToken(s string) string {
	idx := strings.LastIndexAny(s, " \t")
	if idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// ReadLine prints the prompt and status bar, then reads a line of input.
// When storeHistory is true, non-empty lines are added to chat history.
// The status bar is only printed for primary conversational turns (storeHistory == true).
func (s *ChatTUISession) ReadLine(prompt string, storeHistory bool) (string, error) {
	if !s.plainMode && storeHistory {
		s.PrintStatusBar()
	}

	line, err := s.readRawOrPlainLine(prompt)
	if err != nil {
		return "", err
	}

	trimmed := strings.TrimSpace(line)
	if storeHistory && trimmed != "" {
		s.AddHistory(line)
	}

	return line, nil
}

func (s *ChatTUISession) readRawOrPlainLine(prompt string) (string, error) {
	inFile, inIsFile := s.in.(*os.File)
	outFile, outIsFile := s.out.(*os.File)
	if !inIsFile || !outIsFile || s.plainMode || !term.IsTerminal(int(inFile.Fd())) || !term.IsTerminal(int(outFile.Fd())) {
		return s.readPlainLine(prompt)
	}
	return s.readInteractiveTerminalLine(inFile, prompt)
}

func (s *ChatTUISession) readPlainLine(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(s.out, prompt)
	}
	line, err := s.bufioReader.ReadString('\n')
	if err != nil {
		if len(line) > 0 && errors.Is(err, io.EOF) {
			return strings.TrimRight(line, "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (s *ChatTUISession) readInteractiveTerminalLine(file *os.File, prompt string) (string, error) {
	oldState, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return s.readPlainLine(prompt)
	}
	defer term.Restore(int(file.Fd()), oldState)

	fmt.Fprint(s.out, prompt)

	var buf []rune
	cursorPos := 0
	var draft []rune
	draftSaved := false

	s.mu.Lock()
	s.historyIndex = len(s.history)
	s.mu.Unlock()

	refreshLine := func() {
		fmt.Fprintf(s.out, "\r\x1b[K%s%s", prompt, string(buf))
		if back := len(buf) - cursorPos; back > 0 {
			fmt.Fprintf(s.out, "\x1b[%dD", back)
		}
	}

	for {
		r, _, err := s.bufioReader.ReadRune()
		if err != nil {
			return string(buf), err
		}

		switch r {
		case '\r', '\n':
			fmt.Fprint(s.out, "\r\n")
			return string(buf), nil
		case 3: // Ctrl+C
			fmt.Fprint(s.out, "^C\r\n")
			return "", context.Canceled
		case 4: // Ctrl+D
			if len(buf) == 0 {
				fmt.Fprint(s.out, "\r\n")
				return "", io.EOF
			}
		case 127, '\b': // Backspace
			if cursorPos > 0 {
				buf = append(buf[:cursorPos-1], buf[cursorPos:]...)
				cursorPos--
				refreshLine()
			}
		case '\t': // Tab completion
			current := string(buf[:cursorPos])
			token := lastToken(current)
			if token != "" {
				matches := s.CompleteToken(token)
				if len(matches) == 1 {
					match := matches[0]
					prefixPart := current[:len(current)-len(token)]
					remainder := string(buf[cursorPos:])
					newLeft := prefixPart + match
					buf = []rune(newLeft + remainder)
					cursorPos = len([]rune(newLeft))
					refreshLine()
				} else if len(matches) > 1 {
					fmt.Fprintf(s.out, "\r\n  %s\r\n", strings.Join(matches, "  "))
					refreshLine()
				}
			}
		case 27: // Escape sequences
			if s.bufioReader.Buffered() == 0 {
				time.Sleep(10 * time.Millisecond)
			}
			if s.bufioReader.Buffered() == 0 {
				continue
			}
			b1, err := s.bufioReader.ReadByte()
			if err != nil {
				continue
			}
			if b1 == '[' {
				if s.bufioReader.Buffered() == 0 {
					time.Sleep(10 * time.Millisecond)
				}
				b2, err := s.bufioReader.ReadByte()
				if err != nil {
					continue
				}
				switch b2 {
				case 'A': // Arrow Up
					s.mu.Lock()
					if len(s.history) > 0 {
						if !draftSaved && s.historyIndex == len(s.history) {
							draft = make([]rune, len(buf))
							copy(draft, buf)
							draftSaved = true
						}
						if s.historyIndex > 0 {
							s.historyIndex--
							buf = []rune(s.history[s.historyIndex])
							cursorPos = len(buf)
							refreshLine()
						}
					}
					s.mu.Unlock()
				case 'B': // Arrow Down
					s.mu.Lock()
					if s.historyIndex < len(s.history)-1 {
						s.historyIndex++
						buf = []rune(s.history[s.historyIndex])
						cursorPos = len(buf)
						refreshLine()
					} else if s.historyIndex == len(s.history)-1 {
						s.historyIndex = len(s.history)
						if draftSaved {
							buf = make([]rune, len(draft))
							copy(buf, draft)
						} else {
							buf = nil
						}
						cursorPos = len(buf)
						refreshLine()
					}
					s.mu.Unlock()
				case 'C': // Arrow Right
					if cursorPos < len(buf) {
						cursorPos++
						refreshLine()
					}
				case 'D': // Arrow Left
					if cursorPos > 0 {
						cursorPos--
						refreshLine()
					}
				case 'H': // Home
					cursorPos = 0
					refreshLine()
				case 'F': // End
					cursorPos = len(buf)
					refreshLine()
				case '1', '2', '3', '4', '5', '6':
					// Multi-byte sequence e.g. \x1b[3~ (Delete), consume until ~
					for {
						if s.bufioReader.Buffered() == 0 {
							time.Sleep(5 * time.Millisecond)
						}
						if s.bufioReader.Buffered() == 0 {
							break
						}
						bx, err := s.bufioReader.ReadByte()
						if err != nil {
							break
						}
						if bx == '~' {
							break
						}
					}
					if b2 == '3' { // Delete
						if cursorPos < len(buf) {
							buf = append(buf[:cursorPos], buf[cursorPos+1:]...)
							refreshLine()
						}
					} else if b2 == '1' { // Home
						cursorPos = 0
						refreshLine()
					} else if b2 == '4' { // End
						cursorPos = len(buf)
						refreshLine()
					}
				}
			} else if b1 == 'O' {
				if s.bufioReader.Buffered() == 0 {
					time.Sleep(10 * time.Millisecond)
				}
				b2, err := s.bufioReader.ReadByte()
				if err == nil {
					switch b2 {
					case 'H': // Home
						cursorPos = 0
						refreshLine()
					case 'F': // End
						cursorPos = len(buf)
						refreshLine()
					case 'C': // Right
						if cursorPos < len(buf) {
							cursorPos++
							refreshLine()
						}
					case 'D': // Left
						if cursorPos > 0 {
							cursorPos--
							refreshLine()
						}
					}
				}
			}
		default:
			if r >= 32 {
				buf = append(buf[:cursorPos], append([]rune{r}, buf[cursorPos:]...)...)
				cursorPos++
				refreshLine()
			}
		}
	}
}
