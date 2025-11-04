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
	sessionWG    sync.WaitGroup
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
	f.sessionWG.Add(1)
	sl.mu.Lock()

	return func() {
		sl.mu.Unlock()
		if sl.ref.Add(-1) == 0 {
			f.sessionLocks.Delete(sessionID)
		}
		f.sessionWG.Done()
	}
}

// Send processes an Event for a given sessionID.
func (f *FSM[StateT, EventT, CtxT]) Send(ctx context.Context, sessionID string, event EventT, data any) error {
	if f.closed.Load() {
		return ErrClosed
	}
	queue := []queuedEvent[StateT, EventT, CtxT]{{
		ctx:   ctx,
		event: event,
		data:  data,
	}}
	first := true

	for len(queue) > 0 {
		qe := queue[0]
		queue = queue[1:]
		if !first && f.closed.Load() {
			return ErrClosed
		}
		next, err := f.processEvent(qe.ctx, sessionID, qe.event, qe.data)
		if err != nil {
			return err
		}
		queue = append(queue, next...)
		first = false
	}
	return nil
}

func (f *FSM[StateT, EventT, CtxT]) processEvent(ctx context.Context, sessionID string, event EventT, data any) ([]queuedEvent[StateT, EventT, CtxT], error) {
	if ctx == nil {
		ctx = context.Background()
	}
	unlock := f.lockSession(sessionID)
	defer unlock()

	var cur StateT
	var userCtx CtxT
	var ok bool
	var err error

	if f.storage != nil {
		cur, userCtx, ok, err = f.storage.Load(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("fsm: storage load: %w", err)
		}
	}
	if !ok {
		cur = f.initial
	}

	f.mu.RLock()
	evMap, hasState := f.transitions[cur]
	f.mu.RUnlock()
	if !hasState {
		return nil, fmt.Errorf("%w: state=%v", ErrNoSuchState, cur)
	}
	tr, hasEvent := evMap[event]
	if !hasEvent || tr == nil {
		return nil, fmt.Errorf("%w: state=%v event=%v", ErrInvalidTransition, cur, event)
	}

	destState := cur
	if tr.to != nil {
		destState = *tr.to
	}

	s := &Session[StateT, EventT, CtxT]{
		ID:        sessionID,
		StateFrom: cur,
		StateTo:   destState,
		Event:     event,
		Data:      data,
		Ctx:       userCtx,
		fsm:       f,
		ctxBefore: userCtx,
	}

	for _, g := range tr.guards {
		if err := g(ctx, s); err != nil {
			if f.observer != nil {
				f.observer.OnGuardRejected(ctx, *s, err)
			}
			return nil, fmt.Errorf("%w: %w", ErrGuardRejected, err)
		}
	}

	for _, a := range tr.actions {
		if err := a(ctx, s); err != nil {
			if f.observer != nil {
				f.observer.OnActionError(ctx, *s, err)
			}
			return nil, err
		}
	}

	if f.storage != nil {
		if err := f.storage.Save(ctx, sessionID, s.StateTo, s.Ctx); err != nil {
			s.Ctx = s.ctxBefore
			s.StateTo = s.StateFrom
			if f.persistHook != nil {
				if hookErr := f.persistHook(ctx, s, err); hookErr != nil {
					return nil, hookErr
				}
			}
			return nil, fmt.Errorf("fsm: storage save: %w", err)
		}
	}

	f.resetAndScheduleTimers(ctx, sessionID, s.StateTo)

	if f.observer != nil {
		f.observer.OnTransition(ctx, *s)
	}

	queued := append([]queuedEvent[StateT, EventT, CtxT](nil), s.postEvents...)
	s.postEvents = nil
	s.fsm = nil

	return queued, nil
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

	waitCh := make(chan struct{})
	go func() {
		f.sessionWG.Wait()
		close(waitCh)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitCh:
	}
	// ensure map cleanup for any idle entries
	f.sessionLocks.Range(func(key, value any) bool {
		sl := value.(*sessionLock)
		if sl.ref.Load() == 0 {
			f.sessionLocks.Delete(key)
		}
		return true
	})
	return nil
}
