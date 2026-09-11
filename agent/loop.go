package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
	agentcontext "github.com/claudebuddy/claudebuddy-agent-sdk-go/context"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/permissions"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

const defaultSystemPrompt = `You are an AI assistant with access to tools. Use the tools available to you to help the user with their request. Be concise and direct in your responses.`

// runLoop is the main agentic loop.
func (a *Agent) runLoop(ctx context.Context, prompt string, eventCh chan<- types.SDKMessage) error {
	startTime := time.Now()

	// Build system prompt
	systemPrompt := a.opts.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}
	if a.opts.AppendSystemPrompt != "" {
		systemPrompt += "\n\n" + a.opts.AppendSystemPrompt
	}

	// Get context
	sysCtx := agentcontext.GetSystemContext(a.opts.CWD)
	userCtx := agentcontext.GetUserContext(a.opts.CWD)
	systemBlocks := agentcontext.BuildSystemPromptBlocks(systemPrompt, sysCtx, userCtx)

	// Convert system blocks to API format
	apiSystemBlocks := make([]api.SystemBlock, len(systemBlocks))
	for i, b := range systemBlocks {
		block := api.SystemBlock{
			Type: "text",
			Text: b["text"].(string),
		}
		if cc, ok := b["cache_control"]; ok {
			if ccMap, ok := cc.(map[string]string); ok {
				block.CacheControl = &api.CacheControl{Type: ccMap["type"]}
			}
		}
		apiSystemBlocks[i] = block
	}

	// Add user message
	userMsg := types.Message{
		Type: types.MessageTypeUser,
		Role: "user",
		Content: []types.ContentBlock{{
			Type: types.ContentBlockText,
			Text: prompt,
		}},
		UUID:      uuid.New().String(),
		Timestamp: time.Now(),
	}
	a.messages = append(a.messages, userMsg)

	// Apply the same immutable tool bounds to model-visible schemas that the
	// executor enforces again against the concrete registry tool.
	allTools := permissions.FilterTools(a.toolRegistry.All(), a.opts.AllowedTools, a.opts.DisallowedTools)
	apiTools := make([]api.APIToolParam, len(allTools))
	for i, t := range allTools {
		apiTools[i] = api.ToolToAPIParam(t)
	}

	// Create tool context
	toolCtx := &types.ToolUseContext{
		WorkingDir:    a.opts.CWD,
		AbortCtx:      ctx,
		ReadFileState: make(map[string]*types.FileReadState),
	}

	// Create tool executor
	executor := tools.NewExecutor(a.toolRegistry, a.canUseTool, toolCtx)

	var totalUsage types.Usage
	turn := 0

	// Main loop
	for turn < a.opts.MaxTurns {
		turn++

		// Check budget
		if a.opts.MaxBudgetUSD > 0 && a.costTracker.TotalCost() >= a.opts.MaxBudgetUSD {
			eventCh <- types.SDKMessage{
				Type: types.MessageTypeSystem,
				Text: fmt.Sprintf("Budget limit reached ($%.2f)", a.opts.MaxBudgetUSD),
			}
			break
		}

		// Build API messages from conversation history
		apiMessages := a.buildAPIMessages()

		// Call the API
		req := api.MessagesRequest{
			Model:    a.opts.Model,
			System:   apiSystemBlocks,
			Messages: apiMessages,
			Tools:    apiTools,
		}

		// Extended thinking - explicit config takes precedence, then effort-based auto-config
		if a.opts.Thinking != nil {
			switch a.opts.Thinking.Type {
			case ThinkingEnabled:
				req.Thinking = &api.ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: a.opts.Thinking.BudgetTokens,
				}
			case ThinkingAdaptive:
				req.Thinking = &api.ThinkingConfig{
					Type: "adaptive",
				}
			case ThinkingDisabled:
				// No thinking config needed
			}
		} else if a.opts.Effort != "" {
			switch a.opts.Effort {
			case EffortLow:
				// Disabled - no thinking config
			case EffortMedium:
				req.Thinking = &api.ThinkingConfig{
					Type: "adaptive",
				}
			case EffortHigh:
				req.Thinking = &api.ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: 10000,
				}
			case EffortMax:
				req.Thinking = &api.ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: 50000,
				}
			}
		}

		// Structured output (JSON schema)
		if a.opts.JSONSchema != nil {
			req.ToolChoice = map[string]interface{}{
				"type": "any",
			}
		}

		streamEvents, streamErr := a.provider.CreateMessageStream(ctx, req)

		// Accumulate the assistant response
		assistantMsg := &types.Message{
			Type:      types.MessageTypeAssistant,
			Role:      "assistant",
			UUID:      uuid.New().String(),
			Timestamp: time.Now(),
		}

		var toolUseBlocks []types.ToolUseBlock
		var streamError error

		// Process stream
	streamLoop:
		for {
			select {
			case event, ok := <-streamEvents:
				if !ok {
					break streamLoop
				}
				a.processStreamEvent(event, assistantMsg, &toolUseBlocks)

			case err := <-streamErr:
				if err != nil {
					streamError = err
				}
				break streamLoop

			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// If stream failed and fallback model is configured, retry with fallback
		if streamError != nil && a.opts.FallbackModel != "" && req.Model != a.opts.FallbackModel {
			// Reset assistant message for retry
			assistantMsg = &types.Message{
				Type:      types.MessageTypeAssistant,
				Role:      "assistant",
				UUID:      uuid.New().String(),
				Timestamp: time.Now(),
			}
			toolUseBlocks = nil

			fallbackReq := req
			fallbackReq.Model = a.opts.FallbackModel
			streamEvents, streamErr = a.provider.CreateMessageStream(ctx, fallbackReq)
			streamError = nil

		fallbackStreamLoop:
			for {
				select {
				case event, ok := <-streamEvents:
					if !ok {
						break fallbackStreamLoop
					}
					a.processStreamEvent(event, assistantMsg, &toolUseBlocks)

				case err := <-streamErr:
					if err != nil {
						return fmt.Errorf("API stream error (fallback model %s): %w", a.opts.FallbackModel, err)
					}
					break fallbackStreamLoop

				case <-ctx.Done():
					return ctx.Err()
				}
			}
		} else if streamError != nil {
			return fmt.Errorf("API stream error: %w", streamError)
		}

		// Update usage
		if assistantMsg.Usage != nil {
			totalUsage.InputTokens += assistantMsg.Usage.InputTokens
			totalUsage.OutputTokens += assistantMsg.Usage.OutputTokens
			totalUsage.CacheReadInputTokens += assistantMsg.Usage.CacheReadInputTokens
			totalUsage.CacheCreationInputTokens += assistantMsg.Usage.CacheCreationInputTokens
			a.costTracker.AddUsage(a.opts.Model, assistantMsg.Usage)
		}

		// Store assistant message
		a.messages = append(a.messages, *assistantMsg)

		// Emit assistant event
		eventCh <- types.SDKMessage{
			Type:    types.MessageTypeAssistant,
			Message: assistantMsg,
		}

		// Check if we need to run tools
		if len(toolUseBlocks) == 0 {
			// No tool calls — end of turn
			break
		}

		// Check stop reason
		if assistantMsg.StopReason == "end_turn" && len(toolUseBlocks) == 0 {
			break
		}

		// Execute tools
		toolCalls := make([]tools.ToolCallRequest, len(toolUseBlocks))
		for i, tb := range toolUseBlocks {
			toolCalls[i] = tools.ToolCallRequest{
				ToolUseID: tb.ID,
				ToolName:  tb.Name,
				Input:     tb.Input,
			}
		}

		results := executor.RunTools(ctx, toolCalls)

		// Build tool result message
		var toolResultContent []types.ContentBlock
		for _, result := range results {
			content := result.Result.Content
			if len(content) == 0 {
				text := "(no output)"
				if result.Result.Error != "" {
					text = result.Result.Error
				}
				content = []types.ContentBlock{{
					Type: types.ContentBlockText,
					Text: text,
				}}
			}

			toolResultContent = append(toolResultContent, types.ContentBlock{
				Type:      types.ContentBlockToolResult,
				ToolUseID: result.ToolUseID,
				Content:   content,
				IsError:   result.Result.IsError,
			})
		}

		toolResultMsg := types.Message{
			Type:      types.MessageTypeUser,
			Role:      "user",
			Content:   toolResultContent,
			UUID:      uuid.New().String(),
			Timestamp: time.Now(),
		}
		a.messages = append(a.messages, toolResultMsg)

		// Emit tool result events so SSE consumers can display them
		for _, result := range results {
			content := result.Result.Content
			var textContent string
			for _, c := range content {
				if c.Type == types.ContentBlockText {
					textContent += c.Text
				}
			}
			eventCh <- types.SDKMessage{
				Type:  "tool_result",
				Text:  textContent,
				Usage: &types.Usage{},
				Message: &types.Message{
					Type: "tool_result",
					Role: "tool",
					Content: []types.ContentBlock{
						{
							Type:      types.ContentBlockToolResult,
							ToolUseID: result.ToolUseID,
							Content:   content,
							IsError:   result.Result.IsError,
						},
					},
				},
			}
		}
	}

	// Emit result
	eventCh <- types.SDKMessage{
		Type:     types.MessageTypeResult,
		Text:     types.ExtractText(&a.messages[len(a.messages)-1]),
		Usage:    &totalUsage,
		NumTurns: turn,
		Duration: time.Since(startTime).Milliseconds(),
		Cost:     a.costTracker.TotalCost(),
	}

	return nil
}

// processStreamEvent accumulates streaming data into the assistant message.
func (a *Agent) processStreamEvent(event api.StreamEvent, msg *types.Message, toolUseBlocks *[]types.ToolUseBlock) {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			msg.Model = event.Message.Model
			if event.Message.Usage != nil {
				msg.Usage = event.Message.Usage
			}
		}

	case "content_block_start":
		if event.ContentBlock != nil {
			msg.Content = append(msg.Content, *event.ContentBlock)

			// Track tool use blocks
			if event.ContentBlock.Type == types.ContentBlockToolUse {
				*toolUseBlocks = append(*toolUseBlocks, types.ToolUseBlock{
					ID:    event.ContentBlock.ID,
					Name:  event.ContentBlock.Name,
					Input: event.ContentBlock.Input,
				})
			}
		}

	case "content_block_delta":
		if event.Delta == nil || len(msg.Content) == 0 {
			return
		}
		idx := event.Index
		if idx >= len(msg.Content) {
			return
		}

		delta := event.Delta
		switch delta["type"] {
		case "text_delta":
			if text, ok := delta["text"].(string); ok {
				msg.Content[idx].Text += text
			}
		case "input_json_delta":
			if partialJSON, ok := delta["partial_json"].(string); ok {
				// Accumulate JSON for tool input
				// We'll parse the full input when the block stops
				msg.Content[idx].Text += partialJSON
			}
		case "thinking_delta":
			if thinking, ok := delta["thinking"].(string); ok {
				msg.Content[idx].Thinking += thinking
			}
		}

	case "content_block_stop":
		idx := event.Index
		if idx >= len(msg.Content) {
			return
		}
		block := &msg.Content[idx]

		// For tool_use blocks, parse accumulated JSON input
		if block.Type == types.ContentBlockToolUse && block.Text != "" {
			var input map[string]interface{}
			if err := parseJSON(block.Text, &input); err == nil {
				block.Input = input
				block.Text = ""

				// Update the tool use block's input
				for i, tb := range *toolUseBlocks {
					if tb.ID == block.ID {
						(*toolUseBlocks)[i].Input = input
						break
					}
				}
			}
		}

	case "message_delta":
		if event.Delta != nil {
			if sr, ok := event.Delta["stop_reason"].(string); ok {
				msg.StopReason = sr
			}
		}
		if event.Usage != nil {
			if msg.Usage == nil {
				msg.Usage = event.Usage
			} else {
				msg.Usage.OutputTokens += event.Usage.OutputTokens
			}
		}
	}
}

// buildAPIMessages converts internal messages to API format.
// Normalizes content blocks to only include fields required by the API.
func (a *Agent) buildAPIMessages() []api.APIMessage {
	var apiMsgs []api.APIMessage

	for _, msg := range a.messages {
		var normalized []types.ContentBlock
		for _, block := range msg.Content {
			switch block.Type {
			case types.ContentBlockText:
				normalized = append(normalized, types.ContentBlock{
					Type: types.ContentBlockText,
					Text: block.Text,
				})
			case types.ContentBlockToolUse:
				input := block.Input
				if input == nil {
					input = map[string]interface{}{}
				}
				normalized = append(normalized, types.ContentBlock{
					Type:  types.ContentBlockToolUse,
					ID:    block.ID,
					Name:  block.Name,
					Input: input,
				})
			case types.ContentBlockToolResult:
				tb := types.ContentBlock{
					Type:      types.ContentBlockToolResult,
					ToolUseID: block.ToolUseID,
					IsError:   block.IsError,
				}
				// Flatten content to text for the API
				if len(block.Content) > 0 {
					tb.Content = block.Content
				}
				normalized = append(normalized, tb)
			case types.ContentBlockThinking:
				normalized = append(normalized, types.ContentBlock{
					Type:     types.ContentBlockThinking,
					Thinking: block.Thinking,
				})
			default:
				normalized = append(normalized, block)
			}
		}
		apiMsgs = append(apiMsgs, api.APIMessage{
			Role:    msg.Role,
			Content: normalized,
		})
	}

	return apiMsgs
}

// parseJSON safely parses JSON, handling the streaming accumulation pattern.
func parseJSON(data string, v interface{}) error {
	// The streamed JSON might have been accumulated from partial chunks
	return jsonUnmarshal([]byte(data), v)
}

// jsonUnmarshal is a wrapper for json.Unmarshal to handle edge cases.
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
