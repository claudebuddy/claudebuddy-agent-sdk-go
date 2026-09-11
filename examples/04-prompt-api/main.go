// Example 4: Simple Prompt API
//
// Uses the blocking Prompt() method for quick one-shot queries.
// No need to iterate over streaming events.
//
// Run: go run ./examples/04-prompt-api/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
)

func main() {
	fmt.Println("--- Example 4: Simple Prompt API ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	a := agent.New(agent.Options{
		Model:    model,
		MaxTurns: 5,
	})
	defer a.Close()

	ctx := context.Background()
	a.Init(ctx)

	result, err := a.Prompt(ctx,
		"Use Bash to run `go version` and `uname -m`, then tell me the versions.",
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Answer: %s\n", result.Text)
	fmt.Printf("Turns: %d\n", result.NumTurns)
	fmt.Printf("Tokens: %d in / %d out\n", result.Usage.InputTokens, result.Usage.OutputTokens)
	fmt.Printf("Duration: %s\n", result.Duration)
}
