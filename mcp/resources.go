package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Resource represents an MCP server resource.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Server      string `json:"server,omitempty"`
}

// ResourceContent is the content of a resource.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64
}

// ListResources fetches available resources from the server.
func (conn *Connection) ListResources(ctx context.Context) ([]Resource, error) {
	result, err := conn.sendRequest(ctx, "resources/list", nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Resources []Resource `json:"resources"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal resources: %w", err)
	}

	// Tag with server name
	for i := range resp.Resources {
		resp.Resources[i].Server = conn.Name
	}

	return resp.Resources, nil
}

// ReadResource reads a resource's content.
func (conn *Connection) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	result, err := conn.sendRequest(ctx, "resources/read", map[string]interface{}{
		"uri": uri,
	})
	if err != nil {
		return nil, err
	}

	var resp struct {
		Contents []ResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal resource content: %w", err)
	}

	return resp.Contents, nil
}

// ListPrompts fetches available prompts from the server.
func (conn *Connection) ListPrompts(ctx context.Context) ([]PromptDefinition, error) {
	result, err := conn.sendRequest(ctx, "prompts/list", nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Prompts []PromptDefinition `json:"prompts"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal prompts: %w", err)
	}

	return resp.Prompts, nil
}

// GetPrompt retrieves a specific prompt with arguments.
func (conn *Connection) GetPrompt(ctx context.Context, name string, args map[string]string) (*PromptResult, error) {
	result, err := conn.sendRequest(ctx, "prompts/get", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}

	var resp PromptResult
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal prompt: %w", err)
	}

	return &resp, nil
}

// PromptDefinition describes a prompt on an MCP server.
type PromptDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument describes a prompt parameter.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptResult is the result of getting a prompt.
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptMessage is a message in a prompt result.
type PromptMessage struct {
	Role    string                 `json:"role"`
	Content map[string]interface{} `json:"content"`
}
