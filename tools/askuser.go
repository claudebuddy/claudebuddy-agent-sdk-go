package tools

import (
	"context"
	"fmt"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// AskUserFn is a callback that presents a question to the user and returns their answer.
type AskUserFn func(ctx context.Context, question string) (string, error)

// AskUserQuestionTool asks the user a question and returns their response.
type AskUserQuestionTool struct {
	// AskFn handles the actual user interaction. Must be set for the tool to work.
	AskFn AskUserFn
}

func NewAskUserQuestionTool(askFn AskUserFn) *AskUserQuestionTool {
	return &AskUserQuestionTool{AskFn: askFn}
}

func (t *AskUserQuestionTool) Name() string { return "AskUserQuestion" }

func (t *AskUserQuestionTool) Description() string {
	return `Ask the user a question and wait for their response. Use this when you need clarification or input from the user to proceed.`
}

func (t *AskUserQuestionTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to ask the user",
			},
		},
		Required: []string{"question"},
	}
}

func (t *AskUserQuestionTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *AskUserQuestionTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *AskUserQuestionTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	question, _ := input["question"].(string)
	if question == "" {
		return errorResult("question is required"), nil
	}

	if t.AskFn == nil {
		return errorResult("AskUserQuestion is not configured. Set AskFn to handle user interaction."), nil
	}

	answer, err := t.AskFn(ctx, question)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to get user response: %v", err)), nil
	}

	return &types.ToolResult{
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: answer}},
	}, nil
}
