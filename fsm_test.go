package fsm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionMutationPersistsAndStateToEarly(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit  State = "init"
		stateReady State = "ready"

		eventStart Event = "start"
	)

	type ctxData struct {
		Name   string
		SeenTo State
	}

	store := NewMemStorage[State, Event, ctxData](stateInit)
	var recordedGuardState State

	m := New(Config[State, Event, ctxData]{
		Name:    "mutation",
		Initial: stateInit,
		Storage: store,
	})

	guardErr := errors.New("guard fail")
	m.State(stateInit).OnEvent(eventStart).
		Guard(func(ctx context.Context, s *Session[State, Event, ctxData]) error {
			recordedGuardState = s.StateTo
			return nil
		}).
		Action(func(ctx context.Context, s *Session[State, Event, ctxData]) error {
			s.Ctx.Name = "user"
			s.Ctx.SeenTo = s.StateTo
			if s.StateTo != stateReady {
				t.Fatalf("action saw unexpected StateTo: %v", s.StateTo)
			}
			return nil
		}).
		To(stateReady)

	if err := m.Send(context.Background(), "session-1", eventStart, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	state, ctxValue, ok, err := store.Load(context.Background(), "session-1")
	if err != nil || !ok {
		t.Fatalf("Load failed: %v ok=%v", err, ok)
	}
	if state != stateReady {
		t.Fatalf("want state %v, got %v", stateReady, state)
	}
	if ctxValue.Name != "user" {
		t.Fatalf("want ctx.Name=user, got %q", ctxValue.Name)
	}
	if ctxValue.SeenTo != stateReady {
		t.Fatalf("want ctx.SeenTo=%v, got %v", stateReady, ctxValue.SeenTo)
	}
	if recordedGuardState != stateReady {
		t.Fatalf("guard expected to see StateTo=%v, got %v", stateReady, recordedGuardState)
	}

	// ensure guard errors propagate alongside sentinel
	m.State(stateReady).OnEvent(eventStart).Guard(func(ctx context.Context, s *Session[State, Event, ctxData]) error {
		return guardErr
	})
	err = m.Send(context.Background(), "session-1", eventStart, nil)
	if !errors.Is(err, ErrGuardRejected) {
		t.Fatalf("expected ErrGuardRejected, got %v", err)
	}
	if !errors.Is(err, guardErr) {
		t.Fatalf("expected wrapped guard error, got %v", err)
	}
}

func TestConcurrentSendSerialized(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		evTick    Event = "tick"
	)

	type ctxData struct {
		Count int
	}

	store := NewMemStorage[State, Event, *ctxData](stateInit)

	m := New(Config[State, Event, *ctxData]{
		Name:    "concurrency",
		Initial: stateInit,
		Storage: store,
	})

	m.State(stateInit).OnEvent(evTick).Action(func(ctx context.Context, s *Session[State, Event, *ctxData]) error {
		if s.Ctx == nil {
			s.Ctx = &ctxData{}
		}
		s.Ctx.Count++
		return nil
	})

	const goroutines = 32
	const perGoroutine = 8

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if err := m.Send(context.Background(), "shared-session", evTick, nil); err != nil {
					t.Errorf("Send failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	_, ctxValue, ok, err := store.Load(context.Background(), "shared-session")
	if err != nil || !ok {
		t.Fatalf("Load failed: err=%v ok=%v", err, ok)
	}
	want := goroutines * perGoroutine
	if ctxValue == nil || ctxValue.Count != want {
		t.Fatalf("want count %d, got %v", want, ctxValue)
	}
}

type testObserver[StateT, EventT Comparable, CtxT any] struct {
	timerFired chan struct{}
	timerError chan error
}

func (o testObserver[StateT, EventT, CtxT]) OnTransition(context.Context, Session[StateT, EventT, CtxT]) {
}
func (o testObserver[StateT, EventT, CtxT]) OnGuardRejected(context.Context, Session[StateT, EventT, CtxT], error) {
}
func (o testObserver[StateT, EventT, CtxT]) OnActionError(context.Context, Session[StateT, EventT, CtxT], error) {
}
func (o testObserver[StateT, EventT, CtxT]) OnTimerSet(context.Context, string, StateT, time.Duration, EventT) {
}
func (o testObserver[StateT, EventT, CtxT]) OnTimerFired(context.Context, string, EventT) {
	if o.timerFired != nil {
		o.timerFired <- struct{}{}
	}
}
func (o testObserver[StateT, EventT, CtxT]) OnTimerError(_ context.Context, _ string, _ EventT, err error) {
	if o.timerError != nil {
		o.timerError <- err
	}
}

func TestTimerErrorObserved(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		stateNext State = "next"

		eventStart Event = "start"
		eventTimer Event = "timer"
	)

	type ctxData struct{}

	timerFired := make(chan struct{}, 1)
	timerErrors := make(chan error, 1)

	store := NewMemStorage[State, Event, ctxData](stateInit)
	m := New(Config[State, Event, ctxData]{
		Name:    "timer",
		Initial: stateInit,
		Storage: store,
		Observer: testObserver[State, Event, ctxData]{
			timerFired: timerFired,
			timerError: timerErrors,
		},
	})

	m.State(stateInit).OnEvent(eventStart).Action(func(ctx context.Context, s *Session[State, Event, ctxData]) error {
		return nil
	}).To(stateNext)

	m.State(stateNext).OnEnter(WithTimer[State, Event](5*time.Millisecond, eventTimer))

	if err := m.Send(context.Background(), "session-timer", eventStart, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case <-timerFired:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timer did not fire")
	}

	select {
	case err := <-timerErrors:
		if !errors.Is(err, ErrNoSuchState) {
			t.Fatalf("expected ErrNoSuchState, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected timer error observer call")
	}
}

func TestFSMCloseStopsTimers(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		stateNext State = "next"

		eventStart  Event = "start"
		eventExpire Event = "expire"
	)

	store := NewMemStorage[State, Event, struct{}](stateInit)
	timerFired := make(chan struct{}, 1)

	m := New(Config[State, Event, struct{}]{
		Name:    "close",
		Initial: stateInit,
		Storage: store,
		Observer: testObserver[State, Event, struct{}]{
			timerFired: timerFired,
		},
	})

	m.State(stateInit).OnEvent(eventStart).To(stateNext)
	m.State(stateNext).OnEnter(WithTimer[State, Event](50*time.Millisecond, eventExpire))
	m.State(stateNext).OnEvent(eventExpire).Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		t.Fatalf("expire action executed after Close")
		return nil
	})

	if err := m.Send(context.Background(), "sid", eventStart, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case <-timerFired:
		t.Fatalf("timer fired after Close")
	case <-time.After(120 * time.Millisecond):
	}

	if err := m.Send(context.Background(), "sid", eventStart, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestDispatchQueuesEvent(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit  State = "init"
		stateReady State = "ready"
		stateDone  State = "done"

		eventStart Event = "start"
		eventNext  Event = "next"
	)

	type ctxData struct {
		Steps []string
	}

	store := NewMemStorage[State, Event, ctxData](stateInit)

	m := New(Config[State, Event, ctxData]{
		Name:    "dispatch",
		Initial: stateInit,
		Storage: store,
	})

	m.State(stateInit).OnEvent(eventStart).Action(func(ctx context.Context, s *Session[State, Event, ctxData]) error {
		s.Ctx.Steps = append(s.Ctx.Steps, "start")
		if err := s.Dispatch(ctx, eventNext, nil); err != nil {
			return err
		}
		return nil
	}).To(stateReady)

	m.State(stateReady).OnEvent(eventNext).Action(func(ctx context.Context, s *Session[State, Event, ctxData]) error {
		s.Ctx.Steps = append(s.Ctx.Steps, "next")
		return nil
	}).To(stateDone)

	if err := m.Send(context.Background(), "sid", eventStart, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	gotState, ctxValue, ok, err := store.Load(context.Background(), "sid")
	if err != nil || !ok {
		t.Fatalf("Load failed: err=%v ok=%v", err, ok)
	}
	if gotState != stateDone {
		t.Fatalf("expected final state %v, got %v", stateDone, gotState)
	}
	if len(ctxValue.Steps) != 2 || ctxValue.Steps[1] != "next" {
		t.Fatalf("dispatch sequence not executed, ctx=%v", ctxValue)
	}
}

func TestOnEventDuplicatePanics(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		eventGo   Event = "go"
	)

	m := New(Config[State, Event, struct{}]{
		Name:    "panic",
		Initial: stateInit,
	})

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on duplicate transition")
		}
	}()

	m.State(stateInit).OnEvent(eventGo)
	m.State(stateInit).OnEvent(eventGo)
}

func TestPersistSaveFailureHandled(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		stateNext State = "next"

		eventStart Event = "start"
	)

	type ctxData struct {
		Value string
	}

	boom := errors.New("boom")

	store := &failingStorage[State, Event, ctxData]{
		initialState: stateInit,
		err:          boom,
	}

	hookCalled := false

	m := New(Config[State, Event, ctxData]{
		Name:    "persist",
		Initial: stateInit,
		Storage: store,
		OnPersistError: func(ctx context.Context, s *Session[State, Event, ctxData], persistErr error) error {
			hookCalled = true
			if s.StateTo != s.StateFrom {
				t.Fatalf("expected StateTo rollback, got %v -> %v", s.StateFrom, s.StateTo)
			}
			if s.Ctx != (ctxData{}) {
				t.Fatalf("expected ctx rollback, got %+v", s.Ctx)
			}
			if !errors.Is(persistErr, boom) {
				t.Fatalf("unexpected persist error %v", persistErr)
			}
			return nil
		},
	})

	m.State(stateInit).OnEvent(eventStart).Action(func(ctx context.Context, s *Session[State, Event, ctxData]) error {
		s.Ctx.Value = "updated"
		return nil
	}).To(stateNext)

	err := m.Send(context.Background(), "sid", eventStart, nil)
	if err == nil || !strings.Contains(err.Error(), "storage save") {
		t.Fatalf("expected storage save error, got %v", err)
	}
	if !hookCalled {
		t.Fatalf("persist error hook not invoked")
	}
	curState, ctxValue, ok, loadErr := store.Load(context.Background(), "sid")
	if loadErr != nil || !ok {
		t.Fatalf("Load failed err=%v ok=%v", loadErr, ok)
	}
	if curState != stateInit {
		t.Fatalf("state changed despite save failure, got %v", curState)
	}
	if ctxValue != (ctxData{}) {
		t.Fatalf("ctx mutated despite rollback, got %+v", ctxValue)
	}
}

func TestInvalidTransitionErrorAnnotated(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		eventGo   Event = "go"
		eventStay Event = "stay"
	)

	m := New(Config[State, Event, struct{}]{
		Name:    "invalid",
		Initial: stateInit,
	})

	m.State(stateInit).OnEvent(eventStay).To(stateInit)

	err := m.Send(context.Background(), "sid", eventGo, nil)
	if err == nil || !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
	if !strings.Contains(err.Error(), "state=init") || !strings.Contains(err.Error(), "event=go") {
		t.Fatalf("missing state/event context in error: %v", err)
	}
}

type failingStorage[StateT, EventT Comparable, CtxT any] struct {
	initialState StateT
	storedState  StateT
	storedCtx    CtxT
	err          error
}

func (f *failingStorage[StateT, EventT, CtxT]) Load(ctx context.Context, sessionID string) (StateT, CtxT, bool, error) {
	return f.initialState, f.storedCtx, true, nil
}

func (f *failingStorage[StateT, EventT, CtxT]) Save(ctx context.Context, sessionID string, state StateT, userCtx CtxT) error {
	return f.err
}
