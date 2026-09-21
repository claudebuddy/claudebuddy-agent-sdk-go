// Example 9: Subagents
//
// Demonstrates creating a specialized subagent for code review.
// The subagent runs as a separate Agent with restricted tools.
//
// Run: go run ./examples/09-subagents/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func main() {
	fmt.Println("--- Example 9: Subagents ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	ctx := context.Background()

	// Create a specialized code-reviewer subagent
	reviewer := agent.New(agent.Options{
		Model:    model,
		MaxTurns: 5,
		SystemPrompt: "You are an expert code reviewer. " +
			"Analyze code quality and suggest improvements. Focus on " +
			"security, performance, and maintainability. Be concise.",
		AllowedTools: []string{"Read", "Glob", "Grep"},
	})
	defer reviewer.Close()
	reviewer.Init(ctx)

	fmt.Println("Code reviewer agent analyzing sdk.go...")

	events, errs := reviewer.Query(ctx, "Review the file sdk.go for code quality. Be concise.")

	for event := range events {
		if event.Type == types.MessageTypeAssistant && event.Message != nil {
			for _, block := range event.Message.Content {
				switch block.Type {
				case types.ContentBlockText:
					if block.Text != "" {
						fmt.Print(block.Text)
					}
				case types.ContentBlockToolUse:
					fmt.Printf("[%s]\n", block.Name)
				}
			}
		}
		if event.Type == types.MessageTypeResult {
			fmt.Println("\n\n--- Review complete ---")
		}
	}

	if err := <-errs; err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
