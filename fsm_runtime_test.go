package fsm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

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
