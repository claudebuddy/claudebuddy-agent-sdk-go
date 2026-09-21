package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/hooks"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

type hookRuntimeTool struct {
	name   string
	called atomic.Int32
	mu     sync.Mutex
	input  map[string]interface{}
	result *types.ToolResult
	err    error
}

func (t *hookRuntimeTool) Name() string        { return t.name }
func (t *hookRuntimeTool) Description() string { return "hook runtime test tool" }
func (t *hookRuntimeTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{Type: "object"}
}
func (t *hookRuntimeTool) IsConcurrencySafe(map[string]interface{}) bool { return false }
func (t *hookRuntimeTool) IsReadOnly(map[string]interface{}) bool        { return false }
func (t *hookRuntimeTool) Call(_ context.Context, input map[string]interface{}, _ *types.ToolUseContext) (*types.ToolResult, error) {
	t.called.Add(1)
	t.mu.Lock()
	t.input = input
	t.mu.Unlock()
	if t.result != nil {
		return t.result, t.err
	}
	return &types.ToolResult{Content: replyBlocks("ok")}, t.err
}

func hookToolProvider(name string) *sessionProvider {
	return &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) == 1 {
			return []types.ContentBlock{{Type: types.ContentBlockToolUse, ID: "call-1", Name: name, Input: map[string]interface{}{"value": "original"}}}, nil
		}
		return replyBlocks("handled"), nil
	}}
}

func historyToolError(messages []types.Message, id, text string) bool {
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type != types.ContentBlockToolResult || block.ToolUseID != id || !block.IsError {
				continue
			}
			for _, content := range block.Content {
				if content.Text == text || content.Text == "Error: "+text {
					return true
				}
			}
		}
	}
	return false
}

func TestPreToolHookBlocksExecutionAndPairsResult(t *testing.T) {
	tool := &hookRuntimeTool{name: "Danger"}
	var failures atomic.Int32
	a := New(Options{
		ProviderClient: hookToolProvider(tool.name), CustomTools: []types.Tool{tool},
		PermissionMode: types.PermissionModeBypassPermissions,
		Hooks: hooks.HookConfig{
			PreToolUse: []hooks.HookRule{{Matcher: tool.name, HooksEx: []hooks.HookFnEx{
				func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) {
					return &hooks.HookOutput{Decision: hooks.HookDecisionBlock, Reason: "blocked by host"}, nil
				},
			}}},
			PostToolUseFailure: []hooks.HookRule{{Matcher: tool.name, HooksEx: []hooks.HookFnEx{
				func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { failures.Add(1); return nil, nil },
			}}},
		},
	})
	defer a.Close()
	result, err := a.Prompt(sessionTestContext(t), "run it")
	if err != nil || tool.called.Load() != 0 {
		t.Fatalf("called=%d result=%+v err=%v", tool.called.Load(), result, err)
	}
	if !historyToolError(result.Messages, "call-1", "blocked by host") {
		t.Fatal("missing paired blocked tool result")
	}
	if failures.Load() != 1 {
		t.Fatalf("PostToolUseFailure calls=%d", failures.Load())
	}
}

func TestPreToolHookUpdatedInputReachesTool(t *testing.T) {
	tool := &hookRuntimeTool{name: "Mutable"}
	var permissionCalls atomic.Int32
	a := New(Options{
		ProviderClient: hookToolProvider(tool.name), CustomTools: []types.Tool{tool},
		PermissionMode: types.PermissionModeDefault,
		CanUseTool: func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			permissionCalls.Add(1)
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		},
		Hooks: hooks.HookConfig{PreToolUse: []hooks.HookRule{{Matcher: tool.name, HooksEx: []hooks.HookFnEx{
			func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) {
				return &hooks.HookOutput{Decision: hooks.HookDecisionModify, UpdatedInput: map[string]interface{}{"value": "updated"}}, nil
			},
		}}}},
	})
	defer a.Close()
	if _, err := a.Prompt(sessionTestContext(t), "run it"); err != nil {
		t.Fatal(err)
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if tool.input["value"] != "updated" {
		t.Fatalf("tool input=%v", tool.input)
	}
	if permissionCalls.Load() != 1 {
		t.Fatalf("permission callback calls=%d", permissionCalls.Load())
	}
}

func TestPostToolHooksChooseSuccessOrFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  *types.ToolResult
		toolErr error
		wantOK  int32
		wantBad int32
	}{
		{name: "success", result: &types.ToolResult{Content: replyBlocks("ok")}, wantOK: 1},
		{name: "tool result error", result: &types.ToolResult{IsError: true, Error: "bad", Content: replyBlocks("bad")}, wantBad: 1},
		{name: "call error", toolErr: errors.New("boom"), wantBad: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ok, bad atomic.Int32
			tool := &hookRuntimeTool{name: "Outcome", result: tc.result, err: tc.toolErr}
			a := New(Options{ProviderClient: hookToolProvider(tool.name), CustomTools: []types.Tool{tool}, PermissionMode: types.PermissionModeBypassPermissions,
				Hooks: hooks.HookConfig{
					PostToolUse:        []hooks.HookRule{{Matcher: tool.name, HooksEx: []hooks.HookFnEx{func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { ok.Add(1); return nil, nil }}}},
					PostToolUseFailure: []hooks.HookRule{{Matcher: tool.name, HooksEx: []hooks.HookFnEx{func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { bad.Add(1); return nil, nil }}}},
				},
			})
			defer a.Close()
			if _, err := a.Prompt(sessionTestContext(t), "run"); err != nil {
				t.Fatal(err)
			}
			if ok.Load() != tc.wantOK || bad.Load() != tc.wantBad {
				t.Fatalf("success hooks=%d failure hooks=%d", ok.Load(), bad.Load())
			}
		})
	}
}

func TestUserPromptHookBlocksBeforeProviderAdmission(t *testing.T) {
	var calls atomic.Int32
	p := &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
		calls.Add(1)
		return replyBlocks("no"), nil
	}}
	a := New(Options{ProviderClient: p, Hooks: hooks.HookConfig{UserPromptSubmit: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{
		func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) {
			return &hooks.HookOutput{Decision: hooks.HookDecisionBlock, Reason: "prompt blocked"}, nil
		},
	}}}}})
	defer a.Close()
	result, err := a.Prompt(sessionTestContext(t), "blocked")
	if err == nil || result.Subtype != types.ResultErrorDuringExecution || calls.Load() != 0 || len(result.Messages) != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
}

func TestSessionHooksBracketLifetimeAndStopRunsOnce(t *testing.T) {
	var starts, ends, stops atomic.Int32
	a := New(Options{ProviderClient: &recordingProvider{}, Hooks: hooks.HookConfig{
		SessionStart: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { starts.Add(1); return nil, nil }}}},
		SessionEnd:   []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { ends.Add(1); return nil, nil }}}},
		Stop:         []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { stops.Add(1); return nil, nil }}}},
	}})
	s := mustSession(t, a, "lifecycle")
	if _, err := s.Prompt(sessionTestContext(t), "hello"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close()
	if starts.Load() != 2 || ends.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("starts=%d ends=%d stops=%d", starts.Load(), ends.Load(), stops.Load())
	}
	a.Close()
	if ends.Load() != 2 {
		t.Fatalf("default SessionEnd count=%d", ends.Load())
	}
}

func TestHookTimeoutObservesRunCancellation(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan error, 1)
	tool := &hookRuntimeTool{name: "WaitHook"}
	a := New(Options{ProviderClient: hookToolProvider(tool.name), CustomTools: []types.Tool{tool}, PermissionMode: types.PermissionModeBypassPermissions,
		Hooks: hooks.HookConfig{PreToolUse: []hooks.HookRule{{Matcher: tool.name, Timeout: time.Minute, HooksEx: []hooks.HookFnEx{
			func(ctx context.Context, _ *hooks.HookInput) (*hooks.HookOutput, error) {
				close(entered)
				<-ctx.Done()
				exited <- ctx.Err()
				return nil, ctx.Err()
			},
		}}}},
	})
	defer a.Close()
	ctx := sessionTestContext(t)
	r := mustStart(t, mustSession(t, a, "hook-cancel"), ctx, "run")
	awaitSessionValue(t, ctx, entered)
	r.Cancel()
	result, err := r.Wait()
	if !errors.Is(err, context.Canceled) || result.Subtype != types.ResultCancelled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if hookErr := awaitSessionValue(t, ctx, exited); !errors.Is(hookErr, context.Canceled) {
		t.Fatalf("hook error=%v", hookErr)
	}
}

func TestPermissionRequestHookRunsBetweenStaticBoundsAndHostCallback(t *testing.T) {
	tool := &hookRuntimeTool{name: "Danger"}
	var mu sync.Mutex
	var order []string
	a := New(Options{
		ProviderClient: &recordingProvider{}, CustomTools: []types.Tool{tool}, PermissionMode: types.PermissionModeDefault,
		Hooks: hooks.HookConfig{PermissionRequest: []hooks.HookRule{{Matcher: tool.name, HooksEx: []hooks.HookFnEx{
			func(_ context.Context, input *hooks.HookInput) (*hooks.HookOutput, error) {
				mu.Lock()
				order = append(order, "hook")
				mu.Unlock()
				return &hooks.HookOutput{UpdatedInput: map[string]interface{}{"value": "hooked"}}, nil
			},
		}}}},
		CanUseTool: func(_ types.Tool, input map[string]interface{}) (*types.PermissionDecision, error) {
			mu.Lock()
			order = append(order, "host")
			mu.Unlock()
			if input["value"] != "hooked" {
				t.Fatalf("host input=%v", input)
			}
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		},
	})
	defer a.Close()
	decision, err := a.permissionPolicy(sessionTestContext(t))(tool, map[string]interface{}{"value": "original"})
	if err != nil || decision.Behavior != types.PermissionAllow || decision.UpdatedInput["value"] != "hooked" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	mu.Lock()
	if len(order) != 2 || order[0] != "hook" || order[1] != "host" {
		t.Fatalf("order=%v", order)
	}
	mu.Unlock()

	order = nil
	denied := New(Options{ProviderClient: &recordingProvider{}, CustomTools: []types.Tool{tool}, DisallowedTools: []string{tool.name}, PermissionMode: types.PermissionModeDefault,
		Hooks: a.opts.Hooks, CanUseTool: a.opts.CanUseTool})
	defer denied.Close()
	decision, err = denied.permissionPolicy(sessionTestContext(t))(tool, map[string]interface{}{})
	if err != nil || decision.Behavior != types.PermissionDeny {
		t.Fatalf("static decision=%+v err=%v", decision, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 0 {
		t.Fatalf("static denial invoked dynamic callbacks: %v", order)
	}
}

func TestPostSamplingRunsAfterCompleteProviderResponse(t *testing.T) {
	var called atomic.Int32
	a := New(Options{ProviderClient: &recordingProvider{}, Hooks: hooks.HookConfig{PostSampling: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{
		func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { called.Add(1); return nil, nil },
	}}}}})
	defer a.Close()
	if _, err := a.Prompt(sessionTestContext(t), "hello"); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 1 {
		t.Fatalf("PostSampling calls=%d", called.Load())
	}
}

func TestStopHookFailurePreservesStrongerCancellationTerminal(t *testing.T) {
	stopErr := errors.New("stop cleanup failed")
	stopHook := hooks.HookConfig{Stop: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{
		func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { return nil, stopErr },
	}}}}

	success := New(Options{ProviderClient: &recordingProvider{}, Hooks: stopHook})
	result, err := success.Prompt(sessionTestContext(t), "hello")
	success.Close()
	if !errors.Is(err, stopErr) || result.Subtype != types.ResultErrorDuringExecution {
		t.Fatalf("success result=%+v err=%v", result, err)
	}

	entered := make(chan string, 1)
	cancelled := New(Options{ProviderClient: newBlockingSessionProvider(entered, make(chan struct{})), Hooks: stopHook})
	defer cancelled.Close()
	ctx := sessionTestContext(t)
	r := mustStart(t, cancelled.defaultSession, ctx, "hello")
	awaitSessionValue(t, ctx, entered)
	r.Cancel()
	result, err = r.Wait()
	if !errors.Is(err, context.Canceled) || !errors.Is(err, stopErr) || result.Subtype != types.ResultCancelled || len(result.Errors) != 2 {
		t.Fatalf("cancel result=%+v err=%v", result, err)
	}
}

func TestStopHookFiresOnceOnNonSuccessTerminalPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
	}{
		{name: "execution error", opts: Options{ProviderClient: &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
			return nil, errors.New("boom")
		}}}},
		{name: "maximum budget", opts: Options{ProviderClient: &recordingProvider{}, Budget: &BudgetOptions{MaxUSD: 0}}},
		{name: "maximum turns", opts: Options{ProviderClient: hookToolProvider("missing"), MaxTurns: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stops atomic.Int32
			tc.opts.Hooks = hooks.HookConfig{Stop: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{
				func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { stops.Add(1); return nil, nil },
			}}}}
			a := New(tc.opts)
			_, _ = a.Prompt(sessionTestContext(t), "hello")
			a.Close()
			if stops.Load() != 1 {
				t.Fatalf("Stop calls=%d", stops.Load())
			}
		})
	}
}

func TestSessionStartFailureIsReturnedWithoutDefaultSessionPanic(t *testing.T) {
	boom := errors.New("session start failed")
	a := New(Options{ProviderClient: &recordingProvider{}, Hooks: hooks.HookConfig{SessionStart: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{
		func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { return nil, boom },
	}}}}})
	defer a.Close()
	if _, err := a.Prompt(sessionTestContext(t), "hello"); !errors.Is(err, boom) {
		t.Fatalf("Prompt error=%v", err)
	}
}

func TestSubagentHooksBracketChildExecution(t *testing.T) {
	var starts, stops atomic.Int32
	p := &childBudgetProvider{}
	a := New(Options{ProviderClient: p, Budget: &BudgetOptions{MaxUSD: 3}, PermissionMode: types.PermissionModeBypassPermissions,
		Hooks: hooks.HookConfig{
			SubagentStart: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { starts.Add(1); return nil, nil }}}},
			SubagentStop:  []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{func(context.Context, *hooks.HookInput) (*hooks.HookOutput, error) { stops.Add(1); return nil, nil }}}},
		},
	})
	defer a.Close()
	_, _ = a.Prompt(sessionTestContext(t), "parent")
	if starts.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("SubagentStart=%d SubagentStop=%d", starts.Load(), stops.Load())
	}
}

func TestConcurrentCloseCannotEndSessionBeforeStartHookReturns(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	ended := make(chan struct{}, 1)
	a := New(Options{ProviderClient: &recordingProvider{}, Hooks: hooks.HookConfig{
		SessionStart: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{func(_ context.Context, input *hooks.HookInput) (*hooks.HookOutput, error) {
			if input.SessionID == "ordered" {
				close(entered)
				<-release
			}
			return nil, nil
		}}}},
		SessionEnd: []hooks.HookRule{{Matcher: "*", HooksEx: []hooks.HookFnEx{func(_ context.Context, input *hooks.HookInput) (*hooks.HookOutput, error) {
			if input.SessionID == "ordered" {
				ended <- struct{}{}
			}
			return nil, nil
		}}}},
	}})
	created := make(chan error, 1)
	go func() { _, err := a.NewSession(SessionOptions{ID: "ordered"}); created <- err }()
	<-entered
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	select {
	case <-ended:
		t.Fatal("SessionEnd fired before SessionStart returned")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	awaitSessionValue(t, sessionTestContext(t), closed)
	if err := awaitSessionValue(t, sessionTestContext(t), created); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("NewSession error=%v", err)
	}
	select {
	case <-ended:
	default:
		t.Fatal("SessionEnd did not fire after SessionStart")
	}
}
