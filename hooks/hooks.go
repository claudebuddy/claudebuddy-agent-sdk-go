package hooks

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// HookEvent represents when a hook fires.
type HookEvent string

const (
	HookPreToolUse        HookEvent = "PreToolUse"
	HookPostToolUse       HookEvent = "PostToolUse"
	HookPostToolUseFailure HookEvent = "PostToolUseFailure"
	HookPostSampling      HookEvent = "PostSampling"
	HookStop              HookEvent = "Stop"
	HookUserPromptSubmit  HookEvent = "UserPromptSubmit"
	HookSubagentStart     HookEvent = "SubagentStart"
	HookSubagentStop      HookEvent = "SubagentStop"
	HookPreCompact        HookEvent = "PreCompact"
	HookNotification      HookEvent = "Notification"
	HookPermissionRequest HookEvent = "PermissionRequest"
)

// HookDecision represents the decision outcome from a hook.
type HookDecision string

const (
	HookDecisionAllow  HookDecision = "allow"
	HookDecisionBlock  HookDecision = "block"
	HookDecisionSkip   HookDecision = "skip"
	HookDecisionModify HookDecision = "modify"
)

// HookInput provides rich context to HookFnEx hooks.
type HookInput struct {
	// Event is the hook event type being fired.
	Event HookEvent `json:"event"`
	// ToolName is the tool being invoked (for tool-related hooks).
	ToolName string `json:"toolName,omitempty"`
	// Input is the tool input parameters (for tool-related hooks).
	Input map[string]interface{} `json:"input,omitempty"`
	// Output is the tool output (for PostToolUse and PostToolUseFailure).
	Output string `json:"output,omitempty"`
	// Error is the error from tool execution (for PostToolUseFailure).
	Error error `json:"-"`
	// AgentName is the subagent name (for SubagentStart/SubagentStop).
	AgentName string `json:"agentName,omitempty"`
	// UserPrompt is the user's prompt text (for UserPromptSubmit).
	UserPrompt string `json:"userPrompt,omitempty"`
	// NotificationMessage is the notification content (for Notification).
	NotificationMessage string `json:"notificationMessage,omitempty"`
	// PermissionTool is the tool requesting permission (for PermissionRequest).
	PermissionTool string `json:"permissionTool,omitempty"`
}

// HookOutput is the rich result from a HookFnEx hook.
type HookOutput struct {
	// Decision is the hook's decision: allow, block, skip, or modify.
	Decision HookDecision `json:"decision,omitempty"`
	// Reason explains why the decision was made.
	Reason string `json:"reason,omitempty"`
	// SuppressOutput suppresses showing the tool output to the user.
	SuppressOutput bool `json:"suppressOutput,omitempty"`
	// SystemMessage is an extra system message to inject into the conversation.
	SystemMessage string `json:"systemMessage,omitempty"`
	// UpdatedInput replaces the original tool input (for PreToolUse modify).
	UpdatedInput map[string]interface{} `json:"updatedInput,omitempty"`
	// AdditionalContext is extra context to pass along to the model.
	AdditionalContext string `json:"additionalContext,omitempty"`
}

// HookFn is the original hook function signature (kept for backward compatibility).
// Returns an error message to block the action, or empty string to allow.
type HookFn func(ctx context.Context, toolName string, input map[string]interface{}) (string, error)

// HookFnEx is the extended hook function that receives HookInput and returns HookOutput.
type HookFnEx func(ctx context.Context, input *HookInput) (*HookOutput, error)

// HookRule defines when a hook should fire.
type HookRule struct {
	// Matcher is a tool name pattern (e.g., "Bash", "Edit|Write", "*").
	// For non-tool hooks, use "*" to match all events.
	Matcher string `json:"matcher"`
	// Hooks are the legacy hook functions to run.
	Hooks []HookFn `json:"-"`
	// HooksEx are the extended hook functions to run.
	HooksEx []HookFnEx `json:"-"`
	// Timeout is an optional per-rule timeout. Zero means no timeout.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// HookConfig holds all hook definitions.
type HookConfig struct {
	PreToolUse         []HookRule `json:"PreToolUse,omitempty"`
	PostToolUse        []HookRule `json:"PostToolUse,omitempty"`
	PostToolUseFailure []HookRule `json:"PostToolUseFailure,omitempty"`
	PostSampling       []HookRule `json:"PostSampling,omitempty"`
	Stop               []HookRule `json:"Stop,omitempty"`
	UserPromptSubmit   []HookRule `json:"UserPromptSubmit,omitempty"`
	SubagentStart      []HookRule `json:"SubagentStart,omitempty"`
	SubagentStop       []HookRule `json:"SubagentStop,omitempty"`
	PreCompact         []HookRule `json:"PreCompact,omitempty"`
	Notification       []HookRule `json:"Notification,omitempty"`
	PermissionRequest  []HookRule `json:"PermissionRequest,omitempty"`
}

// HookProgress represents progress from a hook execution.
type HookProgress struct {
	Event         HookEvent `json:"event"`
	HookName      string    `json:"hookName"`
	ToolName      string    `json:"toolName"`
	StatusMessage string    `json:"statusMessage,omitempty"`
	Blocked       bool      `json:"blocked,omitempty"`
}

// HookResult is the result of running hooks.
type HookResult struct {
	Blocked  bool           `json:"blocked"`
	Message  string         `json:"message,omitempty"`
	Progress []HookProgress `json:"progress,omitempty"`
	// Output holds the rich output from the last HookFnEx that produced one.
	Output *HookOutput `json:"output,omitempty"`
}

// Manager handles hook execution.
type Manager struct {
	config HookConfig
}

// NewManager creates a new hook manager.
func NewManager(config HookConfig) *Manager {
	return &Manager{config: config}
}

// RunPreToolUse runs pre-tool-use hooks. Returns result with block status.
func (m *Manager) RunPreToolUse(ctx context.Context, toolName string, input map[string]interface{}) (*HookResult, error) {
	hookInput := &HookInput{Event: HookPreToolUse, ToolName: toolName, Input: input}
	return m.runHooks(ctx, HookPreToolUse, m.config.PreToolUse, toolName, input, hookInput)
}

// RunPostToolUse runs post-tool-use hooks.
func (m *Manager) RunPostToolUse(ctx context.Context, toolName string, input map[string]interface{}, output string) (*HookResult, error) {
	hookInput := &HookInput{Event: HookPostToolUse, ToolName: toolName, Input: input, Output: output}
	return m.runHooks(ctx, HookPostToolUse, m.config.PostToolUse, toolName, input, hookInput)
}

// RunPostToolUseFailure runs hooks after a tool fails.
func (m *Manager) RunPostToolUseFailure(ctx context.Context, toolName string, input map[string]interface{}, output string, toolErr error) (*HookResult, error) {
	hookInput := &HookInput{Event: HookPostToolUseFailure, ToolName: toolName, Input: input, Output: output, Error: toolErr}
	return m.runHooks(ctx, HookPostToolUseFailure, m.config.PostToolUseFailure, toolName, input, hookInput)
}

// RunPostSampling runs post-sampling hooks after API response.
func (m *Manager) RunPostSampling(ctx context.Context) (*HookResult, error) {
	hookInput := &HookInput{Event: HookPostSampling}
	return m.runHooks(ctx, HookPostSampling, m.config.PostSampling, "", nil, hookInput)
}

// RunStop runs stop hooks at end of conversation.
func (m *Manager) RunStop(ctx context.Context) (*HookResult, error) {
	hookInput := &HookInput{Event: HookStop}
	return m.runHooks(ctx, HookStop, m.config.Stop, "", nil, hookInput)
}

// RunUserPromptSubmit runs hooks when the user submits a prompt.
func (m *Manager) RunUserPromptSubmit(ctx context.Context, prompt string) (*HookResult, error) {
	hookInput := &HookInput{Event: HookUserPromptSubmit, UserPrompt: prompt}
	return m.runHooks(ctx, HookUserPromptSubmit, m.config.UserPromptSubmit, "", nil, hookInput)
}

// RunSubagentStart runs hooks when a subagent starts.
func (m *Manager) RunSubagentStart(ctx context.Context, agentName string) (*HookResult, error) {
	hookInput := &HookInput{Event: HookSubagentStart, AgentName: agentName}
	return m.runHooks(ctx, HookSubagentStart, m.config.SubagentStart, "", nil, hookInput)
}

// RunSubagentStop runs hooks when a subagent stops.
func (m *Manager) RunSubagentStop(ctx context.Context, agentName string) (*HookResult, error) {
	hookInput := &HookInput{Event: HookSubagentStop, AgentName: agentName}
	return m.runHooks(ctx, HookSubagentStop, m.config.SubagentStop, "", nil, hookInput)
}

// RunPreCompact runs hooks before context compaction.
func (m *Manager) RunPreCompact(ctx context.Context) (*HookResult, error) {
	hookInput := &HookInput{Event: HookPreCompact}
	return m.runHooks(ctx, HookPreCompact, m.config.PreCompact, "", nil, hookInput)
}

// RunNotification runs hooks when a notification is sent.
func (m *Manager) RunNotification(ctx context.Context, message string) (*HookResult, error) {
	hookInput := &HookInput{Event: HookNotification, NotificationMessage: message}
	return m.runHooks(ctx, HookNotification, m.config.Notification, "", nil, hookInput)
}

// RunPermissionRequest runs hooks when a tool requests permission.
func (m *Manager) RunPermissionRequest(ctx context.Context, toolName string, input map[string]interface{}) (*HookResult, error) {
	hookInput := &HookInput{Event: HookPermissionRequest, ToolName: toolName, Input: input, PermissionTool: toolName}
	return m.runHooks(ctx, HookPermissionRequest, m.config.PermissionRequest, toolName, input, hookInput)
}

func (m *Manager) runHooks(ctx context.Context, event HookEvent, rules []HookRule, toolName string, input map[string]interface{}, hookInput *HookInput) (*HookResult, error) {
	result := &HookResult{}

	for _, rule := range rules {
		if toolName != "" && !matchesTool(rule.Matcher, toolName) {
			continue
		}

		// Apply per-rule timeout if configured.
		hookCtx := ctx
		if rule.Timeout > 0 {
			var cancel context.CancelFunc
			hookCtx, cancel = context.WithTimeout(ctx, rule.Timeout)
			defer cancel()
		}

		// Run legacy HookFn hooks.
		for i, hook := range rule.Hooks {
			progress := HookProgress{
				Event:    event,
				HookName: fmt.Sprintf("%s_hook_%d", rule.Matcher, i),
				ToolName: toolName,
			}

			msg, err := hook(hookCtx, toolName, input)
			if err != nil {
				return nil, fmt.Errorf("hook error (%s): %w", progress.HookName, err)
			}
			if msg != "" {
				progress.Blocked = true
				progress.StatusMessage = msg
				result.Blocked = true
				result.Message = msg
			}
			result.Progress = append(result.Progress, progress)

			if result.Blocked {
				return result, nil
			}
		}

		// Run extended HookFnEx hooks.
		for i, hook := range rule.HooksEx {
			progress := HookProgress{
				Event:    event,
				HookName: fmt.Sprintf("%s_hookex_%d", rule.Matcher, i),
				ToolName: toolName,
			}

			output, err := hook(hookCtx, hookInput)
			if err != nil {
				return nil, fmt.Errorf("hook error (%s): %w", progress.HookName, err)
			}
			if output != nil {
				result.Output = output
				if output.Decision == HookDecisionBlock {
					progress.Blocked = true
					progress.StatusMessage = output.Reason
					result.Blocked = true
					result.Message = output.Reason
				}
			}
			result.Progress = append(result.Progress, progress)

			if result.Blocked {
				return result, nil
			}
		}
	}
	return result, nil
}

// HasHooks returns true if any hooks are configured.
func (m *Manager) HasHooks() bool {
	return len(m.config.PreToolUse) > 0 ||
		len(m.config.PostToolUse) > 0 ||
		len(m.config.PostToolUseFailure) > 0 ||
		len(m.config.PostSampling) > 0 ||
		len(m.config.Stop) > 0 ||
		len(m.config.UserPromptSubmit) > 0 ||
		len(m.config.SubagentStart) > 0 ||
		len(m.config.SubagentStop) > 0 ||
		len(m.config.PreCompact) > 0 ||
		len(m.config.Notification) > 0 ||
		len(m.config.PermissionRequest) > 0
}

// GetConfig returns the current hook configuration.
func (m *Manager) GetConfig() HookConfig {
	return m.config
}

// matchesTool checks if a matcher pattern matches a tool name.
func matchesTool(matcher, toolName string) bool {
	if matcher == "*" {
		return true
	}

	parts := strings.Split(matcher, "|")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == toolName {
			return true
		}
		if strings.Contains(part, "*") {
			if strings.HasPrefix(part, "*") && strings.HasSuffix(toolName, strings.TrimPrefix(part, "*")) {
				return true
			}
			if strings.HasSuffix(part, "*") && strings.HasPrefix(toolName, strings.TrimSuffix(part, "*")) {
				return true
			}
		}
		// MCP prefix matching: "mcp__server" matches "mcp__server__tool"
		if strings.HasPrefix(part, "mcp__") && strings.HasPrefix(toolName, part+"__") {
			return true
		}
	}

	return false
}
