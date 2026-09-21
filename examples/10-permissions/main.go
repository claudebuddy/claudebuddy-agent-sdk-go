// Example 10: Permissions and Allowed Tools
//
// Shows how to restrict which tools the agent can use.
// Creates a read-only agent that can analyze but not modify code.
//
// Run: go run ./examples/10-permissions/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func main() {
	fmt.Println("--- Example 10: Read-Only Agent ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	// Read-only agent: can only use Read, Glob, Grep
	a := agent.New(agent.Options{
		Model:        model,
		MaxTurns:     5,
		AllowedTools: []string{"Read", "Glob", "Grep"},
	})
	defer a.Close()

	ctx := context.Background()
	a.Init(ctx)

	events, errs := a.Query(ctx, "Review the code in sdk.go for best practices. Be concise.")

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
			fmt.Println("\n\n--- Done ---")
		}
	}

	if err := <-errs; err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
