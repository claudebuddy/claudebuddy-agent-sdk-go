// Example 5: Custom System Prompt
//
// Shows how to customize the agent's behavior with a system prompt.
//
// Run: go run ./examples/05-custom-system-prompt/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
)

func main() {
	fmt.Println("--- Example 5: Custom System Prompt ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	a := agent.New(agent.Options{
		Model:    model,
		MaxTurns: 5,
		SystemPrompt: "You are a senior code reviewer. When asked to review code, focus on: " +
			"1) Security issues, 2) Performance concerns, 3) Maintainability. " +
			"Be concise and use bullet points.",
	})
	defer a.Close()

	ctx := context.Background()
	a.Init(ctx)

	result, err := a.Prompt(ctx, "Read go.mod and sdk.go and give a brief code review.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if result.Text == "" {
		fmt.Println("(No text in final response, but agent completed successfully)")
		fmt.Printf("Turns: %d, Tokens: %d in / %d out\n",
			result.NumTurns, result.Usage.InputTokens, result.Usage.OutputTokens)
	} else {
		fmt.Println(result.Text)
	}
}
