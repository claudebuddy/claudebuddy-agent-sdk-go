package agent

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

type recordingProvider struct {
	mu     sync.Mutex
	models []string
}

func (p *recordingProvider) CreateMessage(ctx context.Context, req api.MessagesRequest) (*api.StreamMessage, error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()
	return &api.StreamMessage{Role: "assistant", Model: req.Model, StopReason: "end_turn"}, nil
}

func (p *recordingProvider) CreateMessageStream(ctx context.Context, req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()
	return successfulStream(req)
}

func successfulStream(req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
	events := make(chan api.StreamEvent, 4)
	errs := make(chan error)
	events <- api.StreamEvent{Type: "message_start", Message: &api.StreamMessage{Role: "assistant", Model: req.Model}}
	events <- api.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentBlockText}}
	events <- api.StreamEvent{Type: "content_block_delta", Index: 0, Delta: map[string]interface{}{"type": "text_delta", "text": "ok"}}
	events <- api.StreamEvent{Type: "message_delta", Delta: map[string]interface{}{"stop_reason": "end_turn"}}
	close(events)
	return events, errs
}

func (p *recordingProvider) recordedModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

func TestOptionsRejectInvalidLimitsBeforeProviderCall(t *testing.T) {
	provider := &recordingProvider{}
	a := New(Options{ProviderClient: provider, MaxTurns: -1})
	_, err := a.Prompt(context.Background(), "hello")
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("got %v, want ErrInvalidOptions", err)
	}
	if models := provider.recordedModels(); len(models) != 0 {
		t.Fatalf("provider admissions = %v, want none", models)
	}
}

func TestModelSelectionIsRequestScoped(t *testing.T) {
	provider := &recordingProvider{}
	a1 := New(Options{ProviderClient: provider, Model: "primary-one"})
	a2 := New(Options{ProviderClient: provider, Model: "primary-two"})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = a1.Prompt(context.Background(), "one") }()
	go func() { defer wg.Done(); _, _ = a2.Prompt(context.Background(), "two") }()
	wg.Wait()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	sort.Strings(provider.models)
	if len(provider.models) != 2 || provider.models[0] != "primary-one" || provider.models[1] != "primary-two" {
		t.Fatalf("request models=%v", provider.models)
	}
}

type coordinatedProvider struct {
	mu             sync.Mutex
	models         []string
	blockedStarted chan struct{}
	fallbackSeen   chan struct{}
	releaseBlocked chan struct{}
}

func newCoordinatedProvider() *coordinatedProvider {
	return &coordinatedProvider{
		blockedStarted: make(chan struct{}),
		fallbackSeen:   make(chan struct{}),
		releaseBlocked: make(chan struct{}),
	}
}

func (p *coordinatedProvider) CreateMessage(ctx context.Context, req api.MessagesRequest) (*api.StreamMessage, error) {
	return &api.StreamMessage{Role: "assistant", Model: req.Model, StopReason: "end_turn"}, nil
}

func (p *coordinatedProvider) CreateMessageStream(ctx context.Context, req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()

	switch req.Model {
	case "blocked-primary":
		close(p.blockedStarted)
		select {
		case <-p.releaseBlocked:
			return successfulStream(req)
		case <-ctx.Done():
			errs := make(chan error, 1)
			errs <- ctx.Err()
			return make(chan api.StreamEvent), errs
		}
	case "failing-primary":
		errs := make(chan error, 1)
		errs <- errors.New("forced primary failure")
		return make(chan api.StreamEvent), errs
	case "fallback":
		close(p.fallbackSeen)
		return successfulStream(req)
	default:
		return successfulStream(req)
	}
}

func TestFallbackModelSelectionIsRequestScopedWhileAnotherPrimaryIsBlocked(t *testing.T) {
	provider := newCoordinatedProvider()
	blocked := New(Options{ProviderClient: provider, Model: "blocked-primary"})
	fallback := New(Options{ProviderClient: provider, Model: "failing-primary", FallbackModel: "fallback"})

	blockedDone := make(chan error, 1)
	go func() {
		_, err := blocked.Prompt(context.Background(), "blocked")
		blockedDone <- err
	}()

	select {
	case <-provider.blockedStarted:
	case <-time.After(time.Second):
		t.Fatal("blocked primary was not admitted")
	}

	fallbackDone := make(chan error, 1)
	go func() {
		_, err := fallback.Prompt(context.Background(), "fallback")
		fallbackDone <- err
	}()

	select {
	case <-provider.fallbackSeen:
	case <-time.After(time.Second):
		t.Fatal("fallback was not admitted while the other primary was blocked")
	}
	close(provider.releaseBlocked)

	for name, done := range map[string]<-chan error{"blocked": blockedDone, "fallback": fallbackDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s Prompt() error = %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s Prompt() did not finish", name)
		}
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	want := []string{"blocked-primary", "failing-primary", "fallback"}
	if len(provider.models) != len(want) {
		t.Fatalf("request models = %v, want %v", provider.models, want)
	}
	for i := range want {
		if provider.models[i] != want[i] {
			t.Fatalf("request models = %v, want %v", provider.models, want)
		}
	}
}

func TestOptionsValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{name: "negative max turns", opts: Options{MaxTurns: -1}},
		{name: "negative budget", opts: Options{MaxBudgetUSD: -0.01}},
		{name: "negative preferred budget", opts: Options{Budget: &BudgetOptions{MaxUSD: -0.01}}},
		{name: "NaN preferred budget", opts: Options{Budget: &BudgetOptions{MaxUSD: math.NaN()}}},
		{name: "infinite preferred budget", opts: Options{Budget: &BudgetOptions{MaxUSD: math.Inf(1)}}},
		{name: "NaN budget", opts: Options{MaxBudgetUSD: math.NaN()}},
		{name: "positive infinite budget", opts: Options{MaxBudgetUSD: math.Inf(1)}},
		{name: "negative infinite budget", opts: Options{MaxBudgetUSD: math.Inf(-1)}},
		{name: "negative timeout", opts: Options{TimeoutMs: -1}},
		{name: "unsupported permission mode", opts: Options{PermissionMode: types.PermissionMode("unsupported")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.opts.Validate(); !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("Validate() error = %v, want ErrInvalidOptions", err)
			}

			provider := &recordingProvider{}
			opts := tt.opts
			opts.ProviderClient = provider
			if _, err := New(opts).Prompt(context.Background(), "hello"); !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("Prompt() error = %v, want ErrInvalidOptions", err)
			}
			if models := provider.recordedModels(); len(models) != 0 {
				t.Fatalf("provider admissions = %v, want none", models)
			}
		})
	}
}

func TestOptionsValidateAcceptsZeroValues(t *testing.T) {
	if err := (Options{}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	a := New(Options{ProviderClient: &recordingProvider{}})
	if a.opts.MaxTurns != 10 {
		t.Fatalf("default MaxTurns = %d, want 10", a.opts.MaxTurns)
	}
}
