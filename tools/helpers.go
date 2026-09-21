package tools

import "github.com/claudebuddy/claudebuddy-agent-sdk-go/types"

func textResult(text string) *types.ToolResult {
	return &types.ToolResult{
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: text}},
	}
}
