package fsm

import (
	"context"
	"errors"
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

	const goroutines = 64
	const perGoroutine = 10

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
