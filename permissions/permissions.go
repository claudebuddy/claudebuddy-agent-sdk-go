package permissions

import (
	"strings"
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// Rule represents a permission rule.
type Rule struct {
	// ToolName is the tool to match (e.g., "Bash", "Edit")
	ToolName string `json:"tool_name"`
	// Pattern is an optional pattern (e.g., "git *" for Bash)
	Pattern string `json:"pattern,omitempty"`
}

// Config holds all permission configuration.
type Config struct {
	mu          sync.RWMutex
	Mode        types.PermissionMode `json:"mode"`
	AllowRules  []Rule               `json:"allow_rules,omitempty"`
	DenyRules   []Rule               `json:"deny_rules,omitempty"`
	AllowedDirs []string             `json:"allowed_dirs,omitempty"`
}

// SetMode changes the permission mode at runtime (thread-safe).
func (c *Config) SetMode(mode types.PermissionMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Mode = mode
}

// GetMode returns the current permission mode (thread-safe).
func (c *Config) GetMode() types.PermissionMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Mode
}

// AddRules adds rules of the specified type ("allow" or "deny") at runtime.
func (c *Config) AddRules(rules []Rule, ruleType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch ruleType {
	case "allow":
		c.AllowRules = append(c.AllowRules, rules...)
	case "deny":
		c.DenyRules = append(c.DenyRules, rules...)
	}
}

// RemoveRules removes rules matching the given tool names from the specified type.
func (c *Config) RemoveRules(toolNames []string, ruleType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nameSet := make(map[string]bool, len(toolNames))
	for _, n := range toolNames {
		nameSet[n] = true
	}
	switch ruleType {
	case "allow":
		c.AllowRules = filterRules(c.AllowRules, nameSet)
	case "deny":
		c.DenyRules = filterRules(c.DenyRules, nameSet)
	}
}

// ReplaceRules replaces all rules of the specified type.
func (c *Config) ReplaceRules(rules []Rule, ruleType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch ruleType {
	case "allow":
		c.AllowRules = append([]Rule(nil), rules...)
	case "deny":
		c.DenyRules = append([]Rule(nil), rules...)
	}
}

// AddDirectories adds allowed directories at runtime.
func (c *Config) AddDirectories(dirs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AllowedDirs = append(c.AllowedDirs, dirs...)
}

// RemoveDirectories removes allowed directories at runtime.
func (c *Config) RemoveDirectories(dirs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dirSet := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		dirSet[d] = true
	}
	var kept []string
	for _, d := range c.AllowedDirs {
		if !dirSet[d] {
			kept = append(kept, d)
		}
	}
	c.AllowedDirs = kept
}

// filterRules returns rules whose ToolName is not in the nameSet.
func filterRules(rules []Rule, nameSet map[string]bool) []Rule {
	var kept []Rule
	for _, r := range rules {
		if !nameSet[r.ToolName] {
			kept = append(kept, r)
		}
	}
	return kept
}

// DefaultConfig returns the default permission configuration.
func DefaultConfig() *Config {
	return &Config{
		Mode: types.PermissionModeBypassPermissions,
	}
}

// NewCanUseToolFn creates a CanUseToolFn from a permission config.
func NewCanUseToolFn(config *Config, allowedTools []string) types.CanUseToolFn {
	allowedSet := make(map[string]bool, len(allowedTools))
	for _, t := range allowedTools {
		allowedSet[t] = true
	}

	return func(tool types.Tool, input map[string]interface{}) (*types.PermissionDecision, error) {
		toolName := tool.Name()

		config.mu.RLock()
		denyRules := append([]Rule(nil), config.DenyRules...)
		allowRules := append([]Rule(nil), config.AllowRules...)
		mode := config.Mode
		config.mu.RUnlock()

		// Check deny rules first
		for _, rule := range denyRules {
			if matchesRule(rule, toolName, input) {
				return &types.PermissionDecision{
					Behavior: types.PermissionDeny,
					Reason:   "Denied by rule: " + rule.ToolName,
				}, nil
			}
		}

		// Check allow rules
		for _, rule := range allowRules {
			if matchesRule(rule, toolName, input) {
				return &types.PermissionDecision{
					Behavior: types.PermissionAllow,
				}, nil
			}
		}

		// Check allowedTools set
		if len(allowedSet) > 0 {
			if !allowedSet[toolName] {
				if mode == types.PermissionModeBypassPermissions {
					return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
				}
				return &types.PermissionDecision{
					Behavior: types.PermissionDeny,
					Reason:   "Tool not in allowed list",
				}, nil
			}
		}

		// Apply permission mode
		switch mode {
		case types.PermissionModeBypassPermissions:
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		case types.PermissionModeDontAsk:
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		case types.PermissionModeAcceptEdits:
			if tool.IsReadOnly(input) || isFileEditTool(toolName) {
				return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
			}
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		case types.PermissionModePlan:
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		default:
			return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
		}
	}
}

// matchesRule checks if a rule matches the tool and input.
func matchesRule(rule Rule, toolName string, input map[string]interface{}) bool {
	if rule.ToolName != toolName {
		// Check for MCP prefix matching
		if !strings.HasPrefix(toolName, rule.ToolName) {
			return false
		}
	}

	if rule.Pattern == "" {
		return true
	}

	// Match pattern against relevant input
	var value string
	switch toolName {
	case "Bash":
		value, _ = input["command"].(string)
	case "Edit", "Write", "Read":
		value, _ = input["file_path"].(string)
	case "Glob":
		value, _ = input["pattern"].(string)
	case "Grep":
		value, _ = input["pattern"].(string)
	default:
		return true
	}

	return simpleWildcardMatch(rule.Pattern, value)
}

// simpleWildcardMatch performs simple wildcard matching with *.
func simpleWildcardMatch(pattern, value string) bool {
	if pattern == "*" {
		return true
	}

	// Simple prefix/suffix matching
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(value, strings.TrimPrefix(pattern, "*"))
	}

	return pattern == value
}

func isFileEditTool(name string) bool {
	return name == "Edit" || name == "Write" || name == "Read" || name == "Glob" || name == "Grep"
}
