package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// AgentSpawner is a function that creates and runs a subagent.
// It should be set by the agent package to avoid circular imports.
type AgentSpawner func(ctx context.Context, config SubagentConfig) (string, error)

// SubagentConfig defines a subagent to spawn.
type SubagentConfig struct {
	Name         string
	Description  string
	Prompt       string
	Tools        []string
	Model        string
	SystemPrompt string
	CWD          string
}

// AgentTool spawns subagents for parallel task execution.
type AgentTool struct {
	// Spawner creates and runs a subagent, returning its text output.
	Spawner AgentSpawner

	// Definitions maps agent names to their configurations.
	Definitions map[string]SubagentDefinition

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// SubagentDefinition defines a preconfigured subagent.
type SubagentDefinition struct {
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Tools        []string `json:"tools,omitempty"`
	Model        string   `json:"model,omitempty"`
}

func NewAgentTool(definitions map[string]SubagentDefinition, spawner AgentSpawner) *AgentTool {
	return &AgentTool{
		Spawner:     spawner,
		Definitions: definitions,
		running:     make(map[string]context.CancelFunc),
	}
}

func (t *AgentTool) Name() string { return "Agent" }

func (t *AgentTool) Description() string {
	var sb strings.Builder
	sb.WriteString("Launch a new agent to handle complex, multi-step tasks autonomously.\n\n")
	sb.WriteString("Available agent types:\n")
	for name, def := range t.Definitions {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, def.Description))
	}
	return sb.String()
}

func (t *AgentTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The task for the agent to perform",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "A short (3-5 word) description of the task",
			},
			"subagent_type": map[string]interface{}{
				"type":        "string",
				"description": "The type of specialized agent to use",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "Optional model override for this agent",
			},
		},
		Required: []string{"description", "prompt"},
	}
}

func (t *AgentTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *AgentTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *AgentTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	prompt, _ := input["prompt"].(string)
	if prompt == "" {
		return errorResult("prompt is required"), nil
	}

	agentType, _ := input["subagent_type"].(string)
	model, _ := input["model"].(string)

	if t.Spawner == nil {
		return errorResult("Agent spawning is not configured"), nil
	}

	// Find agent definition
	config := SubagentConfig{
		Prompt: prompt,
		CWD:    tCtx.WorkingDir,
	}

	if agentType != "" {
		if def, ok := t.Definitions[agentType]; ok {
			config.Name = agentType
			config.Description = def.Description
			config.SystemPrompt = def.Instructions
			config.Tools = def.Tools
			if def.Model != "" {
				config.Model = def.Model
			}
		}
	}

	if model != "" {
		config.Model = model
	}

	// Run subagent
	result, err := t.Spawner(ctx, config)
	if err != nil {
		return &types.ToolResult{
			IsError: true,
			Error:   fmt.Sprintf("Agent failed: %v", err),
			Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: fmt.Sprintf("Error: %v", err)}},
		}, nil
	}

	return &types.ToolResult{
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: result}},
	}, nil
}

// Stop cancels a running subagent.
func (t *AgentTool) Stop(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cancel, ok := t.running[name]; ok {
		cancel()
		delete(t.running, name)
	}
}
