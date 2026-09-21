package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
	"github.com/google/uuid"
)

// ─── OpenAI request/response types ───────────────

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	ToolChoice  interface{}     `json:"tool_choice,omitempty"`
	Stream      bool            `json:"stream"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content"` // string or []openAIContentPart
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAIContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int           `json:"index"`
		Message      openAIMessage `json:"message"`
		Delta        openAIMessage `json:"delta"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ─── Conversion: Anthropic → OpenAI ──────────────

func convertToOpenAIRequest(req MessagesRequest) openAIRequest {
	oaiReq := openAIRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}

	// Convert system prompt
	if len(req.System) > 0 {
		var sysText strings.Builder
		for _, s := range req.System {
			if sysText.Len() > 0 {
				sysText.WriteString("\n\n")
			}
			sysText.WriteString(s.Text)
		}
		oaiReq.Messages = append(oaiReq.Messages, openAIMessage{
			Role:    "system",
			Content: sysText.String(),
		})
	}

	// Convert messages
	for _, msg := range req.Messages {
		oaiMsg := convertMessageToOpenAI(msg)
		oaiReq.Messages = append(oaiReq.Messages, oaiMsg...)
	}

	// Convert tools
	for _, tool := range req.Tools {
		oaiReq.Tools = append(oaiReq.Tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}

	// Tool choice
	if req.ToolChoice != nil {
		if tc, ok := req.ToolChoice.(map[string]interface{}); ok {
			if tcType, _ := tc["type"].(string); tcType == "any" {
				oaiReq.ToolChoice = "required"
			} else if tcType == "auto" {
				oaiReq.ToolChoice = "auto"
			}
		}
	}

	return oaiReq
}

func convertMessageToOpenAI(msg APIMessage) []openAIMessage {
	var msgs []openAIMessage

	switch msg.Role {
	case "user":
		var toolResults []types.ContentBlock
		var textParts []string

		for _, block := range msg.Content {
			switch block.Type {
			case types.ContentBlockText:
				textParts = append(textParts, block.Text)
			case types.ContentBlockToolResult:
				toolResults = append(toolResults, block)
			}
		}

		// Regular text message
		if len(textParts) > 0 {
			msgs = append(msgs, openAIMessage{
				Role:    "user",
				Content: strings.Join(textParts, "\n"),
			})
		}

		// Tool results as separate messages
		for _, tr := range toolResults {
			resultText := ""
			for _, cb := range tr.Content {
				if cb.Text != "" {
					resultText += cb.Text
				}
			}
			if tr.IsError {
				resultText = "Error: " + resultText
			}
			msgs = append(msgs, openAIMessage{
				Role:       "tool",
				Content:    resultText,
				ToolCallID: tr.ToolUseID,
			})
		}

	case "assistant":
		oaiMsg := openAIMessage{Role: "assistant"}
		var textParts []string
		var toolCalls []openAIToolCall

		for _, block := range msg.Content {
			switch block.Type {
			case types.ContentBlockText:
				textParts = append(textParts, block.Text)
			case types.ContentBlockToolUse:
				inputJSON, _ := json.Marshal(block.Input)
				toolCalls = append(toolCalls, openAIToolCall{
					ID:   block.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      block.Name,
						Arguments: string(inputJSON),
					},
				})
			}
		}

		if len(textParts) > 0 {
			oaiMsg.Content = strings.Join(textParts, "\n")
		}
		if len(toolCalls) > 0 {
			oaiMsg.ToolCalls = toolCalls
		}
		msgs = append(msgs, oaiMsg)
	}

	return msgs
}

// ─── Conversion: OpenAI response → Anthropic events ──

func convertOpenAIStreamChunk(data []byte) (*StreamEvent, error) {
	var resp openAIResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		// Usage-only message at end
		if resp.Usage != nil {
			return &StreamEvent{
				Type: "message_delta",
				Usage: &types.Usage{
					InputTokens:  resp.Usage.PromptTokens,
					OutputTokens: resp.Usage.CompletionTokens,
				},
			}, nil
		}
		return nil, nil
	}

	choice := resp.Choices[0]

	// Handle finish reason
	if choice.FinishReason == "stop" || choice.FinishReason == "tool_calls" {
		stopReason := "end_turn"
		if choice.FinishReason == "tool_calls" {
			stopReason = "tool_use"
		}
		return &StreamEvent{
			Type: "message_delta",
			Delta: map[string]interface{}{
				"type":        "message_delta",
				"stop_reason": stopReason,
			},
		}, nil
	}

	delta := choice.Delta

	// Text content
	if content, ok := delta.Content.(string); ok && content != "" {
		return &StreamEvent{
			Type: "content_block_delta",
			Delta: map[string]interface{}{
				"type": "text_delta",
				"text": content,
			},
		}, nil
	}

	// Tool calls
	if len(delta.ToolCalls) > 0 {
		tc := delta.ToolCalls[0]
		if tc.Function.Name != "" {
			// Tool call start
			return &StreamEvent{
				Type:  "content_block_start",
				Index: tc.Index(),
				ContentBlock: &types.ContentBlock{
					Type: types.ContentBlockToolUse,
					ID:   tc.ID,
					Name: tc.Function.Name,
				},
			}, nil
		}
		if tc.Function.Arguments != "" {
			return &StreamEvent{
				Type: "content_block_delta",
				Delta: map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": tc.Function.Arguments,
				},
			}, nil
		}
	}

	return nil, nil
}

// Index returns the index of a tool call (stored in the zero-based index field)
func (tc openAIToolCall) Index() int {
	return 0 // OpenAI uses array index, we default to 0
}

func convertOpenAIResponse(resp openAIResponse) *StreamMessage {
	if len(resp.Choices) == 0 {
		return nil
	}

	msg := resp.Choices[0].Message
	var content []types.ContentBlock

	// Text
	if text, ok := msg.Content.(string); ok && text != "" {
		content = append(content, types.ContentBlock{
			Type: types.ContentBlockText,
			Text: text,
		})
	}

	// Tool calls
	for _, tc := range msg.ToolCalls {
		var input map[string]interface{}
		json.Unmarshal([]byte(tc.Function.Arguments), &input)
		content = append(content, types.ContentBlock{
			Type:  types.ContentBlockToolUse,
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	stopReason := "end_turn"
	if resp.Choices[0].FinishReason == "tool_calls" {
		stopReason = "tool_use"
	}

	var usage *types.Usage
	if resp.Usage != nil {
		usage = &types.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
	}

	return &StreamMessage{
		ID:         resp.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      resp.Model,
		StopReason: stopReason,
		Usage:      usage,
	}
}

// ─── OpenAI streaming implementation ─────────────

func (c *Client) createOpenAIStream(ctx context.Context, req MessagesRequest, eventCh chan<- StreamEvent) error {
	oaiReq := convertToOpenAIRequest(req)
	oaiReq.Stream = true

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	apiURL := strings.TrimRight(c.config.BaseURL, "/") + "/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	for k, v := range c.config.CustomHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.config.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	// Send message_start
	eventCh <- StreamEvent{
		Type: "message_start",
		Message: &StreamMessage{
			ID:   uuid.New().String(),
			Type: "message",
			Role: "assistant",
		},
	}

	// Track tool call state for reassembly
	var currentToolCalls []openAIToolCall
	var accumulatedText strings.Builder
	textBlockStarted := false

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk openAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		// Text delta
		if content, ok := delta.Content.(string); ok && content != "" {
			if !textBlockStarted {
				eventCh <- StreamEvent{
					Type:  "content_block_start",
					Index: 0,
					ContentBlock: &types.ContentBlock{
						Type: types.ContentBlockText,
					},
				}
				textBlockStarted = true
			}
			accumulatedText.WriteString(content)
			eventCh <- StreamEvent{
				Type: "content_block_delta",
				Delta: map[string]interface{}{
					"type": "text_delta",
					"text": content,
				},
			}
		}

		// Tool call deltas
		for _, tc := range delta.ToolCalls {
			// New tool call
			if tc.Function.Name != "" {
				if textBlockStarted {
					eventCh <- StreamEvent{Type: "content_block_stop"}
					textBlockStarted = false
				}

				toolID := tc.ID
				if toolID == "" {
					toolID = fmt.Sprintf("call_%s", uuid.New().String()[:8])
				}

				currentToolCalls = append(currentToolCalls, openAIToolCall{
					ID:   toolID,
					Type: "function",
				})
				currentToolCalls[len(currentToolCalls)-1].Function.Name = tc.Function.Name

				eventCh <- StreamEvent{
					Type: "content_block_start",
					ContentBlock: &types.ContentBlock{
						Type: types.ContentBlockToolUse,
						ID:   toolID,
						Name: tc.Function.Name,
					},
				}
			}

			// Argument delta
			if tc.Function.Arguments != "" && len(currentToolCalls) > 0 {
				idx := len(currentToolCalls) - 1
				currentToolCalls[idx].Function.Arguments += tc.Function.Arguments
				eventCh <- StreamEvent{
					Type: "content_block_delta",
					Delta: map[string]interface{}{
						"type":         "input_json_delta",
						"partial_json": tc.Function.Arguments,
					},
				}
			}
		}

		// Finish reason
		if chunk.Choices[0].FinishReason != "" {
			if textBlockStarted {
				eventCh <- StreamEvent{Type: "content_block_stop"}
			}
			for range currentToolCalls {
				eventCh <- StreamEvent{Type: "content_block_stop"}
			}
		}
	}

	// Final message_stop
	eventCh <- StreamEvent{Type: "message_stop"}

	return nil
}

// createOpenAIMessage sends a non-streaming request
func (c *Client) createOpenAIMessage(ctx context.Context, req MessagesRequest) (*StreamMessage, error) {
	oaiReq := convertToOpenAIRequest(req)
	oaiReq.Stream = false

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	apiURL := strings.TrimRight(c.config.BaseURL, "/") + "/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	for k, v := range c.config.CustomHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.config.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return convertOpenAIResponse(oaiResp), nil
}
