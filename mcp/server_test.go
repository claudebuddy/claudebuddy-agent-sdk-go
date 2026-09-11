package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func TestNewSdkServer(t *testing.T) {
	s := NewSdkServer("test-server", "1.0.0")
	if s == nil {
		t.Fatal("expected non-nil server")
	}
	if s.name != "test-server" {
		t.Errorf("expected name %q, got %q", "test-server", s.name)
	}
	if s.version != "1.0.0" {
		t.Errorf("expected version %q, got %q", "1.0.0", s.version)
	}
}

func TestRegisterAndListTools(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	s.RegisterTool(&SdkMcpTool{
		Name:        "echo",
		Description: "echoes input",
		InputSchema: types.ToolInputSchema{Type: "object"},
		Handler: func(ctx context.Context, input map[string]interface{}) (*types.ToolResult, error) {
			return &types.ToolResult{Data: input}, nil
		},
	})

	tools := s.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("expected tool name %q, got %q", "echo", tools[0].Name)
	}
}

func TestRemoveTool(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	s.RegisterTool(&SdkMcpTool{Name: "temp", Handler: func(ctx context.Context, input map[string]interface{}) (*types.ToolResult, error) {
		return &types.ToolResult{}, nil
	}})
	s.RemoveTool("temp")

	if len(s.ListTools()) != 0 {
		t.Error("expected 0 tools after removal")
	}
}

func TestCallTool(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	s.RegisterTool(&SdkMcpTool{
		Name: "greet",
		Handler: func(ctx context.Context, input map[string]interface{}) (*types.ToolResult, error) {
			name, _ := input["name"].(string)
			return &types.ToolResult{Data: "hello " + name}, nil
		},
	})

	result, err := s.CallTool(context.Background(), "greet", map[string]interface{}{"name": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected non-error result")
	}
	if len(result.Content) == 0 || result.Content[0].Text != "hello world" {
		t.Errorf("unexpected result content: %+v", result.Content)
	}
}

func TestCallToolNotFound(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	_, err := s.CallTool(context.Background(), "missing", nil)
	if err == nil {
		t.Error("expected error for missing tool")
	}
}

func TestCallToolHandlerError(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	s.RegisterTool(&SdkMcpTool{
		Name: "fail",
		Handler: func(ctx context.Context, input map[string]interface{}) (*types.ToolResult, error) {
			return nil, fmt.Errorf("something went wrong")
		},
	})

	result, err := s.CallTool(context.Background(), "fail", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result")
	}
}

func TestHandleRequestInitialize(t *testing.T) {
	s := NewSdkServer("myserver", "2.0")
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	resp, err := s.HandleRequest(context.Background(), []byte(req))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(resp, &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Errorf("unexpected error in response: %+v", rpcResp.Error)
	}
}

func TestHandleRequestToolsList(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	s.RegisterTool(&SdkMcpTool{
		Name:        "ping",
		Description: "pings",
		InputSchema: types.ToolInputSchema{Type: "object"},
		Handler: func(ctx context.Context, input map[string]interface{}) (*types.ToolResult, error) {
			return &types.ToolResult{Data: "pong"}, nil
		},
	})

	req := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	resp, err := s.HandleRequest(context.Background(), []byte(req))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(resp, &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Errorf("unexpected error: %+v", rpcResp.Error)
	}
}

func TestHandleRequestToolsCall(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	s.RegisterTool(&SdkMcpTool{
		Name: "add",
		Handler: func(ctx context.Context, input map[string]interface{}) (*types.ToolResult, error) {
			a, _ := input["a"].(float64)
			b, _ := input["b"].(float64)
			return &types.ToolResult{Data: a + b}, nil
		},
	})

	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":2}}}`
	resp, err := s.HandleRequest(context.Background(), []byte(req))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(resp, &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Errorf("unexpected error: %+v", rpcResp.Error)
	}
}

func TestHandleRequestUnknownMethod(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	req := `{"jsonrpc":"2.0","id":4,"method":"unknown"}`
	resp, err := s.HandleRequest(context.Background(), []byte(req))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(resp, &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if rpcResp.Error == nil {
		t.Error("expected error for unknown method")
	}
}

func TestHandleRequestInvalidJSON(t *testing.T) {
	s := NewSdkServer("test", "1.0")
	resp, err := s.HandleRequest(context.Background(), []byte("not json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(resp, &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32700 {
		t.Error("expected parse error")
	}
}
