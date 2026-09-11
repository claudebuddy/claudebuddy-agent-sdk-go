package tools

import (
	"context"
	"strings"
	"testing"
)

func TestCronTools(t *testing.T) {
	store := NewCronStore()
	create := NewCronCreateTool(store)
	list := NewCronListTool(store)
	del := NewCronDeleteTool(store)
	ctx := context.Background()
	tCtx := testToolCtx(t)

	// create
	r, err := create.Call(ctx, map[string]interface{}{
		"name": "daily", "schedule": "0 0 * * *", "command": "echo hello",
	}, tCtx)
	if err != nil || r.IsError {
		t.Fatalf("create failed: %v", err)
	}

	// list
	r, _ = list.Call(ctx, map[string]interface{}{}, tCtx)
	if !strings.Contains(r.Content[0].Text, "daily") {
		t.Errorf("cron not in list: %s", r.Content[0].Text)
	}

	// delete
	jobs := store.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	r, err = del.Call(ctx, map[string]interface{}{"id": jobs[0].ID}, tCtx)
	if err != nil || r.IsError {
		t.Fatalf("delete failed: %v", err)
	}
	if len(store.List()) != 0 {
		t.Error("cron not deleted")
	}

	// delete nonexistent
	r, _ = del.Call(ctx, map[string]interface{}{"id": "nonexistent"}, tCtx)
	if !r.IsError {
		t.Error("expected error for nonexistent cron")
	}
}

func TestRemoteTriggerTool(t *testing.T) {
	tool := NewRemoteTriggerTool()
	r, _ := tool.Call(context.Background(), map[string]interface{}{"action": "list"}, testToolCtx(t))
	if r.IsError {
		t.Fatal(r.Error)
	}
	if r.Content[0].Text == "" {
		t.Error("expected non-empty response")
	}
}
