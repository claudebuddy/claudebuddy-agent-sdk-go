package permissions_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/permissions"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

type fakeTool struct {
	name       string
	readOnly   bool
	readOnlyFn func(map[string]interface{}) bool
	called     *atomic.Bool
}

func (t *fakeTool) Name() string        { return t.name }
func (t *fakeTool) Description() string { return t.name }
func (t *fakeTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{Type: "object"}
}
func (t *fakeTool) Call(context.Context, map[string]interface{}, *types.ToolUseContext) (*types.ToolResult, error) {
	if t.called != nil {
		t.called.Store(true)
	}
	return &types.ToolResult{}, nil
}
func (t *fakeTool) IsConcurrencySafe(map[string]interface{}) bool { return t.readOnly }
func (t *fakeTool) IsReadOnly(input map[string]interface{}) bool {
	if t.readOnlyFn != nil {
		return t.readOnlyFn(input)
	}
	return t.readOnly
}

func TestPermissionModesDoNotSilentlyAllowMutations(t *testing.T) {
	tests := []struct {
		name string
		mode types.PermissionMode
		tool types.Tool
		want types.PermissionBehavior
	}{
		{name: "plan mutation", mode: types.PermissionModePlan, tool: &fakeTool{name: "CustomWrite"}, want: types.PermissionDeny},
		{name: "dontAsk mutation", mode: types.PermissionModeDontAsk, tool: &fakeTool{name: "CustomWrite"}, want: types.PermissionDeny},
		{name: "default mutation", mode: types.PermissionModeDefault, tool: &fakeTool{name: "CustomWrite"}, want: types.PermissionDeny},
		{name: "plan read", mode: types.PermissionModePlan, tool: &fakeTool{name: "ReadOnly", readOnly: true}, want: types.PermissionAllow},
		{name: "accept built-in edit", mode: types.PermissionModeAcceptEdits, tool: tools.NewFileEditTool(), want: types.PermissionAllow},
		{name: "accept impersonator", mode: types.PermissionModeAcceptEdits, tool: &fakeTool{name: "Edit"}, want: types.PermissionDeny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := permissions.NewPolicy(&permissions.Config{Mode: tt.mode}, nil, nil, nil)
			got, err := policy(tt.tool, map[string]interface{}{})
			if err != nil || got.Behavior != tt.want {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestExplicitEmptyAllowListExposesNoTools(t *testing.T) {
	toolsIn := []types.Tool{&fakeTool{name: "ReadOnly", readOnly: true}}
	got := permissions.FilterTools(toolsIn, []string{}, nil)
	if len(got) != 0 {
		t.Fatalf("got %d tools", len(got))
	}
}

func TestNilAllowListLeavesToolsVisible(t *testing.T) {
	toolsIn := []types.Tool{&fakeTool{name: "ReadOnly", readOnly: true}}
	got := permissions.FilterTools(toolsIn, nil, nil)
	if len(got) != 1 || got[0].Name() != "ReadOnly" {
		t.Fatalf("got tools=%v", toolNames(got))
	}
}

func TestDenyBoundBeatsAllowBound(t *testing.T) {
	policy := permissions.NewPolicy(
		&permissions.Config{Mode: types.PermissionModeBypassPermissions},
		[]string{"Danger"},
		[]string{"Danger"},
		nil,
	)

	got, err := policy(&fakeTool{name: "Danger"}, nil)
	if err != nil || got.Behavior != types.PermissionDeny {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestCallbackCanDenyAllowedOperation(t *testing.T) {
	policy := permissions.NewPolicy(
		&permissions.Config{Mode: types.PermissionModeDefault},
		nil,
		nil,
		func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			return &types.PermissionDecision{Behavior: types.PermissionDeny, Reason: "host denied"}, nil
		},
	)

	got, err := policy(&fakeTool{name: "ReadOnly", readOnly: true}, nil)
	if err != nil || got == nil || got.Behavior != types.PermissionDeny || got.Reason != "host denied" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestCallbackCannotWidenPlanRestriction(t *testing.T) {
	var callbackCalled atomic.Bool
	policy := permissions.NewPolicy(
		&permissions.Config{Mode: types.PermissionModePlan},
		nil,
		nil,
		func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			callbackCalled.Store(true)
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		},
	)

	got, err := policy(&fakeTool{name: "CustomWrite"}, nil)
	if err != nil || got.Behavior != types.PermissionDeny || callbackCalled.Load() {
		t.Fatalf("got=%+v err=%v callbackCalled=%v", got, err, callbackCalled.Load())
	}
}

func TestCallbackCannotWidenExplicitAllowBound(t *testing.T) {
	var callbackCalled atomic.Bool
	policy := permissions.NewPolicy(
		&permissions.Config{Mode: types.PermissionModeDefault},
		[]string{"ReadOnly"},
		nil,
		func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			callbackCalled.Store(true)
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		},
	)

	got, err := policy(&fakeTool{name: "CustomWrite"}, nil)
	if err != nil || got.Behavior != types.PermissionDeny || callbackCalled.Load() {
		t.Fatalf("got=%+v err=%v callbackCalled=%v", got, err, callbackCalled.Load())
	}
}

func TestDynamicRulesRemainSupportedWithMCPPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		config *permissions.Config
		want   types.PermissionBehavior
	}{
		{
			name: "dynamic allow",
			config: &permissions.Config{
				Mode:       types.PermissionModeDefault,
				AllowRules: []permissions.Rule{{ToolName: "mcp__github"}},
			},
			want: types.PermissionAllow,
		},
		{
			name: "dynamic deny beats dynamic allow",
			config: &permissions.Config{
				Mode:       types.PermissionModeBypassPermissions,
				AllowRules: []permissions.Rule{{ToolName: "mcp__github"}},
				DenyRules:  []permissions.Rule{{ToolName: "mcp__github"}},
			},
			want: types.PermissionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := permissions.NewPolicy(tt.config, nil, nil, nil)
			got, err := policy(&fakeTool{name: "mcp__github__search"}, nil)
			if err != nil || got.Behavior != tt.want {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestDynamicRulesDoNotMatchNameCollisions(t *testing.T) {
	tests := []struct {
		name   string
		config *permissions.Config
		tool   string
		want   types.PermissionBehavior
	}{
		{
			name: "ordinary allow is exact",
			config: &permissions.Config{
				Mode:       types.PermissionModeDefault,
				AllowRules: []permissions.Rule{{ToolName: "Bash"}},
			},
			tool: "BashEvil",
			want: types.PermissionDeny,
		},
		{
			name: "ordinary deny is exact",
			config: &permissions.Config{
				Mode:      types.PermissionModeBypassPermissions,
				DenyRules: []permissions.Rule{{ToolName: "Bash"}},
			},
			tool: "BashEvil",
			want: types.PermissionAllow,
		},
		{
			name: "MCP allow requires namespace boundary",
			config: &permissions.Config{
				Mode:       types.PermissionModeDefault,
				AllowRules: []permissions.Rule{{ToolName: "mcp__github"}},
			},
			tool: "mcp__github_evil__write",
			want: types.PermissionDeny,
		},
		{
			name: "MCP deny requires namespace boundary",
			config: &permissions.Config{
				Mode:      types.PermissionModeBypassPermissions,
				DenyRules: []permissions.Rule{{ToolName: "mcp__github"}},
			},
			tool: "mcp__github_evil__write",
			want: types.PermissionAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := permissions.NewPolicy(tt.config, nil, nil, nil)
			got, err := policy(&fakeTool{name: tt.tool}, nil)
			if err != nil || got.Behavior != tt.want {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestDynamicMCPRuleDoesNotIgnorePattern(t *testing.T) {
	policy := permissions.NewPolicy(&permissions.Config{
		Mode: types.PermissionModeDefault,
		AllowRules: []permissions.Rule{{
			ToolName: "mcp__github",
			Pattern:  "safe*",
		}},
	}, nil, nil, nil)

	got, err := policy(&fakeTool{name: "mcp__github__write"}, map[string]interface{}{"command": "unsafe"})
	if err != nil || got.Behavior != types.PermissionDeny {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestCallbackUpdatedInputCannotWidenPlanRestriction(t *testing.T) {
	var callbackCalled atomic.Bool
	var toolCalled atomic.Bool
	inputSensitive := &fakeTool{
		name: "InputSensitive",
		readOnlyFn: func(input map[string]interface{}) bool {
			return input["operation"] == "read"
		},
		called: &toolCalled,
	}
	policy := permissions.NewPolicy(
		&permissions.Config{Mode: types.PermissionModePlan},
		nil,
		nil,
		func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			callbackCalled.Store(true)
			return &types.PermissionDecision{
				Behavior:     types.PermissionAllow,
				UpdatedInput: map[string]interface{}{"operation": "write"},
			}, nil
		},
	)
	registry := tools.NewRegistry()
	registry.Register(inputSensitive)
	executor := tools.NewExecutor(registry, policy, nil)

	result := executor.RunTools(context.Background(), []tools.ToolCallRequest{{
		ToolUseID: "call-1",
		ToolName:  inputSensitive.Name(),
		Input:     map[string]interface{}{"operation": "read"},
	}})
	if !callbackCalled.Load() || toolCalled.Load() || len(result) != 1 || !result[0].Result.IsError {
		t.Fatalf("callbackCalled=%v toolCalled=%v result=%+v", callbackCalled.Load(), toolCalled.Load(), result)
	}
}

func TestCallbackUpdatedInputIsRecheckedAgainstDynamicDeny(t *testing.T) {
	var callbackCalled atomic.Bool
	var toolCalled atomic.Bool
	bash := &fakeTool{name: "Bash", readOnly: true, called: &toolCalled}
	policy := permissions.NewPolicy(
		&permissions.Config{
			Mode:      types.PermissionModeDefault,
			DenyRules: []permissions.Rule{{ToolName: "Bash", Pattern: "rm *"}},
		},
		nil,
		nil,
		func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			callbackCalled.Store(true)
			return &types.PermissionDecision{
				Behavior:     types.PermissionAllow,
				UpdatedInput: map[string]interface{}{"command": "rm -rf /tmp/not-run"},
			}, nil
		},
	)
	registry := tools.NewRegistry()
	registry.Register(bash)
	executor := tools.NewExecutor(registry, policy, nil)

	result := executor.RunTools(context.Background(), []tools.ToolCallRequest{{
		ToolUseID: "call-1",
		ToolName:  bash.Name(),
		Input:     map[string]interface{}{"command": "pwd"},
	}})
	if !callbackCalled.Load() || toolCalled.Load() || len(result) != 1 || !result[0].Result.IsError {
		t.Fatalf("callbackCalled=%v toolCalled=%v result=%+v", callbackCalled.Load(), toolCalled.Load(), result)
	}
}

func TestPolicyDoesNotHoldConfigLockAcrossCallback(t *testing.T) {
	config := &permissions.Config{Mode: types.PermissionModeDefault}
	policy := permissions.NewPolicy(config, nil, nil, func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
		config.SetMode(types.PermissionModeDontAsk)
		return &types.PermissionDecision{Behavior: types.PermissionDeny}, nil
	})

	done := make(chan struct{})
	go func() {
		_, _ = policy(&fakeTool{name: "ReadOnly", readOnly: true}, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("policy callback deadlocked while updating config")
	}
}

func TestAgentFiltersSchemasAndRetainsExecutionPolicy(t *testing.T) {
	var called atomic.Bool
	danger := &fakeTool{name: "Danger", called: &called}
	provider := &scriptedProvider{toolName: "Danger"}
	a := agent.New(agent.Options{
		ProviderClient:  provider,
		CustomTools:     []types.Tool{danger},
		DisallowedTools: []string{"Danger"},
		PermissionMode:  types.PermissionModeBypassPermissions,
		MaxTurns:        2,
		SystemPrompt:    "test",
		SettingSources:  []string{},
	})
	defer a.Close()

	if _, err := a.Prompt(context.Background(), "run danger"); err != nil {
		t.Fatal(err)
	}
	if called.Load() {
		t.Fatal("denied registry tool executed")
	}
	if provider.sawTool("Danger") {
		t.Fatal("denied tool was exposed in model-visible schemas")
	}
}

func TestAgentComposesCallbackWithPlanBounds(t *testing.T) {
	var toolCalled atomic.Bool
	var callbackCalled atomic.Bool
	provider := &scriptedProvider{toolName: "CustomWrite"}
	a := agent.New(agent.Options{
		ProviderClient: provider,
		CustomTools:    []types.Tool{&fakeTool{name: "CustomWrite", called: &toolCalled}},
		PermissionMode: types.PermissionModePlan,
		CanUseTool: func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
			callbackCalled.Store(true)
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		},
		MaxTurns:       2,
		SystemPrompt:   "test",
		SettingSources: []string{},
	})
	defer a.Close()

	if _, err := a.Prompt(context.Background(), "run write"); err != nil {
		t.Fatal(err)
	}
	if toolCalled.Load() || callbackCalled.Load() {
		t.Fatalf("toolCalled=%v callbackCalled=%v", toolCalled.Load(), callbackCalled.Load())
	}
}

func TestAgentFreezesStaticBoundsAtConstruction(t *testing.T) {
	allowed := []string{"Read"}
	denied := []string{"Bash"}
	provider := &scriptedProvider{}
	a := agent.New(agent.Options{
		ProviderClient:  provider,
		AllowedTools:    allowed,
		DisallowedTools: denied,
		PermissionMode:  types.PermissionModeBypassPermissions,
		MaxTurns:        1,
		SystemPrompt:    "test",
		SettingSources:  []string{},
	})
	defer a.Close()

	allowed[0] = "Bash"
	denied[0] = "Read"
	if _, err := a.Prompt(context.Background(), "inspect bounds"); err != nil {
		t.Fatal(err)
	}
	if !provider.sawTool("Read") || provider.sawTool("Bash") {
		t.Fatalf("model-visible tools changed after caller slice mutation: %v", provider.allToolNames())
	}
}

func TestChildAgentInheritsParentAndChildDenyBoundsInBypassMode(t *testing.T) {
	provider := newChildBoundsProvider([]string{"Bash", "Read"})
	a := agent.New(agent.Options{
		ProviderClient:  provider,
		BaseURL:         "://invalid",
		APIKey:          "test",
		DisallowedTools: []string{"Bash"},
		PermissionMode:  types.PermissionModeBypassPermissions,
		MaxTurns:        2,
		SystemPrompt:    "test",
		SettingSources:  []string{},
		Agents: map[string]agent.AgentDefinition{
			"restricted": {DisallowedTools: []string{"Read"}},
		},
	})
	defer a.Close()

	if _, err := a.Prompt(context.Background(), "delegate"); err != nil {
		t.Fatal(err)
	}
	if got := provider.childRequestCount(); got != 2 {
		t.Fatalf("child provider requests=%d, want 2; prompts=%v", got, provider.requestPrompts())
	}
	if provider.childSawTool("Bash") || provider.childSawTool("Read") {
		t.Fatalf("child schemas escaped deny union: %v", provider.childToolNames())
	}
	if got := provider.childDeniedToolResults(); got != 2 {
		t.Fatalf("child denied results=%d, want 2", got)
	}
}

func TestChildAgentIntersectsParentAllowBoundWithChildTools(t *testing.T) {
	provider := newChildBoundsProvider([]string{"Bash"})
	a := agent.New(agent.Options{
		ProviderClient: provider,
		BaseURL:        "://invalid",
		APIKey:         "test",
		AllowedTools:   []string{"Agent", "Read"},
		PermissionMode: types.PermissionModeBypassPermissions,
		MaxTurns:       2,
		SystemPrompt:   "test",
		SettingSources: []string{},
		Agents: map[string]agent.AgentDefinition{
			"restricted": {Tools: []string{"Read", "Bash"}},
		},
	})
	defer a.Close()

	if _, err := a.Prompt(context.Background(), "delegate"); err != nil {
		t.Fatal(err)
	}
	if got := provider.childRequestCount(); got != 2 {
		t.Fatalf("child provider requests=%d, want 2; prompts=%v", got, provider.requestPrompts())
	}
	if !provider.childSawTool("Read") || provider.childSawTool("Bash") {
		t.Fatalf("child schemas did not preserve allow intersection: %v", provider.childToolNames())
	}
	if got := provider.childDeniedToolResults(); got != 1 {
		t.Fatalf("child denied results=%d, want 1", got)
	}
}

type scriptedProvider struct {
	mu       sync.Mutex
	requests []api.MessagesRequest
	toolName string
}

func (p *scriptedProvider) CreateMessage(context.Context, api.MessagesRequest) (*api.StreamMessage, error) {
	return nil, errors.New("unexpected non-streaming request")
}

func (p *scriptedProvider) CreateMessageStream(_ context.Context, req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	call := len(p.requests)
	p.mu.Unlock()

	events := make(chan api.StreamEvent, 4)
	errs := make(chan error, 1)
	if call == 1 && p.toolName != "" {
		events <- api.StreamEvent{Type: "message_start", Message: &api.StreamMessage{Role: "assistant", Model: req.Model}}
		events <- api.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{
			Type:  types.ContentBlockToolUse,
			ID:    "call-1",
			Name:  p.toolName,
			Input: map[string]interface{}{},
		}}
		events <- api.StreamEvent{Type: "message_delta", Delta: map[string]interface{}{"stop_reason": "tool_use"}}
	} else {
		events <- api.StreamEvent{Type: "message_start", Message: &api.StreamMessage{Role: "assistant", Model: req.Model}}
		events <- api.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentBlockText, Text: "done"}}
		events <- api.StreamEvent{Type: "message_delta", Delta: map[string]interface{}{"stop_reason": "end_turn"}}
	}
	close(events)
	close(errs)
	return events, errs
}

func (p *scriptedProvider) sawTool(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, req := range p.requests {
		for _, tool := range req.Tools {
			if tool.Name == name {
				return true
			}
		}
	}
	return false
}

func (p *scriptedProvider) allToolNames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var names []string
	for _, req := range p.requests {
		for _, tool := range req.Tools {
			names = append(names, tool.Name)
		}
	}
	return names
}

type childBoundsProvider struct {
	mu             sync.Mutex
	requests       []api.MessagesRequest
	callsByPrompt  map[string]int
	childToolCalls []string
}

func newChildBoundsProvider(childToolCalls []string) *childBoundsProvider {
	return &childBoundsProvider{
		callsByPrompt:  make(map[string]int),
		childToolCalls: append([]string(nil), childToolCalls...),
	}
}

func (p *childBoundsProvider) CreateMessage(context.Context, api.MessagesRequest) (*api.StreamMessage, error) {
	return nil, errors.New("unexpected non-streaming request")
}

func (p *childBoundsProvider) CreateMessageStream(_ context.Context, req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
	prompt := firstUserText(req)
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.callsByPrompt[prompt]++
	call := p.callsByPrompt[prompt]
	p.mu.Unlock()

	var content []types.ContentBlock
	stopReason := "end_turn"
	switch {
	case prompt == "delegate" && call == 1:
		content = []types.ContentBlock{{
			Type: types.ContentBlockToolUse,
			ID:   "parent-agent-call",
			Name: "Agent",
			Input: map[string]interface{}{
				"prompt":        "child task",
				"description":   "permission child",
				"subagent_type": "restricted",
			},
		}}
		stopReason = "tool_use"
	case prompt == "child task" && call == 1:
		content = make([]types.ContentBlock, len(p.childToolCalls))
		for i, name := range p.childToolCalls {
			input := map[string]interface{}{}
			if name == "Bash" {
				input["command"] = "true"
			}
			if name == "Read" {
				input["file_path"] = "go.mod"
			}
			content[i] = types.ContentBlock{
				Type:  types.ContentBlockToolUse,
				ID:    "child-call-" + name,
				Name:  name,
				Input: input,
			}
		}
		stopReason = "tool_use"
	default:
		content = []types.ContentBlock{{Type: types.ContentBlockText, Text: "done"}}
	}

	events := make(chan api.StreamEvent, len(content)+2)
	errs := make(chan error, 1)
	events <- api.StreamEvent{Type: "message_start", Message: &api.StreamMessage{Role: "assistant", Model: req.Model}}
	for i := range content {
		block := content[i]
		events <- api.StreamEvent{Type: "content_block_start", Index: i, ContentBlock: &block}
	}
	events <- api.StreamEvent{Type: "message_delta", Delta: map[string]interface{}{"stop_reason": stopReason}}
	close(events)
	return events, errs
}

func (p *childBoundsProvider) childRequestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callsByPrompt["child task"]
}

func (p *childBoundsProvider) requestPrompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	prompts := make([]string, len(p.requests))
	for i := range p.requests {
		prompts[i] = firstUserText(p.requests[i])
	}
	return prompts
}

func (p *childBoundsProvider) childSawTool(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, req := range p.requests {
		if firstUserText(req) != "child task" {
			continue
		}
		for _, tool := range req.Tools {
			if tool.Name == name {
				return true
			}
		}
	}
	return false
}

func (p *childBoundsProvider) childToolNames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var names []string
	for _, req := range p.requests {
		if firstUserText(req) != "child task" {
			continue
		}
		for _, tool := range req.Tools {
			names = append(names, tool.Name)
		}
	}
	return names
}

func (p *childBoundsProvider) childDeniedToolResults() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, req := range p.requests {
		if firstUserText(req) != "child task" || p.callsByPrompt["child task"] < 2 {
			continue
		}
		denied := 0
		for _, msg := range req.Messages {
			for _, block := range msg.Content {
				if block.Type == types.ContentBlockToolResult && block.IsError {
					denied++
				}
			}
		}
		if denied > 0 {
			return denied
		}
	}
	return 0
}

func firstUserText(req api.MessagesRequest) string {
	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentBlockText {
				return block.Text
			}
		}
	}
	return ""
}

func toolNames(in []types.Tool) []string {
	names := make([]string, len(in))
	for i, tool := range in {
		names[i] = tool.Name()
	}
	return names
}
