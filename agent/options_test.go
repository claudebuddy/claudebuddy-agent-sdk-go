package agent

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"testing"

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
	events := make(chan api.StreamEvent, 4)
	errs := make(chan error, 1)
	events <- api.StreamEvent{Type: "message_start", Message: &api.StreamMessage{Role: "assistant", Model: req.Model}}
	events <- api.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentBlockText}}
	events <- api.StreamEvent{Type: "content_block_delta", Index: 0, Delta: map[string]interface{}{"type": "text_delta", "text": "ok"}}
	events <- api.StreamEvent{Type: "message_delta", Delta: map[string]interface{}{"stop_reason": "end_turn"}}
	close(events)
	close(errs)
	return events, errs
}

func TestOptionsRejectInvalidLimitsBeforeProviderCall(t *testing.T) {
	provider := &recordingProvider{}
	a := New(Options{ProviderClient: provider, MaxTurns: -1})
	_, err := a.Prompt(context.Background(), "hello")
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("got %v, want ErrInvalidOptions", err)
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

func TestOptionsValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{name: "negative max turns", opts: Options{MaxTurns: -1}},
		{name: "negative budget", opts: Options{MaxBudgetUSD: -0.01}},
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
