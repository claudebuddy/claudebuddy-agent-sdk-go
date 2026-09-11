// Example 1: Simple Query with Streaming
//
// Demonstrates the basic agent.New() + Query() flow with
// real-time event streaming.
//
// Run: go run ./examples/01-simple-query/
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func main() {
	fmt.Println("--- Example 1: Simple Query ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	a := agent.New(agent.Options{
		Model:    model,
		MaxTurns: 10,
	})
	defer a.Close()

	ctx := context.Background()
	if err := a.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Init error: %v\n", err)
		os.Exit(1)
	}

	events, errs := a.Query(ctx, "Read package.json and tell me the project name and version in one sentence.")

	for event := range events {
		switch event.Type {
		case types.MessageTypeAssistant:
			if event.Message == nil {
				continue
			}
			for _, block := range event.Message.Content {
				switch block.Type {
				case types.ContentBlockToolUse:
					inputJSON, _ := json.Marshal(block.Input)
					s := string(inputJSON)
					if len(s) > 80 {
						s = s[:80]
					}
					fmt.Printf("[Tool] %s(%s)\n", block.Name, s)
				case types.ContentBlockText:
					fmt.Printf("\nAssistant: %s\n", block.Text)
				}
			}

		case types.MessageTypeResult:
			fmt.Printf("\n--- Result ---\n")
			if event.Usage != nil {
				fmt.Printf("Tokens: %d in / %d out\n", event.Usage.InputTokens, event.Usage.OutputTokens)
			}
		}
	}

	if err := <-errs; err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
