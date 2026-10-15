package chat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestClassifyTool(t *testing.T) {
	// 1. Read-only tools
	readOnly := []string{"read_file", "list_dir", "grep_search", "ast_outline", "ask_question"}
	for _, name := range readOnly {
		if got := ClassifyTool(name); got != ToolClassReadOnly {
			t.Errorf("ClassifyTool(%q) = %q; want %q", name, got, ToolClassReadOnly)
		}
	}

	// 2. Edit tools
	edits := []string{"edit_file", "write_file"}
	for _, name := range edits {
		if got := ClassifyTool(name); got != ToolClassEdit {
			t.Errorf("ClassifyTool(%q) = %q; want %q", name, got, ToolClassEdit)
		}
	}

	// 3. Command tools
	commands := []string{"run_command", "bash", "exec"}
	for _, name := range commands {
		if got := ClassifyTool(name); got != ToolClassCommand {
			t.Errorf("ClassifyTool(%q) = %q; want %q", name, got, ToolClassCommand)
		}
	}

	// 4. Security: Prefix-spoofing bypasses must NOT be classified as read_only or edit
	spoofed := []string{
		"read_and_execute",
		"read_file_and_exec",
		"read_files",
		"write_file_root",
		"edit_file_script",
		"custom_dangerous_tool",
		"",
	}
	for _, name := range spoofed {
		if got := ClassifyTool(name); got != ToolClassCommand {
			t.Errorf("Security check failed: ClassifyTool(%q) = %q; want %q", name, got, ToolClassCommand)
		}
	}
}

func TestPermissionPolicyModes(t *testing.T) {
	ctx := context.Background()

	// Mode: yolo (all tools allowed, interactive and non-interactive)
	for _, interactive := range []bool{true, false} {
		p := NewPermissionPolicy(PermissionModeYolo, interactive)
		promptCalled := false
		cb := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
			promptCalled = true
			return DecisionDeny, "", nil
		}

		for _, tool := range []string{"read_file", "edit_file", "run_command"} {
			allowed, msg, err := p.CheckPermission(ctx, tool, nil, cb)
			if err != nil || !allowed || msg != "" {
				t.Errorf("yolo mode (interactive=%v) rejected %s: allowed=%v, msg=%q, err=%v", interactive, tool, allowed, msg, err)
			}
			if promptCalled {
				t.Errorf("yolo mode unexpectedly invoked prompt callback for %s", tool)
			}
		}
	}

	// Mode: auto-edit
	pAuto := NewPermissionPolicy(PermissionModeAutoEdit, true)
	// read_only -> allowed without prompt
	allowed, _, _ := pAuto.CheckPermission(ctx, "read_file", nil, nil)
	if !allowed {
		t.Error("auto-edit: read_file should be allowed without prompt")
	}
	// edit -> allowed without prompt
	allowed, _, _ = pAuto.CheckPermission(ctx, "edit_file", nil, nil)
	if !allowed {
		t.Error("auto-edit: edit_file should be allowed without prompt")
	}
	// command -> requires prompt
	promptCalled := false
	cb := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		promptCalled = true
		return DecisionAllowOnce, "", nil
	}
	allowed, _, _ = pAuto.CheckPermission(ctx, "run_command", nil, cb)
	if !allowed || !promptCalled {
		t.Error("auto-edit: run_command should prompt callback in interactive mode")
	}

	// Mode: ask
	pAsk := NewPermissionPolicy(PermissionModeAsk, true)
	// read_only -> allowed without prompt
	allowed, _, _ = pAsk.CheckPermission(ctx, "read_file", nil, nil)
	if !allowed {
		t.Error("ask: read_file should be allowed without prompt")
	}
	// edit -> requires prompt
	promptCalled = false
	allowed, _, _ = pAsk.CheckPermission(ctx, "edit_file", nil, cb)
	if !allowed || !promptCalled {
		t.Error("ask: edit_file should prompt callback in interactive mode")
	}
	// command -> requires prompt
	promptCalled = false
	allowed, _, _ = pAsk.CheckPermission(ctx, "run_command", nil, cb)
	if !allowed || !promptCalled {
		t.Error("ask: run_command should prompt callback in interactive mode")
	}
}

func TestSessionAllowanceAndCommandScoping(t *testing.T) {
	ctx := context.Background()
	p := NewPermissionPolicy(PermissionModeAsk, true)

	// 1. Edit tool: allow_session is path-scoped (HIGH 5)
	callCount := 0
	cbEdit := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		callCount++
		return DecisionAllowSession, "", nil
	}

	// Approving write_file for "a.txt"
	allowed, _, _ := p.CheckPermission(ctx, "write_file", map[string]any{"path": "a.txt"}, cbEdit)
	if !allowed || callCount != 1 {
		t.Fatalf("first write_file call: allowed=%v, callCount=%d", allowed, callCount)
	}
	if !p.IsFileAllowed("write_file", "a.txt") {
		t.Error("expected write_file:a.txt to be cached in allowedTools")
	}

	// Second call for the SAME file "a.txt" should use session cache without prompting
	allowed, _, _ = p.CheckPermission(ctx, "write_file", map[string]any{"path": "a.txt"}, cbEdit)
	if !allowed || callCount != 1 {
		t.Errorf("second write_file call for a.txt: allowed=%v, callCount=%d; want callCount=1", allowed, callCount)
	}

	// Calling write_file for "b.txt" MUST prompt again (path-scoped security invariant!)
	allowed, _, _ = p.CheckPermission(ctx, "write_file", map[string]any{"path": "b.txt"}, cbEdit)
	if !allowed || callCount != 2 {
		t.Errorf("write_file for b.txt: allowed=%v, callCount=%d; want callCount=2 (must not inherit a.txt allowance)", allowed, callCount)
	}

	// 2. Command tool: session allowance MUST be scoped to exact command and cwd (HIGH & MEDIUM 7)
	cmdCallCount := 0
	cbCmd := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		cmdCallCount++
		return DecisionAllowSession, "", nil
	}

	// First command: "git status" in "/repo"
	gitArgs := map[string]any{"command": "git status", "cwd": "/repo"}
	allowed, _, _ = p.CheckPermission(ctx, "run_command", gitArgs, cbCmd)
	if !allowed || cmdCallCount != 1 {
		t.Fatalf("first git status call: allowed=%v, cmdCallCount=%d", allowed, cmdCallCount)
	}
	if !p.IsCommandWithCwdAllowed("run_command", "/repo", "git status") {
		t.Error("expected 'run_command:/repo:git status' to be in allowedCommands")
	}
	if p.IsToolAllowed("run_command") {
		t.Error("security violation: run_command must NOT be in allowedTools")
	}

	// Second command: identical "git status" in "/repo" -> should use cache without prompt
	allowed, _, _ = p.CheckPermission(ctx, "run_command", gitArgs, cbCmd)
	if !allowed || cmdCallCount != 1 {
		t.Errorf("second git status call: allowed=%v, cmdCallCount=%d; want cmdCallCount=1", allowed, cmdCallCount)
	}

	// Third command: "rm -rf /" -> MUST NOT be allowed by session cache; must invoke prompt!
	rmArgs := map[string]any{"command": "rm -rf /", "cwd": "/repo"}
	if p.IsCommandWithCwdAllowed("run_command", "/repo", "rm -rf /") {
		t.Fatal("security violation: rm -rf / must not be in allowedCommands")
	}

	deniedCalled := false
	cbDeny := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		deniedCalled = true
		return DecisionDeny, "dangerous command rejected", nil
	}

	allowed, denialMsg, _ := p.CheckPermission(ctx, "run_command", rmArgs, cbDeny)
	if allowed {
		t.Fatal("security violation: rm -rf / should have been denied")
	}
	if !deniedCalled {
		t.Fatal("expected cbDeny to be called for unsanctioned rm -rf /")
	}
	if !strings.Contains(denialMsg, "dangerous command rejected") {
		t.Errorf("denialMsg = %q; want rejection reason", denialMsg)
	}
	// Verify no double prefix stuttering
	if strings.Contains(denialMsg, "Tool execution denied by user: Tool execution denied") {
		t.Errorf("denial message contains stuttering prefix: %q", denialMsg)
	}
}

func TestNonInteractiveRejection(t *testing.T) {
	ctx := context.Background()

	// Mode: ask (non-interactive)
	p := NewPermissionPolicy(PermissionModeAsk, false)

	// read_only tools succeed without interactive prompt
	allowed, _, _ := p.CheckPermission(ctx, "read_file", nil, nil)
	if !allowed {
		t.Error("read_file should be allowed in non-interactive ask mode")
	}

	// edit tools denied with clear error
	allowed, msg, err := p.CheckPermission(ctx, "edit_file", nil, nil)
	if allowed || err != nil || !strings.Contains(msg, "Tool requires interactive confirmation; run interactively or with --permissions yolo") {
		t.Errorf("edit_file non-interactive: allowed=%v, msg=%q, err=%v", allowed, msg, err)
	}

	// command tools denied with clear error
	allowed, msg, err = p.CheckPermission(ctx, "run_command", map[string]any{"command": "ls"}, nil)
	if allowed || err != nil || !strings.Contains(msg, "Tool requires interactive confirmation; run interactively or with --permissions yolo") {
		t.Errorf("run_command non-interactive: allowed=%v, msg=%q, err=%v", allowed, msg, err)
	}

	// Mode: auto-edit (non-interactive)
	pAuto := NewPermissionPolicy(PermissionModeAutoEdit, false)
	allowed, _, _ = pAuto.CheckPermission(ctx, "edit_file", nil, nil)
	if !allowed {
		t.Error("edit_file should be allowed in non-interactive auto-edit mode")
	}
	allowed, msg, _ = pAuto.CheckPermission(ctx, "run_command", nil, nil)
	if allowed || !strings.Contains(msg, "Tool requires interactive confirmation") {
		t.Errorf("run_command should be denied in non-interactive auto-edit mode: allowed=%v, msg=%q", allowed, msg)
	}

	// Mode: yolo (non-interactive)
	pYolo := NewPermissionPolicy(PermissionModeYolo, false)
	allowed, _, _ = pYolo.CheckPermission(ctx, "run_command", map[string]any{"command": "ls"}, nil)
	if !allowed {
		t.Error("run_command should be allowed in non-interactive yolo mode")
	}
}

func TestPromptCallbackDecisions(t *testing.T) {
	ctx := context.Background()
	p := NewPermissionPolicy(PermissionModeAsk, true)

	// Decision: allow_once
	cbCount := 0
	cbAllowOnce := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		cbCount++
		return DecisionAllowOnce, "", nil
	}

	allowed, _, _ := p.CheckPermission(ctx, "edit_file", map[string]any{"path": "f.go"}, cbAllowOnce)
	if !allowed || cbCount != 1 {
		t.Errorf("allow_once first call: allowed=%v, cbCount=%d", allowed, cbCount)
	}

	// Second call should prompt again (not cached)
	allowed, _, _ = p.CheckPermission(ctx, "edit_file", map[string]any{"path": "f.go"}, cbAllowOnce)
	if !allowed || cbCount != 2 {
		t.Errorf("allow_once second call: allowed=%v, cbCount=%d; want 2", allowed, cbCount)
	}

	// Decision: deny without feedback
	cbDeny := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		return DecisionDeny, "", nil
	}
	allowed, msg, _ := p.CheckPermission(ctx, "edit_file", nil, cbDeny)
	if allowed || msg != "Tool execution denied by user." {
		t.Errorf("deny without feedback: allowed=%v, msg=%q", allowed, msg)
	}

	// Decision: deny with feedback
	cbDenyFeedback := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		return DecisionDeny, "sensitive file", nil
	}
	allowed, msg, _ = p.CheckPermission(ctx, "edit_file", nil, cbDenyFeedback)
	if allowed || msg != "Tool execution denied by user: sensitive file" {
		t.Errorf("deny with feedback: allowed=%v, msg=%q", allowed, msg)
	}

	// Callback error (e.g. context cancellation)
	cbErr := func(ctx context.Context, name string, args map[string]any) (PromptDecision, string, error) {
		return "", "", errors.New("context cancelled")
	}
	allowed, _, err := p.CheckPermission(ctx, "edit_file", nil, cbErr)
	if allowed || err == nil || err.Error() != "context cancelled" {
		t.Errorf("cb error: allowed=%v, err=%v", allowed, err)
	}
}

func TestPromptChoice(t *testing.T) {
	options := []string{"Option A", "(Recommended) Option B", "Option C"}

	// 1. Non-interactive mode (reader == nil) with recommended option
	selected, err := PromptChoice("Pick one", options, false, nil, nil)
	if err != nil || len(selected) != 1 || selected[0] != "(Recommended) Option B" {
		t.Fatalf("non-interactive recommended: got %v, err %v", selected, err)
	}

	// 2. Non-interactive mode without recommended option (defaults to first)
	noRec := []string{"First Option", "Second Option"}
	selected, err = PromptChoice("Pick one", noRec, false, nil, nil)
	if err != nil || len(selected) != 1 || selected[0] != "First Option" {
		t.Fatalf("non-interactive default first: got %v, err %v", selected, err)
	}

	// 3. Non-interactive mode with empty options
	selected, err = PromptChoice("Pick one", []string{}, false, nil, nil)
	if err != nil || len(selected) != 0 {
		t.Fatalf("non-interactive empty options: got %v, err %v", selected, err)
	}

	// 4. Interactive mode: select by number
	var out bytes.Buffer
	in := strings.NewReader("1\n")
	selected, err = PromptChoice("Select choice", options, false, in, &out)
	if err != nil || len(selected) != 1 || selected[0] != "Option A" {
		t.Fatalf("interactive select 1: got %v, err %v", selected, err)
	}
	if !strings.Contains(out.String(), "[1] Option A") || !strings.Contains(out.String(), "[c] Custom answer") {
		t.Errorf("output missing formatted options: %s", out.String())
	}

	// 5. Interactive mode: select custom answer via 'c' prompt
	out.Reset()
	in = strings.NewReader("c\nMy Custom Answer\n")
	selected, err = PromptChoice("Select choice", options, false, in, &out)
	if err != nil || len(selected) != 1 || selected[0] != "My Custom Answer" {
		t.Fatalf("interactive custom answer: got %v, err %v", selected, err)
	}

	// 6. Interactive mode: select custom answer via 'c <text>' directly
	out.Reset()
	in = strings.NewReader("c direct custom\n")
	selected, err = PromptChoice("Select choice", options, false, in, &out)
	if err != nil || len(selected) != 1 || selected[0] != "direct custom" {
		t.Fatalf("interactive custom inline: got %v, err %v", selected, err)
	}

	// 7. Interactive mode: multi-select comma-separated numbers with deduplication (LOW 11)
	out.Reset()
	in = strings.NewReader("1, 3, 1\n")
	selected, err = PromptChoice("Select choices", options, true, in, &out)
	if err != nil || len(selected) != 2 || selected[0] != "Option A" || selected[1] != "Option C" {
		t.Fatalf("interactive multi-select with dedup: got %v, err %v; want [Option A, Option C]", selected, err)
	}
}

func TestHandleAskQuestion(t *testing.T) {
	// 1. Non-interactive call
	args := map[string]any{
		"question":        "Which database?",
		"options":         []any{"SQLite", "(Recommended) PostgreSQL", "MySQL"},
		"is_multi_select": false,
	}

	res, err := HandleAskQuestion(args, false, nil, nil)
	if err != nil {
		t.Fatalf("HandleAskQuestion non-interactive failed: %v", err)
	}
	answers, ok := res["answers"].([]string)
	if !ok || len(answers) != 1 || answers[0] != "(Recommended) PostgreSQL" {
		t.Errorf("res['answers'] = %v; want [(Recommended) PostgreSQL]", res["answers"])
	}
	if res["answer"] != "(Recommended) PostgreSQL" {
		t.Errorf("res['answer'] = %v; want (Recommended) PostgreSQL", res["answer"])
	}

	// 2. Interactive call
	var out bytes.Buffer
	in := strings.NewReader("1\n")
	res, err = HandleAskQuestion(args, true, in, &out)
	if err != nil {
		t.Fatalf("HandleAskQuestion interactive failed: %v", err)
	}
	answers, ok = res["answers"].([]string)
	if !ok || len(answers) != 1 || answers[0] != "SQLite" {
		t.Errorf("interactive res['answers'] = %v; want [SQLite]", res["answers"])
	}

	// 3. Batch questions
	batchArgs := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Q1",
				"options":  []string{"(Recommended) Ans1", "Ans2"},
			},
			map[string]any{
				"question": "Q2",
				"options":  []string{"Ans3", "(Recommended) Ans4"},
			},
		},
	}
	batchRes, err := HandleAskQuestion(batchArgs, false, nil, nil)
	if err != nil {
		t.Fatalf("batch HandleAskQuestion failed: %v", err)
	}
	batchAnswers := batchRes["answers"].([]string)
	if len(batchAnswers) != 2 || batchAnswers[0] != "(Recommended) Ans1" || batchAnswers[1] != "(Recommended) Ans4" {
		t.Errorf("batch answers = %v; want [(Recommended) Ans1, (Recommended) Ans4]", batchAnswers)
	}

	// 4. Schema verification (LOW 12)
	def := AskQuestionToolDefinition(false, nil, nil)
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok || props["questions"] == nil {
		t.Error("expected 'questions' property in AskQuestionToolDefinition InputSchema")
	}
}

func TestRunTurnWithPermissionPolicy(t *testing.T) {
	store := openTestStore(t)
	session, err := store.CreateSession(Session{Provider: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}

	executedTool := false
	registry := NewToolRegistry()
	if err := registry.Register("edit_file", "edit a file", map[string]any{
		"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required": []any{"path"}, "additionalProperties": false,
	}, func(arguments map[string]any) (any, error) {
		executedTool = true
		return map[string]any{"status": "ok"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	answers := []string{
		`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"main.go\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`{"choices":[{"message":{"role":"assistant","content":"tool was denied"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
	}

	client := &ProviderClient{HTTP: doerFunc(func(request *http.Request) (*http.Response, error) {
		answer := answers[0]
		answers = answers[1:]
		return fakeResponse(http.StatusOK, answer), nil
	})}

	// 1. Run in non-interactive ask mode: tool must be denied and recorded as error
	permPolicy := NewPermissionPolicy(PermissionModeAsk, false)
	reply, err := RunTurn(context.Background(), store, session.ID, "modify code", "openai", "gpt-test", "fake-key", client, TurnOptions{
		ToolRegistry: registry,
		Permissions:  permPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "tool was denied" {
		t.Errorf("reply = %q; want 'tool was denied'", reply)
	}
	if executedTool {
		t.Error("edit_file should NOT have executed when permission was denied")
	}

	// Verify messages recorded tool outcome with error
	messages, err := store.ListMessages(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawToolDenial bool
	for _, msg := range messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "Tool requires interactive confirmation") {
			sawToolDenial = true
			data, ok := msg.ProviderData.(map[string]any)
			if !ok {
				t.Fatalf("expected provider data on tool message: %#v", msg)
			}
			toolRes := data["tool_result"].(map[string]any)
			if toolRes["is_error"] != true {
				t.Errorf("tool_result.is_error = %v; want true", toolRes["is_error"])
			}
		}
	}
	if !sawToolDenial {
		t.Error("did not find tool denial error message in conversation history")
	}

	// 2. Run in yolo mode: tool must be approved and execute
	session2, err := store.CreateSession(Session{Provider: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	answers = []string{
		`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-2","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"main.go\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`{"choices":[{"message":{"role":"assistant","content":"tool succeeded"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
	}
	client2 := &ProviderClient{HTTP: doerFunc(func(request *http.Request) (*http.Response, error) {
		answer := answers[0]
		answers = answers[1:]
		return fakeResponse(http.StatusOK, answer), nil
	})}

	executedTool = false
	permPolicyYolo := NewPermissionPolicy(PermissionModeYolo, false)
	reply, err = RunTurn(context.Background(), store, session2.ID, "modify code", "openai", "gpt-test", "fake-key", client2, TurnOptions{
		ToolRegistry: registry,
		Permissions:  permPolicyYolo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "tool succeeded" {
		t.Errorf("reply = %q; want 'tool succeeded'", reply)
	}
	if !executedTool {
		t.Error("edit_file SHOULD have executed under yolo permission policy")
	}
}
