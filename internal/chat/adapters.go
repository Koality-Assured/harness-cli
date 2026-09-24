package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/auth"
)

const (
	GatewayRoute = "/v1/chat/completions"
	ACPRoute     = "agent"
)

// TurnResult is the adapter-facing result for one prompt.
type TurnResult struct {
	Text             string `json:"text"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	Compacted        bool   `json:"compacted"`
}

type CredentialResolver func(provider string) (string, error)

// AdapterRuntime shares session binding and turn scheduling across gateway and ACP.
type AdapterRuntime struct {
	Provider           string
	Model              string
	DBPath             string
	Client             *ProviderClient
	ToolRegistry       *ToolRegistry
	CredentialResolver CredentialResolver
	queuesMu           sync.Mutex
	queues             map[queueIdentity]*sessionTurnQueue
}

type AdapterRuntimeConfig struct {
	Provider           string
	Model              string
	DBPath             string
	Client             *ProviderClient
	ToolRegistry       *ToolRegistry
	CredentialResolver CredentialResolver
}

func NewAdapterRuntime(config AdapterRuntimeConfig) (*AdapterRuntime, error) {
	provider, ok := NormalizeProvider(config.Provider)
	if !ok {
		return nil, fmt.Errorf("unsupported provider: %s", config.Provider)
	}
	model := config.Model
	if model == "" && provider == "anthropic" {
		model = AnthropicDefaultModel
	}
	if config.Client == nil {
		config.Client = &ProviderClient{}
	}
	if config.ToolRegistry == nil {
		config.ToolRegistry = NewToolRegistry()
	}
	if config.CredentialResolver == nil {
		config.CredentialResolver = resolveAdapterCredential
	}
	return &AdapterRuntime{
		Provider: provider, Model: model, DBPath: config.DBPath,
		Client: config.Client, ToolRegistry: config.ToolRegistry,
		CredentialResolver: config.CredentialResolver,
		queues:             map[queueIdentity]*sessionTurnQueue{},
	}, nil
}

func resolveAdapterCredential(provider string) (string, error) {
	credential, err := auth.EnsureFreshToken(auth.GetVault(), provider, 60*time.Second)
	if err != nil {
		return "", err
	}
	if credential == nil || credential.AccessToken == "" {
		return "", fmt.Errorf("no credentials for '%s'", provider)
	}
	return credential.AccessToken, nil
}

func PrincipalForToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

type queueIdentity struct{ adapter, principal, route, handle string }

func (r *AdapterRuntime) queue(identity queueIdentity) *sessionTurnQueue {
	r.queuesMu.Lock()
	defer r.queuesMu.Unlock()
	if value := r.queues[identity]; value != nil {
		return value
	}
	value := newSessionTurnQueue()
	r.queues[identity] = value
	return value
}

func (r *AdapterRuntime) openStore() (*SessionStore, error) { return OpenSessionStore(r.DBPath) }

// BindSession maps a stable external ID to a durable conversation.
func (r *AdapterRuntime) BindSession(adapter, principal, route, externalID, model, cwd, title string, initial []Message) (string, *Session, error) {
	store, err := r.openStore()
	if err != nil {
		return "", nil, err
	}
	defer store.Close()
	return store.BindAdapterSession(adapter, principal, route, externalID, Session{
		Title: title, CWD: cwd, Provider: r.Provider, Model: firstNonempty(model, r.Model), BusyMode: defaultBusyMode,
	}, initial)
}

func (r *AdapterRuntime) GetSession(adapter, principal, route, handle string) (*Session, error) {
	store, err := r.openStore()
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.GetAdapterSession(adapter, principal, route, handle)
}

func (r *AdapterRuntime) History(adapter, principal, route, handle string) ([]Message, error) {
	store, err := r.openStore()
	if err != nil {
		return nil, err
	}
	defer store.Close()
	session, err := store.GetAdapterSession(adapter, principal, route, handle)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("adapter session not found")
	}
	return store.ListMessages(session.ID)
}

// Cancel interrupts the active prompt associated with a mapped session.
func (r *AdapterRuntime) Cancel(adapter, principal, route, handle string) bool {
	return r.queue(queueIdentity{adapter, principal, route, handle}).cancel()
}

// CancelAndWait interrupts active work, rejects queued work, and waits up to timeout.
func (r *AdapterRuntime) CancelAndWait(adapter, principal, route, handle string, timeout time.Duration) bool {
	return r.queue(queueIdentity{adapter, principal, route, handle}).cancelAndWait(timeout)
}

type ToolRegistryFactory func(context.Context) (ToolDispatcher, error)

type PromptCallbacks struct {
	OnDelta        func(string)
	OnToolStart    func(id, name string, arguments map[string]any)
	OnToolComplete func(id, name string, arguments map[string]any, outcome ToolOutcome)
	ToolRegistry   ToolRegistryFactory
}

// RunPrompt serializes an adapter turn according to the session's persisted busy mode.
func (r *AdapterRuntime) RunPrompt(ctx context.Context, adapter, principal, route, handle, text string, callbacks PromptCallbacks) (TurnResult, error) {
	identity := queueIdentity{adapter, principal, route, handle}
	queue := r.queue(identity)
	store, err := r.openStore()
	if err != nil {
		return TurnResult{}, err
	}
	session, err := store.GetAdapterSession(adapter, principal, route, handle)
	_ = store.Close()
	if err != nil {
		return TurnResult{}, err
	}
	if session == nil {
		return TurnResult{}, errors.New("adapter session not found")
	}
	job := &promptJob{text: text, callbacks: callbacks, done: make(chan struct{})}
	return queue.submit(ctx, job, session.BusyMode, func(activeCtx context.Context, active *activeTurn) (TurnResult, error) {
		store, err := r.openStore()
		if err != nil {
			return TurnResult{}, err
		}
		defer store.Close()
		session, err := store.GetAdapterSession(adapter, principal, route, handle)
		if err != nil {
			return TurnResult{}, err
		}
		if session == nil {
			return TurnResult{}, errors.New("adapter session not found")
		}
		provider := firstNonempty(session.Provider, r.Provider)
		model := firstNonempty(session.Model, r.Model)
		credential, err := r.CredentialResolver(provider)
		if err != nil || credential == "" {
			if err != nil {
				return TurnResult{}, err
			}
			return TurnResult{}, fmt.Errorf("no credentials for '%s'", provider)
		}
		if strings.TrimSpace(text) == "/compact" {
			compacted, err := CompactSession(activeCtx, store, session.ID, provider, model, credential, r.Client)
			if err != nil {
				return TurnResult{}, err
			}
			if compacted == nil {
				return TurnResult{Text: "Session has too little history to compact."}, nil
			}
			if _, err := store.RebindAdapterSession(adapter, principal, route, handle, compacted.Session.ID); err != nil {
				return TurnResult{}, err
			}
			return TurnResult{Text: fmt.Sprintf("Compacted %d older turns.", compacted.CompactedTurns), Compacted: true}, nil
		}
		beforeMessages, err := store.ListMessages(session.ID)
		if err != nil {
			return TurnResult{}, err
		}
		before := make(map[string]struct{}, len(beforeMessages))
		for _, message := range beforeMessages {
			before[message.ID] = struct{}{}
		}
		registry := ToolDispatcher(r.ToolRegistry)
		if callbacks.ToolRegistry != nil {
			registry, err = callbacks.ToolRegistry(activeCtx)
			if err != nil {
				return TurnResult{}, err
			}
		}
		textResult, err := RunTurn(activeCtx, store, session.ID, text, provider, model, credential, r.Client, TurnOptions{
			Stream: true, OnDelta: func(delta string) {
				queue.emit(active, func(cb PromptCallbacks) {
					if cb.OnDelta != nil {
						cb.OnDelta(delta)
					}
				})
			},
			ToolRegistry: registry,
			OnToolBatch:  func() string { return queue.takeSteer(active) },
			OnToolStart: func(id, name string, args map[string]any) {
				queue.emit(active, func(cb PromptCallbacks) {
					if cb.OnToolStart != nil {
						cb.OnToolStart(id, name, args)
					}
				})
			},
			OnToolComplete: func(id, name string, args map[string]any, outcome ToolOutcome) {
				queue.emit(active, func(cb PromptCallbacks) {
					if cb.OnToolComplete != nil {
						cb.OnToolComplete(id, name, args, outcome)
					}
				})
			},
		})
		if err != nil {
			return TurnResult{}, err
		}
		after, err := store.ListMessages(session.ID)
		if err != nil {
			return TurnResult{}, err
		}
		var promptTokens, completionTokens int64
		for _, message := range after {
			if _, exists := before[message.ID]; exists {
				continue
			}
			if message.InputTokens != nil {
				promptTokens += *message.InputTokens
			}
			if message.OutputTokens != nil {
				completionTokens += *message.OutputTokens
			}
		}
		return TurnResult{Text: textResult, PromptTokens: promptTokens, CompletionTokens: completionTokens}, nil
	})
}

func firstNonempty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

type promptJob struct {
	text      string
	callbacks PromptCallbacks
	result    TurnResult
	err       error
	done      chan struct{}
	completed bool
}

type activeTurn struct {
	job       *promptJob
	mode      string
	ctx       context.Context
	cancel    context.CancelFunc
	followers []*promptJob
	steerUsed bool
}

type sessionTurnQueue struct {
	mu      sync.Mutex
	changed chan struct{}
	active  *activeTurn
	pending []*promptJob
}

func newSessionTurnQueue() *sessionTurnQueue { return &sessionTurnQueue{changed: make(chan struct{})} }

func (q *sessionTurnQueue) signalLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func (q *sessionTurnQueue) submit(ctx context.Context, job *promptJob, mode string, execute func(context.Context, *activeTurn) (TurnResult, error)) (TurnResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	q.pending = append(q.pending, job)
	if q.active != nil && mode == "interrupt" {
		q.active.cancel()
	}
	q.signalLocked()
	var active *activeTurn
	ctxDone := ctx.Done()
	for {
		select {
		case <-job.done:
			q.mu.Unlock()
			return job.result, job.err
		default:
		}
		if q.active == nil && len(q.pending) > 0 && q.pending[0] == job {
			q.pending = q.pending[1:]
			activeCtx, cancel := context.WithCancel(ctx)
			active = &activeTurn{job: job, mode: mode, ctx: activeCtx, cancel: cancel}
			q.active = active
			q.signalLocked()
			break
		}
		changed := q.changed
		q.mu.Unlock()
		select {
		case <-changed:
		case <-job.done:
		case <-ctxDone:
			q.mu.Lock()
			if q.active != nil && q.active.job == job {
				q.active.cancel()
				ctxDone = nil
			} else {
				q.removePendingLocked(job)
				q.removeFollowerLocked(job)
				q.completeJobLocked(job, TurnResult{}, TurnCancelled)
				q.signalLocked()
				ctxDone = nil
			}
			q.mu.Unlock()
		}
		q.mu.Lock()
	}
	q.mu.Unlock()
	result, err := execute(active.ctx, active)
	active.cancel()
	q.mu.Lock()
	q.completeJobLocked(job, result, err)
	for _, follower := range active.followers {
		q.completeJobLocked(follower, result, err)
	}
	q.active = nil
	q.signalLocked()
	q.mu.Unlock()
	return result, err
}

func (q *sessionTurnQueue) removePendingLocked(target *promptJob) {
	for index, item := range q.pending {
		if item == target {
			q.pending = append(q.pending[:index], q.pending[index+1:]...)
			return
		}
	}
}

func (q *sessionTurnQueue) removeFollowerLocked(target *promptJob) {
	if q.active == nil {
		return
	}
	for index, item := range q.active.followers {
		if item == target {
			q.active.followers = append(q.active.followers[:index], q.active.followers[index+1:]...)
			return
		}
	}
}

// completeJobLocked publishes a job result once. Cancellation can race with
// completion after a steered job has moved from pending to active.followers.
func (q *sessionTurnQueue) completeJobLocked(job *promptJob, result TurnResult, err error) {
	if job.completed {
		return
	}
	job.result, job.err, job.completed = result, err, true
	close(job.done)
}

func (q *sessionTurnQueue) cancel() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active == nil {
		return false
	}
	q.active.cancel()
	q.signalLocked()
	return true
}

func (q *sessionTurnQueue) cancelAndWait(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	q.mu.Lock()
	for _, job := range q.pending {
		q.completeJobLocked(job, TurnResult{}, TurnCancelled)
	}
	q.pending = nil
	if q.active == nil {
		q.signalLocked()
		q.mu.Unlock()
		return true
	}
	q.active.cancel()
	q.signalLocked()
	for q.active != nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			q.mu.Unlock()
			return false
		}
		changed := q.changed
		q.mu.Unlock()
		timer := time.NewTimer(remaining)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
			return false
		}
		q.mu.Lock()
	}
	q.mu.Unlock()
	return true
}

func (q *sessionTurnQueue) takeSteer(active *activeTurn) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if active.mode != "steer" || active.steerUsed {
		return ""
	}
	if len(q.pending) == 0 {
		return ""
	}
	job := q.pending[0]
	q.pending = q.pending[1:]
	active.followers = append(active.followers, job)
	active.steerUsed = true
	q.signalLocked()
	return strings.TrimSpace(job.text)
}

func (q *sessionTurnQueue) emit(active *activeTurn, callback func(PromptCallbacks)) {
	q.mu.Lock()
	jobs := append([]*promptJob{active.job}, active.followers...)
	q.mu.Unlock()
	for _, job := range jobs {
		callback(job.callbacks)
	}
}
