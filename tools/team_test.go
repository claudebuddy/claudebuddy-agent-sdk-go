package tools

import (
	"context"
	"testing"
)

func TestTeamTools(t *testing.T) {
	store := NewTeamStore()
	create := NewTeamCreateTool(store)
	del := NewTeamDeleteTool(store)
	ctx := context.Background()
	tCtx := testToolCtx(t)

	// create
	r, err := create.Call(ctx, map[string]interface{}{
		"name":    "alpha",
		"members": []interface{}{"agent-1", "agent-2"},
	}, tCtx)
	if err != nil || r.IsError {
		t.Fatalf("create failed: %v, isError=%v", err, r.IsError)
	}

	teams := store.List()
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0].Name != "alpha" {
		t.Errorf("expected name 'alpha', got %q", teams[0].Name)
	}
	if len(teams[0].Members) != 2 {
		t.Errorf("expected 2 members, got %d", len(teams[0].Members))
	}

	// delete
	r, err = del.Call(ctx, map[string]interface{}{"id": teams[0].ID}, tCtx)
	if err != nil || r.IsError {
		t.Fatalf("delete failed: %v", err)
	}
	if len(store.List()) != 0 {
		t.Error("team not deleted")
	}

	// delete nonexistent
	r, _ = del.Call(ctx, map[string]interface{}{"id": "nonexistent"}, tCtx)
	if !r.IsError {
		t.Error("expected error for nonexistent team")
	}
}
