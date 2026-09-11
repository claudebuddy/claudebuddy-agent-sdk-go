package contextusage

import (
	"testing"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func TestNewTracker_Defaults(t *testing.T) {
	tr := NewTracker()
	usage := tr.GetUsage()
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.TotalTokens != 0 {
		t.Errorf("expected 0 total tokens, got %d", usage.TotalTokens)
	}
}

func TestSetMaxTokens(t *testing.T) {
	tr := NewTracker()
	tr.SetMaxTokens(500_000)

	tr.Update("opus-4-6", nil, 0)
	usage := tr.GetUsage()
	if usage.MaxTokens != 500_000 {
		t.Errorf("expected 500000, got %d", usage.MaxTokens)
	}
}

func TestUpdate_WithAPIUsage(t *testing.T) {
	tr := NewTracker()

	messages := []types.Message{
		{
			Role: "user",
			UUID: "msg-1",
			Content: []types.ContentBlock{
				{Type: types.ContentBlockText, Text: "Hello world"},
			},
			Usage: &types.Usage{
				InputTokens:  100,
				OutputTokens: 50,
			},
		},
		{
			Role: "assistant",
			UUID: "msg-2",
			Content: []types.ContentBlock{
				{Type: types.ContentBlockText, Text: "Hi there!"},
			},
			Usage: &types.Usage{
				InputTokens:  200,
				OutputTokens: 100,
			},
		},
	}

	tr.Update("sonnet-4-6", messages, 5)
	usage := tr.GetUsage()

	// 150 (msg1) + 300 (msg2) + 5*150 (tools) = 1200
	expectedTotal := 150 + 300 + 750
	if usage.TotalTokens != expectedTotal {
		t.Errorf("expected %d total tokens, got %d", expectedTotal, usage.TotalTokens)
	}
	if usage.Model != "sonnet-4-6" {
		t.Errorf("expected model sonnet-4-6, got %s", usage.Model)
	}
	if usage.MaxTokens != 200_000 {
		t.Errorf("expected 200000 max tokens, got %d", usage.MaxTokens)
	}
	if usage.Percentage <= 0 {
		t.Error("expected positive percentage")
	}
}

func TestUpdate_CharEstimateFallback(t *testing.T) {
	tr := NewTracker()

	messages := []types.Message{
		{
			Role: "user",
			UUID: "msg-1",
			Content: []types.ContentBlock{
				{Type: types.ContentBlockText, Text: "Hello, this is a test message with some content."},
			},
			// No Usage set -- should fall back to char estimate
		},
	}

	tr.Update("sonnet-4-6", messages, 0)
	usage := tr.GetUsage()

	if usage.TotalTokens <= 0 {
		t.Error("expected positive total tokens from char estimate")
	}

	// Check breakdown
	if len(usage.MessageBreakdown) != 1 {
		t.Fatalf("expected 1 message in breakdown, got %d", len(usage.MessageBreakdown))
	}
	if usage.MessageBreakdown[0].Role != "user" {
		t.Errorf("expected role user, got %s", usage.MessageBreakdown[0].Role)
	}
	if usage.MessageBreakdown[0].UUID != "msg-1" {
		t.Errorf("expected uuid msg-1, got %s", usage.MessageBreakdown[0].UUID)
	}
}

func TestUpdate_ToolCategories(t *testing.T) {
	tr := NewTracker()

	messages := []types.Message{
		{
			Role: "assistant",
			Content: []types.ContentBlock{
				{Type: types.ContentBlockToolUse, Name: "bash", Input: map[string]interface{}{"command": "ls"}},
			},
		},
		{
			Role: "user",
			Content: []types.ContentBlock{
				{Type: types.ContentBlockToolResult, ToolUseID: "tu-1", Content: []types.ContentBlock{
					{Type: types.ContentBlockText, Text: "file1.go\nfile2.go"},
				}},
			},
		},
	}

	tr.Update("sonnet-4-6", messages, 10)
	usage := tr.GetUsage()

	if _, ok := usage.Categories["toolUse"]; !ok {
		t.Error("expected toolUse category")
	}
	if _, ok := usage.Categories["toolResults"]; !ok {
		t.Error("expected toolResults category")
	}
	if usage.Categories["tools"] != 10*150 {
		t.Errorf("expected tools category = %d, got %d", 10*150, usage.Categories["tools"])
	}
}

func TestUpdate_ModelDefaultMaxTokens(t *testing.T) {
	tr := NewTracker()
	tr.SetMaxTokens(0) // force lookup

	tr.Update("opus-4-6", nil, 0)
	usage := tr.GetUsage()
	if usage.MaxTokens != 1_000_000 {
		t.Errorf("expected 1000000 for opus, got %d", usage.MaxTokens)
	}
}

func TestGetUsage_ReturnsCopy(t *testing.T) {
	tr := NewTracker()
	tr.Update("sonnet-4-6", nil, 2)

	u1 := tr.GetUsage()
	u1.Categories["extra"] = 999
	u1.TotalTokens = 999999

	u2 := tr.GetUsage()
	if _, ok := u2.Categories["extra"]; ok {
		t.Error("mutation of returned usage should not affect tracker")
	}
	if u2.TotalTokens == 999999 {
		t.Error("mutation of returned usage should not affect tracker")
	}
}

func TestPercentage(t *testing.T) {
	tr := NewTracker()
	tr.SetMaxTokens(1000)

	messages := []types.Message{
		{
			Role: "user",
			Content: []types.ContentBlock{
				{Type: types.ContentBlockText, Text: "test"},
			},
			Usage: &types.Usage{
				InputTokens:  400,
				OutputTokens: 100,
			},
		},
	}

	tr.Update("sonnet-4-6", messages, 0)
	usage := tr.GetUsage()

	// 500 tokens / 1000 max = 50%
	if usage.Percentage < 49.0 || usage.Percentage > 51.0 {
		t.Errorf("expected ~50%%, got %.2f%%", usage.Percentage)
	}
}

func TestEmptyMessages(t *testing.T) {
	tr := NewTracker()
	tr.Update("sonnet-4-6", nil, 0)
	usage := tr.GetUsage()
	if usage.TotalTokens != 0 {
		t.Errorf("expected 0 tokens for nil messages and 0 tools, got %d", usage.TotalTokens)
	}
}
