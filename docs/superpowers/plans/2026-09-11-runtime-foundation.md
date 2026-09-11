# Runtime Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Go SDK safely run many independent sessions concurrently while enforcing reliable terminal, budget, permission, hook, and ordered-tool contracts.

**Architecture:** A concurrency-safe `Agent` owns shared immutable runtime dependencies and many `Session` values; each Session owns conversation and tool state and admits one history-writing `Run`. Provider model selection and accounting are request/run scoped, while existing `Agent.Query` and `Agent.Prompt` delegate to a default Session.

**Tech Stack:** Go 1.23, standard-library `context`, channels, `sync`, `sync/atomic`, `net/http`, existing `github.com/google/uuid` dependency, table-driven tests, local provider doubles.

**Spec:** `docs/superpowers/specs/2026-09-11-go-ts-parity-design.md`

## Global Constraints

- Module path is `github.com/claudebuddy/claudebuddy-agent-sdk-go`; production code, examples, and current usage docs use only ClaudeBuddy branding.
- Preserve `agent.New`, `Agent.Query`, `Agent.Prompt`, `Agent.GetMessages`, `Agent.Clear`, `Agent.SessionID`, and `Agent.Close` through a default Session.
- One Agent admits many concurrent Sessions; one Session admits one active history-writing Run.
- No lock is held during provider, hook, permission, MCP, or tool callbacks.
- Every goroutine has an owner, cancellation path, and join path.
- No paid API calls; tests use local doubles only.
- Every production change follows red-green-refactor and each task ends with focused tests plus `go test ./...`.

---

### Task 1: Request-scoped provider contract and validated configuration

**Files:**
- Create: `api/provider.go`
- Create: `agent/errors.go`
- Create: `agent/options_test.go`
- Modify: `api/client.go`
- Modify: `agent/agent.go`

**Interfaces:**
- Produces: `api.MessageProvider` with `CreateMessage` and `CreateMessageStream`.
- Produces: `agent.ErrInvalidOptions`, `agent.ErrSessionBusy`, `agent.ErrAgentClosed`.
- Produces: `agent.Options.ProviderClient api.MessageProvider` for offline injection.
- Produces: `Options.Validate() error`; `agent.New` remains source compatible and stores construction failure for delivery from Query/Prompt.
- Consumes later: every Session and Run uses the injected `MessageProvider`; model is set on `api.MessagesRequest.Model`, never by mutating a shared client.

- [ ] **Step 1: Write failing option and provider-isolation tests**

Add tests that name the breaks: invalid values reaching a model request, and one fallback changing another concurrent request's model.

```go
package agent

import (
    "context"
    "errors"
    "sort"
    "sync"
    "testing"

    "github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
    "github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

type recordingProvider struct {
    mu     sync.Mutex
    models []string
}

func (p *recordingProvider) CreateMessage(ctx context.Context, req api.MessagesRequest) (*api.StreamMessage, error) {
    p.mu.Lock()
    p.models = append(p.models, req.Model)
    p.mu.Unlock()
    return &api.StreamMessage{Role:"assistant", Model:req.Model, StopReason:"end_turn"}, nil
}

func (p *recordingProvider) CreateMessageStream(ctx context.Context, req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
    p.mu.Lock()
    p.models = append(p.models, req.Model)
    p.mu.Unlock()
    events := make(chan api.StreamEvent, 4)
    errs := make(chan error, 1)
    events <- api.StreamEvent{Type:"message_start", Message:&api.StreamMessage{Role:"assistant", Model:req.Model}}
    events <- api.StreamEvent{Type:"content_block_start", Index:0, ContentBlock:&types.ContentBlock{Type:types.ContentBlockText}}
    events <- api.StreamEvent{Type:"content_block_delta", Index:0, Delta:map[string]interface{}{"type":"text_delta", "text":"ok"}}
    events <- api.StreamEvent{Type:"message_delta", Delta:map[string]interface{}{"stop_reason":"end_turn"}}
    close(events)
    close(errs)
    return events, errs
}

func TestOptionsRejectInvalidLimitsBeforeProviderCall(t *testing.T) {
    provider := &recordingProvider{}
    a := New(Options{ProviderClient: provider, MaxTurns: -1})
    _, err := a.Prompt(context.Background(), "hello")
    if !errors.Is(err, ErrInvalidOptions) {
        t.Fatalf("got %v, want ErrInvalidOptions", err)
    }
}

func TestModelSelectionIsRequestScoped(t *testing.T) {
    provider := &recordingProvider{}
    a1 := New(Options{ProviderClient: provider, Model: "primary-one"})
    a2 := New(Options{ProviderClient: provider, Model: "primary-two"})
    var wg sync.WaitGroup
    wg.Add(2)
    go func() { defer wg.Done(); _, _ = a1.Prompt(context.Background(), "one") }()
    go func() { defer wg.Done(); _, _ = a2.Prompt(context.Background(), "two") }()
    wg.Wait()
    provider.mu.Lock()
    defer provider.mu.Unlock()
    sort.Strings(provider.models)
    if len(provider.models) != 2 || provider.models[0] != "primary-one" || provider.models[1] != "primary-two" {
        t.Fatalf("request models=%v", provider.models)
    }
}
```

- [ ] **Step 2: Run the tests and verify the expected compile failures**

Run: `go test ./agent -run 'TestOptionsReject|TestModelSelection' -count=1`

Expected: FAIL because `MessageProvider`, `ProviderClient`, `SessionOptions`, and typed errors do not exist.

- [ ] **Step 3: Add the provider interface and typed errors**

```go
// api/provider.go
package api

import "context"

type MessageProvider interface {
    CreateMessage(context.Context, MessagesRequest) (*StreamMessage, error)
    CreateMessageStream(context.Context, MessagesRequest) (<-chan StreamEvent, <-chan error)
}
```

```go
// agent/errors.go
package agent

import "errors"

var (
    ErrInvalidOptions = errors.New("invalid agent options")
    ErrSessionBusy    = errors.New("session already has an active run")
    ErrAgentClosed    = errors.New("agent is closed")
)
```

Add `ProviderClient api.MessageProvider` to Options. Add validation for `MaxTurns < 0`, non-finite or negative budget, non-positive timeout, and unsupported permission mode. Keep zero `MaxTurns` as “use default 10”. Store validation failure on Agent because changing `New` to return two values would break callers.

```go
func (o Options) Validate() error {
    if o.MaxTurns < 0 {
        return fmt.Errorf("%w: max turns must be positive", ErrInvalidOptions)
    }
    if math.IsNaN(o.MaxBudgetUSD) || math.IsInf(o.MaxBudgetUSD, 0) || o.MaxBudgetUSD < 0 {
        return fmt.Errorf("%w: max budget must be finite and non-negative", ErrInvalidOptions)
    }
    return validatePermissionMode(o.PermissionMode)
}
```

`New` uses the injected provider when non-nil; otherwise it creates `*api.Client`. Remove Agent-loop calls to `SetModel`; set `MessagesRequest.Model` for primary and fallback attempts.

- [ ] **Step 4: Run focused and full tests**

Run: `go test ./agent ./api -count=1`

Expected: PASS.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add api/provider.go api/client.go agent/errors.go agent/options_test.go agent/agent.go
git commit -m "refactor: add request-scoped provider contract"
```

### Task 2: Ordered, bounded, cancellation-aware tool scheduler

**Files:**
- Create: `tools/executor_order_test.go`
- Modify: `tools/executor.go`

**Interfaces:**
- Preserves: `NewExecutor(registry, canUseTool, toolCtx)` with default concurrency 10.
- Produces: `NewExecutorWithOptions(ExecutorOptions) *Executor`.
- Produces: `ExecutorOptions{Registry, CanUseTool, ToolContext, MaxConcurrency}`.
- Consumes later: Run creates an Executor with the Agent-level tool concurrency limit.

- [ ] **Step 1: Write failing ordering, bound, and cancellation tests**

Use a real test Tool whose Call function records start/end order. The mutation that must fail these tests is moving a read ahead of an earlier write or starting more than the configured bound.

```go
func TestExecutorPreservesMutationBarriers(t *testing.T) {
    var mu sync.Mutex
    order := []string{}
    reg := NewRegistry()
    reg.Register(&executorTestTool{name: "write", call: func(context.Context) {
        mu.Lock(); order = append(order, "write"); mu.Unlock()
    }})
    reg.Register(&executorTestTool{name: "read", readOnly: true, concurrent: true, call: func(context.Context) {
        mu.Lock(); order = append(order, "read"); mu.Unlock()
    }})
    ex := NewExecutorWithOptions(ExecutorOptions{Registry: reg, MaxConcurrency: 2})
    got := ex.RunTools(context.Background(), []ToolCallRequest{
        {ToolUseID: "1", ToolName: "write"},
        {ToolUseID: "2", ToolName: "read"},
    })
    if len(got) != 2 || strings.Join(order, ",") != "write,read" {
        t.Fatalf("order=%v results=%d", order, len(got))
    }
}

func TestExecutorBoundsAdjacentSafeReads(t *testing.T) {
    var active, peak atomic.Int32
    gate := make(chan struct{})
    tool := &executorTestTool{name: "read", readOnly: true, concurrent: true, call: func(ctx context.Context) {
        n := active.Add(1)
        for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {}
        <-gate
        active.Add(-1)
    }}
    reg := NewRegistry(); reg.Register(tool)
    ex := NewExecutorWithOptions(ExecutorOptions{Registry: reg, MaxConcurrency: 2})
    done := make(chan struct{})
    go func() {
        ex.RunTools(context.Background(), []ToolCallRequest{{ToolUseID:"1",ToolName:"read"},{ToolUseID:"2",ToolName:"read"},{ToolUseID:"3",ToolName:"read"}})
        close(done)
    }()
    for peak.Load() < 2 { runtime.Gosched() }
    if peak.Load() != 2 { t.Fatalf("peak=%d", peak.Load()) }
    close(gate); <-done
}
```

Also add a cancellation test where one read waits for capacity, the context is cancelled, and the queued read returns a paired error without invoking Call.

- [ ] **Step 2: Run tests and verify behavioral failures**

Run: `go test ./tools -run 'TestExecutorPreserves|TestExecutorBounds|TestExecutorCancellation' -count=1`

Expected: the barrier test reports `read,write`, and the new constructor is initially undefined.

- [ ] **Step 3: Implement contiguous batching**

Scan calls from left to right. Execute one serial barrier directly; otherwise collect only the adjacent safe-read run. Use a buffered channel as a semaphore and write each result to its original slot.

```go
for start := 0; start < len(calls); {
    if !e.isParallelRead(calls[start]) {
        results[start] = e.runSingle(ctx, calls[start])
        start++
        continue
    }
    end := start
    for end < len(calls) && e.isParallelRead(calls[end]) { end++ }
    e.runParallelRange(ctx, calls, results, start, end)
    start = end
}
```

Before acquiring capacity and before invoking a tool, check `ctx.Err()`. Return results in input order. Normalize `MaxConcurrency <= 0` to 10.

- [ ] **Step 4: Verify focused tests, full tests, and race behavior**

Run: `go test ./tools -count=1`

Expected: PASS.

Run: `go test -race ./tools -count=1`

Expected: PASS with no race report.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/executor.go tools/executor_order_test.go
git commit -m "fix: preserve tool ordering with bounded reads"
```

### Task 3: Permission bounds with Go dynamic-rule support

**Files:**
- Create: `permissions/policy_test.go`
- Modify: `permissions/permissions.go`
- Modify: `agent/loop.go`

**Interfaces:**
- Produces: `permissions.NewPolicy(config, allowed, denied, callback) types.CanUseToolFn`.
- Preserves: `NewCanUseToolFn` as a compatibility wrapper with no deny list or callback.
- Produces: `permissions.FilterTools([]types.Tool, allowed, denied) []types.Tool`.
- Consumes later: Session tool assembly and child Agent creation use the same policy and bounds.

- [ ] **Step 1: Write table-driven failing policy tests**

```go
func TestPermissionModesDoNotSilentlyAllowMutations(t *testing.T) {
    tests := []struct{
        name string
        mode types.PermissionMode
        tool types.Tool
        want types.PermissionBehavior
    }{
        {"plan mutation", types.PermissionModePlan, &fakeTool{name:"CustomWrite"}, types.PermissionDeny},
        {"dontAsk mutation", types.PermissionModeDontAsk, &fakeTool{name:"CustomWrite"}, types.PermissionDeny},
        {"default mutation", types.PermissionModeDefault, &fakeTool{name:"CustomWrite"}, types.PermissionDeny},
        {"plan read", types.PermissionModePlan, &fakeTool{name:"ReadOnly", readOnly:true}, types.PermissionAllow},
        {"accept built-in edit", types.PermissionModeAcceptEdits, tools.NewFileEditTool(), types.PermissionAllow},
        {"accept impersonator", types.PermissionModeAcceptEdits, &fakeTool{name:"Edit"}, types.PermissionDeny},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            policy := NewPolicy(&Config{Mode:tt.mode}, nil, nil, nil)
            got, err := policy(tt.tool, map[string]interface{}{})
            if err != nil || got.Behavior != tt.want { t.Fatalf("got=%+v err=%v", got, err) }
        })
    }
}

func TestExplicitEmptyAllowListExposesNoTools(t *testing.T) {
    toolsIn := []types.Tool{&fakeTool{name:"ReadOnly", readOnly:true}}
    got := FilterTools(toolsIn, []string{}, nil)
    if len(got) != 0 { t.Fatalf("got %d tools", len(got)) }
}
```

Add cases proving deny beats allow, callback can deny an allowed operation, callback cannot widen plan, and MCP prefix rules still work.

- [ ] **Step 2: Run tests and verify current unsafe behavior fails**

Run: `go test ./permissions -run 'TestPermission|TestExplicit|TestDeny|TestCallback' -count=1`

Expected: FAIL because current plan/default/dontAsk permit mutations and empty allow is ignored.

- [ ] **Step 3: Implement composed policy**

Evaluate in this order: deny bounds, dynamic deny rules, plan restriction, read-only/preapproved/bypass/real built-in edit, dynamic allow rules, callback where allowed, final deny. Identify trusted edit tools by concrete implementation type rather than name.

```go
func isBuiltInEditTool(tool types.Tool) bool {
    switch tool.(type) {
    case *tools.FileEditTool, *tools.FileWriteTool, *tools.NotebookEditTool:
        return true
    default:
        return false
    }
}
```

Use `allowed != nil` to distinguish absent from explicitly empty. Filter model-visible tools before building schemas; still run policy at execution time to prevent registry replacement bypasses.

- [ ] **Step 4: Wire policy into Agent without allowing raw callback bypass**

In `New`, always construct the composed policy. In `runLoop`, build schemas from `FilterTools` and do not duplicate divergent allow/deny logic.

- [ ] **Step 5: Verify**

Run: `go test ./permissions ./agent -count=1`

Expected: PASS.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add permissions/permissions.go permissions/policy_test.go agent/loop.go agent/agent.go
git commit -m "fix: enforce permission bounds across tools"
```

### Task 4: Concurrent Agent, isolated Session, and owned Run APIs

**Files:**
- Create: `agent/session.go`
- Create: `agent/run.go`
- Create: `agent/session_concurrency_test.go`
- Modify: `agent/agent.go`
- Modify: `agent/loop.go`
- Modify: `tools/registry.go`

**Interfaces:**
- Produces: `SessionOptions{ID string; History []types.Message}`.
- Produces: `(*Agent).NewSession(SessionOptions) (*Session, error)` and `(*Agent).GetSession(string) (*Session, bool)`.
- Produces: `(*Session).Start(context.Context, string) (*Run, error)`, `Query`, `Prompt`, `GetMessages`, `Clear`, `Fork`, and `SessionID`.
- Produces: `(*Run).Events() <-chan types.SDKMessage`, `Err() <-chan error`, `Cancel()`, and `Wait() (*QueryResult, error)`.
- Preserves: legacy Agent methods by delegation to `defaultSession`.
- Consumes later: terminal accounting, background task runtime, persistence, and interaction attach to Run/Session ownership.

- [ ] **Step 1: Write failing multi-session and same-session exclusion tests**

```go
func TestAgentRunsIndependentSessionsConcurrently(t *testing.T) {
    entered := make(chan string, 2)
    release := make(chan struct{})
    provider := newTextProvider(func(prompt string) string {
        entered <- prompt
        <-release
        return "reply:" + prompt
    })
    a := New(Options{ProviderClient: provider, MaxConcurrentRuns: 2})
    one, _ := a.NewSession(SessionOptions{ID:"one"})
    two, _ := a.NewSession(SessionOptions{ID:"two"})
    r1, err1 := one.Start(context.Background(), "alpha")
    r2, err2 := two.Start(context.Background(), "beta")
    if err1 != nil || err2 != nil { t.Fatalf("start errors: %v %v", err1, err2) }
    <-entered; <-entered
    close(release)
    got1, e1 := r1.Wait(); got2, e2 := r2.Wait()
    if e1 != nil || e2 != nil || got1.Text == got2.Text { t.Fatalf("results leaked: %+v %+v", got1, got2) }
    if len(one.GetMessages()) != 2 || len(two.GetMessages()) != 2 { t.Fatal("history not isolated") }
}

func TestSessionRejectsOverlappingRuns(t *testing.T) {
    provider, entered, release := blockingTextProvider()
    a := New(Options{ProviderClient:provider})
    s, _ := a.NewSession(SessionOptions{ID:"same"})
    first, err := s.Start(context.Background(), "first")
    if err != nil { t.Fatal(err) }
    <-entered
    if _, err := s.Start(context.Background(), "second"); !errors.Is(err, ErrSessionBusy) {
        t.Fatalf("got %v", err)
    }
    close(release); _, _ = first.Wait()
}
```

Add tests for `Fork` copying history without sharing backing slices, context cancellation while waiting for Agent run capacity, Agent Close rejecting starts, and legacy Agent methods using one default Session.

- [ ] **Step 2: Run tests and verify missing API failures**

Run: `go test ./agent -run 'TestAgentRuns|TestSessionRejects|TestSessionFork|TestAgentClose|TestLegacy' -count=1`

Expected: FAIL because Session and Run APIs do not exist.

- [ ] **Step 3: Implement runtime ownership**

Agent holds an immutable option snapshot, provider, MCP client, a guarded session map, default Session, closed atomic flag, and bounded run semaphore.

```go
type Agent struct {
    opts Options
    provider api.MessageProvider
    mcpClient *mcp.Client
    sessionsMu sync.RWMutex
    sessions map[string]*Session
    defaultSession *Session
    runSlots chan struct{}
    closed atomic.Bool
}

type Session struct {
    runtime *Agent
    id string
    historyMu sync.RWMutex
    messages []types.Message
    runMu sync.Mutex
    active *Run
    registry *tools.Registry
}
```

`Session.Start` reserves the Session first, then Agent capacity with a context-aware select. On any startup failure it releases both. Run cleanup releases both exactly once with `sync.Once`.

```go
select {
case a.runSlots <- struct{}{}:
case <-ctx.Done():
    s.releaseRun(run)
    return nil, ctx.Err()
}
```

Build a fresh base registry per Session so mutable Task/Todo/Config/Mailbox/Team/Plan/Worktree/Cron state does not leak. Copy custom and MCP tool references into the Session registry explicitly.

- [ ] **Step 4: Move loop state from Agent to Run/Session**

Change loop methods to receive `*Run`; read/write history only through Session snapshot/append methods. The Run owns its derived context and event/error channels. `Wait` caches the final result and is safe for multiple callers.

- [ ] **Step 5: Verify concurrency and race behavior**

Run: `go test ./agent -count=1`

Expected: PASS.

Run: `go test -race ./agent ./tools -count=1`

Expected: PASS with no race report.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/run.go agent/session_concurrency_test.go agent/agent.go agent/loop.go tools/registry.go
git commit -m "feat: add concurrent session runtime"
```

### Task 5: Explicit terminal results, Run ledger, and cancellation

**Files:**
- Create: `costtracker/ledger.go`
- Create: `costtracker/ledger_test.go`
- Create: `agent/run_terminal_test.go`
- Modify: `types/message.go`
- Modify: `agent/run.go`
- Modify: `agent/loop.go`
- Modify: `agent/agent.go`

**Interfaces:**
- Produces: `types.ResultSubtype` constants `success`, `cancelled`, `error_max_turns`, `error_max_budget_usd`, `error_during_execution`.
- Extends: `types.SDKMessage` with subtype, error flag, errors, stop reason, model usage, and permission denials.
- Produces: `costtracker.Ledger` shared by a Run and its descendants.
- Produces: `agent.BudgetOptions{MaxUSD float64}` and `Options.Budget *BudgetOptions`; the pointer distinguishes omitted budget from an explicit zero.
- Extends: `QueryResult` with the same terminal information.

- [ ] **Step 1: Write failing terminal-contract tests**

```go
func TestRunPublishesOneErrorTerminalResult(t *testing.T) {
    provider := errorProvider(errors.New("provider unavailable"))
    a := New(Options{ProviderClient:provider})
    s, _ := a.NewSession(SessionOptions{ID:"failure"})
    run, _ := s.Start(context.Background(), "hello")
    terminals := 0
    var terminal types.SDKMessage
    for event := range run.Events() {
        if event.Type == types.MessageTypeResult { terminals++; terminal = event }
    }
    _, err := run.Wait()
    if err == nil || terminals != 1 { t.Fatalf("terminals=%d err=%v", terminals, err) }
    if terminal.Subtype != types.ResultErrorDuringExecution || !terminal.IsError {
        t.Fatalf("terminal=%+v", terminal)
    }
}

func TestZeroBudgetStartsNoProviderRequest(t *testing.T) {
    calls := atomic.Int32{}
    provider := countingProvider(&calls)
    a := New(Options{ProviderClient:provider, Budget:&BudgetOptions{MaxUSD:0}})
    result, err := a.Prompt(context.Background(), "hello")
    if err == nil || result.Subtype != types.ResultErrorMaxBudget || calls.Load() != 0 {
        t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
    }
}
```

Because Go cannot distinguish an omitted float from explicit zero, retain
`MaxBudgetUSD` for compatibility when it is positive and add the preferred
`Budget *BudgetOptions` field. Add tests for max turns, pre-cancelled contexts,
cancellation during a provider wait, final-turn success, and two sequential runs
each receiving a fresh ledger.

- [ ] **Step 2: Run tests and verify failures**

Run: `go test ./agent ./costtracker -run 'TestRunPublishes|TestZeroBudget|TestMaxTurns|TestCancelled|TestFinalTurn|TestFreshLedger' -count=1`

Expected: FAIL because typed terminal fields and Ledger do not exist.

- [ ] **Step 3: Implement concurrency-safe execution ledger**

```go
type Ledger struct {
    mu sync.RWMutex
    cost float64
    usage types.Usage
    modelUsage map[string]types.Usage
}

func (l *Ledger) Add(model string, usage types.Usage, cost float64) {
    l.mu.Lock()
    defer l.mu.Unlock()
    l.cost += cost
    l.usage.Add(usage)
    current := l.modelUsage[model]
    current.Add(usage)
    l.modelUsage[model] = current
}
```

Return deep-copy snapshots. The Run owns one Ledger; children receive its pointer. Agent aggregate metrics receive a completed snapshot but never drive Run admission.

- [ ] **Step 4: Implement exactly-once finish**

Use `sync.Once` in Run. Persist result on Run, emit exactly one result event, then close channels and release ownership.

```go
func (r *Run) finish(result *QueryResult, err error) {
    r.finishOnce.Do(func() {
        r.resultMu.Lock(); r.result, r.err = cloneResult(result), err; r.resultMu.Unlock()
        r.events <- result.SDKMessage()
        close(r.events)
        if err != nil { r.errs <- err }
        close(r.errs)
        close(r.done)
        r.session.releaseRun(r)
    })
}
```

Do not index the last message when history can be empty. Treat completion on the final allowed turn as success. Check context and budget immediately before every model admission and tool batch.

- [ ] **Step 5: Preserve blocking API errors and accounting**

`Run.Wait` waits on `done` and returns cloned terminal state. `Prompt` delegates to it instead of reconstructing a partial result from event fields. Use `%w` wrapping so callers can use `errors.Is`.

- [ ] **Step 6: Verify**

Run: `go test ./agent ./costtracker -count=1`

Expected: PASS.

Run: `go test -race ./agent ./costtracker -count=1`

Expected: PASS with no race report.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add costtracker/ledger.go costtracker/ledger_test.go agent/run_terminal_test.go types/message.go agent/run.go agent/loop.go agent/agent.go
git commit -m "feat: add explicit run terminal accounting"
```

### Task 6: Execute hooks at real lifecycle boundaries

**Files:**
- Create: `agent/hooks_integration_test.go`
- Modify: `hooks/hooks.go`
- Modify: `tools/executor.go`
- Modify: `agent/run.go`
- Modify: `agent/loop.go`

**Interfaces:**
- Extends: `tools.ExecutorOptions` with `Hooks *hooks.Manager`.
- Preserves: existing Go hook callbacks and `PostSampling` extension.
- Produces: explicit Session/Run execution of `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PostSampling`, `Stop`, `SubagentStart`, `SubagentStop`, `PermissionRequest`, and notification hooks.

- [ ] **Step 1: Write failing integration tests through Session, not Hook Manager alone**

```go
func TestPreToolHookBlocksExecutionAndPairsResult(t *testing.T) {
    called := atomic.Bool{}
    dangerous := &hookTestTool{name:"Danger", call:func(){ called.Store(true) }}
    provider := scriptedProvider(
        assistantToolCall("call-1", "Danger"),
        assistantText("handled"),
    )
    a := New(Options{
        ProviderClient: provider,
        CustomTools: []types.Tool{dangerous},
        PermissionMode: types.PermissionModeBypassPermissions,
        Hooks: hooks.HookConfig{PreToolUse: []hooks.HookRule{{Matcher:"Danger", HooksEx:[]hooks.HookFnEx{
            func(context.Context, *hooks.HookInput) (*hooks.HookOutput,error) {
                return &hooks.HookOutput{Decision:hooks.HookDecisionBlock, Reason:"blocked by host"}, nil
            },
        }}}},
    })
    result, err := a.Prompt(context.Background(), "run it")
    if err != nil || called.Load() { t.Fatalf("called=%v err=%v", called.Load(), err) }
    if !historyHasToolError(result.Messages, "call-1", "blocked by host") { t.Fatal("missing paired result") }
}
```

Add tests that SessionStart/SessionEnd bracket one Session lifetime,
UserPromptSubmit blocks before provider admission, UpdatedInput reaches the real
tool, post-success and post-failure choose the correct hook, timeout observes Run
cancellation, and Stop fires once on every terminal path.

- [ ] **Step 2: Run tests and verify hooks are currently never called**

Run: `go test ./agent -run 'TestPreToolHook|TestUserPromptHook|TestHookUpdated|TestPostTool|TestHookTimeout|TestStopHook' -count=1`

Expected: FAIL because Agent currently stores Hook Manager without invoking it.

- [ ] **Step 3: Pass hooks through the executor and run lifecycle**

Invoke permission-request hooks immediately before a permission callback. Invoke PreToolUse after permission input updates and before Tool.Call. If PreToolUse returns UpdatedInput, replace the call input. A block returns a normal paired error result and never invokes the tool. Execute exactly one of PostToolUse or PostToolUseFailure afterward.

Run UserPromptSubmit before appending the initial prompt. Run PostSampling after a complete provider response. Run Stop in the Run finalizer with the Run context replaced by a short cleanup context if the primary context is already cancelled.

- [ ] **Step 4: Fix per-rule timeout cancellation lifetime**

Current `defer cancel()` inside the rule loop retains timers until all rules finish. Wrap each rule execution in a helper so cancellation occurs at the end of that rule:

```go
func withRuleContext(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
    if timeout <= 0 { return fn(ctx) }
    hookCtx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    return fn(hookCtx)
}
```

- [ ] **Step 5: Verify**

Run: `go test ./hooks ./agent ./tools -count=1`

Expected: PASS.

Run: `go test -race ./hooks ./agent ./tools -count=1`

Expected: PASS with no race report.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/hooks_integration_test.go hooks/hooks.go tools/executor.go agent/run.go agent/loop.go
git commit -m "feat: integrate hooks with run lifecycle"
```

### Task 7: Runtime foundation documentation and full verification

**Files:**
- Create: `docs/runtime-concurrency.md`
- Create: `docs/migration-to-claudebuddy.md`
- Create: `examples/11-concurrent-sessions/main.go`
- Modify: `README.md`

**Interfaces:**
- Documents: Agent/Session/Run ownership, capacity limits, cancellation, fork behavior, terminal statuses, permission semantics, and breaking branding/module migration.
- Demonstrates: two independent Sessions running concurrently and collecting events safely.

- [ ] **Step 1: Add the runnable concurrency example**

```go
a := agent.New(agent.Options{MaxConcurrentRuns: 32})
defer a.Close()

var wg sync.WaitGroup
for _, id := range []string{"customer-a", "customer-b"} {
    session, err := a.NewSession(agent.SessionOptions{ID:id})
    if err != nil { log.Fatal(err) }
    wg.Add(1)
    go func(s *agent.Session) {
        defer wg.Done()
        result, err := s.Prompt(context.Background(), "Summarize this session")
        if err != nil { log.Printf("%s: %v", s.SessionID(), err); return }
        log.Printf("%s: %s", s.SessionID(), result.Text)
    }(session)
}
wg.Wait()
```

- [ ] **Step 2: Document observable behavior and migration**

README links to both new documents and uses only current ClaudeBuddy install and environment names. The migration document lists the former module/environment/context names only in a historical mapping table, as permitted by the spec. State explicitly that one Session has one active Run, not one Agent.

- [ ] **Step 3: Run formatting, tests, race detector, vet, and example builds**

Run: `rg -l --glob '*.go' . agent api costtracker hooks permissions tools types examples/11-concurrent-sessions | xargs gofmt -w`

Expected: no output.

Run: `go test ./... -count=1`

Expected: PASS with zero failures.

Run: `go test -race ./... -count=1`

Expected: PASS with no race report.

Run: `go vet ./...`

Expected: exit 0 with no diagnostics.

Run: `go build ./examples/...`

Expected: every example builds.

Run: `rg -n -i 'codeany' --glob '*.go' --glob 'go.mod' --glob 'README.md' --glob 'examples/**' --glob '!docs/migration-to-claudebuddy.md'`

Expected: no matches.

- [ ] **Step 4: Inspect the complete diff against the published baseline**

Run: `git diff fc32694 --stat && git diff fc32694 --check`

Expected: the stat contains only runtime-foundation tests/code/docs and `git diff --check` emits no whitespace errors.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/runtime-concurrency.md docs/migration-to-claudebuddy.md examples/11-concurrent-sessions/main.go
git commit -m "docs: explain concurrent session runtime"
```

- [ ] **Step 6: Push the verified phase**

```bash
git push origin main
```

Expected: the remote `main` advances to the verified local HEAD.
