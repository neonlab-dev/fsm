package fsm

import (
	"bytes"
	"context"
	"errors"
	"log"
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

func TestCloseWaitsForInFlightSessions(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		eventWork Event = "work"
	)

	m := New(Config[State, Event, struct{}]{
		Name:    "close-wait",
		Initial: stateInit,
	})

	started := make(chan struct{})
	release := make(chan struct{})

	m.State(stateInit).OnEvent(eventWork).Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		close(started)
		<-release
		return nil
	})

	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		if err := m.Send(context.Background(), "sid", eventWork, nil); err != nil {
			t.Errorf("Send failed: %v", err)
		}
	}()

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("action did not start")
	}

	closeErr := make(chan error, 1)
	go func() {
		closeErr <- m.Close(context.Background())
	}()

	select {
	case err := <-closeErr:
		t.Fatalf("Close returned prematurely: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	if err := <-closeErr; err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	sendWG.Wait()
}

func TestCloseRespectsContextDeadline(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		eventWork Event = "work"
	)

	m := New(Config[State, Event, struct{}]{
		Name:    "close-deadline",
		Initial: stateInit,
	})

	block := make(chan struct{})
	started := make(chan struct{})

	m.State(stateInit).OnEvent(eventWork).Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		close(started)
		<-block
		return nil
	})

	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		if err := m.Send(context.Background(), "sid", eventWork, nil); err != nil && !errors.Is(err, ErrClosed) {
			t.Errorf("Send failed: %v", err)
		}
	}()

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("action did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := m.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}

	close(block)
	sendWG.Wait()
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second close should succeed: %v", err)
	}
}

func TestQueuedEventsProcessedIteratively(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		eventNext Event = "next"
	)

	const chain = 1024

	m := New(Config[State, Event, struct{}]{
		Name:    "queue-iter",
		Initial: stateInit,
	})

	var processed int
	m.State(stateInit).OnEvent(eventNext).Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		processed++
		if processed < chain {
			return s.Dispatch(ctx, eventNext, nil)
		}
		return nil
	})

	if err := m.Send(context.Background(), "sid", eventNext, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if processed != chain {
		t.Fatalf("expected %d processed events, got %d", chain, processed)
	}
}

func TestSessionLockCleanupAfterSend(t *testing.T) {
	type State string
	type Event string

	m := New(Config[State, Event, struct{}]{
		Name:    "lock-cleanup",
		Initial: "init",
	})

	m.State("init").OnEvent("noop")

	if err := m.Send(context.Background(), "sid", "noop", nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if _, ok := m.sessionLocks.Load("sid"); ok {
		t.Fatalf("session lock for sid should be cleaned up")
	}
}

func TestTimersClearedWhenLeavingTimedState(t *testing.T) {
	type State string
	type Event string

	const (
		stateIdle  State = "idle"
		stateTimed State = "timed"
		eventGo    Event = "go"
		eventStop  Event = "stop"
	)

	store := NewMemStorage[State, Event, struct{}](stateIdle)
	m := New(Config[State, Event, struct{}]{
		Name:    "timer-clean",
		Initial: stateIdle,
		Storage: store,
	})

	m.State(stateIdle).OnEvent(eventGo).To(stateTimed)
	m.State(stateTimed).OnEnter(WithTimer[State, Event](1*time.Hour, eventStop))
	m.State(stateTimed).OnEvent(eventStop).To(stateIdle)

	if err := m.Send(context.Background(), "sid", eventGo, nil); err != nil {
		t.Fatalf("Send go failed: %v", err)
	}

	m.timerMu.Lock()
	if _, ok := m.activeTimers["sid"]; !ok {
		t.Fatalf("expected timer entry for sid")
	}
	m.timerMu.Unlock()

	if err := m.Send(context.Background(), "sid", eventStop, nil); err != nil {
		t.Fatalf("Send stop failed: %v", err)
	}

	m.timerMu.Lock()
	if _, ok := m.activeTimers["sid"]; ok {
		t.Fatalf("expected timer entry to be cleared")
	}
	m.timerMu.Unlock()

	state, _, ok, err := store.Load(context.Background(), "sid")
	if err != nil || !ok {
		t.Fatalf("load failed after stop: err=%v ok=%v", err, ok)
	}
	if state != stateIdle {
		t.Fatalf("expected state back to idle, got %s", state)
	}
}

func TestSendWithNilContextUsesBackground(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		stateNext State = "next"
		eventGo   Event = "go"
	)

	var observed bool
	m := New(Config[State, Event, struct{}]{
		Name:    "nil-ctx",
		Initial: stateInit,
	})

	m.State(stateInit).OnEvent(eventGo).Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		if ctx == nil {
			t.Fatal("action received nil ctx")
		}
		observed = true
		return nil
	}).To(stateNext)

	if err := m.Send(nil, "sid", eventGo, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if !observed {
		t.Fatalf("action did not run")
	}
}

func TestCloseCleansIdleSessionLocks(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		eventPing Event = "ping"
	)

	m := New(Config[State, Event, struct{}]{
		Name:    "close-cleanup",
		Initial: stateInit,
	})

	// ensure Close handles nil context
	m.sessionLocks.Store("ghost", &sessionLock{})

	if err := m.Close(nil); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if _, ok := m.sessionLocks.Load("ghost"); ok {
		t.Fatalf("expected ghost lock to be removed")
	}
	if !m.closed.Load() {
		t.Fatalf("expected closed flag to be true")
	}
}

func TestMemStorageLoadSave(t *testing.T) {
	type State string
	type Event string
	type Ctx struct {
		Value string
	}

	initial := State("initial")
	store := NewMemStorage[State, Event, Ctx](initial)

	state, ctxValue, ok, err := store.Load(context.Background(), "sid")
	if err != nil || ok {
		t.Fatalf("expected empty load, err=%v ok=%v", err, ok)
	}
	if state != initial {
		t.Fatalf("expected initial state, got %v", state)
	}
	if ctxValue != (Ctx{}) {
		t.Fatalf("expected zero ctx, got %+v", ctxValue)
	}

	want := Ctx{Value: "stored"}
	if err := store.Save(context.Background(), "sid", "next", want); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	state, ctxValue, ok, err = store.Load(context.Background(), "sid")
	if err != nil || !ok {
		t.Fatalf("expected load success, err=%v ok=%v", err, ok)
	}
	if state != "next" {
		t.Fatalf("want state next, got %v", state)
	}
	if ctxValue != want {
		t.Fatalf("want ctx %+v, got %+v", want, ctxValue)
	}
}

func TestNoopObserverCoverage(t *testing.T) {
	obs := NoopObserver[string, string, struct{}]{}
	obs.OnTransition(context.Background(), Session[string, string, struct{}]{})
	obs.OnGuardRejected(context.Background(), Session[string, string, struct{}]{}, errors.New("ignored"))
	obs.OnActionError(context.Background(), Session[string, string, struct{}]{}, errors.New("ignored"))
	obs.OnTimerSet(context.Background(), "sid", "state", time.Second, "event")
	obs.OnTimerFired(context.Background(), "sid", "event")
	obs.OnTimerError(context.Background(), "sid", "event", errors.New("ignored"))
}

func TestLogObserverDefaultPrefix(t *testing.T) {
	var buf bytes.Buffer
	logger := log.Default()
	origOut := logger.Writer()
	logger.SetOutput(&buf)
	defer logger.SetOutput(origOut)

	obs := LogObserver[string, string, struct{}]{}
	obs.OnTransition(context.Background(), Session[string, string, struct{}]{
		StateFrom: "from",
		StateTo:   "to",
		Event:     "event",
		ID:        "sid",
	})

	out := buf.String()
	if !strings.Contains(out, "[FSM] from --event--> to (sid=sid)") {
		t.Fatalf("log output missing data: %q", out)
	}
}

func TestLogObserverCustomPrefix(t *testing.T) {
	var buf bytes.Buffer
	logger := log.Default()
	origOut := logger.Writer()
	logger.SetOutput(&buf)
	defer logger.SetOutput(origOut)

	obs := LogObserver[string, string, struct{}]{Prefix: "[custom]"}
	obs.OnGuardRejected(context.Background(), Session[string, string, struct{}]{
		StateFrom: "from",
		Event:     "event",
		ID:        "sid",
	}, errors.New("blocked"))
	obs.OnActionError(context.Background(), Session[string, string, struct{}]{
		StateFrom: "from",
		Event:     "event",
		ID:        "sid",
	}, errors.New("fail"))
	obs.OnTimerSet(context.Background(), "sid", "state", time.Second, "event")
	obs.OnTimerFired(context.Background(), "sid", "event")
	obs.OnTimerError(context.Background(), "sid", "event", errors.New("timeout"))

	out := buf.String()
	for _, want := range []string{
		"[custom] guard rejected",
		"[custom] action error",
		"[custom] timer set",
		"[custom] timer fired",
		"[custom] timer error",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in log output: %s", want, out)
		}
	}
}

func TestOnEventReplaceOverridesTransition(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		stateNext State = "next"
		eventGo   Event = "go"
	)

	type transitionCtx struct{ Val string }

	store := NewMemStorage[State, Event, *transitionCtx](stateInit)
	m := New(Config[State, Event, *transitionCtx]{
		Name:    "builder",
		Initial: stateInit,
		Storage: store,
	})

	m.State(stateInit).OnEvent(eventGo).Action(func(ctx context.Context, s *Session[State, Event, *transitionCtx]) error {
		if s.Ctx == nil {
			s.Ctx = &transitionCtx{}
		}
		s.Ctx.Val = "old"
		return nil
	}).To(stateNext)

	m.State(stateInit).OnEventReplace(eventGo).Action(func(ctx context.Context, s *Session[State, Event, *transitionCtx]) error {
		if s.Ctx == nil {
			s.Ctx = &transitionCtx{}
		}
		s.Ctx.Val = "new"
		return nil
	}).To(stateNext)

	if err := m.Send(context.Background(), "sid", eventGo, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	_, savedCtx, ok, err := store.Load(context.Background(), "sid")
	if err != nil || !ok {
		t.Fatalf("Load failed: err=%v ok=%v", err, ok)
	}
	if savedCtx == nil || savedCtx.Val != "new" {
		t.Fatalf("expected replacement action to run, ctx=%v", savedCtx)
	}
}

func TestActionMiddlewareOrder(t *testing.T) {
	type State string
	type Event string

	order := make([]string, 0, 5)

	m := New(Config[State, Event, struct{}]{
		Name:    "middlewares",
		Initial: "init",
		Middlewares: []Middleware[State, Event, struct{}]{
			func(next ActionFn[State, Event, struct{}]) ActionFn[State, Event, struct{}] {
				return func(ctx context.Context, s *Session[State, Event, struct{}]) error {
					order = append(order, "m1-pre")
					err := next(ctx, s)
					order = append(order, "m1-post")
					return err
				}
			},
			func(next ActionFn[State, Event, struct{}]) ActionFn[State, Event, struct{}] {
				return func(ctx context.Context, s *Session[State, Event, struct{}]) error {
					order = append(order, "m2-pre")
					err := next(ctx, s)
					order = append(order, "m2-post")
					return err
				}
			},
		},
	})

	m.State("init").OnEvent("go").Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		order = append(order, "action")
		return nil
	})

	if err := m.Send(context.Background(), "sid", "go", nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	want := []string{"m1-pre", "m2-pre", "action", "m2-post", "m1-post"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected middleware order: %v", order)
	}
}

type errLoadStorage[StateT, EventT Comparable, CtxT any] struct {
	initial StateT
	err     error
}

func (e errLoadStorage[StateT, EventT, CtxT]) Load(ctx context.Context, sessionID string) (StateT, CtxT, bool, error) {
	var zero CtxT
	return e.initial, zero, false, e.err
}

func (errLoadStorage[StateT, EventT, CtxT]) Save(ctx context.Context, sessionID string, state StateT, userCtx CtxT) error {
	return nil
}

func TestSendStorageLoadError(t *testing.T) {
	type State string
	type Event string

	loadErr := errors.New("load boom")
	m := New(Config[State, Event, struct{}]{
		Name:    "load-error",
		Initial: "init",
		Storage: errLoadStorage[State, Event, struct{}]{initial: "init", err: loadErr},
	})

	err := m.Send(context.Background(), "sid", "event", nil)
	if err == nil || !strings.Contains(err.Error(), "storage load") || !errors.Is(err, loadErr) {
		t.Fatalf("expected wrapped load error, got %v", err)
	}
}

type fixedStateStorage[StateT, EventT Comparable, CtxT any] struct {
	state StateT
}

func (f fixedStateStorage[StateT, EventT, CtxT]) Load(ctx context.Context, sessionID string) (StateT, CtxT, bool, error) {
	var zero CtxT
	return f.state, zero, true, nil
}

func (fixedStateStorage[StateT, EventT, CtxT]) Save(ctx context.Context, sessionID string, state StateT, userCtx CtxT) error {
	return nil
}

func TestSendNoSuchStateAnnotated(t *testing.T) {
	type State string
	type Event string

	store := fixedStateStorage[State, Event, struct{}]{state: "unknown"}
	m := New(Config[State, Event, struct{}]{
		Name:    "invalid-state",
		Initial: "init",
		Storage: store,
	})

	err := m.Send(context.Background(), "sid", "event", nil)
	if err == nil || !errors.Is(err, ErrNoSuchState) || !strings.Contains(err.Error(), "state=unknown") {
		t.Fatalf("expected ErrNoSuchState with context, got %v", err)
	}
}

func TestPersistHookErrorOverridesSaveError(t *testing.T) {
	type State string
	type Event string
	type persistCtx struct{ Flag bool }

	saveErr := errors.New("save boom")
	expected := errors.New("hook override")

	store := &failingStorage[State, Event, persistCtx]{
		initialState: "init",
		err:          saveErr,
	}

	m := New(Config[State, Event, persistCtx]{
		Name:    "persist-hook",
		Initial: "init",
		Storage: store,
		OnPersistError: func(ctx context.Context, s *Session[State, Event, persistCtx], persistErr error) error {
			return expected
		},
	})

	m.State("init").OnEvent("go").Action(func(ctx context.Context, s *Session[State, Event, persistCtx]) error {
		s.Ctx.Flag = true
		return nil
	}).To("next")

	err := m.Send(context.Background(), "sid", "go", nil)
	if !errors.Is(err, expected) {
		t.Fatalf("want hook error, got %v", err)
	}
}

func TestCloseContextDeadline(t *testing.T) {
	m := New(Config[string, string, struct{}]{
		Name:    "close-pending",
		Initial: "init",
	})

	unlock := m.lockSession("sid")
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := m.Close(ctx); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	m := New(Config[string, string, struct{}]{Name: "close-idempotent", Initial: "init"})

	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("first close failed: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second close should be no-op, got %v", err)
	}
}

func TestDispatchErrorsAndNilContext(t *testing.T) {
	type State string
	type Event string
	type ctxData struct{}

	fsmNilSession := &Session[State, Event, ctxData]{}
	if err := fsmNilSession.Dispatch(context.Background(), "ev", nil); err == nil || !strings.Contains(err.Error(), "dispatch unavailable") {
		t.Fatalf("expected unavailable error, got %v", err)
	}

	m := New(Config[State, Event, ctxData]{Name: "dispatch", Initial: "init"})
	_ = m.Close(context.Background())

	session := &Session[State, Event, ctxData]{
		ID:        "sid",
		StateFrom: "init",
		StateTo:   "init",
		Event:     "ev",
		fsm:       m,
	}
	if err := session.Dispatch(context.Background(), "ev2", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}

	m2 := New(Config[State, Event, ctxData]{Name: "dispatch-ok", Initial: "init"})
	sessionOK := &Session[State, Event, ctxData]{
		ID:        "sid",
		StateFrom: "init",
		StateTo:   "init",
		Event:     "ev",
		fsm:       m2,
	}
	if err := sessionOK.Dispatch(nil, "ev2", nil); err != nil {
		t.Fatalf("dispatch with nil ctx failed: %v", err)
	}
	if len(sessionOK.postEvents) != 1 {
		t.Fatalf("expected 1 queued event, got %d", len(sessionOK.postEvents))
	}

	var nilSession *Session[State, Event, ctxData]
	if err := nilSession.Dispatch(context.Background(), "ev", nil); err == nil || !strings.Contains(err.Error(), "dispatch on nil session") {
		t.Fatalf("expected nil session error, got %v", err)
	}
}

func TestSendQueuedEventsAbortWhenClosed(t *testing.T) {
	type State string
	type Event string

	store := NewMemStorage[State, Event, struct{}]("init")
	m := New(Config[State, Event, struct{}]{
		Name:    "queued-closed",
		Initial: "init",
		Storage: store,
	})

	m.State("init").OnEvent("start").Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		if err := s.Dispatch(ctx, "next", nil); err != nil {
			return err
		}
		s.fsm.closed.Store(true)
		return nil
	}).To("init")

	m.State("init").OnEvent("next").Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		t.Fatalf("queued event should not execute when closed")
		return nil
	})

	err := m.Send(context.Background(), "sid", "start", nil)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

type observerSpy[StateT, EventT Comparable, CtxT any] struct {
	lastGuard  *Session[StateT, EventT, CtxT]
	guardErr   error
	lastAction *Session[StateT, EventT, CtxT]
	actionErr  error
}

func (o *observerSpy[StateT, EventT, CtxT]) OnTransition(context.Context, Session[StateT, EventT, CtxT]) {
}
func (o *observerSpy[StateT, EventT, CtxT]) OnGuardRejected(ctx context.Context, s Session[StateT, EventT, CtxT], reason error) {
	o.lastGuard = &s
	o.guardErr = reason
}
func (o *observerSpy[StateT, EventT, CtxT]) OnActionError(ctx context.Context, s Session[StateT, EventT, CtxT], err error) {
	o.lastAction = &s
	o.actionErr = err
}
func (o *observerSpy[StateT, EventT, CtxT]) OnTimerSet(context.Context, string, StateT, time.Duration, EventT) {
}
func (observerSpy[StateT, EventT, CtxT]) OnTimerFired(context.Context, string, EventT)        {}
func (observerSpy[StateT, EventT, CtxT]) OnTimerError(context.Context, string, EventT, error) {}

func TestGuardFailureNotifiesObserver(t *testing.T) {
	type State string
	type Event string

	const (
		stateInit State = "init"
		eventGo   Event = "go"
	)

	store := NewMemStorage[State, Event, struct{}](stateInit)
	spy := &observerSpy[State, Event, struct{}]{}

	blockErr := errors.New("blocked")

	m := New(Config[State, Event, struct{}]{
		Name:     "guard-observer",
		Initial:  stateInit,
		Storage:  store,
		Observer: spy,
	})

	m.State(stateInit).OnEvent(eventGo).Guard(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		return blockErr
	}).To(stateInit)

	err := m.Send(context.Background(), "sid", eventGo, nil)
	if err == nil || !errors.Is(err, ErrGuardRejected) || !errors.Is(err, blockErr) {
		t.Fatalf("expected ErrGuardRejected wrapping blockErr, got %v", err)
	}
	if spy.lastGuard == nil || spy.guardErr == nil {
		t.Fatalf("observer not notified of guard rejection")
	}
	if !errors.Is(spy.guardErr, blockErr) {
		t.Fatalf("observer received wrong error: %v", spy.guardErr)
	}
	if spy.lastAction != nil {
		t.Fatalf("action observer should not have been invoked on guard failure")
	}
}

func TestActionFailureNotifiesObserver(t *testing.T) {
	type State string
	type Event string

	actionErr := errors.New("action failed")
	spy := &observerSpy[State, Event, struct{}]{}

	m := New(Config[State, Event, struct{}]{
		Name:     "action-observer",
		Initial:  "init",
		Observer: spy,
	})

	m.State("init").OnEvent("go").Action(func(ctx context.Context, s *Session[State, Event, struct{}]) error {
		return actionErr
	})

	err := m.Send(context.Background(), "sid", "go", nil)
	if err == nil || !errors.Is(err, actionErr) {
		t.Fatalf("expected action error, got %v", err)
	}
	if spy.lastAction == nil || spy.actionErr == nil {
		t.Fatalf("observer did not capture action failure")
	}
	if !errors.Is(spy.actionErr, actionErr) {
		t.Fatalf("observer recorded wrong error: %v", spy.actionErr)
	}
}

type timerSpy struct {
	fired chan struct{}
}

func (timerSpy) OnTransition(context.Context, Session[string, string, struct{}])           {}
func (timerSpy) OnGuardRejected(context.Context, Session[string, string, struct{}], error) {}
func (timerSpy) OnActionError(context.Context, Session[string, string, struct{}], error)   {}
func (timerSpy) OnTimerSet(context.Context, string, string, time.Duration, string)         {}
func (s timerSpy) OnTimerFired(context.Context, string, string) {
	select {
	case s.fired <- struct{}{}:
	default:
	}
}
func (timerSpy) OnTimerError(context.Context, string, string, error) {}

func TestTimerSkipWhenClosed(t *testing.T) {
	m := New(Config[string, string, struct{}]{
		Name:     "timer-closed",
		Initial:  "init",
		Observer: timerSpy{fired: make(chan struct{}, 1)},
	})

	m.State("init").OnEnter(WithTimer[string, string](5*time.Millisecond, "tick"))
	m.State("init").OnEvent("stay").To("init")

	if err := m.Send(context.Background(), "sid", "stay", nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	m.closed.Store(true)
	time.Sleep(30 * time.Millisecond)

	if spy, ok := m.observer.(timerSpy); ok {
		select {
		case <-spy.fired:
			t.Fatalf("timer fired even though FSM was closed")
		default:
		}
	}
}

type timerObserver struct {
	timerSpy
}

func (t timerObserver) OnTimerSet(ctx context.Context, sessionID string, state string, d time.Duration, event string) {
	t.timerSpy.OnTimerSet(ctx, sessionID, state, d, event)
}

func TestResetTimersStopsPrevious(t *testing.T) {
	store := NewMemStorage[string, string, struct{}]("init")
	m := New(Config[string, string, struct{}]{
		Name:    "timer-reset",
		Initial: "init",
		Storage: store,
	})

	ob := timerSpy{fired: make(chan struct{}, 1)}
	m.observer = ob

	m.State("init").OnEvent("start").To("waiting")
	m.State("waiting").OnEnter(WithTimer[string, string](50*time.Millisecond, "timeout"))
	m.State("waiting").OnEvent("finish").To("done")
	m.State("done").OnEvent("noop")

	if err := m.Send(context.Background(), "sid", "start", nil); err != nil {
		t.Fatalf("Send start failed: %v", err)
	}

	if err := m.Send(context.Background(), "sid", "finish", nil); err != nil {
		t.Fatalf("Send finish failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	select {
	case <-ob.fired:
		t.Fatalf("timer should have been stopped when leaving waiting state")
	default:
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
