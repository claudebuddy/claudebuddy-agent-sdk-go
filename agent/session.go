package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/costtracker"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/hooks"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
	"github.com/google/uuid"
)

// ErrSessionClosed indicates an explicitly closed conversation.
var ErrSessionClosed = errors.New("session is closed")

// SessionOptions selects an ID and an independently owned initial history.
type SessionOptions struct {
	ID      string
	History []types.Message
}

// Session owns conversation and built-in tool state. Only one Run may write its
// history at a time, including while waiting for Agent capacity.
type Session struct {
	runtime     *Agent
	id          string
	historyMu   sync.RWMutex
	messages    []types.Message
	runMu       sync.Mutex
	active      *Run
	closed      bool
	registry    *tools.Registry
	costTracker *costtracker.Tracker
	endOnce     sync.Once
	startDone   chan struct{}
	startOK     bool
}

// NewSession creates a conversation with fresh built-in state. Empty IDs are
// generated; duplicate IDs and invalid Agent options return ErrInvalidOptions.
func (a *Agent) NewSession(opts SessionOptions) (*Session, error) {
	if a.initErr != nil {
		return nil, a.initErr
	}
	return a.newSession(opts)
}

func (a *Agent) newSession(opts SessionOptions) (*Session, error) {
	if opts.ID == "" {
		opts.ID = uuid.New().String()
	}
	s := &Session{runtime: a, id: opts.ID, messages: cloneMessages(opts.History), costTracker: costtracker.NewTracker(opts.ID), startDone: make(chan struct{})}
	// Assembly calls custom tool metadata, so it must happen outside runtime locks.
	s.registry = a.newRegistry(s)
	a.sessionsMu.Lock()
	if a.closed.Load() {
		a.sessionsMu.Unlock()
		return nil, ErrAgentClosed
	}
	if _, exists := a.sessions[s.id]; exists {
		a.sessionsMu.Unlock()
		return nil, fmt.Errorf("%w: session ID %q already exists", ErrInvalidOptions, s.id)
	}
	for _, tool := range a.mcpTools {
		s.registry.RegisterNamed(tool.name, tool.tool)
	}
	a.sessions[s.id] = s
	a.sessionsMu.Unlock()
	start, err := a.hookManager.RunSessionStart(a.lifetime, s.id)
	if hookErr := hookOutcomeError(hooks.HookSessionStart, start, err); hookErr != nil {
		a.sessionsMu.Lock()
		if a.sessions[s.id] == s {
			delete(a.sessions, s.id)
		}
		a.sessionsMu.Unlock()
		s.runMu.Lock()
		s.closed = true
		s.runMu.Unlock()
		close(s.startDone)
		return s, hookErr
	}
	s.runMu.Lock()
	s.startOK = true
	closed := s.closed
	s.runMu.Unlock()
	close(s.startDone)
	if closed || a.closed.Load() {
		return s, ErrAgentClosed
	}
	return s, nil
}

// GetSession returns a registered Session, including one that has been closed.
func (a *Agent) GetSession(id string) (*Session, bool) {
	a.sessionsMu.RLock()
	defer a.sessionsMu.RUnlock()
	s, ok := a.sessions[id]
	return s, ok
}

// Start reserves this Session, then waits for bounded Agent capacity. Waiting
// occurs in the caller; no goroutine is created for a queued admission.
func (s *Session) Start(ctx context.Context, prompt string) (*Run, error) {
	a := s.runtime
	s.runMu.Lock()
	if a.closed.Load() {
		s.runMu.Unlock()
		return nil, ErrAgentClosed
	}
	if s.closed {
		s.runMu.Unlock()
		return nil, ErrSessionClosed
	}
	if a.initErr != nil {
		s.runMu.Unlock()
		return nil, a.initErr
	}
	if err := ctx.Err(); err != nil {
		s.runMu.Unlock()
		return nil, err
	}
	if s.active != nil {
		s.runMu.Unlock()
		return nil, ErrSessionBusy
	}
	r := newRun(s, ctx)
	s.active = r
	s.runMu.Unlock()

	select {
	case a.runSlots <- struct{}{}:
		r.hasCapacity = true
	case <-r.ctx.Done():
	}
	s.runMu.Lock()
	var err error
	switch {
	case a.closed.Load():
		err = ErrAgentClosed
	case s.closed:
		err = ErrSessionClosed
	default:
		err = r.ctx.Err()
	}
	s.runMu.Unlock()
	if err != nil {
		r.complete(err)
		return nil, err
	}
	// This goroutine belongs to Run. Cancel/Close cancel it; Wait/Close join it.
	go func() { r.complete(r.runLoop(prompt)) }()
	return r, nil
}

// Query starts streaming after capacity admission. The caller must drain events
// or cancel ctx; Prompt can be used when no event stream is needed.
func (s *Session) Query(ctx context.Context, prompt string) (<-chan types.SDKMessage, <-chan error) {
	r, err := s.Start(ctx, prompt)
	if err != nil {
		events := make(chan types.SDKMessage)
		errs := make(chan error, 1)
		errs <- err
		close(events)
		close(errs)
		return events, errs
	}
	return r.Events(), r.Err()
}

// Prompt starts a Run and waits for its result, draining its event stream.
func (s *Session) Prompt(ctx context.Context, prompt string) (*QueryResult, error) {
	r, err := s.Start(ctx, prompt)
	if err != nil {
		return nil, err
	}
	return r.Wait()
}

func (s *Session) SessionID() string { return s.id }

func (s *Session) GetMessages() []types.Message {
	s.historyMu.RLock()
	defer s.historyMu.RUnlock()
	return cloneMessages(s.messages)
}

func (s *Session) appendMessage(message types.Message) {
	s.historyMu.Lock()
	s.messages = append(s.messages, cloneMessages([]types.Message{message})[0])
	s.historyMu.Unlock()
}

// Clear resets idle history. It is a deterministic no-op while a Run is reserved
// or active, so an in-flight conversation cannot lose its tool/result pairing.
func (s *Session) Clear() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.active != nil {
		return
	}
	s.historyMu.Lock()
	s.messages = nil
	s.historyMu.Unlock()
}

// Fork creates a fresh Session with a deep snapshot of this history and fresh
// built-in state. History overrides are rejected; ID may name the new branch.
func (s *Session) Fork(opts SessionOptions) (*Session, error) {
	if opts.History != nil {
		return nil, fmt.Errorf("%w: Fork does not accept a History override", ErrInvalidOptions)
	}
	s.runMu.Lock()
	closed := s.closed
	s.runMu.Unlock()
	if closed {
		return nil, ErrSessionClosed
	}
	opts.History = s.GetMessages()
	return s.runtime.NewSession(opts)
}

func (s *Session) requestClose() *Run {
	s.runMu.Lock()
	s.closed = true
	r := s.active
	s.runMu.Unlock()
	if r != nil {
		r.Cancel()
	}
	return r
}

// Close cancels and joins an active Run or a Start waiting for Agent capacity.
// Custom providers and tools must honor their contexts for shutdown to finish.
func (s *Session) Close() {
	if r := s.requestClose(); r != nil {
		<-r.done
	}
	<-s.startDone
	if !s.startOK {
		return
	}
	s.endOnce.Do(func() {
		ctx, cancel := hookCleanupContext(s.runtime.lifetime)
		defer cancel()
		_, _ = s.runtime.hookManager.RunSessionEnd(ctx, s.id)
	})
}

func cloneMessages(messages []types.Message) []types.Message {
	if messages == nil {
		return nil
	}
	out := make([]types.Message, len(messages))
	for i, m := range messages {
		out[i] = m
		out[i].Content = cloneBlocks(m.Content)
		if m.Usage != nil {
			u := *m.Usage
			out[i].Usage = &u
		}
	}
	return out
}
func cloneBlocks(blocks []types.ContentBlock) []types.ContentBlock {
	if blocks == nil {
		return nil
	}
	out := make([]types.ContentBlock, len(blocks))
	for i, b := range blocks {
		out[i] = b
		if b.Input != nil {
			out[i].Input = cloneCollection(reflect.ValueOf(b.Input)).Interface().(map[string]interface{})
		}
		out[i].Content = cloneBlocks(b.Content)
		if b.Source != nil {
			src := *b.Source
			out[i].Source = &src
		}
	}
	return out
}

// JSON-shaped collections may also contain typed slices/maps. Preserve their
// Go types instead of a JSON round-trip that changes numbers and drops fields.
func cloneCollection(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneCollection(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneCollection(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneCollection(v.Index(i)))
		}
		return out
	default:
		return v
	}
}
