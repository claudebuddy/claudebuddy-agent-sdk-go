package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

type terminalTool struct{ name string }

func (t *terminalTool) Name() string        { return t.name }
func (t *terminalTool) Description() string { return "terminal test tool" }
func (t *terminalTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{Type: "object"}
}
func (t *terminalTool) IsConcurrencySafe(map[string]interface{}) bool { return false }
func (t *terminalTool) IsReadOnly(map[string]interface{}) bool        { return false }
func (t *terminalTool) Call(context.Context, map[string]interface{}, *types.ToolUseContext) (*types.ToolResult, error) {
	return &types.ToolResult{Content: replyBlocks("should not run")}, nil
}

type childBudgetProvider struct{ calls atomic.Int32 }

func (p *childBudgetProvider) CreateMessage(context.Context, api.MessagesRequest) (*api.StreamMessage, error) {
	return nil, errors.New("stream only")
}

func (p *childBudgetProvider) CreateMessageStream(_ context.Context, req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
	p.calls.Add(1)
	last := req.Messages[len(req.Messages)-1]
	prompt := types.ExtractText(&types.Message{Content: last.Content})
	var blocks []types.ContentBlock
	var usage *types.Usage
	switch prompt {
	case "parent":
		blocks = []types.ContentBlock{{Type: types.ContentBlockToolUse, ID: "parent-agent", Name: "Agent", Input: map[string]interface{}{"description": "child", "prompt": "child"}}}
	case "child":
		usage = &types.Usage{InputTokens: 1_000_000}
		blocks = []types.ContentBlock{{Type: types.ContentBlockToolUse, ID: "child-tool", Name: "missing", Input: map[string]interface{}{}}}
	default:
		blocks = replyBlocks("unexpected extra model admission")
	}
	events := make(chan api.StreamEvent, len(blocks)+2)
	errs := make(chan error)
	events <- api.StreamEvent{Type: "message_start", Message: &api.StreamMessage{Role: "assistant", Model: req.Model, Usage: usage}}
	for i := range blocks {
		block := blocks[i]
		events <- api.StreamEvent{Type: "content_block_start", Index: i, ContentBlock: &block}
	}
	stopReason := "end_turn"
	if len(blocks) > 0 && blocks[0].Type == types.ContentBlockToolUse {
		stopReason = "tool_use"
	}
	events <- api.StreamEvent{Type: "message_delta", Delta: map[string]interface{}{"stop_reason": stopReason}}
	close(events)
	close(errs)
	return events, errs
}

func collectTerminalEvents(events <-chan types.SDKMessage) []types.SDKMessage {
	var terminals []types.SDKMessage
	for event := range events {
		if event.Type == types.MessageTypeResult {
			terminals = append(terminals, event)
		}
	}
	return terminals
}

func TestRunPublishesOneErrorTerminalResult(t *testing.T) {
	boom := errors.New("provider unavailable")
	a := New(Options{ProviderClient: &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
		return nil, boom
	}}})
	defer a.Close()
	r := mustStart(t, mustSession(t, a, "failure-terminal"), sessionTestContext(t), "hello")
	terminals := collectTerminalEvents(r.Events())
	result, err := r.Wait()
	if !errors.Is(err, boom) || result == nil || len(terminals) != 1 {
		t.Fatalf("result=%+v terminals=%d err=%v", result, len(terminals), err)
	}
	terminal := terminals[0]
	if terminal.Subtype != types.ResultErrorDuringExecution || !terminal.IsError || len(terminal.Errors) != 1 {
		t.Fatalf("terminal=%+v", terminal)
	}
	terminal.Usage.InputTokens = 999
	result, _ = r.Wait()
	if result.Usage.InputTokens != 0 {
		t.Fatalf("terminal usage aliases cached result: %+v", result.Usage)
	}
}

func TestZeroBudgetStartsNoProviderRequest(t *testing.T) {
	var calls atomic.Int32
	p := &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
		calls.Add(1)
		return replyBlocks("unexpected"), nil
	}}
	a := New(Options{ProviderClient: p, Budget: &BudgetOptions{MaxUSD: 0}})
	defer a.Close()
	result, err := a.Prompt(sessionTestContext(t), "hello")
	if !errors.Is(err, ErrMaxBudget) || result == nil || result.Subtype != types.ResultErrorMaxBudget || calls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
}

func TestRunReportsMaximumTurnsOnlyWhenFinalTurnNeedsAnotherModelCall(t *testing.T) {
	toolThenText := &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) == 1 {
			return []types.ContentBlock{{Type: types.ContentBlockToolUse, ID: "call-1", Name: "missing"}}, nil
		}
		return replyBlocks("done"), nil
	}}
	limited := New(Options{ProviderClient: toolThenText, MaxTurns: 1})
	defer limited.Close()
	result, err := limited.Prompt(sessionTestContext(t), "hello")
	if !errors.Is(err, ErrMaxTurns) || result.Subtype != types.ResultErrorMaxTurns {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	finalSuccess := New(Options{ProviderClient: &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
		return replyBlocks("done"), nil
	}}, MaxTurns: 1})
	defer finalSuccess.Close()
	result, err = finalSuccess.Prompt(sessionTestContext(t), "hello")
	if err != nil || result.Subtype != types.ResultSuccess || result.Text != "done" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunCancellationHasTypedTerminal(t *testing.T) {
	entered := make(chan string, 1)
	a := New(Options{ProviderClient: newBlockingSessionProvider(entered, make(chan struct{}))})
	defer a.Close()
	ctx := sessionTestContext(t)
	r := mustStart(t, mustSession(t, a, "cancel-terminal"), ctx, "hello")
	awaitSessionValue(t, ctx, entered)
	r.Cancel()
	terminals := collectTerminalEvents(r.Events())
	result, err := r.Wait()
	if !errors.Is(err, context.Canceled) || result.Subtype != types.ResultCancelled || len(terminals) != 1 || terminals[0].Subtype != types.ResultCancelled {
		t.Fatalf("result=%+v terminals=%+v err=%v", result, terminals, err)
	}
}

func TestRunTerminalPreservesPermissionDenials(t *testing.T) {
	p := &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) == 1 {
			return []types.ContentBlock{{Type: types.ContentBlockToolUse, ID: "denied-call", Name: "Danger", Input: map[string]interface{}{}}}, nil
		}
		return replyBlocks("handled"), nil
	}}
	a := New(Options{
		ProviderClient: p,
		CustomTools:    []types.Tool{&terminalTool{name: "Danger"}},
		PermissionMode: types.PermissionModeDefault,
		CanUseTool: func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			return &types.PermissionDecision{Behavior: types.PermissionDeny, Reason: "host denied"}, nil
		},
	})
	defer a.Close()
	r := mustStart(t, mustSession(t, a, "permission-terminal"), sessionTestContext(t), "hello")
	terminals := collectTerminalEvents(r.Events())
	result, err := r.Wait()
	if err != nil || result.Subtype != types.ResultSuccess || len(terminals) != 1 {
		t.Fatalf("result=%+v terminals=%+v err=%v", result, terminals, err)
	}
	want := types.PermissionDenial{Tool: "Danger", Reason: "host denied"}
	if len(result.PermissionDenials) != 1 || result.PermissionDenials[0] != want {
		t.Fatalf("result denials=%+v", result.PermissionDenials)
	}
	if len(terminals[0].PermissionDenials) != 1 || terminals[0].PermissionDenials[0] != want {
		t.Fatalf("terminal denials=%+v", terminals[0].PermissionDenials)
	}
}

func TestSequentialRunsUseFreshLedgers(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	p := &sessionProvider{usage: &types.Usage{InputTokens: 1_000_000}, respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return replyBlocks("ok"), nil
	}}
	a := New(Options{ProviderClient: p, Budget: &BudgetOptions{MaxUSD: 3}})
	defer a.Close()
	s := mustSession(t, a, "fresh-ledger")
	for i := 0; i < 2; i++ {
		result, err := s.Prompt(sessionTestContext(t), "hello")
		if err != nil || result.Subtype != types.ResultSuccess || result.NumTurns != 1 {
			t.Fatalf("run %d result=%+v err=%v", i, result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("provider calls=%d", calls)
	}
}

func TestChildAgentInheritsParentRunBudget(t *testing.T) {
	p := &childBudgetProvider{}
	a := New(Options{ProviderClient: p, Budget: &BudgetOptions{MaxUSD: 3}, PermissionMode: types.PermissionModeBypassPermissions})
	defer a.Close()
	result, err := a.Prompt(sessionTestContext(t), "parent")
	if !errors.Is(err, ErrMaxBudget) || result.Subtype != types.ResultErrorMaxBudget {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if calls := p.calls.Load(); calls != 2 {
		t.Fatalf("provider calls=%d, want parent and one child admission", calls)
	}
	input, _ := a.CostTracker().TotalTokens()
	if input != 1_000_000 {
		t.Fatalf("parent aggregate input tokens=%d, want child usage", input)
	}
}

func TestPreCancelledStartMakesNoProviderRequest(t *testing.T) {
	var calls atomic.Int32
	p := &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
		calls.Add(1)
		return replyBlocks("unexpected"), nil
	}}
	a := New(Options{ProviderClient: p})
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Prompt(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}
