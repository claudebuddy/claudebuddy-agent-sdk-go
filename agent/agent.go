package agent

import (
	"context"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/costtracker"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/hooks"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/mcp"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/permissions"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

const (
	defaultMaxTurns = 10
)

// ThinkingType represents the type of thinking configuration.
type ThinkingType string

const (
	// ThinkingAdaptive allows the model to decide when to think.
	ThinkingAdaptive ThinkingType = "adaptive"
	// ThinkingEnabled forces extended thinking on every request.
	ThinkingEnabled ThinkingType = "enabled"
	// ThinkingDisabled disables extended thinking.
	ThinkingDisabled ThinkingType = "disabled"
)

// ThinkingConfig configures extended thinking.
type ThinkingConfig struct {
	// Type controls the thinking mode: "adaptive", "enabled", or "disabled".
	Type ThinkingType `json:"type"`
	// BudgetTokens is the max number of thinking tokens (only for "enabled" type).
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

// Effort controls the reasoning effort level.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortMax    Effort = "max"
)

// Options configures an Agent.
type Options struct {
	// ProviderClient overrides the default API client.
	ProviderClient api.MessageProvider

	// Model ID (e.g. "sonnet-4-6")
	Model string

	// API key
	APIKey string

	// API base URL override
	BaseURL string

	// API provider: "anthropic" or "openai" (auto-detected if empty)
	Provider string

	// Working directory for tools
	CWD string

	// System prompt override
	SystemPrompt string

	// Append to default system prompt
	AppendSystemPrompt string

	// Maximum agentic turns per query
	MaxTurns int

	// Maximum USD budget per query
	MaxBudgetUSD float64

	// Permission mode
	PermissionMode types.PermissionMode

	// Tool names to pre-approve
	AllowedTools []string

	// Permission handler callback
	CanUseTool types.CanUseToolFn

	// MCP server configurations
	MCPServers map[string]types.MCPServerConfig

	// Custom tools to add
	CustomTools []types.Tool

	// Hook configuration
	Hooks hooks.HookConfig

	// Environment variables (for API key, model, etc.)
	Env map[string]string

	// Extended thinking configuration
	Thinking *ThinkingConfig

	// Effort level for automatic thinking configuration
	Effort Effort

	// FallbackModel to use if the primary model fails
	FallbackModel string

	// DisallowedTools are tool names to deny
	DisallowedTools []string

	// Betas are beta feature flags to enable
	Betas []string

	// SettingSources specifies setting sources: "user", "project", "local"
	SettingSources []string

	// EnableFileCheckpointing enables file state tracking
	EnableFileCheckpointing bool

	// Structured output JSON schema name and schema
	JSONSchema map[string]interface{}

	// Custom HTTP headers
	CustomHeaders map[string]string

	// Proxy URL for API requests
	ProxyURL string

	// API timeout in milliseconds
	TimeoutMs int

	// Subagent definitions
	Agents map[string]AgentDefinition
}

// AgentDefinition defines a subagent configuration.
type AgentDefinition struct {
	Description     string                           `json:"description"`
	Instructions    string                           `json:"instructions"`
	Tools           []string                         `json:"tools,omitempty"`
	DisallowedTools []string                         `json:"disallowedTools,omitempty"`
	Model           string                           `json:"model,omitempty"`
	Skills          []string                         `json:"skills,omitempty"`
	Memory          string                           `json:"memory,omitempty"`
	Effort          Effort                           `json:"effort,omitempty"`
	MaxTurns        int                              `json:"maxTurns,omitempty"`
	Background      bool                             `json:"background,omitempty"`
	PermissionMode  types.PermissionMode             `json:"permissionMode,omitempty"`
	MCPServers      map[string]types.MCPServerConfig `json:"mcpServers,omitempty"`
	InitialPrompt   string                           `json:"initialPrompt,omitempty"`
}

// Agent is the main agent that runs the agentic loop.
type Agent struct {
	opts         Options
	provider     api.MessageProvider
	initErr      error
	toolRegistry *tools.Registry
	mcpClient    *mcp.Client
	costTracker  *costtracker.Tracker
	hookManager  *hooks.Manager
	canUseTool   types.CanUseToolFn
	messages     []types.Message
	sessionID    string
}

// New creates a new Agent.
func New(opts Options) *Agent {
	resolveEnvOptions(&opts)
	opts.AllowedTools = cloneStringSlice(opts.AllowedTools)
	opts.DisallowedTools = cloneStringSlice(opts.DisallowedTools)
	opts.Agents = cloneAgentDefinitions(opts.Agents)
	initErr := opts.Validate()

	sessionID := uuid.New().String()

	provider := opts.ProviderClient
	if provider == nil {
		provider = api.NewClient(api.ClientConfig{
			APIKey:        opts.APIKey,
			BaseURL:       opts.BaseURL,
			Model:         opts.Model,
			Provider:      api.Provider(opts.Provider),
			CustomHeaders: opts.CustomHeaders,
			ProxyURL:      opts.ProxyURL,
			TimeoutMs:     opts.TimeoutMs,
		})
	}

	registry := tools.DefaultRegistry()
	for _, t := range opts.CustomTools {
		registry.Register(t)
	}

	permConfig := &permissions.Config{Mode: opts.PermissionMode}
	if permConfig.Mode == "" {
		permConfig.Mode = types.PermissionModeBypassPermissions
	}
	canUseTool := permissions.NewPolicy(permConfig, opts.AllowedTools, opts.DisallowedTools, opts.CanUseTool)

	hookManager := hooks.NewManager(opts.Hooks)

	a := &Agent{
		opts:         opts,
		provider:     provider,
		initErr:      initErr,
		toolRegistry: registry,
		mcpClient:    mcp.NewClient(),
		costTracker:  costtracker.NewTracker(sessionID),
		hookManager:  hookManager,
		canUseTool:   canUseTool,
		sessionID:    sessionID,
	}

	// Register AgentTool with subagent spawner if definitions provided
	if len(opts.Agents) > 0 {
		defs := make(map[string]tools.SubagentDefinition, len(opts.Agents))
		for name, def := range opts.Agents {
			defs[name] = tools.SubagentDefinition{
				Description:  def.Description,
				Instructions: def.Instructions,
				Tools:        cloneStringSlice(def.Tools),
				Model:        def.Model,
			}
		}
		agentTool := tools.NewAgentTool(defs, a.spawnSubagent)
		registry.Register(agentTool)
	} else {
		// Register with default agent types even if none configured
		defaultDefs := map[string]tools.SubagentDefinition{
			"general-purpose": {Description: "General-purpose agent for complex multi-step tasks"},
			"explore":         {Description: "Fast agent for codebase exploration and search"},
			"plan":            {Description: "Planning agent for designing implementation strategies"},
		}
		agentTool := tools.NewAgentTool(defaultDefs, a.spawnSubagent)
		registry.Register(agentTool)
	}

	return a
}

// Init performs async initialization (MCP connections, etc.)
func (a *Agent) Init(ctx context.Context) error {
	if a.opts.MCPServers == nil {
		return nil
	}

	for name, config := range a.opts.MCPServers {
		conn, err := a.mcpClient.ConnectServer(ctx, name, config)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Failed to connect to %q: %v\n", name, err)
			continue
		}

		mcpTools := mcp.ToolsFromConnection(conn)
		for _, t := range mcpTools {
			a.toolRegistry.Register(t)
		}
	}

	return nil
}

// QueryResult is the final result of a query.
type QueryResult struct {
	Text     string          `json:"text"`
	Usage    types.Usage     `json:"usage"`
	NumTurns int             `json:"num_turns"`
	Duration time.Duration   `json:"duration"`
	Messages []types.Message `json:"messages"`
	Cost     float64         `json:"cost"`
}

// Query runs the agentic loop with streaming events.
func (a *Agent) Query(ctx context.Context, prompt string) (<-chan types.SDKMessage, <-chan error) {
	eventCh := make(chan types.SDKMessage, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(eventCh)
		defer close(errCh)
		if a.initErr != nil {
			errCh <- a.initErr
			return
		}

		err := a.runLoop(ctx, prompt, eventCh)
		if err != nil {
			errCh <- err
		}
	}()

	return eventCh, errCh
}

// Validate checks whether Options contains supported values.
func (o Options) Validate() error {
	if o.MaxTurns < 0 {
		return fmt.Errorf("%w: max turns must be positive", ErrInvalidOptions)
	}
	if math.IsNaN(o.MaxBudgetUSD) || math.IsInf(o.MaxBudgetUSD, 0) || o.MaxBudgetUSD < 0 {
		return fmt.Errorf("%w: max budget must be finite and non-negative", ErrInvalidOptions)
	}
	if o.TimeoutMs < 0 {
		return fmt.Errorf("%w: timeout must be positive when specified", ErrInvalidOptions)
	}
	return validatePermissionMode(o.PermissionMode)
}

func validatePermissionMode(mode types.PermissionMode) error {
	switch mode {
	case "",
		types.PermissionModeDefault,
		types.PermissionModeAcceptEdits,
		types.PermissionModeBypassPermissions,
		types.PermissionModePlan,
		types.PermissionModeDontAsk:
		return nil
	default:
		return fmt.Errorf("%w: unsupported permission mode %q", ErrInvalidOptions, mode)
	}
}

// Prompt runs a query and returns the final result (blocking).
func (a *Agent) Prompt(ctx context.Context, prompt string) (*QueryResult, error) {
	start := time.Now()
	eventCh, errCh := a.Query(ctx, prompt)

	var result QueryResult
	var lastAssistantText string

	for event := range eventCh {
		switch event.Type {
		case types.MessageTypeAssistant:
			if event.Message != nil {
				if text := types.ExtractText(event.Message); text != "" {
					lastAssistantText = text
				}
			}
		case types.MessageTypeResult:
			if event.Usage != nil {
				result.Usage = *event.Usage
			}
			result.NumTurns = event.NumTurns
			result.Cost = event.Cost
		}
	}

	select {
	case err := <-errCh:
		if err != nil {
			return nil, err
		}
	default:
	}

	result.Text = lastAssistantText
	result.Duration = time.Since(start)
	result.Messages = append([]types.Message{}, a.messages...)

	return &result, nil
}

// GetMessages returns conversation history.
func (a *Agent) GetMessages() []types.Message {
	return append([]types.Message{}, a.messages...)
}

// Clear resets conversation history.
func (a *Agent) Clear() {
	a.messages = nil
}

// Close cleans up resources.
func (a *Agent) Close() {
	a.mcpClient.Close()
}

// spawnSubagent creates a child agent and runs a prompt synchronously.
func (a *Agent) spawnSubagent(ctx context.Context, config tools.SubagentConfig) (string, error) {
	model := config.Model
	if model == "" {
		model = a.opts.Model
	}
	var childDeniedTools []string
	if definition, ok := a.opts.Agents[config.Name]; ok {
		childDeniedTools = definition.DisallowedTools
	}

	childOpts := Options{
		ProviderClient:  a.provider,
		Model:           model,
		APIKey:          a.opts.APIKey,
		BaseURL:         a.opts.BaseURL,
		CWD:             config.CWD,
		MaxTurns:        30,
		PermissionMode:  a.opts.PermissionMode,
		AllowedTools:    intersectToolBounds(a.opts.AllowedTools, config.Tools),
		DisallowedTools: unionToolBounds(a.opts.DisallowedTools, childDeniedTools),
		SystemPrompt:    config.SystemPrompt,
		CustomHeaders:   a.opts.CustomHeaders,
		ProxyURL:        a.opts.ProxyURL,
		TimeoutMs:       a.opts.TimeoutMs,
	}

	if childOpts.CWD == "" {
		childOpts.CWD = a.opts.CWD
	}

	// Use parent's permission callback if available
	if a.opts.CanUseTool != nil {
		childOpts.CanUseTool = a.opts.CanUseTool
	} else {
		childOpts.PermissionMode = a.opts.PermissionMode
	}

	child := New(childOpts)
	defer child.Close()

	if err := child.Init(ctx); err != nil {
		return "", fmt.Errorf("subagent init failed: %w", err)
	}

	result, err := child.Prompt(ctx, config.Prompt)
	if err != nil {
		return "", err
	}

	// Merge cost into parent tracker
	if child.costTracker != nil && a.costTracker != nil {
		childIn, childOut := child.costTracker.TotalTokens()
		a.costTracker.AddUsage(model, &types.Usage{
			InputTokens:  childIn,
			OutputTokens: childOut,
		})
	}

	return result.Text, nil
}

// SessionID returns the current session ID.
func (a *Agent) SessionID() string {
	return a.sessionID
}

// CostTracker returns the cost tracker.
func (a *Agent) CostTracker() *costtracker.Tracker {
	return a.costTracker
}

// MCPClient returns the MCP client for managing MCP server connections.
func (a *Agent) MCPClient() *mcp.Client {
	return a.mcpClient
}

// envFirst returns the first non-empty value from the env map or os env,
// trying CLAUDEBUDDY_ prefix first, then ANTHROPIC_ for compatibility.
func envFirst(env map[string]string, keys ...string) string {
	for _, key := range keys {
		if env != nil {
			if v := env[key]; v != "" {
				return v
			}
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func resolveEnvOptions(opts *Options) {
	env := opts.Env

	if opts.APIKey == "" {
		opts.APIKey = envFirst(env, "CLAUDEBUDDY_API_KEY", "ANTHROPIC_API_KEY", "CLAUDEBUDDY_AUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN")
	}

	if opts.BaseURL == "" {
		opts.BaseURL = envFirst(env, "CLAUDEBUDDY_BASE_URL", "ANTHROPIC_BASE_URL")
	}

	if opts.Model == "" {
		opts.Model = envFirst(env, "CLAUDEBUDDY_MODEL", "ANTHROPIC_MODEL")
		if opts.Model == "" {
			opts.Model = "sonnet-4-6"
		}
	}

	if opts.CWD == "" {
		opts.CWD, _ = os.Getwd()
	}

	if opts.MaxTurns == 0 {
		opts.MaxTurns = defaultMaxTurns
	}
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneAgentDefinitions(definitions map[string]AgentDefinition) map[string]AgentDefinition {
	if definitions == nil {
		return nil
	}
	cloned := make(map[string]AgentDefinition, len(definitions))
	for name, definition := range definitions {
		definition.Tools = cloneStringSlice(definition.Tools)
		definition.DisallowedTools = cloneStringSlice(definition.DisallowedTools)
		definition.Skills = cloneStringSlice(definition.Skills)
		definition.MCPServers = cloneMCPServerConfigs(definition.MCPServers)
		cloned[name] = definition
	}
	return cloned
}

func cloneMCPServerConfigs(configs map[string]types.MCPServerConfig) map[string]types.MCPServerConfig {
	if configs == nil {
		return nil
	}
	cloned := make(map[string]types.MCPServerConfig, len(configs))
	for name, config := range configs {
		config.Args = cloneStringSlice(config.Args)
		config.Env = cloneStringMap(config.Env)
		config.Headers = cloneStringMap(config.Headers)
		cloned[name] = config
	}
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func intersectToolBounds(parent, child []string) []string {
	if parent == nil {
		return cloneStringSlice(child)
	}
	if child == nil {
		return cloneStringSlice(parent)
	}

	childSet := make(map[string]bool, len(child))
	for _, name := range child {
		childSet[name] = true
	}
	intersection := make([]string, 0)
	for _, name := range parent {
		if childSet[name] {
			intersection = append(intersection, name)
		}
	}
	return intersection
}

func unionToolBounds(parent, child []string) []string {
	if parent == nil && child == nil {
		return nil
	}

	union := make([]string, 0, len(parent)+len(child))
	seen := make(map[string]bool, len(parent)+len(child))
	for _, bounds := range [][]string{parent, child} {
		for _, name := range bounds {
			if seen[name] {
				continue
			}
			seen[name] = true
			union = append(union, name)
		}
	}
	return union
}
