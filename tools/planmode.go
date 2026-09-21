package tools

import (
	"context"
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// PlanModeState tracks whether plan mode is active.
type PlanModeState struct {
	mu     sync.Mutex
	active bool
	plan   string
}

// NewPlanModeState creates a new PlanModeState.
func NewPlanModeState() *PlanModeState { return &PlanModeState{} }

// IsActive reports whether plan mode is currently active.
func (s *PlanModeState) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// GetPlan returns the last saved plan.
func (s *PlanModeState) GetPlan() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plan
}

// EnterPlanModeTool activates plan mode.
type EnterPlanModeTool struct{ State *PlanModeState }

// NewEnterPlanModeTool creates a new EnterPlanModeTool.
func NewEnterPlanModeTool(state *PlanModeState) *EnterPlanModeTool {
	return &EnterPlanModeTool{State: state}
}

func (t *EnterPlanModeTool) Name() string { return "EnterPlanMode" }
func (t *EnterPlanModeTool) Description() string {
	return "Enter plan/design mode for complex tasks. Focus on designing the approach before executing."
}
func (t *EnterPlanModeTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{Type: "object", Properties: map[string]interface{}{}}
}
func (t *EnterPlanModeTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *EnterPlanModeTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *EnterPlanModeTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	t.State.mu.Lock()
	defer t.State.mu.Unlock()
	if t.State.active {
		return textResult("Already in plan mode."), nil
	}
	t.State.active = true
	t.State.plan = ""
	return textResult("Entered plan mode. Design your approach before executing. Use ExitPlanMode when the plan is ready."), nil
}

// ExitPlanModeTool deactivates plan mode and saves the plan.
type ExitPlanModeTool struct{ State *PlanModeState }

// NewExitPlanModeTool creates a new ExitPlanModeTool.
func NewExitPlanModeTool(state *PlanModeState) *ExitPlanModeTool {
	return &ExitPlanModeTool{State: state}
}

func (t *ExitPlanModeTool) Name() string { return "ExitPlanMode" }
func (t *ExitPlanModeTool) Description() string {
	return "Exit plan mode with a completed plan."
}
func (t *ExitPlanModeTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"plan":     map[string]interface{}{"type": "string", "description": "The completed plan"},
			"approved": map[string]interface{}{"type": "boolean", "description": "Whether the plan is approved for execution"},
		},
	}
}
func (t *ExitPlanModeTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *ExitPlanModeTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *ExitPlanModeTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	t.State.mu.Lock()
	defer t.State.mu.Unlock()
	if !t.State.active {
		return errorResult("Not in plan mode."), nil
	}
	t.State.active = false
	plan, _ := input["plan"].(string)
	t.State.plan = plan
	approved := true
	if v, ok := input["approved"].(bool); ok {
		approved = v
	}
	status := "approved"
	if !approved {
		status = "pending approval"
	}
	msg := "Plan mode exited. Plan status: " + status + "."
	if plan != "" {
		msg += "\n\nPlan:\n" + plan
	}
	return textResult(msg), nil
}
