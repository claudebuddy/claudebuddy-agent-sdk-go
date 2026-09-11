package tools

import (
	"context"
	"testing"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func TestListMcpResourcesTool(t *testing.T) {
	tool := NewListMcpResourcesTool(nil)
	r, err := tool.Call(context.Background(), map[string]interface{}{}, &types.ToolUseContext{})
	if err != nil || r.IsError {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Content[0].Text == "" {
		t.Error("expected non-empty response")
	}
}

func TestReadMcpResourceTool(t *testing.T) {
	tool := NewReadMcpResourceTool(nil)
	r, err := tool.Call(context.Background(), map[string]interface{}{
		"server": "test", "uri": "file:///test",
	}, &types.ToolUseContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// nil client → error result expected
	if !r.IsError {
		t.Error("expected error when client is nil")
	}
}
