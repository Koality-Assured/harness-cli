package chat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type busyModeTransport struct {
	mode         string
	calls        atomic.Int32
	firstStarted chan struct{}
	releaseFirst chan struct{}
	startOnce    sync.Once
	releaseOnce  sync.Once
	mu           sync.Mutex
	bodies       [][]byte
}

type busyOutcome struct {
	result TurnResult
	err    error
}

func (f *busyModeTransport) release() { f.releaseOnce.Do(func() { close(f.releaseFirst) }) }

func (f *busyModeTransport) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()
	call := f.calls.Add(1)
	if call == 1 {
		f.startOnce.Do(func() { close(f.firstStarted) })
		if f.mode == "interrupt" || f.mode == "queue" || f.mode == "steer" {
			select {
			case <-f.releaseFirst:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
	}
	answer := ""
	switch f.mode {
	case "queue":
		if call == 1 {
			answer = anthroTextStream("first answer", 1, 1)
		} else {
			answer = anthroTextStream("second answer", 2, 2)
		}
	case "steer":
		if call == 1 {
			answer = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-steer\",\"name\":\"echo\",\"input\":{}}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"value\\\":\\\"tool\\\"}\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n"
		} else {
			answer = anthroTextStream("steered answer", 3, 2)
		}
	case "interrupt":
		answer = anthroTextStream("after interrupt", 4, 2)
	default:
		return nil, context.Canceled
	}
	return fakeResponse(http.StatusOK, answer), nil
}

func TestAdapterBusyModesInterruptQueueAndSteer(t *testing.T) {
	for _, mode := range []string{"interrupt", "queue", "steer"} {
		t.Run(mode, func(t *testing.T) { testAdapterBusyMode(t, mode) })
	}
}

func TestSteeredFollowerCancellationWhileOwnerIsActive(t *testing.T) {
	queue := newSessionTurnQueue()
	ownerStarted := make(chan struct{})
	allowSteer := make(chan struct{})
	steeredText := make(chan string, 1)
	releaseOwner := make(chan struct{})
	ownerJob := &promptJob{text: "owner", done: make(chan struct{})}
	ownerOutcome := make(chan busyOutcome, 1)
	go func() {
		result, err := queue.submit(context.Background(), ownerJob, "steer", func(_ context.Context, active *activeTurn) (TurnResult, error) {
			close(ownerStarted)
			<-allowSteer
			steeredText <- queue.takeSteer(active)
			<-releaseOwner
			return TurnResult{Text: "owner completed"}, nil
		})
		ownerOutcome <- busyOutcome{result: result, err: err}
	}()
	select {
	case <-ownerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("owner turn did not start")
	}

	followerCtx, cancelFollower := context.WithCancel(context.Background())
	defer cancelFollower()
	followerJob := &promptJob{text: "steered follow-up", done: make(chan struct{})}
	followerOutcome := make(chan busyOutcome, 1)
	go func() {
		result, err := queue.submit(followerCtx, followerJob, "steer", func(context.Context, *activeTurn) (TurnResult, error) {
			return TurnResult{}, errors.New("steered follower unexpectedly became an owner turn")
		})
		followerOutcome <- busyOutcome{result: result, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		queue.mu.Lock()
		pending := len(queue.pending)
		queue.mu.Unlock()
		if pending == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	queue.mu.Lock()
	if len(queue.pending) != 1 {
		queue.mu.Unlock()
		t.Fatal("steered follower did not enter the pending queue")
	}
	queue.mu.Unlock()

	close(allowSteer)
	select {
	case got := <-steeredText:
		if got != "steered follow-up" {
			t.Fatalf("steered text = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not take the pending steered follower")
	}
	queue.mu.Lock()
	movedToFollower := queue.active != nil && len(queue.active.followers) == 1 && queue.active.followers[0] == followerJob
	queue.mu.Unlock()
	if !movedToFollower {
		t.Fatal("pending job was not moved into active followers")
	}

	cancelFollower()
	select {
	case outcome := <-followerOutcome:
		if outcome.err != TurnCancelled {
			t.Fatalf("steered follower cancellation = %#v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled steered follower did not finish")
	}
	queue.mu.Lock()
	ownerStillActive := queue.active != nil && queue.active.job == ownerJob && len(queue.active.followers) == 0
	queue.mu.Unlock()
	if !ownerStillActive {
		t.Fatal("cancelling a follower changed the active owner")
	}

	close(releaseOwner)
	select {
	case outcome := <-ownerOutcome:
		if outcome.err != nil || outcome.result.Text != "owner completed" {
			t.Fatalf("owner completion = %#v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner turn did not finish after release")
	}
}

func testAdapterBusyMode(t *testing.T, mode string) {
	t.Helper()
	transport := &busyModeTransport{mode: mode, firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	registry := NewToolRegistry()
	if err := registry.Register("echo", "return value", map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []any{"value"}}, func(arguments map[string]any) (any, error) {
		return arguments["value"], nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
		Provider: "anthropic", Model: "claude-test", DBPath: filepath.Join(t.TempDir(), "state.db"),
		Client: &ProviderClient{HTTP: transport}, ToolRegistry: registry,
		CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, session, err := runtime.BindSession("busy-test", "principal", "/busy", "external-handle", "claude-test", "", "busy mode", nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenSessionStore(runtime.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBusyMode(session.ID, mode); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.release)
	first := make(chan busyOutcome, 1)
	go func() {
		result, err := runtime.RunPrompt(context.Background(), "busy-test", "principal", "/busy", handle, "first prompt", PromptCallbacks{})
		first <- busyOutcome{result: result, err: err}
	}()
	select {
	case <-transport.firstStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("first provider request did not begin")
	}
	second := make(chan busyOutcome, 1)
	go func() {
		result, err := runtime.RunPrompt(context.Background(), "busy-test", "principal", "/busy", handle, "queued followup", PromptCallbacks{})
		second <- busyOutcome{result: result, err: err}
	}()
	waitForBusyPending(t, runtime, "busy-test", "principal", "/busy", handle)
	if mode != "interrupt" && transport.calls.Load() != 1 {
		t.Fatalf("second provider request started before first released: calls=%d", transport.calls.Load())
	}
	if mode == "interrupt" {
		firstResult := awaitBusyOutcome(t, first)
		if firstResult.err != TurnCancelled {
			t.Fatalf("interrupted first turn = %#v", firstResult)
		}
		secondResult := awaitBusyOutcome(t, second)
		if secondResult.err != nil || secondResult.result.Text != "after interrupt" {
			t.Fatalf("interrupt followup = %#v", secondResult)
		}
	} else {
		transport.release()
		firstResult := awaitBusyOutcome(t, first)
		secondResult := awaitBusyOutcome(t, second)
		if firstResult.err != nil || secondResult.err != nil {
			t.Fatalf("busy mode %s turn errors = %v / %v", mode, firstResult.err, secondResult.err)
		}
		if mode == "queue" && (firstResult.result.Text != "first answer" || secondResult.result.Text != "second answer") {
			t.Fatalf("queue results = %#v / %#v", firstResult.result, secondResult.result)
		}
		if mode == "steer" && (firstResult.result.Text != "steered answer" || secondResult.result.Text != firstResult.result.Text) {
			t.Fatalf("steer results = %#v / %#v", firstResult.result, secondResult.result)
		}
	}
	if mode == "interrupt" {
		transport.release()
	}
	if got := transport.calls.Load(); got != 2 {
		t.Fatalf("%s mode provider calls = %d, want 2", mode, got)
	}
	transport.mu.Lock()
	bodies := append([][]byte(nil), transport.bodies...)
	transport.mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("captured provider bodies = %d", len(bodies))
	}
	if mode == "steer" && (!strings.Contains(string(bodies[1]), "queued followup") || !strings.Contains(string(bodies[1]), "tool_result")) {
		t.Fatalf("steered text or native tool result missing in next provider request: %s", bodies[1])
	}
}

func waitForBusyPending(t *testing.T, runtime *AdapterRuntime, adapter, principal, route, handle string) {
	t.Helper()
	queue := runtime.queue(queueIdentity{adapter: adapter, principal: principal, route: route, handle: handle})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		queue.mu.Lock()
		pending := len(queue.pending)
		active := queue.active != nil
		secondStarted := queue.active != nil && queue.active.job.text == "queued followup"
		queue.mu.Unlock()
		if (pending == 1 && active) || secondStarted {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("second turn did not enter the active session's pending queue")
}

func awaitBusyOutcome(t *testing.T, outcomes <-chan busyOutcome) busyOutcome {
	t.Helper()
	select {
	case result := <-outcomes:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for adapter turn")
		return busyOutcome{}
	}
}
