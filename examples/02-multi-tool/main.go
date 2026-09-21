// Example 2: Multi-Tool Orchestration
//
// The agent autonomously uses Glob, Bash, and Read tools to
// accomplish a multi-step task.
//
// Run: go run ./examples/02-multi-tool/
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func main() {
	fmt.Println("--- Example 2: Multi-Tool Orchestration ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	a := agent.New(agent.Options{
		Model:    model,
		MaxTurns: 15,
	})
	defer a.Close()

	ctx := context.Background()
	a.Init(ctx)

	prompt := "Do these steps: " +
		"1) Use Glob to find all .go files in the current directory (pattern \"*.go\"). " +
		"2) Use Bash to count lines in go.mod with `wc -l`. " +
		"3) Give a brief summary."

	events, errs := a.Query(ctx, prompt)

	for event := range events {
		if event.Type == types.MessageTypeAssistant && event.Message != nil {
			for _, block := range event.Message.Content {
				switch block.Type {
				case types.ContentBlockToolUse:
					inputJSON, _ := json.Marshal(block.Input)
					s := string(inputJSON)
					if len(s) > 100 {
						s = s[:100]
					}
					fmt.Printf("[%s] %s\n", block.Name, s)
				case types.ContentBlockText:
					if strings.TrimSpace(block.Text) != "" {
						fmt.Printf("\n%s\n", block.Text)
					}
				}
			}
		}
		if event.Type == types.MessageTypeResult {
			fmt.Printf("\n--- Done | %d/%d tokens ---\n",
				event.Usage.InputTokens, event.Usage.OutputTokens)
		}
	}

	if err := <-errs; err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
