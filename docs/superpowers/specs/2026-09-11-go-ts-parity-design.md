# ClaudeBuddy Go SDK parity design

## Objective

Bring the Go SDK as close as practical to the current TypeScript SDK v0.6.0 in
public capability and runtime behavior while retaining useful Go-only packages.
The implementation must use Go-native concurrency and lifecycle patterns rather
than translate TypeScript promises, iterators, or global maps literally.

This is also a complete product-name migration. Replace every `CODEANY` and
`codeany` occurrence in source, package paths, environment variables, context
file discovery, documentation, examples, and generated metadata with
`CLAUDEBUDDY` or `claudebuddy`. The module path becomes
`github.com/claudebuddy/claudebuddy-agent-sdk-go`. No legacy CodeAny alias is
retained; release notes must call out the breaking change.

## Compatibility baseline

- Preserve the existing high-level `agent.New`, `Agent.Query`, and `Agent.Prompt`
  entry points through a default session.
- Add Go-native runtime, session, and run APIs without copying TypeScript method
  shapes where Go has a clearer abstraction.
- Keep existing public Go packages unless they are unsafe or provably redundant.
- Default to the TypeScript runtime contracts where existing Go behavior is
  ambiguous: default maximum turns is 10, default permission mode remains bypass,
  and a configured zero-dollar budget prevents model admission.
- Do not make paid model requests in tests. Provider doubles and local HTTP/MCP
  servers cover transport behavior.
- Support the Go version declared in `go.mod`; do not require a newer language
  version without a concrete implementation need.

## Architecture

### Runtime, session, and run ownership

Split the current stateful Agent into three ownership levels:

1. `Agent` is a concurrency-safe runtime. It owns immutable configuration,
   provider factories or clients, MCP connection pools, plugins, and global
   concurrency limiters.
2. `Session` owns one conversation: normalized history, session-scoped built-in
   tool state, interaction broker, event journal, checkpoints, and metadata.
3. `Run` owns one execution: context, terminal state, cost/usage ledger,
   background task group, pending tool schedule, and event stream.

One Agent may execute many Sessions concurrently. A Session permits one active
history-writing Run. Concurrent branches use `Session.Fork` so two goroutines
never mutate the same history. Existing Agent methods delegate to a lazily created
default Session for source compatibility.

Never hold a session or runtime mutex during a provider request, hook, permission
callback, MCP call, or tool execution. Use narrow locks for maps and lifecycle
transitions, atomics for simple state, and bounded channels for queues. Every
goroutine must have a documented owner, cancellation path, and join path.

### Concurrency and backpressure

Expose finite limits for concurrent runs, model requests, tools, and background
tasks. Waiting for capacity must observe `context.Context`; configuration may
choose immediate rejection instead of waiting. Do not create an unbounded
goroutine per queued operation.

Within a Run, preserve model tool-call order. Only a contiguous group of tools
whose declarations are both read-only and concurrency-safe may execute in
parallel. Mutations, unknown tools, and tools without both guarantees are serial
barriers. Results remain in request order. Session state stores and registries
must be race safe, and the complete suite must pass the race detector.

### Provider boundary and streaming

Define a provider interface whose request carries model and fallback choice
per call; never mutate a shared client to select a fallback model. Both Anthropic
and OpenAI-compatible implementations must propagate context cancellation and
return an authoritative completed response only after a valid terminal frame.

Use a real SSE decoder that handles fragmented UTF-8, CRLF, comments, multiline
data, and torn streams. Assemble OpenAI tool calls by their actual index, allowing
interleaved argument deltas. Malformed or truncated tool input is an execution
error and is never dispatched. A stream that has emitted partial output is not
automatically retried.

When partial messages are enabled, emit incremental text and tool-input events for
presentation. The final assistant event remains authoritative. Consumer exit
cancels the run before the producer and background groups are joined so blocked
questions or transports cannot deadlock cleanup.

### Terminal results, cancellation, and budgets

Every Run publishes exactly one terminal result with an explicit subtype:
success, cancelled, maximum turns, maximum budget, or execution error. Preserve
errors, stop reason, permission denials, total usage, per-model usage, cost, and
durations in the blocking result and event stream.

Cancellation reaches provider transports, retry waits, permission and hook
contexts, tools, MCP operations, compaction, subagents, questions, and background
tasks. No new provider or tool operation starts after cancellation.

A Run ledger is shared by its goal rounds, compaction calls, and child agents.
Admission checks use that ledger and the configured budget. Parallel in-flight
requests may overshoot an estimate, so the budget is not presented as a billing
guarantee. Session and Agent aggregate statistics are separate from admission.

## Permissions, hooks, and tool state

Apply constructor and run-level allow/deny bounds to built-in, MCP, replacement,
and child tools. An explicit empty allow-list exposes no tools. Query overrides
may narrow but never widen constructor bounds.

- `plan` allows only declared read-only tools.
- `default`, `dontAsk`, and `auto` allow read-only or explicitly allowed tools;
  default/auto may ask the configured callback for other tools.
- `acceptEdits` additionally permits the concrete built-in edit/write/notebook
  tools, not custom tools that reuse those names.
- `bypassPermissions` remains unrestricted except for explicit deny bounds.
- A callback may further deny an operation but cannot override plan or deny lists.

Retain Go's dynamic permission rules and filesystem validator as additional policy
inputs. The filesystem validator is not an operating-system sandbox. Keep the
standalone sandbox validator, label it accurately, and do not claim isolation.
If enforcement is requested without an installed enforcement backend, fail before
the first model request.

Run all configured hooks at their documented lifecycle points. Hook waits use the
Run context and configured timeout. Pre-tool modifications are validated and
applied before execution; blocks generate paired tool results. Hook failures have
an explicit policy and are not silently converted into success. Add the lifecycle
events supported by TypeScript while retaining Go's `PostSampling` extension.

All mutable built-in tool state belongs to a Session. Child agents receive
explicitly shared state and inherited permission/tool bounds. No package-level
global state controls an Agent instance.

## Background work

Replace the independent Bash background map and TaskStore runtime fields with a
single session-scoped `TaskRuntime`. Both Bash and subagents register owned tasks
with status, bounded output, exit information, cancellation, and completion.

`TaskOutput` may wait with a finite timeout without cancelling the task.
`TaskStop` requests cancellation and the public stop method waits for settlement.
TaskUpdate cannot forge runtime-owned status or output. On normal completion the
Run drains owned work, emits task notifications, and includes child usage. On
cancellation, failure, turn exhaustion, budget exhaustion, or consumer exit it
cancels and joins owned work.

On POSIX, Bash runs in a process group; cancellation sends TERM and escalates to
KILL after a short grace period. Output capture keeps a bounded tail and is race
safe. Document the reduced Windows process-tree guarantee.

Cron definitions remain direct helper functionality only. Until a scheduler and
remote backend exist, Cron and RemoteTrigger placeholders are not advertised to
the model, and direct unsupported operations return an explicit error.

## Persistence and recovery

Session persistence uses two distinct stores:

- `CheckpointStore` writes normalized conversation snapshots during execution,
  before publishing semantic events, and in cleanup. The file implementation uses
  a temporary file, file sync, and atomic rename.
- `EventJournal` appends versioned semantic events with unique cursor IDs and
  timestamps. Reads tolerate a torn last line and the next append repairs it.

Resume never automatically repeats an unmatched tool call. It inserts an explicit
unknown-outcome result and asks the model to inspect external state. Checkpoints
and journal appends are separate writes, not an exactly-once transaction.

Use per-session writer locks rather than one global file lock. Hosts may disable
SDK persistence and supply initial normalized history. Retain the existing input
history store as an optional, separate user-experience feature.

Go's file-content checkpoint/rewind package remains separate from conversation
checkpoints. When enabled for a Session, edit tools record file states and expose
rewind through an explicit Session API. It stays opt-in because rewind is a
destructive filesystem action.

## Runtime interaction

Each active Session has a bounded interaction broker. A steering message receives
an immediate receipt ID and transitions through queued, applied, or not-applied.
FIFO messages pass user-prompt hooks and are inserted at safe boundaries: before a
model request, before starting a tool batch, and after a completed response.
Already running operations finish; unstarted superseded tool calls receive paired
skipped results. Unconsumed messages are marked not-applied at Run termination and
never carry into a later Run.

Interactive questions are opt-in. Emit question events with a session-scoped ID,
suggested options, and multiselect metadata. Session methods answer or cancel the
matching question. Free text is allowed; slices require multiselect. Timeout,
cancellation, run abort, and consumer exit remove pending state and emit an
explicit closure status. Timeout never chooses an answer. Legacy callback-style
question handlers remain supported and receive a cancellable context.

## Context management and advanced parity

Add automatic compaction with explicit success/failure, finite recovery attempts,
and pre/post hooks. Add micro-compaction for safe removal of redundant conversation
material. Context usage estimates drive admission conservatively but remain labeled
as estimates.

Add a general spill store for oversized tool output, with configurable inline and
preview limits, per-tool allow/deny selection, stable locators, and cleanup.
Read-like tools that would create a spill/read loop remain excluded by default.
Retain Bash's useful large-output behavior by routing it through the shared policy.

Add an LRU file-state cache for staleness checks and bound its memory. Add the
TypeScript skill registry, bundled skills, and Skill tool as Go interfaces and
session-scoped registries. Plugin manifests and skills coexist: a plugin may later
contribute skills, tools, or MCP definitions, but metadata-only plugins must not be
advertised as executable capability.

Add goal-driven execution and the internal update-goal tool. Goal rounds share the
Run ledger, cancellation, background-task group, and final result. Emit one public
aggregate terminal result; reports from another run cannot complete the current
goal.

## Go-only capabilities to retain and integrate

- Categorized context-usage reporting.
- File-content checkpoints and rewind.
- Rate-limit header tracking across supported windows.
- Persistable aggregate cost and operational metrics.
- MCP prompt APIs and reconnection.
- Dynamic permission rules and directory policy.
- Optional local input history.
- Plugin manifest discovery.
- In-process MCP SDK server and the web example.

These features remain independently usable. Agent integration must not turn an
optional helper into hidden global behavior.

## Branding migration

Perform a case-sensitive repository scan and replace all old branding:

- `CODEANY_*` becomes `CLAUDEBUDDY_*`.
- `.codeany` becomes `.claudebuddy`.
- `CODEANY.md` and `CODEANY.local.md` become `CLAUDEBUDDY.md` and
  `CLAUDEBUDDY.local.md`.
- `github.com/codeany-ai/open-agent-sdk-go` becomes
  `github.com/claudebuddy/claudebuddy-agent-sdk-go`.
- Product names, repository links, examples, comments, and user-visible strings
  use ClaudeBuddy.

Do not retain hidden fallbacks to the old environment variables or file names.
Historical migration notes and this design may name the former tokens solely to
explain the breaking change; production code, active configuration, examples, and
current usage documentation must contain only ClaudeBuddy names.

## Error handling

Use typed sentinel errors or error types for invalid state, admission rejection,
permission denial, incomplete stream, unknown tool outcome, question closure, and
terminal run status. Wrap underlying errors with `%w`. Do not match ordinary error
strings to control internal behavior when a typed alternative exists.

Channel ownership is unambiguous: the producer closes event/error channels, sends
at most one terminal error, and never closes a channel from a consumer path. Public
blocking methods drain the event stream and return the same terminal information
without losing provider errors.

Storage, hook, and cleanup errors are surfaced or joined with the primary error;
they are not discarded. Non-critical discovery failures may become warning events
when strict mode is disabled.

## Testing and verification

Every behavior change follows test-driven development. Core tests use provider
doubles and local servers; no credentials or paid requests are allowed.

Required coverage includes:

- many Sessions running concurrently on one Agent without history, model, budget,
  receipt, or task leakage;
- rejection of overlapping Runs on one Session and successful forked concurrency;
- permission bounds for every tool source and every mode;
- correct mutation barriers, safe parallel reads, result ordering, concurrency
  limits, and cancellation while waiting for capacity;
- Anthropic and OpenAI SSE fragmentation, interleaved tool arguments, malformed
  data, torn streams, usage, retry boundaries, and transport cancellation;
- exactly one terminal result for success, provider failure, cancellation, turn
  exhaustion, and budget exhaustion;
- background Bash and child execution, bounded output, wait timeout, process-group
  termination, drain/join behavior, and shared accounting;
- atomic checkpoints, torn journal tails, cursor replay, unknown tool outcomes,
  per-session file locking, and host-owned persistence;
- steering FIFO, safe-boundary replanning, receipts, questions, timeout,
  multiselect, child questions, and consumer early exit;
- compaction, spill, skills, goals, hook lifecycles, MCP reconnect/resources/prompts,
  rate-limit events, context usage, and file rewind;
- a scan asserting that no case-insensitive former-brand occurrence remains in
  production Go, active configuration, examples, or current usage documentation;
  historical migration notes and this design are the only permitted exceptions.

Run at minimum:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Format changed Go files with `gofmt`. Build all examples. Tests that require POSIX
process groups must skip with an explicit reason on unsupported platforms.

## Implementation decomposition

The work is intentionally split into four independently reviewable and releasable
subprojects. Each receives its own implementation plan and must leave all earlier
contracts passing:

1. **Runtime foundation:** branding migration, configuration validation,
   Agent/Session/Run ownership, typed terminal results, request-scoped models,
   budgets, cancellation, permission policy, hooks, and ordered tool scheduling.
2. **Transport and durability:** reliable Anthropic/OpenAI streaming, partial
   events, session checkpoints, event journal, recovery, MCP cancellation and
   reconnect integration.
3. **Background and interaction:** unified task runtime, Bash process lifecycle,
   background subagents, steering receipts, interactive questions, and child-event
   forwarding.
4. **Advanced parity and Go integrations:** compaction, spill, file cache, skills,
   goal execution, context usage, rate limits, file rewind, plugins, examples, and
   documentation.

These subprojects share the ownership model and public contracts in this umbrella
design, but can be accepted or rolled back independently. The first implementation
plan covers only the runtime foundation.

## Delivery order

1. Branding migration and typed configuration foundations.
2. Agent/Session/Run ownership, provider injection, terminal results, budgets,
   cancellation, permission policy, and ordered scheduler.
3. Reliable streaming and partial events.
4. Session persistence, journal recovery, and file checkpoint integration.
5. Unified background task runtime and subagent inheritance.
6. Runtime steering and interactive questions.
7. Compaction, spill, file cache, skills, and goal execution.
8. Go-only integrations, examples, README, migration notes, race tests, vet, and
   final repository scan.

Each step must leave the repository compiling and its focused tests passing. Avoid
unrelated refactors and do not remove a working Go-only public API merely because
the TypeScript package lacks it.
