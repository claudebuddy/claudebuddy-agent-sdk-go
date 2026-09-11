package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

const (
	maxReconnectAttempts = 3
	reconnectBaseDelay   = 2 * time.Second
)

// Reconnect attempts to reconnect a disconnected server.
func (c *Client) Reconnect(ctx context.Context, name string) error {
	c.mu.RLock()
	conn, ok := c.connections[name]
	c.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown server: %s", name)
	}

	// Clean up existing connection
	if conn.cleanup != nil {
		conn.cleanup()
	}

	// Retry with backoff
	var lastErr error
	for attempt := 0; attempt < maxReconnectAttempts; attempt++ {
		if attempt > 0 {
			delay := reconnectBaseDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		newConn, err := c.ConnectServer(ctx, name, conn.Config)
		if err != nil {
			lastErr = err
			continue
		}

		if newConn.Status == "connected" {
			return nil
		}
		lastErr = fmt.Errorf("connection status: %s", newConn.Status)
	}

	return fmt.Errorf("reconnection failed after %d attempts: %w", maxReconnectAttempts, lastErr)
}

// IsSessionExpiredError checks if an error indicates MCP session expiry.
func IsSessionExpiredError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "-32001") || // JSON-RPC session not found
		strings.Contains(s, "session expired") ||
		strings.Contains(s, "404")
}

// CallToolWithReconnect calls a tool, reconnecting if the session expired.
func (c *Client) CallToolWithReconnect(ctx context.Context, serverName, toolName string, input map[string]interface{}) (*types.MCPToolCallResult, error) {
	c.mu.RLock()
	conn, ok := c.connections[serverName]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown server: %s", serverName)
	}

	result, err := conn.CallTool(ctx, toolName, input)
	if err != nil && IsSessionExpiredError(err) {
		// Try reconnecting
		if reconnErr := c.Reconnect(ctx, serverName); reconnErr != nil {
			return nil, fmt.Errorf("tool call failed and reconnection failed: %w (original: %v)", reconnErr, err)
		}

		// Retry the call
		c.mu.RLock()
		conn = c.connections[serverName]
		c.mu.RUnlock()

		return conn.CallTool(ctx, toolName, input)
	}

	return result, err
}

// Note: MCPToolCallResult is defined in types/mcp.go and used via types package.
