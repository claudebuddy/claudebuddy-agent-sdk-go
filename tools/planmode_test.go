package tools

import (
	"context"
	"strings"
	"testing"
)

func TestPlanModeTools(t *testing.T) {
	state := NewPlanModeState()
	enter := NewEnterPlanModeTool(state)
	exit := NewExitPlanModeTool(state)
	ctx := context.Background()
	tCtx := testToolCtx(t)

	// enter
	r, err := enter.Call(ctx, map[string]interface{}{}, tCtx)
	if err != nil || r.IsError {
		t.Fatalf("enter failed: %v", err)
	}
	if !state.IsActive() {
		t.Error("expected plan mode active after enter")
	}

	// enter again (idempotent)
	r, _ = enter.Call(ctx, map[string]interface{}{}, tCtx)
	if r.IsError {
		t.Error("double enter should not error")
	}

	// exit with plan
	r, err = exit.Call(ctx, map[string]interface{}{
		"plan":     "step 1: do X\nstep 2: do Y",
		"approved": true,
	}, tCtx)
	if err != nil || r.IsError {
		t.Fatalf("exit failed: %v", err)
	}
	if state.IsActive() {
		t.Error("expected plan mode inactive after exit")
	}
	if !strings.Contains(state.GetPlan(), "step 1") {
		t.Errorf("plan not saved: %q", state.GetPlan())
	}

	// exit when not active
	r, _ = exit.Call(ctx, map[string]interface{}{}, tCtx)
	if !r.IsError {
		t.Error("expected error when exiting inactive plan mode")
	}
}
