package tools

import (
	"context"
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

const defaultExecutorMaxConcurrency = 10

// ToolCallRequest represents a pending tool call.
type ToolCallRequest struct {
	ToolUseID string
	ToolName  string
	Input     map[string]interface{}
}

// ToolCallResponse is the result of a tool call execution.
type ToolCallResponse struct {
	ToolUseID        string
	ToolName         string
	Result           *types.ToolResult
	Error            error
	PermissionDenial *types.PermissionDenial
}

// Executor runs tool calls with concurrency management.
type Executor struct {
	registry       *Registry
	canUseTool     types.CanUseToolFn
	toolCtx        *types.ToolUseContext
	maxConcurrency int
}

// ExecutorOptions configures a tool executor.
type ExecutorOptions struct {
	Registry       *Registry
	CanUseTool     types.CanUseToolFn
	ToolContext    *types.ToolUseContext
	MaxConcurrency int
}

// NewExecutor creates a new tool executor.
func NewExecutor(registry *Registry, canUseTool types.CanUseToolFn, toolCtx *types.ToolUseContext) *Executor {
	return NewExecutorWithOptions(ExecutorOptions{
		Registry:    registry,
		CanUseTool:  canUseTool,
		ToolContext: toolCtx,
	})
}

// NewExecutorWithOptions creates a tool executor with explicit options.
func NewExecutorWithOptions(opts ExecutorOptions) *Executor {
	maxConcurrency := opts.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = defaultExecutorMaxConcurrency
	}
	return &Executor{
		registry:       opts.Registry,
		canUseTool:     opts.CanUseTool,
		toolCtx:        opts.ToolContext,
		maxConcurrency: maxConcurrency,
	}
}

// RunTools executes a batch of tool calls in request order. Only contiguous
// ranges of read-only, concurrency-safe tools execute in parallel.
func (e *Executor) RunTools(ctx context.Context, calls []ToolCallRequest) []ToolCallResponse {
	if len(calls) == 0 {
		return nil
	}

	results := make([]ToolCallResponse, len(calls))
	for start := 0; start < len(calls); {
		if !e.isParallelRead(calls[start]) {
			results[start] = e.runSingle(ctx, calls[start])
			start++
			continue
		}

		end := start + 1
		for end < len(calls) && e.isParallelRead(calls[end]) {
			end++
		}
		e.runParallelRange(ctx, calls, results, start, end)
		start = end
	}

	return results
}

func (e *Executor) isParallelRead(call ToolCallRequest) bool {
	tool := e.registry.Get(call.ToolName)
	return tool != nil && tool.IsReadOnly(call.Input) && tool.IsConcurrencySafe(call.Input)
}

func (e *Executor) runParallelRange(
	ctx context.Context,
	calls []ToolCallRequest,
	results []ToolCallResponse,
	start int,
	end int,
) {
	slots := make(chan struct{}, e.maxConcurrency)
	var wg sync.WaitGroup

	for i := start; i < end; i++ {
		if err := ctx.Err(); err != nil {
			fillCancellationResults(results, calls, i, end, err)
			break
		}

		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			fillCancellationResults(results, calls, i, end, ctx.Err())
			wg.Wait()
			return
		}

		if err := ctx.Err(); err != nil {
			<-slots
			fillCancellationResults(results, calls, i, end, err)
			break
		}

		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			defer func() { <-slots }()

			if err := ctx.Err(); err != nil {
				results[index] = cancellationResponse(calls[index], err)
				return
			}
			results[index] = e.runSingle(ctx, calls[index])
		}(i)
	}

	wg.Wait()
}

func fillCancellationResults(
	results []ToolCallResponse,
	calls []ToolCallRequest,
	start int,
	end int,
	err error,
) {
	for i := start; i < end; i++ {
		results[i] = cancellationResponse(calls[i], err)
	}
}

func cancellationResponse(call ToolCallRequest, err error) ToolCallResponse {
	return ToolCallResponse{
		ToolUseID: call.ToolUseID,
		ToolName:  call.ToolName,
		Result: &types.ToolResult{
			IsError: true,
			Error:   err.Error(),
			Content: []types.ContentBlock{{
				Type: types.ContentBlockText,
				Text: "Error: " + err.Error(),
			}},
		},
		Error: err,
	}
}

func (e *Executor) runSingle(ctx context.Context, call ToolCallRequest) ToolCallResponse {
	if err := ctx.Err(); err != nil {
		return cancellationResponse(call, err)
	}

	tool := e.registry.Get(call.ToolName)
	if tool == nil {
		return ToolCallResponse{
			ToolUseID: call.ToolUseID,
			ToolName:  call.ToolName,
			Result: &types.ToolResult{
				IsError: true,
				Error:   "Unknown tool: " + call.ToolName,
				Content: []types.ContentBlock{{
					Type: types.ContentBlockText,
					Text: "Error: tool '" + call.ToolName + "' not found",
				}},
			},
		}
	}

	// Check permissions
	if e.canUseTool != nil {
		decision, err := e.canUseTool(tool, call.Input)
		if err != nil {
			return ToolCallResponse{
				ToolUseID: call.ToolUseID,
				ToolName:  call.ToolName,
				Result: &types.ToolResult{
					IsError: true,
					Error:   "Permission check failed: " + err.Error(),
				},
			}
		}
		if decision.Behavior == types.PermissionDeny {
			reason := decision.Reason
			if reason == "" {
				reason = "Permission denied"
			}
			return ToolCallResponse{
				ToolUseID:        call.ToolUseID,
				ToolName:         call.ToolName,
				PermissionDenial: &types.PermissionDenial{Tool: call.ToolName, Reason: reason},
				Result: &types.ToolResult{
					IsError: true,
					Error:   reason,
					Content: []types.ContentBlock{{
						Type: types.ContentBlockText,
						Text: "Error: " + reason,
					}},
				},
			}
		}
		// Apply updated input if permission handler modified it
		if decision.UpdatedInput != nil {
			call.Input = decision.UpdatedInput
		}
	}

	// Execute tool
	if err := ctx.Err(); err != nil {
		return cancellationResponse(call, err)
	}
	result, err := tool.Call(ctx, call.Input, e.toolCtx)
	if err != nil {
		return ToolCallResponse{
			ToolUseID: call.ToolUseID,
			ToolName:  call.ToolName,
			Result: &types.ToolResult{
				IsError: true,
				Error:   err.Error(),
				Content: []types.ContentBlock{{
					Type: types.ContentBlockText,
					Text: "Error: " + err.Error(),
				}},
			},
		}
	}

	return ToolCallResponse{
		ToolUseID: call.ToolUseID,
		ToolName:  call.ToolName,
		Result:    result,
	}
}
