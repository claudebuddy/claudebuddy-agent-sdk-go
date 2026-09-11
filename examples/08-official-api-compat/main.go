// Example 8: One-shot Query
//
// Demonstrates creating an agent inline for quick one-shot usage.
//
// Run: go run ./examples/08-official-api-compat/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func main() {
	fmt.Println("--- Example 8: One-shot Query ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	a := agent.New(agent.Options{
		Model:        model,
		AllowedTools: []string{"Bash", "Glob"},
	})
	defer a.Close()

	ctx := context.Background()
	events, errs := a.Query(ctx, "What files are in this directory? Be brief.")

	for event := range events {
		if event.Type == types.MessageTypeAssistant && event.Message != nil {
			for _, block := range event.Message.Content {
				switch block.Type {
				case types.ContentBlockText:
					if block.Text != "" {
						fmt.Println(block.Text)
					}
				case types.ContentBlockToolUse:
					fmt.Printf("Tool: %s\n", block.Name)
				}
			}
		}
		if event.Type == types.MessageTypeResult {
			fmt.Println("\nDone.")
		}
	}

	if err := <-errs; err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
