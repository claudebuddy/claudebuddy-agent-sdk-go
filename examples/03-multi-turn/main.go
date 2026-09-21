// Example 3: Multi-Turn Conversation
//
// Demonstrates session persistence across multiple turns.
// The agent remembers context from previous interactions.
//
// Run: go run ./examples/03-multi-turn/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
)

func main() {
	fmt.Println("--- Example 3: Multi-Turn Conversation ---")

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

	// Turn 1: Create a file
	fmt.Println("> Turn 1: Create a file")
	r1, err := a.Prompt(ctx, `Use Bash to run: echo "Hello Open Agent SDK Go" > /tmp/oas-go-test.txt. Confirm briefly.`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  %s\n\n", r1.Text)

	// Turn 2: Read back (should remember context)
	fmt.Println("> Turn 2: Read the file back")
	r2, err := a.Prompt(ctx, "Read the file you just created and tell me its contents.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  %s\n\n", r2.Text)

	// Turn 3: Clean up
	fmt.Println("> Turn 3: Cleanup")
	r3, err := a.Prompt(ctx, "Delete that file with Bash. Confirm.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  %s\n\n", r3.Text)

	fmt.Printf("Session history: %d messages\n", len(a.GetMessages()))
}
