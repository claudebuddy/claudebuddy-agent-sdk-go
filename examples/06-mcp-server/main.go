// Example 6: MCP Server Integration
//
// Connects to an MCP (Model Context Protocol) server and uses
// its tools through the agent. Uses the filesystem MCP server.
//
// Prerequisites:
//
//	npm install -g @modelcontextprotocol/server-filesystem
//
// Run: go run ./examples/06-mcp-server/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func main() {
	fmt.Println("--- Example 6: MCP Server Integration ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	a := agent.New(agent.Options{
		Model:    model,
		MaxTurns: 10,
		MCPServers: map[string]types.MCPServerConfig{
			"filesystem": {
				Type:    types.MCPTransportStdio,
				Command: "npx",
				Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
			},
		},
	})
	defer a.Close()

	ctx := context.Background()
	fmt.Println("Connecting to MCP filesystem server...")

	if err := a.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Init error: %v\n", err)
		os.Exit(1)
	}

	result, err := a.Prompt(ctx,
		"Use the filesystem MCP tools to list files in /tmp. Be brief.",
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "\nMCP server not found. Install it with:")
			fmt.Fprintln(os.Stderr, "  npm install -g @modelcontextprotocol/server-filesystem")
		}
		os.Exit(1)
	}

	fmt.Printf("Answer: %s\n", result.Text)
	fmt.Printf("Turns: %d\n", result.NumTurns)
}
