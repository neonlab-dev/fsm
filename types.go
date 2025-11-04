package fsm

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrInvalidTransition is returned when there is no transition registered for (state,event).
	ErrInvalidTransition = errors.New("fsm: invalid transition")
	// ErrGuardRejected is returned when any guard fails.
	ErrGuardRejected = errors.New("fsm: guard rejected")
	// ErrNoSuchState is returned when a state has no transitions registered.
	ErrNoSuchState = errors.New("fsm: no such state")
	// ErrClosed indicates the FSM has been closed and no longer accepts events.
	ErrClosed = errors.New("fsm: closed")
)

// Comparable constrains State and Event to typical enum-friendly types.
type Comparable interface {
	~int | ~int32 | ~int64 | ~string
}

// GuardFn runs before actions; any non-nil error aborts the transition.
// Guards receive a pointer to the mutable session so updates to Ctx/StateTo persist.
type GuardFn[StateT, EventT Comparable, CtxT any] func(ctx context.Context, s *Session[StateT, EventT, CtxT]) error

// ActionFn runs as part of a transition. Actions are executed in order.
// Actions can mutate the session in-place so the storage layer observes the changes.
type ActionFn[StateT, EventT Comparable, CtxT any] func(ctx context.Context, s *Session[StateT, EventT, CtxT]) error

// Session contains transition-scoped data.
type Session[StateT, EventT Comparable, CtxT any] struct {
	ID        string
	StateFrom StateT
	StateTo   StateT
	Event     EventT
	Data      any // event payload (free-form)
	Ctx       CtxT

	fsm *FSM[StateT, EventT, CtxT]

	postEvents []queuedEvent[StateT, EventT, CtxT]
	ctxBefore  CtxT
}

// Storage persists (State, Ctx) per sessionID.
type Storage[StateT, EventT Comparable, CtxT any] interface {
	Load(ctx context.Context, sessionID string) (state StateT, userCtx CtxT, ok bool, err error)
	Save(ctx context.Context, sessionID string, state StateT, userCtx CtxT) error
}

// Observer exposes hooks for telemetry/logging.
type Observer[StateT, EventT Comparable, CtxT any] interface {
	OnTransition(ctx context.Context, s Session[StateT, EventT, CtxT])
	OnGuardRejected(ctx context.Context, s Session[StateT, EventT, CtxT], reason error)
	OnActionError(ctx context.Context, s Session[StateT, EventT, CtxT], err error)
	OnTimerSet(ctx context.Context, sessionID string, state StateT, d time.Duration, event EventT)
	OnTimerFired(ctx context.Context, sessionID string, event EventT)
	// OnTimerError receives any error returned when the delayed event dispatch fails.
	OnTimerError(ctx context.Context, sessionID string, event EventT, err error)
}

// Middleware wraps actions (e.g., tracing/recover/metrics).
type Middleware[StateT, EventT Comparable, CtxT any] func(next ActionFn[StateT, EventT, CtxT]) ActionFn[StateT, EventT, CtxT]

// TimerConfig schedules an Event after entering a state.
type TimerConfig[StateT, EventT Comparable] struct {
	After time.Duration
	Event EventT
}

// PersistErrorHandler allows callers to observe storage failures.
type PersistErrorHandler[StateT, EventT Comparable, CtxT any] func(ctx context.Context, s *Session[StateT, EventT, CtxT], persistErr error) error

type queuedEvent[StateT, EventT Comparable, CtxT any] struct {
	ctx   context.Context
	event EventT
	data  any
}
