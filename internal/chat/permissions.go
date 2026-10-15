package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Permission modes supported by harness chat.
const (
	PermissionModeAsk      = "ask"
	PermissionModeAutoEdit = "auto-edit"
	PermissionModeYolo     = "yolo"
)

// Tool classification categories.
const (
	ToolClassReadOnly = "read_only"
	ToolClassEdit     = "edit"
	ToolClassCommand  = "command"
)

// Exact sets for tool classification to prevent prefix-spoofing bypasses.
var readOnlyTools = map[string]struct{}{
	"read_file":    {},
	"list_dir":     {},
	"grep_search":  {},
	"ast_outline":  {},
	"ask_question": {},
}

var editTools = map[string]struct{}{
	"edit_file":  {},
	"write_file": {},
}

// ClassifyTool categorizes a tool by its exact name.
// Any tool not in readOnlyTools or editTools is classified as command.
func ClassifyTool(name string) string {
	cleaned := strings.TrimSpace(name)
	if _, ok := readOnlyTools[cleaned]; ok {
		return ToolClassReadOnly
	}
	if _, ok := editTools[cleaned]; ok {
		return ToolClassEdit
	}
	return ToolClassCommand
}

// PromptDecision represents a user's choice when approving a tool execution.
type PromptDecision string

const (
	DecisionAllowOnce    PromptDecision = "allow_once"
	DecisionAllowSession PromptDecision = "allow_session"
	DecisionDeny         PromptDecision = "deny"
)

// PromptCallback is called when interactive confirmation is needed for a tool.
// It accepts ctx for cancellation on SIGINT/Ctrl+C and returns the decision,
// optional feedback/reasoning (for denials), and any error.
type PromptCallback func(ctx context.Context, name string, arguments map[string]any) (PromptDecision, string, error)

// ExtractCommandString retrieves the target command string from tool arguments.
func ExtractCommandString(name string, arguments map[string]any) string {
	if arguments == nil {
		return ""
	}
	for _, key := range []string{"command", "CommandLine", "cmd", "script"} {
		if val, ok := arguments[key]; ok {
			if s, ok := val.(string); ok && s != "" {
				return s
			}
		}
	}
	encoded, err := json.Marshal(arguments)
	if err == nil {
		return string(encoded)
	}
	return fmt.Sprint(arguments)
}

// ExtractFilePath retrieves the target file path from tool arguments for edit tools.
func ExtractFilePath(name string, arguments map[string]any) string {
	if arguments == nil {
		return ""
	}
	for _, key := range []string{"path", "filepath", "file", "target", "TargetFile"} {
		if val, ok := arguments[key]; ok {
			if s, ok := val.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// ExtractCwd retrieves the working directory from tool arguments if present.
func ExtractCwd(arguments map[string]any) string {
	if arguments == nil {
		return ""
	}
	for _, key := range []string{"cwd", "Cwd", "working_dir", "working_directory"} {
		if val, ok := arguments[key]; ok {
			if s, ok := val.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// CommandKey constructs a scoped cache key including tool name, working directory, and command string.
func CommandKey(name, cwd, cmdStr string) string {
	return fmt.Sprintf("%s:%s:%s", strings.TrimSpace(name), cwd, cmdStr)
}

// EditKey constructs a scoped cache key for edit tools targeting a specific file path.
func EditKey(name, path string) string {
	return fmt.Sprintf("%s:%s", strings.TrimSpace(name), path)
}

func formatDenialFeedback(feedback string) string {
	trimmed := strings.TrimSpace(feedback)
	if trimmed == "" {
		return "Tool execution denied by user."
	}
	if strings.HasPrefix(trimmed, "Tool execution denied") {
		return trimmed
	}
	return fmt.Sprintf("Tool execution denied by user: %s", trimmed)
}

// PermissionPolicy evaluates and caches tool execution allowances.
type PermissionPolicy struct {
	mu              sync.RWMutex
	mode            string
	isInteractive   bool
	allowedTools    map[string]bool
	allowedCommands map[string]bool
}

// NewPermissionPolicy initializes a PermissionPolicy with mode and interactive flag.
func NewPermissionPolicy(mode string, isInteractive bool) *PermissionPolicy {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized != PermissionModeAsk && normalized != PermissionModeAutoEdit && normalized != PermissionModeYolo {
		normalized = PermissionModeAsk
	}
	return &PermissionPolicy{
		mode:            normalized,
		isInteractive:   isInteractive,
		allowedTools:    make(map[string]bool),
		allowedCommands: make(map[string]bool),
	}
}

// Mode returns the active permission mode.
func (p *PermissionPolicy) Mode() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// SetMode sets the active permission mode.
func (p *PermissionPolicy) SetMode(mode string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized != PermissionModeAsk && normalized != PermissionModeAutoEdit && normalized != PermissionModeYolo {
		return fmt.Errorf("invalid permission mode %q (must be ask, auto-edit, or yolo)", mode)
	}
	p.mode = normalized
	return nil
}

// IsInteractive reports whether the policy runs in an interactive session.
func (p *PermissionPolicy) IsInteractive() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.isInteractive
}

// SetInteractive sets whether the session is interactive.
func (p *PermissionPolicy) SetInteractive(interactive bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isInteractive = interactive
}

// IsToolAllowed reports whether a tool name is cached as session-allowed.
func (p *PermissionPolicy) IsToolAllowed(name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowedTools[strings.TrimSpace(name)]
}

// IsFileAllowed reports whether a specific file path is session-allowed for an edit tool.
func (p *PermissionPolicy) IsFileAllowed(name, path string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowedTools[EditKey(name, path)]
}

// IsCommandAllowed reports whether a specific command string is session-allowed for a tool.
func (p *PermissionPolicy) IsCommandAllowed(name, cmdStr string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	keyWithCwd := CommandKey(name, "", cmdStr)
	if p.allowedCommands[keyWithCwd] {
		return true
	}
	legacyKey := fmt.Sprintf("%s:%s", strings.TrimSpace(name), cmdStr)
	return p.allowedCommands[legacyKey]
}

// IsCommandWithCwdAllowed reports whether a tool+cwd+command tuple is session-allowed.
func (p *PermissionPolicy) IsCommandWithCwdAllowed(name, cwd, cmdStr string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowedCommands[CommandKey(name, cwd, cmdStr)]
}

// CheckPermission evaluates whether the tool execution is allowed.
// Returns (allowed bool, message string, error).
func (p *PermissionPolicy) CheckPermission(ctx context.Context, name string, arguments map[string]any, promptFn PromptCallback) (bool, string, error) {
	p.mu.Lock()
	cleanedName := strings.TrimSpace(name)

	// 1. If mode == "yolo": return allowed.
	if p.mode == PermissionModeYolo {
		p.mu.Unlock()
		return true, "", nil
	}

	// 2. If ClassifyTool(name) == "read_only": return allowed.
	classification := ClassifyTool(cleanedName)
	if classification == ToolClassReadOnly {
		p.mu.Unlock()
		return true, "", nil
	}

	// 3. If mode == "auto-edit" and ClassifyTool(name) == "edit": return allowed.
	if p.mode == PermissionModeAutoEdit && classification == ToolClassEdit {
		p.mu.Unlock()
		return true, "", nil
	}

	// 4. Check session allowed (scoped to exact command or file path).
	if classification == ToolClassCommand {
		cmdStr := ExtractCommandString(cleanedName, arguments)
		cwd := ExtractCwd(arguments)
		key := CommandKey(cleanedName, cwd, cmdStr)
		legacyKey := fmt.Sprintf("%s:%s", cleanedName, cmdStr)
		if p.allowedCommands != nil && (p.allowedCommands[key] || p.allowedCommands[legacyKey]) {
			p.mu.Unlock()
			return true, "", nil
		}
	} else if classification == ToolClassEdit {
		filePath := ExtractFilePath(cleanedName, arguments)
		if filePath != "" {
			key := EditKey(cleanedName, filePath)
			if p.allowedTools != nil && p.allowedTools[key] {
				p.mu.Unlock()
				return true, "", nil
			}
		} else if p.allowedTools != nil && p.allowedTools[cleanedName] {
			p.mu.Unlock()
			return true, "", nil
		}
	} else {
		if p.allowedTools != nil && p.allowedTools[cleanedName] {
			p.mu.Unlock()
			return true, "", nil
		}
	}

	// 5. If not interactive: return denied with clear error.
	if !p.isInteractive {
		p.mu.Unlock()
		return false, "Tool requires interactive confirmation; run interactively or with --permissions yolo", nil
	}

	// 6. If promptFn == nil: return denied.
	if promptFn == nil {
		p.mu.Unlock()
		return false, "Tool requires interactive confirmation; run interactively or with --permissions yolo", nil
	}

	// Explicitly unlock while prompting user to avoid holding lock across I/O.
	mode := p.mode
	p.mu.Unlock()

	decision, feedback, err := promptFn(ctx, cleanedName, arguments)

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check mode in case it changed while prompting.
	if p.mode != mode && p.mode == PermissionModeYolo {
		return true, "", nil
	}

	if err != nil {
		return false, "", err
	}

	switch decision {
	case DecisionAllowOnce:
		return true, "", nil

	case DecisionAllowSession:
		if classification == ToolClassCommand {
			cmdStr := ExtractCommandString(cleanedName, arguments)
			cwd := ExtractCwd(arguments)
			key := CommandKey(cleanedName, cwd, cmdStr)
			if p.allowedCommands == nil {
				p.allowedCommands = make(map[string]bool)
			}
			p.allowedCommands[key] = true
		} else if classification == ToolClassEdit {
			filePath := ExtractFilePath(cleanedName, arguments)
			if p.allowedTools == nil {
				p.allowedTools = make(map[string]bool)
			}
			if filePath != "" {
				p.allowedTools[EditKey(cleanedName, filePath)] = true
			} else {
				p.allowedTools[cleanedName] = true
			}
		} else {
			if p.allowedTools == nil {
				p.allowedTools = make(map[string]bool)
			}
			p.allowedTools[cleanedName] = true
		}
		return true, "", nil

	case DecisionDeny:
		denialMsg := formatDenialFeedback(feedback)
		return false, denialMsg, nil

	default:
		denialMsg := formatDenialFeedback(feedback)
		return false, denialMsg, nil
	}
}
