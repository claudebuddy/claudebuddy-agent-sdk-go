package types

import (
	"encoding/json"
	"time"
)

// MessageType represents the kind of message in a conversation.
type MessageType string

const (
	MessageTypeUser      MessageType = "user"
	MessageTypeAssistant MessageType = "assistant"
	MessageTypeProgress  MessageType = "progress"
	MessageTypeSystem    MessageType = "system"
	MessageTypeResult    MessageType = "result"
)

// ContentBlockType represents a type of content block.
type ContentBlockType string

const (
	ContentBlockText      ContentBlockType = "text"
	ContentBlockToolUse   ContentBlockType = "tool_use"
	ContentBlockToolResult ContentBlockType = "tool_result"
	ContentBlockThinking  ContentBlockType = "thinking"
	ContentBlockImage     ContentBlockType = "image"
)

// ContentBlock represents a block of content in a message.
type ContentBlock struct {
	Type  ContentBlockType       `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	// For tool_result
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   []ContentBlock `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`

	// For image
	Source *ImageSource `json:"source,omitempty"`

	// For thinking
	Thinking string `json:"thinking,omitempty"`
}

// MarshalJSON customizes JSON serialization per block type.
// Ensures tool_use blocks always include "input" (even if empty).
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case ContentBlockToolUse:
		input := b.Input
		if input == nil {
			input = map[string]interface{}{}
		}
		return json.Marshal(struct {
			Type  ContentBlockType       `json:"type"`
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		}{b.Type, b.ID, b.Name, input})

	case ContentBlockToolResult:
		m := map[string]interface{}{
			"type":        b.Type,
			"tool_use_id": b.ToolUseID,
		}
		if b.IsError {
			m["is_error"] = true
		}
		if len(b.Content) > 0 {
			m["content"] = b.Content
		}
		return json.Marshal(m)

	case ContentBlockText:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{string(b.Type), b.Text})

	default:
		// Fallback: use a type alias to avoid recursion
		type Alias ContentBlock
		return json.Marshal((*Alias)(&b))
	}
}

// ImageSource represents an inline image.
type ImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// Message represents a conversation message.
type Message struct {
	Type      MessageType    `json:"type"`
	Role      string         `json:"role"`
	Content   []ContentBlock `json:"content,omitempty"`
	UUID      string         `json:"uuid"`
	Timestamp time.Time      `json:"timestamp"`

	// For assistant messages
	Model     string `json:"model,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`

	// Usage tracking
	Usage *Usage `json:"usage,omitempty"`
}

// Usage represents token usage for a message.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// SDKMessage is the streaming event type yielded by the agent loop.
type SDKMessage struct {
	Type MessageType `json:"type"`

	// For "assistant" type
	Message *Message `json:"message,omitempty"`

	// For "result" type
	Text     string   `json:"text,omitempty"`
	Usage    *Usage   `json:"usage,omitempty"`
	NumTurns int      `json:"num_turns,omitempty"`
	Duration int64    `json:"duration_ms,omitempty"`
	Messages []Message `json:"messages,omitempty"`
	Cost     float64  `json:"cost,omitempty"`
}

// ToolUseBlock extracts tool use info from a content block.
type ToolUseBlock struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// ExtractToolUseBlocks extracts all tool_use blocks from a message.
func ExtractToolUseBlocks(msg *Message) []ToolUseBlock {
	var blocks []ToolUseBlock
	for _, b := range msg.Content {
		if b.Type == ContentBlockToolUse {
			blocks = append(blocks, ToolUseBlock{
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			})
		}
	}
	return blocks
}

// ExtractText extracts all text from a message's content blocks.
func ExtractText(msg *Message) string {
	var text string
	for _, b := range msg.Content {
		if b.Type == ContentBlockText {
			text += b.Text
		}
	}
	return text
}
