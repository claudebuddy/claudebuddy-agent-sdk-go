package hooks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMatchesTool(t *testing.T) {
	tests := []struct {
		matcher  string
		toolName string
		want     bool
	}{
		{"*", "Bash", true},
		{"Bash", "Bash", true},
		{"Bash", "Edit", false},
		{"Bash|Edit", "Edit", true},
		{"Bash|Edit", "Write", false},
		{"mcp__*", "mcp__server__tool", true},
		{"mcp__server", "mcp__server__tool", true},
		{"*Tool", "MyTool", true},
		{"My*", "MyTool", true},
		{"My*", "Other", false},
	}
	for _, tt := range tests {
		got := matchesTool(tt.matcher, tt.toolName)
		if got != tt.want {
			t.Errorf("matchesTool(%q, %q) = %v, want %v", tt.matcher, tt.toolName, got, tt.want)
		}
	}
}

func TestRunPreToolUse_LegacyHookFn(t *testing.T) {
	called := false
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			Hooks: []HookFn{
				func(ctx context.Context, toolName string, input map[string]interface{}) (string, error) {
					called = true
					if toolName != "Bash" {
						t.Errorf("expected toolName=Bash, got %s", toolName)
					}
					return "", nil
				},
			},
		}},
	})

	result, err := mgr.RunPreToolUse(context.Background(), "Bash", map[string]interface{}{"command": "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("hook was not called")
	}
	if result.Blocked {
		t.Error("expected not blocked")
	}
}

func TestRunPreToolUse_Block(t *testing.T) {
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "Bash",
			Hooks: []HookFn{
				func(ctx context.Context, toolName string, input map[string]interface{}) (string, error) {
					return "blocked: dangerous command", nil
				},
			},
		}},
	})

	result, err := mgr.RunPreToolUse(context.Background(), "Bash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked {
		t.Error("expected blocked")
	}
	if result.Message != "blocked: dangerous command" {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestRunPreToolUse_HookFnEx(t *testing.T) {
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					if input.Event != HookPreToolUse {
						t.Errorf("expected event PreToolUse, got %s", input.Event)
					}
					if input.ToolName != "Edit" {
						t.Errorf("expected toolName=Edit, got %s", input.ToolName)
					}
					return &HookOutput{
						Decision:          HookDecisionAllow,
						AdditionalContext: "extra info",
					}, nil
				},
			},
		}},
	})

	result, err := mgr.RunPreToolUse(context.Background(), "Edit", map[string]interface{}{"file": "test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked {
		t.Error("expected not blocked")
	}
	if result.Output == nil {
		t.Fatal("expected output")
	}
	if result.Output.AdditionalContext != "extra info" {
		t.Errorf("unexpected additional context: %s", result.Output.AdditionalContext)
	}
}

func TestRunPreToolUse_HookFnExBlock(t *testing.T) {
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					return &HookOutput{
						Decision: HookDecisionBlock,
						Reason:   "not allowed",
					}, nil
				},
			},
		}},
	})

	result, err := mgr.RunPreToolUse(context.Background(), "Bash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked {
		t.Error("expected blocked")
	}
	if result.Message != "not allowed" {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestRunPostToolUse(t *testing.T) {
	var receivedOutput string
	mgr := NewManager(HookConfig{
		PostToolUse: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					receivedOutput = input.Output
					return &HookOutput{Decision: HookDecisionAllow}, nil
				},
			},
		}},
	})

	_, err := mgr.RunPostToolUse(context.Background(), "Bash", nil, "file1.go\nfile2.go")
	if err != nil {
		t.Fatal(err)
	}
	if receivedOutput != "file1.go\nfile2.go" {
		t.Errorf("unexpected output: %s", receivedOutput)
	}
}

func TestRunPostToolUseFailure(t *testing.T) {
	var receivedErr error
	var receivedOutput string
	mgr := NewManager(HookConfig{
		PostToolUseFailure: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					receivedErr = input.Error
					receivedOutput = input.Output
					return nil, nil
				},
			},
		}},
	})

	toolErr := errors.New("permission denied")
	_, err := mgr.RunPostToolUseFailure(context.Background(), "Bash", nil, "error output", toolErr)
	if err != nil {
		t.Fatal(err)
	}
	if receivedErr == nil || receivedErr.Error() != "permission denied" {
		t.Errorf("unexpected error: %v", receivedErr)
	}
	if receivedOutput != "error output" {
		t.Errorf("unexpected output: %s", receivedOutput)
	}
}

func TestRunUserPromptSubmit(t *testing.T) {
	var receivedPrompt string
	mgr := NewManager(HookConfig{
		UserPromptSubmit: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					receivedPrompt = input.UserPrompt
					return &HookOutput{
						Decision:     HookDecisionModify,
						UpdatedInput: map[string]interface{}{"prompt": "modified prompt"},
					}, nil
				},
			},
		}},
	})

	result, err := mgr.RunUserPromptSubmit(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if receivedPrompt != "hello world" {
		t.Errorf("unexpected prompt: %s", receivedPrompt)
	}
	if result.Output == nil || result.Output.Decision != HookDecisionModify {
		t.Error("expected modify decision")
	}
}

func TestRunSubagentStartStop(t *testing.T) {
	var startAgent, stopAgent string
	mgr := NewManager(HookConfig{
		SubagentStart: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					startAgent = input.AgentName
					return nil, nil
				},
			},
		}},
		SubagentStop: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					stopAgent = input.AgentName
					return nil, nil
				},
			},
		}},
	})

	_, err := mgr.RunSubagentStart(context.Background(), "researcher")
	if err != nil {
		t.Fatal(err)
	}
	if startAgent != "researcher" {
		t.Errorf("unexpected agent name: %s", startAgent)
	}

	_, err = mgr.RunSubagentStop(context.Background(), "researcher")
	if err != nil {
		t.Fatal(err)
	}
	if stopAgent != "researcher" {
		t.Errorf("unexpected agent name: %s", stopAgent)
	}
}

func TestRunPreCompact(t *testing.T) {
	called := false
	mgr := NewManager(HookConfig{
		PreCompact: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					called = true
					if input.Event != HookPreCompact {
						t.Errorf("expected PreCompact event, got %s", input.Event)
					}
					return nil, nil
				},
			},
		}},
	})

	_, err := mgr.RunPreCompact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("hook was not called")
	}
}

func TestRunNotification(t *testing.T) {
	var receivedMsg string
	mgr := NewManager(HookConfig{
		Notification: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					receivedMsg = input.NotificationMessage
					return nil, nil
				},
			},
		}},
	})

	_, err := mgr.RunNotification(context.Background(), "task complete")
	if err != nil {
		t.Fatal(err)
	}
	if receivedMsg != "task complete" {
		t.Errorf("unexpected message: %s", receivedMsg)
	}
}

func TestRunPermissionRequest(t *testing.T) {
	mgr := NewManager(HookConfig{
		PermissionRequest: []HookRule{{
			Matcher: "Bash",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					if input.PermissionTool != "Bash" {
						t.Errorf("expected Bash, got %s", input.PermissionTool)
					}
					return &HookOutput{Decision: HookDecisionAllow}, nil
				},
			},
		}},
	})

	result, err := mgr.RunPermissionRequest(context.Background(), "Bash", map[string]interface{}{"command": "rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked {
		t.Error("expected not blocked")
	}
}

func TestRunPermissionRequest_NoMatch(t *testing.T) {
	mgr := NewManager(HookConfig{
		PermissionRequest: []HookRule{{
			Matcher: "Bash",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					t.Error("should not be called for Edit")
					return nil, nil
				},
			},
		}},
	})

	_, err := mgr.RunPermissionRequest(context.Background(), "Edit", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHasHooks(t *testing.T) {
	empty := NewManager(HookConfig{})
	if empty.HasHooks() {
		t.Error("empty config should have no hooks")
	}

	configs := []HookConfig{
		{PreToolUse: []HookRule{{Matcher: "*"}}},
		{PostToolUse: []HookRule{{Matcher: "*"}}},
		{PostToolUseFailure: []HookRule{{Matcher: "*"}}},
		{PostSampling: []HookRule{{Matcher: "*"}}},
		{Stop: []HookRule{{Matcher: "*"}}},
		{UserPromptSubmit: []HookRule{{Matcher: "*"}}},
		{SubagentStart: []HookRule{{Matcher: "*"}}},
		{SubagentStop: []HookRule{{Matcher: "*"}}},
		{PreCompact: []HookRule{{Matcher: "*"}}},
		{Notification: []HookRule{{Matcher: "*"}}},
		{PermissionRequest: []HookRule{{Matcher: "*"}}},
	}
	for i, cfg := range configs {
		mgr := NewManager(cfg)
		if !mgr.HasHooks() {
			t.Errorf("config %d should have hooks", i)
		}
	}
}

func TestHookError(t *testing.T) {
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			Hooks: []HookFn{
				func(ctx context.Context, toolName string, input map[string]interface{}) (string, error) {
					return "", errors.New("hook failed")
				},
			},
		}},
	})

	_, err := mgr.RunPreToolUse(context.Background(), "Bash", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errors.Unwrap(err)) {
		// Just verify the error wrapping contains context.
	}
}

func TestHookFnExError(t *testing.T) {
	mgr := NewManager(HookConfig{
		PostToolUse: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					return nil, errors.New("extended hook failed")
				},
			},
		}},
	})

	_, err := mgr.RunPostToolUse(context.Background(), "Bash", nil, "output")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLegacyAndExtendedHooksRunTogether(t *testing.T) {
	legacyCalled := false
	extendedCalled := false
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			Hooks: []HookFn{
				func(ctx context.Context, toolName string, input map[string]interface{}) (string, error) {
					legacyCalled = true
					return "", nil
				},
			},
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					extendedCalled = true
					return nil, nil
				},
			},
		}},
	})

	_, err := mgr.RunPreToolUse(context.Background(), "Bash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !legacyCalled {
		t.Error("legacy hook not called")
	}
	if !extendedCalled {
		t.Error("extended hook not called")
	}
}

func TestLegacyBlockStopsExtended(t *testing.T) {
	extendedCalled := false
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			Hooks: []HookFn{
				func(ctx context.Context, toolName string, input map[string]interface{}) (string, error) {
					return "blocked by legacy", nil
				},
			},
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					extendedCalled = true
					return nil, nil
				},
			},
		}},
	})

	result, err := mgr.RunPreToolUse(context.Background(), "Bash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked {
		t.Error("expected blocked")
	}
	if extendedCalled {
		t.Error("extended hook should not run after legacy block")
	}
}

func TestRuleTimeout(t *testing.T) {
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			Timeout: 50 * time.Millisecond,
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(200 * time.Millisecond):
						return nil, nil
					}
				},
			},
		}},
	})

	_, err := mgr.RunPreToolUse(context.Background(), "Bash", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRunPostSampling(t *testing.T) {
	called := false
	mgr := NewManager(HookConfig{
		PostSampling: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					called = true
					if input.Event != HookPostSampling {
						t.Errorf("expected PostSampling, got %s", input.Event)
					}
					return nil, nil
				},
			},
		}},
	})

	_, err := mgr.RunPostSampling(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("hook was not called")
	}
}

func TestRunStop(t *testing.T) {
	called := false
	mgr := NewManager(HookConfig{
		Stop: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					called = true
					return nil, nil
				},
			},
		}},
	})

	_, err := mgr.RunStop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("hook was not called")
	}
}

func TestSuppressOutput(t *testing.T) {
	mgr := NewManager(HookConfig{
		PostToolUse: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					return &HookOutput{
						Decision:       HookDecisionAllow,
						SuppressOutput: true,
					}, nil
				},
			},
		}},
	})

	result, err := mgr.RunPostToolUse(context.Background(), "Bash", nil, "secret output")
	if err != nil {
		t.Fatal(err)
	}
	if result.Output == nil || !result.Output.SuppressOutput {
		t.Error("expected SuppressOutput=true")
	}
}

func TestSystemMessageInjection(t *testing.T) {
	mgr := NewManager(HookConfig{
		PreToolUse: []HookRule{{
			Matcher: "*",
			HooksEx: []HookFnEx{
				func(ctx context.Context, input *HookInput) (*HookOutput, error) {
					return &HookOutput{
						Decision:      HookDecisionAllow,
						SystemMessage: "Remember: be careful with this tool",
					}, nil
				},
			},
		}},
	})

	result, err := mgr.RunPreToolUse(context.Background(), "Bash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output == nil || result.Output.SystemMessage != "Remember: be careful with this tool" {
		t.Error("expected system message")
	}
}
