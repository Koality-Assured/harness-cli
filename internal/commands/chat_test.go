package commands

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/chat"
	"github.com/spf13/cobra"
)

type fakeChatTransport struct {
	response string
	calls    int
	endpoint string
}

func (f *fakeChatTransport) Do(request *http.Request) (*http.Response, error) {
	f.calls++
	f.endpoint = request.URL.String()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(f.response)),
	}, nil
}

func TestChatOneShotSessionCommandsAndResume(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	transport := &fakeChatTransport{response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fake answer\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n"}
	client := &chat.ProviderClient{HTTP: transport}
	var output, errorOutput strings.Builder
	command := &cobra.Command{Use: "chat-test"}
	command.SetOut(&output)
	command.SetErr(&errorOutput)
	options := chatOptionsValue{query: "hello from cli", title: "CLI Session", provider: "anthropic"}
	if err := runChat(command, options, client, func(string) (string, error) { return "fake-cli-token", nil }); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || transport.endpoint != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("chat used %d fake provider calls at %q", transport.calls, transport.endpoint)
	}
	if !strings.Contains(output.String(), "fake answer") || !strings.Contains(errorOutput.String(), "input_tokens=3") {
		t.Fatalf("chat output/stderr = %q / %q", output.String(), errorOutput.String())
	}

	store, err := chat.OpenSessionStore(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ListSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "CLI Session" || sessions[0].Provider != "anthropic" {
		t.Fatalf("CLI-created sessions = %#v", sessions)
	}
	history, err := store.ListMessages(sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Content != "hello from cli" || history[1].Content != "fake answer" {
		t.Fatalf("CLI conversation history = %#v", history)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	JSONOutput = true
	t.Cleanup(func() { JSONOutput = false })
	output.Reset()
	if err := runSessionAction(command, "list", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), sessions[0].ID) || !strings.Contains(output.String(), "CLI Session") {
		t.Fatalf("sessions list output = %s", output.String())
	}
	output.Reset()
	if err := runSessionAction(command, "rename", []string{sessions[0].ID, "Renamed"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Renamed") {
		t.Fatalf("sessions rename output = %s", output.String())
	}
	output.Reset()
	if err := runSessionAction(command, "search", []string{"hello from cli"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), sessions[0].ID) || !strings.Contains(output.String(), "hello from cli") {
		t.Fatalf("sessions search output = %s", output.String())
	}

	store, err = chat.OpenSessionStore(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resumed, code, err := openOrResumeSession(store, chatOptionsValue{resume: sessions[0].ID})
	if err != nil || code != 0 || resumed == nil || resumed.ID != sessions[0].ID || resumed.Title != "Renamed" {
		t.Fatalf("resume result = %#v code=%d err=%v", resumed, code, err)
	}
	continued, code, err := openOrResumeSession(store, chatOptionsValue{continueSession: true})
	if err != nil || code != 0 || continued == nil || continued.ID != sessions[0].ID {
		t.Fatalf("continue result = %#v code=%d err=%v", continued, code, err)
	}
}

func TestChatOneShotMatchesPrototypeStreamingAndNonStreamingOutput(t *testing.T) {
	streamResponse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fake answer\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n"
	cases := []struct {
		name     string
		noStream bool
		response string
	}{
		{name: "streaming", response: streamResponse},
		{name: "non-streaming", noStream: true, response: `{"content":[{"type":"text","text":"fake answer"}],"usage":{"input_tokens":3,"output_tokens":2}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stateDB := filepath.Join(t.TempDir(), "state.db")
			t.Setenv("HARNESS_STATE_DB", stateDB)
			transport := &fakeChatTransport{response: testCase.response}
			var output, errorOutput strings.Builder
			command := &cobra.Command{Use: "chat-test"}
			command.SetOut(&output)
			command.SetErr(&errorOutput)
			options := chatOptionsValue{
				query: "hello from cli", provider: "anthropic", noStream: testCase.noStream,
			}
			if err := runChat(command, options, &chat.ProviderClient{HTTP: transport}, func(string) (string, error) {
				return "fake-cli-token", nil
			}); err != nil {
				t.Fatal(err)
			}
			want := "fake answer\n"
			if !testCase.noStream {
				want = "fake answer\nfake answer\n"
			}
			if got := output.String(); got != want {
				t.Fatalf("stdout = %q, want exactly %q (stderr %q)", got, want, errorOutput.String())
			}
		})
	}
}

func TestChatRequiresOpenAIAndGeminiModelsBeforeCreatingSession(t *testing.T) {
	for _, provider := range []string{"openai", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			stateDB := filepath.Join(t.TempDir(), "state.db")
			t.Setenv("HARNESS_STATE_DB", stateDB)
			transport := &fakeChatTransport{}
			var output, errorOutput strings.Builder
			command := &cobra.Command{Use: "chat-test"}
			command.SetOut(&output)
			command.SetErr(&errorOutput)
			options := chatOptionsValue{query: "should not run", provider: provider}
			credentialCalls := 0
			err := runChat(command, options, &chat.ProviderClient{HTTP: transport}, func(string) (string, error) {
				credentialCalls++
				return "fake-cli-token", nil
			})
			if err == nil || !strings.Contains(err.Error(), "requires --model") {
				t.Fatalf("missing model error = %v", err)
			}
			if transport.calls != 0 {
				t.Fatalf("missing-model request made %d provider calls", transport.calls)
			}
			if credentialCalls != 0 {
				t.Fatalf("missing-model validation resolved credentials %d times", credentialCalls)
			}
			store, err := chat.OpenSessionStore(stateDB)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sessions, err := store.ListSessions(10)
			if err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 0 {
				t.Fatalf("missing model left persisted sessions: %#v", sessions)
			}
		})
	}
}
