# Concurrent runtime

ClaudeBuddy separates shared runtime resources from conversation state and one
execution:

```text
Agent
├── Session customer-a ── Run 1
├── Session customer-b ── Run 1
└── Session customer-c ── Run 1
```

An `Agent` is a concurrency-safe runtime. It owns the provider, MCP connections,
immutable configuration, and global capacity limits. A `Session` owns one
conversation history and its mutable built-in tool state. A `Run` owns one
execution context, event stream, terminal result, and accounting ledger.

One Agent can run many Sessions concurrently. One Session permits one active
history-writing Run. Use `Session.Fork` when two callers need independent branches
of the same history.

## Starting sessions and runs

```go
a := agent.New(agent.Options{
    MaxConcurrentRuns:  32,
    MaxConcurrentTools: 10,
})
defer a.Close()

s, err := a.NewSession(agent.SessionOptions{ID: "customer-a"})
if err != nil {
    return err
}

run, err := s.Start(ctx, "Summarize this repository")
if err != nil {
    return err
}

for event := range run.Events() {
    // Handle assistant, tool, and terminal result events.
}
result, err := run.Wait()
```

`Session.Prompt` is the blocking convenience API. `Session.Query` preserves the
legacy pair of event and error channels. The original `Agent.Prompt`, `Query`,
`GetMessages`, and `Clear` methods delegate to an owned default Session.

`Run.Events()` has one consumer owner. Once called, that consumer must drain the
channel or cancel the Run. If no consumer claims events, `Run.Wait()` drains them
internally. Multiple Wait callers receive independent result snapshots.

## Capacity and cancellation

`MaxConcurrentRuns` limits active Runs across all Sessions; values at or below
zero use the default of 32. `MaxConcurrentTools` limits each contiguous parallel
batch of declared read-only, concurrency-safe tools; values at or below zero use
the default of 10.

Capacity waits observe `context.Context` and do not create a goroutine for every
queued caller. Cancellation propagates into provider requests, tool calls, Hook
callbacks, MCP requests, child agents, and capacity waits. `Session.Close`
cancels and joins its active Run. `Agent.Close` first cancels all Sessions and
then joins them before closing shared MCP resources.

Injected providers, custom tools, and callbacks may be shared by concurrent
Sessions. Their implementations must therefore be concurrency-safe and honor
the supplied context.

## Tool ordering and permissions

The executor preserves model order. Only adjacent tools that declare both
`IsReadOnly(input) == true` and `IsConcurrencySafe(input) == true` run in
parallel. A mutation, unknown tool, or tool without both guarantees is a serial
barrier. Results are returned in original request order.

Permission bounds apply to model-visible schemas and are checked again against
the concrete tool. A non-nil empty `AllowedTools` slice exposes no tools. A
PreToolUse Hook may change input, but the changed input is rechecked against
immutable and dynamic bounds before execution.

## Terminal results and budgets

Every started Run emits exactly one terminal `result` event and caches the same
information for `Wait`:

- `success`
- `cancelled`
- `error_max_turns`
- `error_max_budget_usd`
- `error_during_execution`

The terminal record includes errors, stop reason, turns, duration, total usage,
per-model usage, approximate cost, permission denials, and a history snapshot.
An unsuccessful Wait returns both the result and an error, so callers can use
`errors.Is` without losing structured diagnostics.

Prefer `Budget: &agent.BudgetOptions{MaxUSD: value}`. A non-nil budget with zero
USD prevents the first provider admission. Positive `MaxBudgetUSD` remains for
source compatibility. Accounting belongs to the Run and is shared with child
agents; Session aggregates are reporting data and never control later Run
admission. Cost is an estimate, not a billing guarantee.

## Forking

`Session.Fork` deep-copies normalized history and creates fresh mutable built-in
tool state:

```go
branch, err := session.Fork(agent.SessionOptions{ID: "customer-a-experiment"})
```

Passing `History` to `Fork` is rejected. To create unrelated initial history,
pass it to `Agent.NewSession` instead.

