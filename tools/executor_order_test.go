package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

const executorTestTimeout = 2 * time.Second

type executorTestTool struct {
	name       string
	readOnly   bool
	concurrent bool
	call       func(context.Context) (*types.ToolResult, error)
}

func (t *executorTestTool) Name() string { return t.name }

func (t *executorTestTool) Description() string { return "executor test tool" }

func (t *executorTestTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{Type: "object"}
}

func (t *executorTestTool) Call(
	ctx context.Context,
	_ map[string]interface{},
	_ *types.ToolUseContext,
) (*types.ToolResult, error) {
	return t.call(ctx)
}

func (t *executorTestTool) IsConcurrencySafe(map[string]interface{}) bool {
	return t.concurrent
}

func (t *executorTestTool) IsReadOnly(map[string]interface{}) bool { return t.readOnly }

func TestExecutorPreservesMutationBarriers(t *testing.T) {
	var mu sync.Mutex
	var order []string
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseWriteOnce sync.Once
	releaseMutation := func() { releaseWriteOnce.Do(func() { close(releaseWrite) }) }
	defer releaseMutation()
	readEntered := make(chan struct{})

	reg := NewRegistry()
	reg.Register(&executorTestTool{
		name:       "write",
		concurrent: true,
		call: func(context.Context) (*types.ToolResult, error) {
			close(writeEntered)
			<-releaseWrite
			mu.Lock()
			order = append(order, "write")
			mu.Unlock()
			return &types.ToolResult{}, nil
		},
	})
	reg.Register(&executorTestTool{
		name:       "read",
		readOnly:   true,
		concurrent: true,
		call: func(context.Context) (*types.ToolResult, error) {
			close(readEntered)
			mu.Lock()
			order = append(order, "read")
			mu.Unlock()
			return &types.ToolResult{}, nil
		},
	})

	ex := NewExecutorWithOptions(ExecutorOptions{Registry: reg, MaxConcurrency: 2})
	done := make(chan []ToolCallResponse, 1)
	go func() {
		done <- ex.RunTools(context.Background(), []ToolCallRequest{
			{ToolUseID: "1", ToolName: "write"},
			{ToolUseID: "2", ToolName: "read"},
		})
	}()

	waitForSignal(t, writeEntered, "write to start")
	select {
	case <-readEntered:
		releaseMutation()
		waitForResults(t, done)
		t.Fatal("read started before the earlier mutation completed")
	case <-time.After(50 * time.Millisecond):
	}
	releaseMutation()

	results := waitForResults(t, done)
	mu.Lock()
	gotOrder := strings.Join(order, ",")
	mu.Unlock()
	if gotOrder != "write,read" {
		t.Fatalf("order=%q, want write,read", gotOrder)
	}
	if len(results) != 2 || results[0].ToolUseID != "1" || results[1].ToolUseID != "2" {
		t.Fatalf("results=%+v, want request order", results)
	}
}

func TestExecutorBoundsAdjacentSafeReads(t *testing.T) {
	assertExecutorBound(t, 2, 3, func(reg *Registry) *Executor {
		return NewExecutorWithOptions(ExecutorOptions{Registry: reg, MaxConcurrency: 2})
	})
}

func TestExecutorBoundsDefaultConcurrency(t *testing.T) {
	tests := []struct {
		name string
		new  func(*Registry) *Executor
	}{
		{
			name: "options zero",
			new: func(reg *Registry) *Executor {
				return NewExecutorWithOptions(ExecutorOptions{Registry: reg})
			},
		},
		{
			name: "options negative",
			new: func(reg *Registry) *Executor {
				return NewExecutorWithOptions(ExecutorOptions{Registry: reg, MaxConcurrency: -1})
			},
		},
		{
			name: "legacy constructor",
			new: func(reg *Registry) *Executor {
				return NewExecutor(reg, nil, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertExecutorBound(t, 10, 11, tt.new)
		})
	}
}

func TestExecutorCancellationWhileWaitingForCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	firstEntered := make(chan struct{})
	tool := &executorTestTool{
		name:       "read",
		readOnly:   true,
		concurrent: true,
		call: func(ctx context.Context) (*types.ToolResult, error) {
			if calls.Add(1) == 1 {
				close(firstEntered)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	reg := NewRegistry()
	reg.Register(tool)
	ex := NewExecutorWithOptions(ExecutorOptions{Registry: reg, MaxConcurrency: 1})
	done := make(chan []ToolCallResponse, 1)
	go func() {
		done <- ex.RunTools(ctx, []ToolCallRequest{
			{ToolUseID: "1", ToolName: "read"},
			{ToolUseID: "2", ToolName: "read"},
		})
	}()

	waitForSignal(t, firstEntered, "first read to start")
	cancel()
	results := waitForResults(t, done)

	if got := calls.Load(); got != 1 {
		t.Fatalf("Call invoked %d times, want only the active read", got)
	}
	if len(results) != 2 || results[1].ToolUseID != "2" {
		t.Fatalf("results=%+v, want a result paired with tool use 2", results)
	}
	if results[1].Result == nil || !results[1].Result.IsError {
		t.Fatalf("queued result=%+v, want cancellation error", results[1])
	}
	if !strings.Contains(results[1].Result.Error, context.Canceled.Error()) {
		t.Fatalf("queued error=%q, want %q", results[1].Result.Error, context.Canceled)
	}
}

func assertExecutorBound(
	t *testing.T,
	bound int,
	callCount int,
	newExecutor func(*Registry) *Executor,
) {
	t.Helper()

	started := make(chan struct{}, callCount)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	tool := &executorTestTool{
		name:       "read",
		readOnly:   true,
		concurrent: true,
		call: func(context.Context) (*types.ToolResult, error) {
			started <- struct{}{}
			<-release
			return &types.ToolResult{}, nil
		},
	}
	reg := NewRegistry()
	reg.Register(tool)
	ex := newExecutor(reg)
	calls := make([]ToolCallRequest, callCount)
	for i := range calls {
		calls[i] = ToolCallRequest{ToolUseID: fmt.Sprint(i), ToolName: "read"}
	}

	done := make(chan []ToolCallResponse, 1)
	go func() { done <- ex.RunTools(context.Background(), calls) }()
	for i := 0; i < bound; i++ {
		waitForSignal(t, started, fmt.Sprintf("read %d to start", i+1))
	}
	select {
	case <-started:
		releaseAll()
		waitForResults(t, done)
		t.Fatalf("more than %d reads started before capacity was released", bound)
	case <-time.After(50 * time.Millisecond):
	}
	releaseAll()
	if results := waitForResults(t, done); len(results) != callCount {
		t.Fatalf("results=%d, want %d", len(results), callCount)
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(executorTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForResults(t *testing.T, ch <-chan []ToolCallResponse) []ToolCallResponse {
	t.Helper()
	select {
	case results := <-ch:
		return results
	case <-time.After(executorTestTimeout):
		t.Fatal("timed out waiting for executor results")
		return nil
	}
}

func TestExecutorCancellationErrorIsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var called atomic.Bool
	tool := &executorTestTool{
		name:       "read",
		readOnly:   true,
		concurrent: true,
		call: func(context.Context) (*types.ToolResult, error) {
			called.Store(true)
			return &types.ToolResult{}, nil
		},
	}
	reg := NewRegistry()
	reg.Register(tool)
	results := NewExecutorWithOptions(ExecutorOptions{Registry: reg}).RunTools(ctx, []ToolCallRequest{{
		ToolUseID: "cancelled",
		ToolName:  "read",
	}})

	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("results=%+v, want one paired error", results)
	}
	if !errors.Is(results[0].Error, context.Canceled) {
		t.Fatalf("response error=%v, want context cancellation", results[0].Error)
	}
	if called.Load() {
		t.Fatal("Call invoked with an already cancelled context")
	}
}
