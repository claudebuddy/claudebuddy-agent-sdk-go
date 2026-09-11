package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// Run owns one execution and its producer channels. Events has one consumer
// owner: call Events and drain it (or Cancel), or let Wait claim and drain it.
type Run struct {
	session     *Session
	ctx         context.Context
	cancel      context.CancelFunc
	events      chan types.SDKMessage
	errs        chan error
	done        chan struct{}
	eventOwner  atomic.Uint32 // 0 unclaimed, 1 Events caller, 2 Wait drainer
	finishOnce  sync.Once
	hasCapacity bool
	started     time.Time
	result      QueryResult // producer-only until done closes
	err         error
}

func newRun(s *Session, ctx context.Context) *Run {
	ctx, cancel := context.WithCancel(ctx)
	return &Run{session: s, ctx: ctx, cancel: cancel, events: make(chan types.SDKMessage, 64), errs: make(chan error, 1), done: make(chan struct{}), started: time.Now()}
}

// Events atomically claims the stream. Once claimed, the caller owns draining
// or cancellation. If Wait claimed first, Events returns an already closed stream.
func (r *Run) Events() <-chan types.SDKMessage {
	r.eventOwner.CompareAndSwap(0, 1)
	if r.eventOwner.Load() == 2 {
		closed := make(chan types.SDKMessage)
		close(closed)
		return closed
	}
	return r.events
}

// Err returns the producer's at-most-one error channel. Consuming it does not
// consume the cached error returned to every Wait caller.
func (r *Run) Err() <-chan error { return r.errs }
func (r *Run) Cancel()           { r.cancel() }

// Wait drains events only when no Events caller claimed them, joins execution,
// and returns an independent copy of the cached result. It is safe concurrently.
func (r *Run) Wait() (*QueryResult, error) {
	if r.eventOwner.CompareAndSwap(0, 2) {
		for range r.events {
		}
	}
	<-r.done
	if r.err != nil {
		return nil, r.err
	}
	result := r.result
	result.Messages = cloneMessages(r.result.Messages)
	return &result, nil
}

func (r *Run) emit(event types.SDKMessage) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	switch event.Type {
	case types.MessageTypeAssistant:
		if event.Message != nil {
			if text := types.ExtractText(event.Message); text != "" {
				r.result.Text = text
			}
		}
	case types.MessageTypeResult:
		if event.Usage != nil {
			r.result.Usage = *event.Usage
		}
		r.result.NumTurns = event.NumTurns
		r.result.Cost = event.Cost
	}
	// Consumers own their event data; mutation must not affect execution inputs.
	if event.Message != nil {
		message := cloneMessages([]types.Message{*event.Message})[0]
		event.Message = &message
	}
	if event.Usage != nil {
		usage := *event.Usage
		event.Usage = &usage
	}
	event.Messages = cloneMessages(event.Messages)
	select {
	case r.events <- event:
		return nil
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

// complete is the sole producer cleanup, also used by failed startup. Task 5
// adds terminal status/accounting here without changing channel ownership.
func (r *Run) complete(err error) {
	r.finishOnce.Do(func() {
		r.cancel()
		r.err = err
		r.result.Duration = time.Since(r.started)
		r.result.Messages = r.session.GetMessages()
		close(r.events)
		if err != nil {
			r.errs <- err
		}
		close(r.errs)
		s := r.session
		s.runMu.Lock()
		if r.hasCapacity {
			<-s.runtime.runSlots
		}
		if s.active == r {
			s.active = nil
		}
		close(r.done)
		s.runMu.Unlock()
	})
}
