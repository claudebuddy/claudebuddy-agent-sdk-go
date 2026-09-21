package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// ToolSearchTool searches for and returns tool schemas.
// Used for deferred tool discovery — tools announced by name but not
// loaded until explicitly searched for.
type ToolSearchTool struct {
	// Registry to search from.
	Registry *Registry
	// DeferredTools are tools announced but not included in the active tool set.
	DeferredTools []types.Tool
}

func NewToolSearchTool(registry *Registry, deferred []types.Tool) *ToolSearchTool {
	return &ToolSearchTool{Registry: registry, DeferredTools: deferred}
}

func (t *ToolSearchTool) Name() string { return "ToolSearch" }

func (t *ToolSearchTool) Description() string {
	return `Fetches full schema definitions for deferred tools so they can be called.

Query forms:
- "select:Read,Edit,Grep" — fetch these exact tools by name
- "keyword search" — keyword search, up to max_results best matches`
}

func (t *ToolSearchTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Query to find deferred tools. Use \"select:<tool_name>\" for direct selection, or keywords to search.",
			},
			"max_results": map[string]interface{}{
				"type":        "number",
				"description": "Maximum number of results to return (default: 5)",
				"default":     5,
			},
		},
		Required: []string{"query"},
	}
}

func (t *ToolSearchTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *ToolSearchTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *ToolSearchTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	query, _ := input["query"].(string)
	if query == "" {
		return errorResult("query is required"), nil
	}

	maxResults := 5
	if mr, ok := input["max_results"].(float64); ok {
		maxResults = int(mr)
	}

	var matched []types.Tool

	if strings.HasPrefix(query, "select:") {
		// Direct selection: "select:Read,Edit,Grep"
		names := strings.Split(strings.TrimPrefix(query, "select:"), ",")
		for _, name := range names {
			name = strings.TrimSpace(name)
			for _, tool := range t.DeferredTools {
				if tool.Name() == name {
					matched = append(matched, tool)
				}
			}
			// Also check registry
			if tool := t.Registry.Get(name); tool != nil {
				matched = append(matched, tool)
			}
		}
	} else {
		// Keyword search
		queryLower := strings.ToLower(query)
		for _, tool := range t.DeferredTools {
			nameLower := strings.ToLower(tool.Name())
			descLower := strings.ToLower(tool.Description())
			if strings.Contains(nameLower, queryLower) || strings.Contains(descLower, queryLower) {
				matched = append(matched, tool)
			}
		}
	}

	if len(matched) > maxResults {
		matched = matched[:maxResults]
	}

	if len(matched) == 0 {
		return &types.ToolResult{
			Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: "No matching tools found."}},
		}, nil
	}

	var sb strings.Builder
	for _, tool := range matched {
		sb.WriteString(fmt.Sprintf("## %s\n", tool.Name()))
		sb.WriteString(fmt.Sprintf("%s\n\n", tool.Description()))
	}

	return &types.ToolResult{
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: sb.String()}},
	}, nil
}
