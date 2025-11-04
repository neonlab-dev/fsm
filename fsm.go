// Package fsm provides a typed finite-state machine with Go generics.
// Features:
//   - Strongly-typed State and Event (constraints over ~int/~string)
//   - Guards and Actions (with middleware support)
//   - Pluggable storage (persist State + Context per session)
//   - Timers fired on state entry (schedule an Event after a duration)
//   - Observability hooks (transition, guard, action, timer)
//   - In-memory storage and basic observers included
//
// Usage quickstart:
//
//	type State int
//	const ( S_INIT State = iota; S_READY )
//
//	type Event string
//	const ( E_START Event = "start" )
//
//	type Ctx struct{ Name string }
//
//	store := fsm.NewMemStorage[State, Event, *Ctx](S_INIT)
//	m := fsm.New(fsm.Config[State, Event, *Ctx]{ Name: "demo", Initial: S_INIT, Storage: store })
//	m.State(S_INIT).OnEvent(E_START).Action(func(ctx context.Context, s fsm.Session[State, Event, *Ctx]) error {
//	    if s.Ctx == nil { s.Ctx = &Ctx{} }
//	    s.Ctx.Name = "User"
//	    return nil
//	}).To(S_READY)
//	_ = m.Send(context.Background(), "session-1", E_START, nil)
package fsm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

var (
	// ErrInvalidTransition is returned when there is no transition registered for (state,event).
	ErrInvalidTransition = errors.New("fsm: invalid transition")
	// ErrGuardRejected is returned when any guard fails.
	ErrGuardRejected = errors.New("fsm: guard rejected")
	// ErrNoSuchState is returned when a state has no transitions registered.
	ErrNoSuchState = errors.New("fsm: no such state")
)

// Comparable constrains State and Event to typical enum-friendly types.
type Comparable interface{ ~int | ~int32 | ~int64 | ~string }

// GuardFn runs before actions; any non-nil error aborts the transition.
type GuardFn[StateT, EventT Comparable, CtxT any] func(ctx context.Context, s Session[StateT, EventT, CtxT]) error

// ActionFn runs as part of a transition. Actions are executed in order.
type ActionFn[StateT, EventT Comparable, CtxT any] func(ctx context.Context, s Session[StateT, EventT, CtxT]) error

// Session contains transition-scoped data.
type Session[StateT, EventT Comparable, CtxT any] struct {
	ID        string
	StateFrom StateT
	StateTo   StateT
	Event     EventT
	Data      any // event payload (free-form)
	Ctx       CtxT

	fsm *FSM[StateT, EventT, CtxT]
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
}

// Middleware wraps actions (e.g., tracing/recover/metrics).
type Middleware[StateT, EventT Comparable, CtxT any] func(next ActionFn[StateT, EventT, CtxT]) ActionFn[StateT, EventT, CtxT]

// TimerConfig schedules an Event after entering a state.
type TimerConfig[StateT, EventT Comparable] struct {
	After time.Duration
	Event EventT
}

// state/event builders (fluent API)
type stateBuilder[StateT, EventT Comparable, CtxT any] struct {
	parent *FSM[StateT, EventT, CtxT]
	state  StateT
}
type eventBuilder[StateT, EventT Comparable, CtxT any] struct {
	parent *FSM[StateT, EventT, CtxT]
	state  StateT
	event  EventT
	tr     *transition[StateT, EventT, CtxT]
}

type transition[StateT, EventT Comparable, CtxT any] struct {
	guards  []GuardFn[StateT, EventT, CtxT]
	actions []ActionFn[StateT, EventT, CtxT]
	to      *StateT // nil = self-loop
}

// Config defines FSM construction-time options.
type Config[StateT, EventT Comparable, CtxT any] struct {
	Name        string
	Initial     StateT
	Storage     Storage[StateT, EventT, CtxT]
	Observer    Observer[StateT, EventT, CtxT]
	Middlewares []Middleware[StateT, EventT, CtxT]
}

// FSM is a typed finite-state machine with timers and pluggable storage.
type FSM[StateT, EventT Comparable, CtxT any] struct {
	name    string
	initial StateT

	mu           sync.RWMutex
	transitions  map[StateT]map[EventT]*transition[StateT, EventT, CtxT]
	onEnterTimer map[StateT][]TimerConfig[StateT, EventT]

	storage     Storage[StateT, EventT, CtxT]
	observer    Observer[StateT, EventT, CtxT]
	middlewares []Middleware[StateT, EventT, CtxT]

	timerMu      sync.Mutex
	activeTimers map[string][]*time.Timer // timers per session
}

// New creates a new FSM instance.
func New[StateT, EventT Comparable, CtxT any](cfg Config[StateT, EventT, CtxT]) *FSM[StateT, EventT, CtxT] {
	return &FSM[StateT, EventT, CtxT]{
		name:         cfg.Name,
		initial:      cfg.Initial,
		transitions:  make(map[StateT]map[EventT]*transition[StateT, EventT, CtxT]),
		onEnterTimer: make(map[StateT][]TimerConfig[StateT, EventT]),
		storage:      cfg.Storage,
		observer:     cfg.Observer,
		middlewares:  cfg.Middlewares,
		activeTimers: make(map[string][]*time.Timer),
	}
}

// State returns a builder for defining transitions from the given state.
func (f *FSM[StateT, EventT, CtxT]) State(state StateT) *stateBuilder[StateT, EventT, CtxT] {
	return &stateBuilder[StateT, EventT, CtxT]{parent: f, state: state}
}

// OnEvent begins a new transition definition for (state,event).
func (b *stateBuilder[StateT, EventT, CtxT]) OnEvent(ev EventT) *eventBuilder[StateT, EventT, CtxT] {
	b.parent.mu.Lock()
	defer b.parent.mu.Unlock()

	if _, ok := b.parent.transitions[b.state]; !ok {
		b.parent.transitions[b.state] = make(map[EventT]*transition[StateT, EventT, CtxT])
	}
	tr := &transition[StateT, EventT, CtxT]{}
	b.parent.transitions[b.state][ev] = tr
	return &eventBuilder[StateT, EventT, CtxT]{parent: b.parent, state: b.state, event: ev, tr: tr}
}

// Guard appends a guard to the transition.
func (e *eventBuilder[StateT, EventT, CtxT]) Guard(g GuardFn[StateT, EventT, CtxT]) *eventBuilder[StateT, EventT, CtxT] {
	e.tr.guards = append(e.tr.guards, g)
	return e
}

// Action appends an action to the transition (wrapped by middlewares).
func (e *eventBuilder[StateT, EventT, CtxT]) Action(a ActionFn[StateT, EventT, CtxT]) *eventBuilder[StateT, EventT, CtxT] {
	wrapped := a
	for i := len(e.parent.middlewares) - 1; i >= 0; i-- {
		wrapped = e.parent.middlewares[i](wrapped)
	}
	e.tr.actions = append(e.tr.actions, wrapped)
	return e
}

// To sets the destination state for the transition (omit for self-loop).
func (e *eventBuilder[StateT, EventT, CtxT]) To(dst StateT) *FSM[StateT, EventT, CtxT] {
	e.tr.to = &dst
	return e.parent
}

// OnEnter registers timers that will fire after entering the state.
func (b *stateBuilder[StateT, EventT, CtxT]) OnEnter(timers ...TimerConfig[StateT, EventT]) *FSM[StateT, EventT, CtxT] {
	b.parent.mu.Lock()
	defer b.parent.mu.Unlock()
	b.parent.onEnterTimer[b.state] = append(b.parent.onEnterTimer[b.state], timers...)
	return b.parent
}

// WithTimer is a helper to create a TimerConfig.
func WithTimer[StateT, EventT Comparable](after time.Duration, event EventT) TimerConfig[StateT, EventT] {
	return TimerConfig[StateT, EventT]{After: after, Event: event}
}

// Send processes an Event for a given sessionID.
func (f *FSM[StateT, EventT, CtxT]) Send(ctx context.Context, sessionID string, event EventT, data any) error {
	// 1) Load state+ctx
	var cur StateT
	var userCtx CtxT
	var ok bool
	var err error

	if f.storage != nil {
		cur, userCtx, ok, err = f.storage.Load(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("fsm: storage load: %w", err)
		}
	}
	if !ok {
		cur = f.initial
	}

	// 2) Resolve transition
	f.mu.RLock()
	evMap, hasState := f.transitions[cur]
	f.mu.RUnlock()
	if !hasState {
		return ErrNoSuchState
	}
	tr, hasEvent := evMap[event]
	if !hasEvent || tr == nil {
		return ErrInvalidTransition
	}

	// 3) Build session
	s := Session[StateT, EventT, CtxT]{
		ID:        sessionID,
		StateFrom: cur,
		StateTo:   cur,
		Event:     event,
		Data:      data,
		Ctx:       userCtx,
		fsm:       f,
	}

	// 4) Guards
	for _, g := range tr.guards {
		if err := g(ctx, s); err != nil {
			if f.observer != nil {
				f.observer.OnGuardRejected(ctx, s, err)
			}
			return ErrGuardRejected
		}
	}

	// 5) Actions
	for _, a := range tr.actions {
		if err := a(ctx, s); err != nil {
			if f.observer != nil {
				f.observer.OnActionError(ctx, s, err)
			}
			return err
		}
	}

	// 6) Destination
	if tr.to != nil {
		s.StateTo = *tr.to
	}

	// 7) Persist
	if f.storage != nil {
		if err := f.storage.Save(ctx, sessionID, s.StateTo, s.Ctx); err != nil {
			return fmt.Errorf("fsm: storage save: %w", err)
		}
	}

	// 8) Timers
	f.resetAndScheduleTimers(ctx, sessionID, s.StateTo)

	// 9) Observer
	if f.observer != nil {
		f.observer.OnTransition(ctx, s)
	}
	return nil
}

func (f *FSM[StateT, EventT, CtxT]) resetAndScheduleTimers(ctx context.Context, sessionID string, newState StateT) {
	// cancel old timers
	f.timerMu.Lock()
	if arr, ok := f.activeTimers[sessionID]; ok {
		for _, t := range arr {
			t.Stop()
		}
	}
	delete(f.activeTimers, sessionID)
	f.timerMu.Unlock()

	// schedule new
	f.mu.RLock()
	timers := f.onEnterTimer[newState]
	f.mu.RUnlock()
	if len(timers) == 0 {
		return
	}

	var created []*time.Timer
	for _, tc := range timers {
		tc := tc
		t := time.AfterFunc(tc.After, func() {
			if f.observer != nil {
				f.observer.OnTimerFired(ctx, sessionID, tc.Event)
			}
			_ = f.Send(context.Background(), sessionID, tc.Event, nil)
		})
		created = append(created, t)
	}

	f.timerMu.Lock()
	f.activeTimers[sessionID] = created
	f.timerMu.Unlock()

	if f.observer != nil {
		for _, tc := range timers {
			f.observer.OnTimerSet(ctx, sessionID, newState, tc.After, tc.Event)
		}
	}
}

// ------------------------
// Built-in Storage/Observer
// ------------------------

// MemStorage is an in-memory Storage implementation for testing/dev.
type MemStorage[StateT, EventT Comparable, CtxT any] struct {
	mu      sync.RWMutex
	data    map[string]struct {
		State StateT
		Ctx   CtxT
	}
	initial StateT
	zero    CtxT
}

// NewMemStorage creates a memory-backed storage with the provided initial state.
func NewMemStorage[StateT, EventT Comparable, CtxT any](initial StateT) *MemStorage[StateT, EventT, CtxT] {
	return &MemStorage[StateT, EventT, CtxT]{
		data:    make(map[string]struct{ State StateT; Ctx CtxT }),
		initial: initial,
	}
}

// Load implements Storage.
func (m *MemStorage[StateT, EventT, CtxT]) Load(ctx context.Context, sessionID string) (StateT, CtxT, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.data[sessionID]; ok {
		return v.State, v.Ctx, true, nil
	}
	return m.initial, m.zero, false, nil
}

// Save implements Storage.
func (m *MemStorage[StateT, EventT, CtxT]) Save(ctx context.Context, sessionID string, state StateT, userCtx CtxT) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[sessionID] = struct {
		State StateT
		Ctx   CtxT
	}{State: state, Ctx: userCtx}
	return nil
}

// NoopObserver discards all observer events.
type NoopObserver[StateT, EventT Comparable, CtxT any] struct{}

func (NoopObserver[StateT, EventT, CtxT]) OnTransition(context.Context, Session[StateT, EventT, CtxT])                     {}
func (NoopObserver[StateT, EventT, CtxT]) OnGuardRejected(context.Context, Session[StateT, EventT, CtxT], error)         {}
func (NoopObserver[StateT, EventT, CtxT]) OnActionError(context.Context, Session[StateT, EventT, CtxT], error)           {}
func (NoopObserver[StateT, EventT, CtxT]) OnTimerSet(context.Context, string, StateT, time.Duration, EventT)            {}
func (NoopObserver[StateT, EventT, CtxT]) OnTimerFired(context.Context, string, EventT)                                  {}

// LogObserver writes simple logs via the standard library log package.
type LogObserver[StateT, EventT Comparable, CtxT any] struct{ Prefix string }

func (o LogObserver[StateT, EventT, CtxT]) pfx() string {
	if o.Prefix == "" { return "[FSM]" }
	return o.Prefix
}

func (o LogObserver[StateT, EventT, CtxT]) OnTransition(_ context.Context, s Session[StateT, EventT, CtxT]) {
	log.Printf("%s %v --%v--> %v (sid=%s)", o.pfx(), s.StateFrom, s.Event, s.StateTo, s.ID)
}
func (o LogObserver[StateT, EventT, CtxT]) OnGuardRejected(_ context.Context, s Session[StateT, EventT, CtxT], reason error) {
	log.Printf("%s guard rejected in %v on %v (sid=%s): %v", o.pfx(), s.StateFrom, s.Event, s.ID, reason)
}
func (o LogObserver[StateT, EventT, CtxT]) OnActionError(_ context.Context, s Session[StateT, EventT, CtxT], err error) {
	log.Printf("%s action error in %v on %v (sid=%s): %v", o.pfx(), s.StateFrom, s.Event, s.ID, err)
}
func (o LogObserver[StateT, EventT, CtxT]) OnTimerSet(_ context.Context, sid string, st StateT, d time.Duration, _ EventT) {
	log.Printf("%s timer set: sid=%s state=%v after=%s", o.pfx(), sid, st, d)
}
func (o LogObserver[StateT, EventT, CtxT]) OnTimerFired(_ context.Context, sid string, ev EventT) {
	log.Printf("%s timer fired: sid=%s event=%v", o.pfx(), sid, ev)
}
