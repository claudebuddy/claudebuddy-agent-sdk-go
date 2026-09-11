package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// SdkMcpTool defines a tool for the SDK MCP server.
type SdkMcpTool struct {
	Name        string
	Description string
	InputSchema types.ToolInputSchema
	Handler     func(ctx context.Context, input map[string]interface{}) (*types.ToolResult, error)
}

// SdkServer is an in-process MCP server that can be used within the agent.
type SdkServer struct {
	name    string
	version string
	tools   map[string]*SdkMcpTool
	mu      sync.RWMutex
}

// NewSdkServer creates a new in-process MCP server.
func NewSdkServer(name, version string) *SdkServer {
	return &SdkServer{
		name:    name,
		version: version,
		tools:   make(map[string]*SdkMcpTool),
	}
}

// RegisterTool adds a tool to the server.
func (s *SdkServer) RegisterTool(tool *SdkMcpTool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[tool.Name] = tool
}

// RemoveTool removes a tool from the server by name.
func (s *SdkServer) RemoveTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tools, name)
}

// ListTools returns all registered tools as MCP tool definitions.
func (s *SdkServer) ListTools() []types.MCPToolDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]types.MCPToolDefinition, 0, len(s.tools))
	for _, t := range s.tools {
		result = append(result, types.MCPToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return result
}

// CallTool invokes a registered tool by name and returns an MCP tool call result.
func (s *SdkServer) CallTool(ctx context.Context, name string, input map[string]interface{}) (*types.MCPToolCallResult, error) {
	s.mu.RLock()
	tool, ok := s.tools[name]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}

	result, err := tool.Handler(ctx, input)
	if err != nil {
		return &types.MCPToolCallResult{
			Content: []types.MCPContentBlock{
				{Type: "text", Text: err.Error()},
			},
			IsError: true,
		}, nil
	}

	// Convert ToolResult to MCPToolCallResult.
	mcpResult := &types.MCPToolCallResult{
		IsError: result.IsError,
	}

	if len(result.Content) > 0 {
		for _, cb := range result.Content {
			mcpResult.Content = append(mcpResult.Content, types.MCPContentBlock{
				Type: string(cb.Type),
				Text: cb.Text,
			})
		}
	} else if result.Data != nil {
		text := fmt.Sprintf("%v", result.Data)
		mcpResult.Content = []types.MCPContentBlock{
			{Type: "text", Text: text},
		}
	} else if result.Error != "" {
		mcpResult.Content = []types.MCPContentBlock{
			{Type: "text", Text: result.Error},
		}
		mcpResult.IsError = true
	}

	return mcpResult, nil
}

// jsonRPCRequest represents a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      interface{}      `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse represents a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// HandleRequest processes a JSON-RPC request for stdio/HTTP integration.
func (s *SdkServer) HandleRequest(ctx context.Context, req []byte) ([]byte, error) {
	var rpcReq jsonRPCRequest
	if err := json.Unmarshal(req, &rpcReq); err != nil {
		return json.Marshal(jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "parse error"},
		})
	}

	var resp jsonRPCResponse
	resp.JSONRPC = "2.0"
	resp.ID = rpcReq.ID

	switch rpcReq.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    s.name,
				"version": s.version,
			},
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
		}

	case "tools/list":
		tools := s.ListTools()
		resp.Result = map[string]interface{}{
			"tools": tools,
		}

	case "tools/call":
		if rpcReq.Params == nil {
			resp.Error = &rpcError{Code: -32602, Message: "missing params"}
			break
		}
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(*rpcReq.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid params"}
			break
		}
		result, err := s.CallTool(ctx, params.Name, params.Arguments)
		if err != nil {
			resp.Error = &rpcError{Code: -32603, Message: err.Error()}
			break
		}
		resp.Result = result

	default:
		resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("method %q not found", rpcReq.Method)}
	}

	return json.Marshal(resp)
}
