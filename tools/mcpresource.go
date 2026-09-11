package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/mcp"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// ListMcpResourcesTool lists available resources from connected MCP servers.
type ListMcpResourcesTool struct{ Client *mcp.Client }

// NewListMcpResourcesTool creates a new ListMcpResourcesTool.
func NewListMcpResourcesTool(client *mcp.Client) *ListMcpResourcesTool {
	return &ListMcpResourcesTool{Client: client}
}

func (t *ListMcpResourcesTool) Name() string { return "ListMcpResources" }
func (t *ListMcpResourcesTool) Description() string {
	return "List available resources from connected MCP servers."
}
func (t *ListMcpResourcesTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"server": map[string]interface{}{"type": "string", "description": "Filter by MCP server name"},
		},
	}
}
func (t *ListMcpResourcesTool) IsConcurrencySafe(_ map[string]interface{}) bool { return true }
func (t *ListMcpResourcesTool) IsReadOnly(_ map[string]interface{}) bool        { return true }

func (t *ListMcpResourcesTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	if t.Client == nil {
		return textResult("No MCP servers connected."), nil
	}
	filterServer, _ := input["server"].(string)
	connections := t.Client.AllConnections()
	var lines []string
	for _, conn := range connections {
		if filterServer != "" && conn.Name != filterServer {
			continue
		}
		resources, err := conn.ListResources(ctx)
		if err != nil {
			lines = append(lines, fmt.Sprintf("Server: %s (resource listing not supported)", conn.Name))
			continue
		}
		lines = append(lines, fmt.Sprintf("Server: %s", conn.Name))
		for _, r := range resources {
			desc := r.Description
			if desc == "" {
				desc = r.MimeType
			}
			lines = append(lines, fmt.Sprintf("  - %s (%s): %s", r.Name, r.URI, desc))
		}
	}
	if len(lines) == 0 {
		return textResult("No resources found."), nil
	}
	return textResult(strings.Join(lines, "\n")), nil
}

// ReadMcpResourceTool reads a specific resource from an MCP server.
type ReadMcpResourceTool struct{ Client *mcp.Client }

// NewReadMcpResourceTool creates a new ReadMcpResourceTool.
func NewReadMcpResourceTool(client *mcp.Client) *ReadMcpResourceTool {
	return &ReadMcpResourceTool{Client: client}
}

func (t *ReadMcpResourceTool) Name() string { return "ReadMcpResource" }
func (t *ReadMcpResourceTool) Description() string {
	return "Read a specific resource from an MCP server."
}
func (t *ReadMcpResourceTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"server": map[string]interface{}{"type": "string", "description": "MCP server name"},
			"uri":    map[string]interface{}{"type": "string", "description": "Resource URI to read"},
		},
		Required: []string{"server", "uri"},
	}
}
func (t *ReadMcpResourceTool) IsConcurrencySafe(_ map[string]interface{}) bool { return true }
func (t *ReadMcpResourceTool) IsReadOnly(_ map[string]interface{}) bool        { return true }

func (t *ReadMcpResourceTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	if t.Client == nil {
		return errorResult("No MCP client configured."), nil
	}
	server, _ := input["server"].(string)
	uri, _ := input["uri"].(string)
	conn := t.Client.GetConnection(server)
	if conn == nil {
		return errorResult(fmt.Sprintf("MCP server not found: %s", server)), nil
	}
	contents, err := conn.ReadResource(ctx, uri)
	if err != nil {
		return errorResult(fmt.Sprintf("Error reading resource: %s", err)), nil
	}
	var parts []string
	for _, c := range contents {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) == 0 {
		return textResult("(empty resource)"), nil
	}
	return textResult(strings.Join(parts, "\n")), nil
}
