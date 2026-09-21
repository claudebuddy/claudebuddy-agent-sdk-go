package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/costtracker"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/hooks"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

var (
	ErrMaxTurns  = errors.New("maximum turns reached")
	ErrMaxBudget = errors.New("maximum budget reached")
)

type ledgerContextKey struct{}

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
	ledger      *costtracker.Ledger
	result      QueryResult // producer-only until done closes
	err         error
}

func newRun(s *Session, ctx context.Context) *Run {
	ledger := s.runtime.opts.sharedLedger
	if ledger == nil {
		ledger = costtracker.NewLedger()
	}
	ctx = context.WithValue(ctx, ledgerContextKey{}, ledger)
	ctx, cancel := context.WithCancel(ctx)
	return &Run{session: s, ctx: ctx, cancel: cancel, events: make(chan types.SDKMessage, 64), errs: make(chan error, 1), done: make(chan struct{}), started: time.Now(), ledger: ledger}
}

func ledgerFromContext(ctx context.Context) *costtracker.Ledger {
	ledger, _ := ctx.Value(ledgerContextKey{}).(*costtracker.Ledger)
	return ledger
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
	result := cloneQueryResult(r.result)
	return &result, r.err
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
		r.result.Subtype = event.Subtype
		r.result.IsError = event.IsError
		r.result.Errors = append([]string(nil), event.Errors...)
		r.result.StopReason = event.StopReason
		r.result.ModelUsage = cloneModelUsage(event.ModelUsage)
		r.result.PermissionDenials = append([]types.PermissionDenial(nil), event.PermissionDenials...)
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
	event.Errors = append([]string(nil), event.Errors...)
	event.ModelUsage = cloneModelUsage(event.ModelUsage)
	event.PermissionDenials = append([]types.PermissionDenial(nil), event.PermissionDenials...)
	select {
	case r.events <- event:
		return nil
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

// complete is the sole producer cleanup, also used by failed startup. It
// publishes exactly one authoritative terminal event before closing channels.
func (r *Run) complete(err error) {
	r.finishOnce.Do(func() {
		var errorMessages []string
		if err != nil {
			errorMessages = append(errorMessages, err.Error())
		}
		cleanupCtx, cancel := hookCleanupContext(r.ctx)
		stop, hookErr := r.session.runtime.hookManager.RunStop(cleanupCtx)
		cancel()
		if outcomeErr := hookOutcomeError(hooks.HookStop, stop, hookErr); outcomeErr != nil {
			errorMessages = append(errorMessages, outcomeErr.Error())
			err = errors.Join(err, outcomeErr)
		}
		snapshot := r.ledger.Snapshot()
		r.result.Duration = time.Since(r.started)
		r.result.Messages = r.session.GetMessages()
		r.result.Usage = snapshot.Usage
		r.result.ModelUsage = snapshot.ModelUsage
		r.result.Cost = snapshot.Cost
		r.result.Subtype, r.result.IsError = resultStatus(err)
		r.result.Errors = errorMessages
		r.err = err
		terminalUsage := r.result.Usage
		terminal := types.SDKMessage{
			Type:              types.MessageTypeResult,
			Text:              r.result.Text,
			Subtype:           r.result.Subtype,
			IsError:           r.result.IsError,
			Errors:            append([]string(nil), r.result.Errors...),
			StopReason:        r.result.StopReason,
			Usage:             &terminalUsage,
			ModelUsage:        cloneModelUsage(r.result.ModelUsage),
			PermissionDenials: append([]types.PermissionDenial(nil), r.result.PermissionDenials...),
			NumTurns:          r.result.NumTurns,
			Duration:          r.result.Duration.Milliseconds(),
			Messages:          cloneMessages(r.result.Messages),
			Cost:              r.result.Cost,
		}
		r.emitTerminal(terminal)
		r.cancel()
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

func hookOutcomeError(event hooks.HookEvent, result *hooks.HookResult, err error) error {
	if err != nil {
		return fmt.Errorf("%s hook: %w", event, err)
	}
	if result != nil && result.Blocked {
		reason := result.Message
		if reason == "" {
			reason = "blocked"
		}
		return fmt.Errorf("%s hook: %s", event, reason)
	}
	return nil
}

func hookCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil || ctx.Err() != nil {
		return context.WithTimeout(context.Background(), 2*time.Second)
	}
	return context.WithTimeout(ctx, 2*time.Second)
}

func resultStatus(err error) (types.ResultSubtype, bool) {
	switch {
	case err == nil:
		return types.ResultSuccess, false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return types.ResultCancelled, true
	case errors.Is(err, ErrMaxTurns):
		return types.ResultErrorMaxTurns, true
	case errors.Is(err, ErrMaxBudget):
		return types.ResultErrorMaxBudget, true
	default:
		return types.ResultErrorDuringExecution, true
	}
}

// A terminal event must survive cancellation and a full progress buffer. Since
// Run is the sole producer, discarding the oldest buffered progress event is
// safe and guarantees consumers can always observe the authoritative result.
func (r *Run) emitTerminal(event types.SDKMessage) {
	for {
		select {
		case r.events <- event:
			return
		default:
		}
		select {
		case <-r.events:
		default:
		}
	}
}

func cloneQueryResult(result QueryResult) QueryResult {
	result.Messages = cloneMessages(result.Messages)
	result.Errors = append([]string(nil), result.Errors...)
	result.ModelUsage = cloneModelUsage(result.ModelUsage)
	result.PermissionDenials = append([]types.PermissionDenial(nil), result.PermissionDenials...)
	return result
}

func cloneModelUsage(usage map[string]types.Usage) map[string]types.Usage {
	if usage == nil {
		return nil
	}
	cloned := make(map[string]types.Usage, len(usage))
	for model, value := range usage {
		cloned[model] = value
	}
	return cloned
}

func (r *Run) budgetLimit() (float64, bool) {
	if budget := r.session.runtime.opts.Budget; budget != nil {
		return budget.MaxUSD, true
	}
	if limit := r.session.runtime.opts.MaxBudgetUSD; limit > 0 {
		return limit, true
	}
	return 0, false
}

func (r *Run) checkAdmission() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if limit, configured := r.budgetLimit(); configured && r.ledger.Snapshot().Cost >= limit {
		return fmt.Errorf("%w: limit $%.6f", ErrMaxBudget, limit)
	}
	return nil
}
