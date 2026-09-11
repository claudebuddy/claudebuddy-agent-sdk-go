package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/api"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// Providers are the offline boundary; histories, admission, tools and lifecycle
// below are the real runtime. Every blocking fixture observes the test context.
type sessionProvider struct {
	respond func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error)
	usage   *types.Usage
}

func (p *sessionProvider) CreateMessage(ctx context.Context, req api.MessagesRequest) (*api.StreamMessage, error) {
	return nil, errors.New("stream only")
}
func (p *sessionProvider) CreateMessageStream(ctx context.Context, req api.MessagesRequest) (<-chan api.StreamEvent, <-chan error) {
	blocks, err := p.respond(ctx, req)
	events := make(chan api.StreamEvent, len(blocks)+2)
	errs := make(chan error, 1)
	if err != nil {
		errs <- err
	} else {
		events <- api.StreamEvent{Type: "message_start", Message: &api.StreamMessage{Role: "assistant", Model: req.Model, Usage: p.usage}}
		for i := range blocks {
			b := blocks[i]
			events <- api.StreamEvent{Type: "content_block_start", Index: i, ContentBlock: &b}
		}
		events <- api.StreamEvent{Type: "message_delta", Delta: map[string]interface{}{"stop_reason": "end_turn"}}
	}
	close(events)
	close(errs)
	return events, errs
}
func sessionTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func awaitSessionValue[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("timed out waiting for runtime progress")
		var z T
		return z
	}
}
func replyBlocks(text string) []types.ContentBlock {
	return []types.ContentBlock{{Type: types.ContentBlockText, Text: text}}
}
func lastPrompt(req api.MessagesRequest) string {
	return types.ExtractText(&types.Message{Content: req.Messages[len(req.Messages)-1].Content})
}
func newBlockingSessionProvider(entered chan<- string, release <-chan struct{}) *sessionProvider {
	return &sessionProvider{respond: func(ctx context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		prompt := lastPrompt(req)
		select {
		case entered <- prompt:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-release:
			return replyBlocks("reply:" + prompt), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
}
func mustSession(t *testing.T, a *Agent, id string) *Session {
	t.Helper()
	s, err := a.NewSession(SessionOptions{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func mustStart(t *testing.T, s *Session, ctx context.Context, prompt string) *Run {
	t.Helper()
	r, err := s.Start(ctx, prompt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAgentRunsIndependentSessionsConcurrently(t *testing.T) {
	ctx := sessionTestContext(t)
	entered := make(chan string, 2)
	release := make(chan struct{})
	a := New(Options{ProviderClient: newBlockingSessionProvider(entered, release), MaxConcurrentRuns: 2})
	defer a.Close()
	one := mustSession(t, a, "one")
	two := mustSession(t, a, "two")
	r1 := mustStart(t, one, ctx, "alpha")
	r2 := mustStart(t, two, ctx, "beta")
	awaitSessionValue(t, ctx, entered)
	awaitSessionValue(t, ctx, entered)
	close(release)
	got1, e1 := r1.Wait()
	got2, e2 := r2.Wait()
	if e1 != nil || e2 != nil || got1.Text != "reply:alpha" || got2.Text != "reply:beta" {
		t.Fatalf("results: %+v %+v errors: %v %v", got1, got2, e1, e2)
	}
	if len(one.GetMessages()) != 2 || len(two.GetMessages()) != 2 {
		t.Fatal("history not isolated")
	}
}
func TestSessionRejectsOverlappingRuns(t *testing.T) {
	ctx := sessionTestContext(t)
	entered := make(chan string, 1)
	release := make(chan struct{})
	a := New(Options{ProviderClient: newBlockingSessionProvider(entered, release)})
	defer a.Close()
	s := mustSession(t, a, "same")
	r := mustStart(t, s, ctx, "first")
	awaitSessionValue(t, ctx, entered)
	if _, err := s.Start(ctx, "second"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("got %v", err)
	}
	close(release)
	if _, err := r.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt(ctx, "third"); err != nil {
		t.Fatal(err)
	}
}
func TestSessionReservesBeforeCapacityAndReleasesOnCancellation(t *testing.T) {
	ctx := sessionTestContext(t)
	entered := make(chan string, 2)
	release := make(chan struct{})
	a := New(Options{ProviderClient: newBlockingSessionProvider(entered, release), MaxConcurrentRuns: 1})
	defer a.Close()
	one := mustSession(t, a, "one")
	two := mustSession(t, a, "two")
	r := mustStart(t, one, ctx, "first")
	awaitSessionValue(t, ctx, entered)
	queuedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	queued := make(chan error, 1)
	go func() { _, err := two.Start(queuedCtx, "queued"); queued <- err }()
	// Observe reservation without guessing scheduler timing.
	for {
		two.runMu.Lock()
		reserved := two.active != nil
		two.runMu.Unlock()
		if reserved {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("not reserved")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := two.Start(ctx, "overlap"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("got %v", err)
	}
	cancel()
	if err := awaitSessionValue(t, ctx, queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	select {
	case name := <-entered:
		t.Fatalf("queued provider admitted: %s", name)
	default:
	}
	close(release)
	if _, err := r.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Prompt(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
}
func TestRunWaitDrainsUnclaimedEventsAndCachesForAllWaiters(t *testing.T) {
	ctx := sessionTestContext(t)
	p := &sessionProvider{respond: func(ctx context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) == 1 {
			b := make([]types.ContentBlock, 100)
			for i := range b {
				b[i] = types.ContentBlock{Type: types.ContentBlockToolUse, ID: fmt.Sprint(i), Name: "missing", Input: map[string]interface{}{}}
			}
			return b, nil
		}
		return replyBlocks("done"), nil
	}}
	a := New(Options{ProviderClient: p})
	defer a.Close()
	r := mustStart(t, mustSession(t, a, "wait"), ctx, "go")
	const n = 8
	results := make(chan *QueryResult, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { res, err := r.Wait(); results <- res; errs <- err }()
	}
	for i := 0; i < n; i++ {
		res := awaitSessionValue(t, ctx, results)
		if err := awaitSessionValue(t, ctx, errs); err != nil {
			t.Fatal(err)
		}
		if res.Text != "done" {
			t.Fatalf("result %+v", res)
		}
		res.Messages[0].Content[0].Text = "mutated"
	}
	res, err := r.Wait()
	if err != nil || res.Messages[0].Content[0].Text != "go" {
		t.Fatalf("cached result aliased: %+v %v", res, err)
	}
}
func TestRunClaimedEventsRequireDrainOrCancellation(t *testing.T) {
	ctx := sessionTestContext(t)
	p := &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) {
		b := make([]types.ContentBlock, 100)
		for i := range b {
			b[i] = types.ContentBlock{Type: types.ContentBlockToolUse, ID: fmt.Sprint(i), Name: "missing"}
		}
		return b, nil
	}}
	a := New(Options{ProviderClient: p})
	defer a.Close()
	r := mustStart(t, mustSession(t, a, "claim"), ctx, "go")
	events := r.Events()
	done := make(chan error, 1)
	go func() { _, err := r.Wait(); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("Wait stole claimed stream: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	r.Cancel()
	if err := awaitSessionValue(t, ctx, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error %v", err)
	}
	for range events {
	}
}
func TestRunErrorsAreCachedEvenWhenErrChannelConsumed(t *testing.T) {
	ctx := sessionTestContext(t)
	boom := errors.New("offline provider failure")
	a := New(Options{ProviderClient: &sessionProvider{respond: func(context.Context, api.MessagesRequest) ([]types.ContentBlock, error) { return nil, boom }}})
	defer a.Close()
	r := mustStart(t, mustSession(t, a, "error"), ctx, "go")
	if err := awaitSessionValue(t, ctx, r.Err()); !errors.Is(err, boom) {
		t.Fatalf("Err: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Wait(); !errors.Is(err, boom) {
			t.Fatalf("Wait: %v", err)
		}
	}
}
func TestAgentCloseCancelsRunsAndQueuedStarts(t *testing.T) {
	ctx := sessionTestContext(t)
	entered := make(chan string, 1)
	a := New(Options{ProviderClient: newBlockingSessionProvider(entered, make(chan struct{})), MaxConcurrentRuns: 1})
	one := mustSession(t, a, "one")
	two := mustSession(t, a, "two")
	r := mustStart(t, one, ctx, "go")
	awaitSessionValue(t, ctx, entered)
	queued := make(chan error, 1)
	go func() { _, err := two.Start(ctx, "queued"); queued <- err }()
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	awaitSessionValue(t, ctx, closed)
	if _, err := r.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("active error %v", err)
	}
	if err := awaitSessionValue(t, ctx, queued); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("queued error %v", err)
	}
	if _, err := a.NewSession(SessionOptions{}); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := one.Start(ctx, "late"); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("Start: %v", err)
	}
	a.Close()
}
func TestLegacyAgentUsesDefaultSession(t *testing.T) {
	ctx := sessionTestContext(t)
	a := New(Options{ProviderClient: &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		return replyBlocks(fmt.Sprint(len(req.Messages))), nil
	}}})
	defer a.Close()
	s, ok := a.GetSession(a.SessionID())
	if !ok {
		t.Fatal("default session missing")
	}
	first, err := a.Prompt(ctx, "first")
	if err != nil || first.Text != "1" {
		t.Fatalf("first %+v %v", first, err)
	}
	events, errs := a.Query(ctx, "second")
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(s.GetMessages()) != 4 || len(a.GetMessages()) != 4 {
		t.Fatal("legacy history diverged")
	}
	a.Clear()
	if len(s.GetMessages()) != 0 {
		t.Fatal("Clear did not delegate")
	}
}
func TestSessionsHaveFreshBuiltinStateAndCopiedCustomTools(t *testing.T) {
	ctx := sessionTestContext(t)
	custom := tools.NewFileReadTool()
	a := New(Options{ProviderClient: &recordingProvider{}, CustomTools: []types.Tool{custom}})
	defer a.Close()
	one := mustSession(t, a, "one")
	two := mustSession(t, a, "two")
	one.registry.Get("Config").Call(ctx, map[string]interface{}{"action": "set", "key": "only-one", "value": "secret"}, nil)
	res, err := two.registry.Get("Config").Call(ctx, map[string]interface{}{"action": "list"}, nil)
	if err != nil || strings.Contains(res.Content[0].Text, "secret") {
		t.Fatalf("shared config %+v %v", res, err)
	}
	if one.registry.Get(custom.Name()) != custom || two.registry.Get(custom.Name()) != custom {
		t.Fatal("custom reference missing")
	}
}

func TestSessionForkAndSnapshotsDeepCopyHistory(t *testing.T) {
	ctx := sessionTestContext(t)
	a := New(Options{ProviderClient: &recordingProvider{}})
	defer a.Close()
	history := []types.Message{{Role: "assistant", Content: []types.ContentBlock{{Type: types.ContentBlockToolUse, Input: map[string]interface{}{"nested": map[string]interface{}{"value": "original"}}}}, Usage: &types.Usage{InputTokens: 7}}}
	s, err := a.NewSession(SessionOptions{ID: "source", History: history})
	if err != nil {
		t.Fatal(err)
	}
	history[0].Content[0].Input["nested"].(map[string]interface{})["value"] = "caller"
	fork, err := s.Fork(SessionOptions{ID: "fork"})
	if err != nil {
		t.Fatal(err)
	}
	snap := s.GetMessages()
	snap[0].Content[0].Input["nested"].(map[string]interface{})["value"] = "snapshot"
	snap[0].Usage.InputTokens = 99
	for _, session := range []*Session{s, fork} {
		got := session.GetMessages()
		if got[0].Content[0].Input["nested"].(map[string]interface{})["value"] != "original" || got[0].Usage.InputTokens != 7 {
			t.Fatalf("aliased history %+v", got)
		}
	}
	if _, err := fork.Prompt(ctx, "branch"); err != nil {
		t.Fatal(err)
	}
	if len(s.GetMessages()) != 1 {
		t.Fatal("fork appended to source")
	}
	if _, err := s.Fork(SessionOptions{History: []types.Message{}}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("history override %v", err)
	}
	if _, err := a.NewSession(SessionOptions{ID: "source"}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("duplicate ID %v", err)
	}
}
func TestSessionCloseCancelsQueuedStartAndClearWhileBusyIsNoop(t *testing.T) {
	ctx := sessionTestContext(t)
	entered := make(chan string, 1)
	a := New(Options{ProviderClient: newBlockingSessionProvider(entered, make(chan struct{})), MaxConcurrentRuns: 1})
	defer a.Close()
	one := mustSession(t, a, "one")
	two := mustSession(t, a, "two")
	r := mustStart(t, one, ctx, "preserved")
	awaitSessionValue(t, ctx, entered)
	one.Clear()
	if len(one.GetMessages()) != 1 {
		t.Fatal("Clear erased active history")
	}
	queued := make(chan error, 1)
	go func() { _, err := two.Start(ctx, "queued"); queued <- err }()
	for {
		two.runMu.Lock()
		reserved := two.active != nil
		two.runMu.Unlock()
		if reserved {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("not reserved")
		case <-time.After(time.Millisecond):
		}
	}
	closed := make(chan struct{})
	go func() { two.Close(); close(closed) }()
	awaitSessionValue(t, ctx, closed)
	if err := awaitSessionValue(t, ctx, queued); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("queued error %v", err)
	}
	if _, err := two.Start(ctx, "late"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed error %v", err)
	}
	one.Close()
	if _, err := r.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error %v", err)
	}
	two.Close()
}
func TestNonPositiveConcurrencyLimitsDefaultWithoutBlockingRuns(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx := sessionTestContext(t)
			a := New(Options{ProviderClient: &recordingProvider{}, MaxConcurrentRuns: limit, MaxConcurrentTools: limit})
			defer a.Close()
			if _, err := a.Prompt(ctx, "go"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdjacentBuiltinReadsShareSynchronizedFileState(t *testing.T) {
	ctx := sessionTestContext(t)
	path := filepath.Join(t.TempDir(), "read.txt")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) > 1 {
			return replyBlocks("read done"), nil
		}
		b := make([]types.ContentBlock, 100)
		for i := range b {
			b[i] = types.ContentBlock{Type: types.ContentBlockToolUse, ID: fmt.Sprint(i), Name: "Read", Input: map[string]interface{}{"file_path": path}}
		}
		return b, nil
	}}
	a := New(Options{ProviderClient: p, MaxConcurrentTools: 10})
	defer a.Close()
	res, err := a.Prompt(ctx, "read all")
	if err != nil || res.Text != "read done" {
		t.Fatalf("result %+v %v", res, err)
	}
}

type sessionCallTool struct {
	name string
	call func(context.Context, map[string]interface{}, *types.ToolUseContext) (*types.ToolResult, error)
}

func (t *sessionCallTool) Name() string        { return t.name }
func (t *sessionCallTool) Description() string { return "offline test tool" }
func (t *sessionCallTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{Type: "object"}
}
func (t *sessionCallTool) IsReadOnly(map[string]interface{}) bool        { return true }
func (t *sessionCallTool) IsConcurrencySafe(map[string]interface{}) bool { return true }
func (t *sessionCallTool) Call(ctx context.Context, input map[string]interface{}, tc *types.ToolUseContext) (*types.ToolResult, error) {
	return t.call(ctx, input, tc)
}

func TestConsumerEventMutationCannotChangeToolInput(t *testing.T) {
	ctx := sessionTestContext(t)
	changed := make(chan struct{})
	tool := &sessionCallTool{name: "ReadValue", call: func(ctx context.Context, input map[string]interface{}, _ *types.ToolUseContext) (*types.ToolResult, error) {
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &types.ToolResult{Content: replyBlocks(input["value"].(string))}, nil
	}}
	p := &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) > 1 {
			return replyBlocks(req.Messages[len(req.Messages)-1].Content[0].Content[0].Text), nil
		}
		return []types.ContentBlock{{Type: types.ContentBlockToolUse, ID: "call", Name: tool.Name(), Input: map[string]interface{}{"value": "original"}}}, nil
	}}
	a := New(Options{ProviderClient: p, CustomTools: []types.Tool{tool}})
	defer a.Close()
	r := mustStart(t, mustSession(t, a, "events"), ctx, "go")
	events := r.Events()
	first := awaitSessionValue(t, ctx, events)
	first.Message.Content[0].Input["value"] = "mutated"
	close(changed)
	for range events {
	}
	res, err := r.Wait()
	if err != nil || res.Text != "original" {
		t.Fatalf("event changed execution: %+v %v", res, err)
	}
}

// The helper process speaks only local stdio JSON-RPC; no network or credentials.
func TestSessionMCPHelperProcess(t *testing.T) {
	if os.Getenv("CLAUDEBUDDY_TEST_SESSION_MCP") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		if req.ID == 0 {
			continue
		}
		var result interface{} = map[string]interface{}{}
		switch req.Method {
		case "tools/list":
			result = map[string]interface{}{"tools": []interface{}{map[string]interface{}{"name": "echo", "description": "offline echo", "inputSchema": map[string]interface{}{"type": "object"}}}}
		case "tools/call":
			text := "alive"
			if value, ok := req.Params.Arguments["text"].(string); ok {
				text = value
			}
			result = map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": text}}}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}
func TestMCPInitPreservesConnectionAndPublishesToSessions(t *testing.T) {
	ctx := sessionTestContext(t)
	a := New(Options{ProviderClient: &recordingProvider{}, MCPServers: map[string]types.MCPServerConfig{"local": {Command: os.Args[0], Args: []string{"-test.run=^TestSessionMCPHelperProcess$"}, Env: map[string]string{"CLAUDEBUDDY_TEST_SESSION_MCP": "1"}}}})
	defer a.Close()
	before := mustSession(t, a, "before")
	initCtx, cancelInit := context.WithCancel(ctx)
	if err := a.Init(initCtx); err != nil {
		t.Fatal(err)
	}
	cancelInit()
	after := mustSession(t, a, "after")
	// Let any erroneous Init cancellation reach the subprocess before calling it.
	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, s := range []*Session{before, after} {
		tool := s.registry.Get("mcp__local__echo")
		if tool == nil {
			t.Fatal("MCP tool not published")
		}
		res, err := tool.Call(ctx, map[string]interface{}{}, nil)
		if err != nil || res == nil || len(res.Content) == 0 || res.Content[0].Text != "alive" {
			t.Fatalf("MCP failed after Init: %+v %v", res, err)
		}
	}
}

func TestConcurrentSessionsShareMCPWithoutCrossWiredReplies(t *testing.T) {
	ctx := sessionTestContext(t)
	provider := &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) > 1 {
			return replyBlocks(req.Messages[len(req.Messages)-1].Content[0].Content[0].Text), nil
		}
		return []types.ContentBlock{{Type: types.ContentBlockToolUse, ID: "echo", Name: "mcp__local__echo", Input: map[string]interface{}{"text": lastPrompt(req)}}}, nil
	}}
	a := New(Options{ProviderClient: provider, MaxConcurrentRuns: 8, MCPServers: map[string]types.MCPServerConfig{"local": {Command: os.Args[0], Args: []string{"-test.run=^TestSessionMCPHelperProcess$"}, Env: map[string]string{"CLAUDEBUDDY_TEST_SESSION_MCP": "1"}}}})
	defer a.Close()
	if err := a.Init(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		s := mustSession(t, a, fmt.Sprint(i))
		go func(s *Session) {
			result, err := s.Prompt(ctx, s.SessionID())
			if err == nil && result.Text != s.SessionID() {
				err = fmt.Errorf("session %s got %s", s.SessionID(), result.Text)
			}
			done <- err
		}(s)
	}
	for i := 0; i < 8; i++ {
		if err := awaitSessionValue(t, ctx, done); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentToolConcurrencyLimitAndCallbackReentry(t *testing.T) {
	ctx := sessionTestContext(t)
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var active, peak atomic.Int32
	tool := &sessionCallTool{name: "ParallelRead", call: func(ctx context.Context, _ map[string]interface{}, _ *types.ToolUseContext) (*types.ToolResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
			return &types.ToolResult{Content: replyBlocks("ok")}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	var a *Agent
	p := &sessionProvider{respond: func(_ context.Context, req api.MessagesRequest) ([]types.ContentBlock, error) {
		if len(req.Messages) > 1 {
			return replyBlocks("done"), nil
		}
		b := make([]types.ContentBlock, 3)
		for i := range b {
			b[i] = types.ContentBlock{Type: types.ContentBlockToolUse, ID: fmt.Sprint(i), Name: tool.Name()}
		}
		return b, nil
	}}
	a = New(Options{ProviderClient: p, CustomTools: []types.Tool{tool}, MaxConcurrentTools: 2, CanUseTool: func(types.Tool, map[string]interface{}) (*types.PermissionDecision, error) {
		s, ok := a.GetSession(a.SessionID())
		if !ok {
			return nil, errors.New("missing session")
		}
		_ = s.GetMessages()
		s.Clear()
		return &types.PermissionDecision{Behavior: types.PermissionAllow}, nil
	}})
	defer a.Close()
	done := make(chan error, 1)
	go func() { _, err := a.Prompt(ctx, "go"); done <- err }()
	awaitSessionValue(t, ctx, entered)
	awaitSessionValue(t, ctx, entered)
	select {
	case <-entered:
		t.Fatal("exceeded tool concurrency")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := awaitSessionValue(t, ctx, done); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatalf("peak %d", peak.Load())
	}
}

func TestRegistryConcurrentPublicationAndReentrantFilter(t *testing.T) {
	ctx := sessionTestContext(t)
	reg := tools.NewRegistry()
	tool := tools.NewFileReadTool()
	reg.Register(tool)
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					reg.Register(tool)
					reg.Get("Read")
					reg.Names()
					reg.Filter(func(types.Tool) bool { reg.Register(tool); return true })
				}
			}()
		}
		wg.Wait()
		close(done)
	}()
	awaitSessionValue(t, ctx, done)
	if reg.Get("Read") != tool {
		t.Fatal("lost published tool")
	}
}

func TestConcurrentSessionLookupForkAndClose(t *testing.T) {
	ctx := sessionTestContext(t)
	a := New(Options{ProviderClient: &recordingProvider{}})
	defer a.Close()
	source := mustSession(t, a, "source")
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				s, err := source.Fork(SessionOptions{ID: fmt.Sprint(index)})
				if err != nil {
					return
				}
				a.GetSession(s.SessionID())
				s.GetMessages()
				s.Close()
			}(i)
		}
		wg.Wait()
		close(done)
	}()
	awaitSessionValue(t, ctx, done)
}
