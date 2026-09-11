package permissions

import (
	"strings"
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools"
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

// NewCanUseToolFn creates a CanUseToolFn from a permission config. It is kept
// for compatibility; callers that need deny bounds or a host callback should
// use NewPolicy.
func NewCanUseToolFn(config *Config, allowedTools []string) types.CanUseToolFn {
	return NewPolicy(config, allowedTools, nil, nil)
}

// NewPolicy composes immutable tool bounds, dynamic rules, permission mode,
// and an optional host callback into one execution-time policy.
func NewPolicy(
	config *Config,
	allowedTools []string,
	deniedTools []string,
	callback types.CanUseToolFn,
) types.CanUseToolFn {
	allowedSet := toolNameSet(allowedTools)
	deniedSet := toolNameSet(deniedTools)
	hasAllowBound := allowedTools != nil

	return func(tool types.Tool, input map[string]interface{}) (*types.PermissionDecision, error) {
		if tool == nil {
			return deny("Tool is unavailable"), nil
		}

		toolName := tool.Name()
		mode, allowRules, denyRules := permissionSnapshot(config)

		if deniedSet[toolName] {
			return deny("Tool is in denied list"), nil
		}
		if rule := firstMatchingRule(denyRules, toolName, input); rule != nil {
			return deny("Denied by rule: " + rule.ToolName), nil
		}
		if hasAllowBound && !allowedSet[toolName] {
			return deny("Tool not in allowed list"), nil
		}
		if mode == types.PermissionModePlan && !tool.IsReadOnly(input) {
			return deny("Plan mode only allows read-only tools"), nil
		}

		preapproved := isPreapproved(tool, input, mode, hasAllowBound && allowedSet[toolName], allowRules)
		callbackEligible := preapproved || mode == types.PermissionModeDefault || mode == types.PermissionModeAcceptEdits
		if callback == nil || !callbackEligible {
			if preapproved {
				return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
			}
			return deny("Permission denied"), nil
		}

		decision, err := callback(tool, input)
		if decision == nil {
			decision = deny("Permission callback returned no decision")
		}
		if err != nil {
			return decision, err
		}

		checkedInput := input
		if decision.UpdatedInput != nil {
			checkedInput = decision.UpdatedInput
		}
		if rule := firstMatchingRule(denyRules, toolName, checkedInput); rule != nil {
			return deny("Denied by rule: " + rule.ToolName), nil
		}
		if mode == types.PermissionModePlan && !tool.IsReadOnly(checkedInput) {
			return deny("Plan mode only allows read-only tools"), nil
		}
		if mode == types.PermissionModeDontAsk && !isPreapproved(tool, checkedInput, mode, hasAllowBound && allowedSet[toolName], allowRules) {
			return deny("Permission denied"), nil
		}
		if decision.Behavior != types.PermissionAllow {
			decision.Behavior = types.PermissionDeny
			if decision.Reason == "" {
				decision.Reason = "Permission denied"
			}
		}
		return decision, nil
	}
}

// FilterTools applies immutable visibility bounds while preserving input order.
// A nil allow list means no allow bound; a non-nil empty list exposes no tools.
func FilterTools(allTools []types.Tool, allowedTools []string, deniedTools []string) []types.Tool {
	allowedSet := toolNameSet(allowedTools)
	deniedSet := toolNameSet(deniedTools)
	hasAllowBound := allowedTools != nil

	filtered := make([]types.Tool, 0, len(allTools))
	for _, tool := range allTools {
		if tool == nil || deniedSet[tool.Name()] {
			continue
		}
		if hasAllowBound && !allowedSet[tool.Name()] {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func toolNameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

func permissionSnapshot(config *Config) (types.PermissionMode, []Rule, []Rule) {
	if config == nil {
		return types.PermissionModeBypassPermissions, nil, nil
	}
	config.mu.RLock()
	defer config.mu.RUnlock()

	mode := config.Mode
	if mode == "" {
		mode = types.PermissionModeDefault
	}
	return mode, append([]Rule(nil), config.AllowRules...), append([]Rule(nil), config.DenyRules...)
}

func firstMatchingRule(rules []Rule, toolName string, input map[string]interface{}) *Rule {
	for i := range rules {
		if matchesRule(rules[i], toolName, input) {
			return &rules[i]
		}
	}
	return nil
}

func isPreapproved(
	tool types.Tool,
	input map[string]interface{},
	mode types.PermissionMode,
	explicitlyAllowed bool,
	allowRules []Rule,
) bool {
	if tool.IsReadOnly(input) || explicitlyAllowed || mode == types.PermissionModeBypassPermissions {
		return true
	}
	if mode == types.PermissionModeAcceptEdits && isBuiltInEditTool(tool) {
		return true
	}
	return firstMatchingRule(allowRules, tool.Name(), input) != nil
}

func isBuiltInEditTool(tool types.Tool) bool {
	switch tool.(type) {
	case *tools.FileEditTool, *tools.FileWriteTool, *tools.NotebookEditTool:
		return true
	default:
		return false
	}
}

func deny(reason string) *types.PermissionDecision {
	return &types.PermissionDecision{Behavior: types.PermissionDeny, Reason: reason}
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
