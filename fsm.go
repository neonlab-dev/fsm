package fsm

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Config defines FSM construction-time options.
type Config[StateT, EventT Comparable, CtxT any] struct {
	Name           string
	Initial        StateT
	Storage        Storage[StateT, EventT, CtxT]
	Observer       Observer[StateT, EventT, CtxT]
	Middlewares    []Middleware[StateT, EventT, CtxT]
	OnPersistError PersistErrorHandler[StateT, EventT, CtxT]
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
	persistHook PersistErrorHandler[StateT, EventT, CtxT]

	timerMu      sync.Mutex
	activeTimers map[string][]*time.Timer // timers per session

	sessionLocks sync.Map // sessionID -> *sessionLock
	closed       atomic.Bool
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
		persistHook:  cfg.OnPersistError,
	}
}

// sessionLock reference-counts a mutex so we can clean up when idle.
type sessionLock struct {
	mu  sync.Mutex
	ref atomic.Int32
}

func (f *FSM[StateT, EventT, CtxT]) lockSession(sessionID string) func() {
	actual, _ := f.sessionLocks.LoadOrStore(sessionID, &sessionLock{})
	sl := actual.(*sessionLock)
	sl.ref.Add(1)
	sl.mu.Lock()

	return func() {
		sl.mu.Unlock()
		if sl.ref.Add(-1) == 0 {
			f.sessionLocks.Delete(sessionID)
		}
	}
}

// Send processes an Event for a given sessionID.
func (f *FSM[StateT, EventT, CtxT]) Send(ctx context.Context, sessionID string, event EventT, data any) error {
	if f.closed.Load() {
		return ErrClosed
	}
	unlock := f.lockSession(sessionID)
	lockHeld := true
	defer func() {
		if lockHeld {
			unlock()
		}
	}()

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
		return fmt.Errorf("%w: state=%v", ErrNoSuchState, cur)
	}
	tr, hasEvent := evMap[event]
	if !hasEvent || tr == nil {
		return fmt.Errorf("%w: state=%v event=%v", ErrInvalidTransition, cur, event)
	}

	// 3) Build session
	destState := cur
	if tr.to != nil {
		destState = *tr.to
	}

	s := &Session[StateT, EventT, CtxT]{
		ID:        sessionID,
		StateFrom: cur,
		StateTo:   destState, // expose intended destination early so guards/actions can react
		Event:     event,
		Data:      data,
		Ctx:       userCtx,
		fsm:       f,
		ctxBefore: userCtx,
	}

	// 4) Guards
	for _, g := range tr.guards {
		if err := g(ctx, s); err != nil {
			if f.observer != nil {
				f.observer.OnGuardRejected(ctx, *s, err)
			}
			return fmt.Errorf("%w: %w", ErrGuardRejected, err)
		}
	}

	// 5) Actions
	for _, a := range tr.actions {
		if err := a(ctx, s); err != nil {
			if f.observer != nil {
				f.observer.OnActionError(ctx, *s, err)
			}
			return err
		}
	}

	// 7) Persist
	if f.storage != nil {
		if err := f.storage.Save(ctx, sessionID, s.StateTo, s.Ctx); err != nil {
			s.Ctx = s.ctxBefore
			s.StateTo = s.StateFrom
			if f.persistHook != nil {
				if hookErr := f.persistHook(ctx, s, err); hookErr != nil {
					return hookErr
				}
			}
			return fmt.Errorf("fsm: storage save: %w", err)
		}
	}

	// 8) Timers
	f.resetAndScheduleTimers(ctx, sessionID, s.StateTo)

	// 9) Observer
	if f.observer != nil {
		f.observer.OnTransition(ctx, *s)
	}
	queued := append([]queuedEvent[StateT, EventT, CtxT](nil), s.postEvents...)
	s.postEvents = nil
	s.fsm = nil

	if lockHeld {
		unlock()
		lockHeld = false
	}

	for _, qe := range queued {
		if f.closed.Load() {
			return ErrClosed
		}
		if err := f.Send(qe.ctx, sessionID, qe.event, qe.data); err != nil {
			return err
		}
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
	timerCtx := context.WithoutCancel(ctx)
	for _, tc := range timers {
		tc := tc
		t := time.AfterFunc(tc.After, func() {
			if f.closed.Load() {
				return
			}
			if f.observer != nil {
				f.observer.OnTimerFired(timerCtx, sessionID, tc.Event)
			}
			if err := f.Send(timerCtx, sessionID, tc.Event, nil); err != nil && f.observer != nil {
				f.observer.OnTimerError(timerCtx, sessionID, tc.Event, err)
			}
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

// Close stops timers, waits for in-flight sessions to drain, and prevents new Sends.
func (f *FSM[StateT, EventT, CtxT]) Close(ctx context.Context) error {
	if !f.closed.CompareAndSwap(false, true) {
		return nil
	}
	f.timerMu.Lock()
	timers := f.activeTimers
	f.activeTimers = make(map[string][]*time.Timer)
	f.timerMu.Unlock()

	for _, arr := range timers {
		for _, t := range arr {
			t.Stop()
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		idle := true
		f.sessionLocks.Range(func(key, value any) bool {
			sl := value.(*sessionLock)
			if sl.ref.Load() == 0 {
				f.sessionLocks.Delete(key)
				return true
			}
			idle = false
			return true
		})
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
