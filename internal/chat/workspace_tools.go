package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var scrubPatterns = []string{
	"DATABASE_URL",
	"TOKEN",
	"KEY",
	"AUTH",
	"SECRET",
	"PASS",
	"CREDENTIAL",
	"PRIVATE",
	"PAT",
	"COOKIE",
}

func cleanExtendedPrefix(p string) string {
	if len(p) >= 8 && strings.EqualFold(p[:8], `\\?\UNC\`) {
		return `\\` + p[8:]
	}
	if strings.HasPrefix(p, `\\?\`) {
		return p[4:]
	}
	return p
}

func isSubpath(root, target string) bool {
	root = filepath.Clean(cleanExtendedPrefix(root))
	target = filepath.Clean(cleanExtendedPrefix(target))

	rel, err := filepath.Rel(root, target)
	if err == nil {
		if rel == "." {
			return true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}

	if runtime.GOOS == "windows" {
		lowerRoot := strings.ToLower(root)
		lowerTarget := strings.ToLower(target)
		lowerRel, err := filepath.Rel(lowerRoot, lowerTarget)
		if err == nil {
			if lowerRel == "." {
				return true
			}
			if lowerRel != ".." && !strings.HasPrefix(lowerRel, ".."+string(filepath.Separator)) {
				return true
			}
		}
	}

	return false
}

func evalSymlinksDeep(target string) (string, error) {
	curr := target
	var tail []string
	for {
		evalCurr, err := filepath.EvalSymlinks(curr)
		if err == nil {
			evalCurr = cleanExtendedPrefix(evalCurr)
			for i := len(tail) - 1; i >= 0; i-- {
				evalCurr = filepath.Join(evalCurr, tail[i])
			}
			return filepath.Clean(evalCurr), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return target, nil
		}
		tail = append(tail, filepath.Base(curr))
		curr = parent
	}
}

// ResolveWorkspacePath verifies and resolves pathStr strictly within rootStr, preventing symlink escape or junction traversal.
func ResolveWorkspacePath(pathStr, rootStr string) (string, error) {
	if strings.TrimSpace(pathStr) == "" {
		return "", errors.New("path cannot be empty")
	}
	if strings.TrimSpace(rootStr) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current working directory: %w", err)
		}
		rootStr = cwd
	}

	rootAbs, err := filepath.Abs(rootStr)
	if err != nil {
		return "", fmt.Errorf("invalid workspace root: %w", err)
	}
	evalRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		evalRoot = rootAbs
	}
	evalRoot = cleanExtendedPrefix(filepath.Clean(evalRoot))

	var target string
	if filepath.IsAbs(pathStr) {
		target = filepath.Clean(pathStr)
	} else {
		target = filepath.Clean(filepath.Join(evalRoot, pathStr))
	}
	target = cleanExtendedPrefix(target)

	evalTarget, err := evalSymlinksDeep(target)
	if err != nil {
		return "", err
	}
	evalTarget = cleanExtendedPrefix(filepath.Clean(evalTarget))

	if !isSubpath(evalRoot, evalTarget) {
		return "", fmt.Errorf("access denied: path %q is outside workspace root %q", pathStr, rootStr)
	}

	return evalTarget, nil
}

func detectLineEnding(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func normalizeLineEndings(s, ending string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if ending == "\r\n" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

func argToInt(v any, def int) int {
	if v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

func argToBool(v any, def bool) bool {
	if v == nil {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true") || b == "1"
	}
	return def
}

func argToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func generateUnifiedDiff(relPath, oldText, newText string) string {
	displayPath := filepath.ToSlash(relPath)
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")

	oldLines := strings.Split(oldNorm, "\n")
	newLines := strings.Split(newNorm, "\n")

	commonPrefix := 0
	for commonPrefix < len(oldLines) && commonPrefix < len(newLines) && oldLines[commonPrefix] == newLines[commonPrefix] {
		commonPrefix++
	}

	commonSuffix := 0
	for commonSuffix < (len(oldLines)-commonPrefix) && commonSuffix < (len(newLines)-commonPrefix) &&
		oldLines[len(oldLines)-1-commonSuffix] == newLines[len(newLines)-1-commonSuffix] {
		commonSuffix++
	}

	ctxBeforeStart := commonPrefix - 3
	if ctxBeforeStart < 0 {
		ctxBeforeStart = 0
	}
	ctxBefore := oldLines[ctxBeforeStart:commonPrefix]

	delLines := oldLines[commonPrefix : len(oldLines)-commonSuffix]
	addLines := newLines[commonPrefix : len(newLines)-commonSuffix]

	ctxAfterEnd := len(oldLines) - commonSuffix + 3
	if ctxAfterEnd > len(oldLines) {
		ctxAfterEnd = len(oldLines)
	}
	ctxAfter := oldLines[len(oldLines)-commonSuffix : ctxAfterEnd]

	origStart := ctxBeforeStart + 1
	origCount := len(ctxBefore) + len(delLines) + len(ctxAfter)
	newStart := ctxBeforeStart + 1
	newCount := len(ctxBefore) + len(addLines) + len(ctxAfter)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- a/%s\n", displayPath))
	sb.WriteString(fmt.Sprintf("+++ b/%s\n", displayPath))
	sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", origStart, origCount, newStart, newCount))
	for _, l := range ctxBefore {
		sb.WriteString(" " + l + "\n")
	}
	for _, l := range delLines {
		sb.WriteString("-" + l + "\n")
	}
	for _, l := range addLines {
		sb.WriteString("+" + l + "\n")
	}
	for _, l := range ctxAfter {
		sb.WriteString(" " + l + "\n")
	}
	return sb.String()
}

func HandleReadFile(args map[string]any, workspaceRoot string) (any, error) {
	pathStr := argToString(args["path"])
	if strings.TrimSpace(pathStr) == "" {
		return nil, errors.New("read_file requires non-empty 'path'")
	}

	resolved, err := ResolveWorkspacePath(pathStr, workspaceRoot)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", pathStr)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("cannot read directory as file: %s", pathStr)
	}
	if fi.Size() > 10*1024*1024 {
		return nil, fmt.Errorf("file %s exceeds maximum size of 10MB", pathStr)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", pathStr, err)
	}

	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, fmt.Errorf("file %s is binary or contains invalid UTF-8 and cannot be read", pathStr)
	}

	text := string(data)
	textNorm := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(textNorm, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" && strings.HasSuffix(textNorm, "\n") {
		lines = lines[:len(lines)-1]
	}

	totalLines := len(lines)
	startLine := argToInt(args["start_line"], 1)
	if startLine < 1 {
		startLine = 1
	}
	endLine := argToInt(args["end_line"], totalLines)
	if endLine < 1 || endLine > totalLines {
		endLine = totalLines
	}

	if totalLines == 0 || startLine > totalLines || startLine > endLine {
		return map[string]any{
			"content":     "",
			"path":        pathStr,
			"start_line":  startLine,
			"end_line":    endLine,
			"total_lines": totalLines,
		}, nil
	}

	if endLine-startLine+1 > 2000 {
		endLine = startLine + 1999
	}

	var sb strings.Builder
	for i := startLine; i <= endLine; i++ {
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(": ")
		sb.WriteString(lines[i-1])
		sb.WriteByte('\n')
	}

	return map[string]any{
		"content":     sb.String(),
		"path":        pathStr,
		"start_line":  startLine,
		"end_line":    endLine,
		"total_lines": totalLines,
	}, nil
}

func HandleListDir(args map[string]any, workspaceRoot string) (any, error) {
	pathStr := argToString(args["path"])
	if strings.TrimSpace(pathStr) == "" {
		pathStr = "."
	}

	resolved, err := ResolveWorkspacePath(pathStr, workspaceRoot)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("directory not found: %s", pathStr)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", pathStr)
	}

	depth := argToInt(args["depth"], 1)
	if depth < 1 {
		depth = 1
	}
	if depth > 10 {
		depth = 10
	}
	includeHidden := argToBool(args["include_hidden"], false)

	visited := make(map[string]bool)
	var entries []string

	var walk func(currentDir string, currentDepth int, relPrefix string) error
	walk = func(currentDir string, currentDepth int, relPrefix string) error {
		realDir, err := filepath.EvalSymlinks(currentDir)
		if err != nil {
			realDir = currentDir
		}
		realDir = cleanExtendedPrefix(filepath.Clean(realDir))
		realKey := strings.ToLower(realDir)
		if visited[realKey] {
			return nil
		}
		visited[realKey] = true

		dirEntries, err := os.ReadDir(currentDir)
		if err != nil {
			return err
		}

		sort.Slice(dirEntries, func(i, j int) bool {
			return dirEntries[i].Name() < dirEntries[j].Name()
		})

		for _, de := range dirEntries {
			name := de.Name()
			if !includeHidden && strings.HasPrefix(name, ".") {
				continue
			}

			entryRel := name
			if relPrefix != "" {
				entryRel = filepath.Join(relPrefix, name)
			}
			entryFull := filepath.Join(currentDir, name)

			if _, err := ResolveWorkspacePath(entryFull, workspaceRoot); err != nil {
				continue
			}

			isDir := de.IsDir()
			if !isDir && (de.Type()&os.ModeSymlink != 0) {
				if targetFi, err := os.Stat(entryFull); err == nil && targetFi.IsDir() {
					isDir = true
				}
			}

			displayRel := filepath.ToSlash(entryRel)
			if isDir {
				entries = append(entries, fmt.Sprintf("[DIR]  %s", displayRel))
				if currentDepth < depth {
					_ = walk(entryFull, currentDepth+1, entryRel)
				}
			} else {
				entries = append(entries, fmt.Sprintf("[FILE] %s", displayRel))
			}
		}
		return nil
	}

	if err := walk(resolved, 1, ""); err != nil {
		return nil, err
	}

	formatted := strings.Join(entries, "\n")
	return map[string]any{
		"output":  formatted,
		"entries": entries,
		"count":   len(entries),
	}, nil
}

func HandleGrepSearch(args map[string]any, workspaceRoot string) (any, error) {
	pattern := argToString(args["pattern"])
	if strings.TrimSpace(pattern) == "" {
		return nil, errors.New("grep_search requires non-empty 'pattern'")
	}

	pathStr := argToString(args["path"])
	if strings.TrimSpace(pathStr) == "" {
		pathStr = "."
	}

	globPattern := argToString(args["glob"])

	resolved, err := ResolveWorkspacePath(pathStr, workspaceRoot)
	if err != nil {
		return nil, err
	}

	re, regexErr := regexp.Compile(pattern)

	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("path not found: %s", pathStr)
	}

	var matches []string
	const maxMatches = 500

	searchFile := func(filePath, relDisplay string) error {
		if _, err := ResolveWorkspacePath(filePath, workspaceRoot); err != nil {
			return nil
		}
		if len(matches) >= maxMatches {
			return nil
		}
		if globPattern != "" {
			matched := false
			baseName := filepath.Base(filePath)
			if m, err := filepath.Match(globPattern, baseName); err == nil && m {
				matched = true
			} else if m, err := filepath.Match(globPattern, filepath.ToSlash(relDisplay)); err == nil && m {
				matched = true
			}
			if !matched {
				return nil
			}
		}

		info, err := os.Stat(filePath)
		if err != nil || info.IsDir() || info.Size() > 10*1024*1024 {
			return nil
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			return nil
		}

		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for idx, line := range lines {
			lineNum := idx + 1
			matched := false
			if regexErr == nil {
				matched = re.MatchString(line)
			} else {
				matched = strings.Contains(line, pattern)
			}
			if matched {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(relDisplay), lineNum, line))
				if len(matches) >= maxMatches {
					break
				}
			}
		}
		return nil
	}

	if !fi.IsDir() {
		rel, err := filepath.Rel(workspaceRoot, resolved)
		if err != nil {
			rel = filepath.Base(resolved)
		}
		if err := searchFile(resolved, rel); err != nil {
			return nil, err
		}
	} else {
		visited := make(map[string]bool)
		err = filepath.Walk(resolved, func(currPath string, currInfo os.FileInfo, walkErr error) error {
			if walkErr != nil || len(matches) >= maxMatches {
				return nil
			}
			if _, err := ResolveWorkspacePath(currPath, workspaceRoot); err != nil {
				if currInfo.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			name := currInfo.Name()
			if currInfo.IsDir() {
				if name == ".git" || (strings.HasPrefix(name, ".") && currPath != resolved) {
					return filepath.SkipDir
				}
				realDir, err := filepath.EvalSymlinks(currPath)
				if err != nil {
					realDir = currPath
				}
				key := strings.ToLower(cleanExtendedPrefix(realDir))
				if visited[key] {
					return filepath.SkipDir
				}
				visited[key] = true
				return nil
			}
			rel, err := filepath.Rel(workspaceRoot, currPath)
			if err != nil {
				rel = currPath
			}
			return searchFile(currPath, rel)
		})
		if err != nil {
			return nil, err
		}
	}

	return map[string]any{
		"matches": matches,
		"output":  strings.Join(matches, "\n"),
		"count":   len(matches),
	}, nil
}

func HandleEditFile(args map[string]any, workspaceRoot string) (any, error) {
	pathStr := argToString(args["path"])
	if strings.TrimSpace(pathStr) == "" {
		return nil, errors.New("edit_file requires non-empty 'path'")
	}
	targetRaw, hasTarget := args["target_content"]
	if !hasTarget || targetRaw == nil {
		return nil, errors.New("edit_file requires 'target_content'")
	}
	targetContent := argToString(targetRaw)
	if targetContent == "" {
		return nil, errors.New("edit_file requires non-empty 'target_content'")
	}
	repRaw, hasRep := args["replacement_content"]
	if !hasRep || repRaw == nil {
		return nil, errors.New("edit_file requires 'replacement_content'")
	}
	replacementContent := argToString(repRaw)

	resolved, err := ResolveWorkspacePath(pathStr, workspaceRoot)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", pathStr)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("cannot edit directory: %s", pathStr)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", pathStr, err)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, fmt.Errorf("file %s is binary or contains invalid UTF-8 and cannot be edited", pathStr)
	}

	fileEnding := detectLineEnding(data)
	originalText := string(data)

	matchTarget := targetContent
	if strings.Count(originalText, matchTarget) != 1 {
		normTarget := normalizeLineEndings(targetContent, fileEnding)
		if strings.Count(originalText, normTarget) == 1 {
			matchTarget = normTarget
		}
	}

	count := strings.Count(originalText, matchTarget)
	if count == 0 {
		return nil, fmt.Errorf("target_content not found in %s", pathStr)
	}
	if count > 1 {
		return nil, fmt.Errorf("target_content found %d times in %s; target must be unique", count, pathStr)
	}

	normReplacement := normalizeLineEndings(replacementContent, fileEnding)
	newText := strings.Replace(originalText, matchTarget, normReplacement, 1)

	diff := generateUnifiedDiff(pathStr, originalText, newText)

	if err := os.WriteFile(resolved, []byte(newText), fi.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("failed to write edited file %s: %w", pathStr, err)
	}

	return map[string]any{
		"path":   pathStr,
		"diff":   diff,
		"status": "success",
	}, nil
}

func HandleWriteFile(args map[string]any, workspaceRoot string) (any, error) {
	pathStr := argToString(args["path"])
	if strings.TrimSpace(pathStr) == "" {
		return nil, errors.New("write_file requires non-empty 'path'")
	}
	contentRaw, hasContent := args["content"]
	if !hasContent || contentRaw == nil {
		return nil, errors.New("write_file requires 'content'")
	}
	content := argToString(contentRaw)

	overwrite := argToBool(args["overwrite"], false)
	appendMode := argToBool(args["append"], false)

	if overwrite && appendMode {
		return nil, errors.New("cannot combine overwrite=true and append=true")
	}

	resolved, err := ResolveWorkspacePath(pathStr, workspaceRoot)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(resolved)
	fileExists := err == nil
	if fileExists && fi.IsDir() {
		return nil, fmt.Errorf("path is an existing directory: %s", pathStr)
	}
	if fileExists && !overwrite && !appendMode {
		return nil, fmt.Errorf("file %s already exists; set overwrite=true to overwrite or append=true to append", pathStr)
	}

	parentDir := filepath.Dir(resolved)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create parent directories for %s: %w", pathStr, err)
	}

	if fileExists {
		existingData, err := os.ReadFile(resolved)
		if err == nil && len(existingData) > 0 {
			fileEnding := detectLineEnding(existingData)
			content = normalizeLineEndings(content, fileEnding)
		}
	}

	if appendMode {
		f, err := os.OpenFile(resolved, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open file for appending %s: %w", pathStr, err)
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			return nil, fmt.Errorf("failed to append to file %s: %w", pathStr, err)
		}
	} else {
		perm := os.FileMode(0644)
		if fileExists {
			perm = fi.Mode().Perm()
		}
		if err := os.WriteFile(resolved, []byte(content), perm); err != nil {
			return nil, fmt.Errorf("failed to write file %s: %w", pathStr, err)
		}
	}

	return map[string]any{
		"path":          pathStr,
		"bytes_written": len(content),
		"status":        "success",
	}, nil
}

func scrubEnvironment(env []string) []string {
	var scrubbed []string
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 0 {
			continue
		}
		varNameUpper := strings.ToUpper(parts[0])
		shouldScrub := false
		for _, pattern := range scrubPatterns {
			if pattern == "PAT" && (varNameUpper == "PATH" || varNameUpper == "PATHEXT" || strings.HasPrefix(varNameUpper, "PATH_")) {
				continue
			}
			if strings.Contains(varNameUpper, pattern) {
				shouldScrub = true
				break
			}
		}
		if !shouldScrub {
			scrubbed = append(scrubbed, entry)
		}
	}
	return scrubbed
}

const maxOutputBytes = 4 * 1024 * 1024 // 4MB

type boundedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{max: max}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.truncated {
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if len(p) <= remaining {
		return b.buf.Write(p)
	}
	if remaining > 0 {
		_, _ = b.buf.Write(p[:remaining])
	}
	b.truncated = true
	b.buf.WriteString("\n... (output truncated at 4MB)")
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	return b.buf.String()
}

func HandleRunCommand(args map[string]any, workspaceRoot string) (any, error) {
	command := argToString(args["command"])
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("run_command requires non-empty 'command'")
	}

	timeoutSec := argToInt(args["timeout_seconds"], 60)
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	setSysProcAttr(cmd)
	cmd.Cancel = func() error {
		killProcessTree(cmd)
		return nil
	}

	if workspaceRoot != "" {
		cmd.Dir = workspaceRoot
	}
	cmd.Env = scrubEnvironment(os.Environ())

	stdoutBuf := newBoundedBuffer(maxOutputBytes)
	stderrBuf := newBoundedBuffer(maxOutputBytes)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	err := cmd.Run()

	if ctx.Err() != nil {
		killProcessTree(cmd)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("command timed out after %d seconds", timeoutSec)
		}
		return nil, fmt.Errorf("command cancelled: %w", ctx.Err())
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("failed to execute command: %w", err)
		}
	}

	return map[string]any{
		"command":   command,
		"stdout":    stdoutBuf.String(),
		"stderr":    stderrBuf.String(),
		"exit_code": exitCode,
	}, nil
}

// BuildWorkspaceToolRegistry constructs a *ToolRegistry with all default workspace coding tools registered.
func BuildWorkspaceToolRegistry(workspaceRoot string, isInteractive bool, pumpReader io.Reader) *ToolRegistry {
	registry := NewToolRegistry()
	if workspaceRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspaceRoot = cwd
		}
	}

	_ = registry.Register("read_file", "Read the contents of a file within the workspace with line numbers", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read (relative to workspace root or absolute path within workspace)",
			},
			"start_line": map[string]any{
				"type":        "integer",
				"description": "Optional 1-based start line number (inclusive)",
			},
			"end_line": map[string]any{
				"type":        "integer",
				"description": "Optional 1-based end line number (inclusive)",
			},
		},
		"required": []any{"path"},
	}, func(args map[string]any) (any, error) {
		return HandleReadFile(args, workspaceRoot)
	})

	_ = registry.Register("list_dir", "List files and directories in a workspace directory", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Optional relative or absolute directory path to list (defaults to workspace root)",
			},
			"depth": map[string]any{
				"type":        "integer",
				"description": "Optional maximum traversal depth (default 1, max 10)",
			},
			"include_hidden": map[string]any{
				"type":        "boolean",
				"description": "Optional flag to include hidden files and directories (default false)",
			},
		},
	}, func(args map[string]any) (any, error) {
		return HandleListDir(args, workspaceRoot)
	})

	_ = registry.Register("grep_search", "Search for a regex pattern or substring in workspace files", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression pattern or substring to search for",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional directory or file path to search within (defaults to workspace root)",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Optional file glob pattern to filter searched files (e.g. *.go)",
			},
		},
		"required": []any{"pattern"},
	}, func(args map[string]any) (any, error) {
		return HandleGrepSearch(args, workspaceRoot)
	})

	_ = registry.Register("edit_file", "Perform an exact contiguous replacement in a file, preserving line endings", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to edit",
			},
			"target_content": map[string]any{
				"type":        "string",
				"description": "Exact contiguous text block to replace; must occur exactly once in the file",
			},
			"replacement_content": map[string]any{
				"type":        "string",
				"description": "Replacement text block to substitute",
			},
		},
		"required": []any{"path", "target_content", "replacement_content"},
	}, func(args map[string]any) (any, error) {
		return HandleEditFile(args, workspaceRoot)
	})

	_ = registry.Register("write_file", "Create or overwrite a file in the workspace, creating parent directories as needed and preserving line endings", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "Optional flag to overwrite existing file (default false)",
			},
			"append": map[string]any{
				"type":        "boolean",
				"description": "Optional flag to append to existing file (default false)",
			},
		},
		"required": []any{"path", "content"},
	}, func(args map[string]any) (any, error) {
		return HandleWriteFile(args, workspaceRoot)
	})

	_ = registry.Register("run_command", "Execute a shell command in the workspace directory with scrubbed environment and timeout", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in seconds (default 60, max 300)",
			},
		},
		"required": []any{"command"},
	}, func(args map[string]any) (any, error) {
		return HandleRunCommand(args, workspaceRoot)
	})

	_ = RegisterAskQuestionTool(registry, isInteractive, pumpReader, os.Stdout)

	return registry
}
