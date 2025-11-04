package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"fsm"
)

type jobState string
type jobEvent string

const (
	statePending  jobState = "pending"
	stateRunning  jobState = "running"
	stateSuccess  jobState = "success"
	stateFailed   jobState = "failed"
	stateTimedOut jobState = "timed_out"

	eventStart   jobEvent = "start"
	eventFail    jobEvent = "fail"
	eventRetry   jobEvent = "retry"
	eventFinish  jobEvent = "finish"
	eventTimeout jobEvent = "timeout"
)

type jobCtx struct {
	Attempts   int
	StartedAt  time.Time
	FinishedAt time.Time
	LastError  string
}

func main() {
	store := newMemoryStore(statePending)
	observer := verboseObserver{}

	machine := fsm.New(fsm.Config[jobState, jobEvent, *jobCtx]{
		Name:        "jobs-advanced",
		Initial:     statePending,
		Storage:     store,
		Observer:    observer,
		Middlewares: []fsm.Middleware[jobState, jobEvent, *jobCtx]{withTracing, withRecover},
	})

	defineTransitions(machine)
	runAdvancedDemo(machine, store)
}

func defineTransitions(machine *fsm.FSM[jobState, jobEvent, *jobCtx]) {
	// pending -> running
	machine.State(statePending).
		OnEvent(eventStart).
		Guard(limitAttempts(3)).
		Action(startJob()).
		To(stateRunning)

	// failed -> running (retry)
	machine.State(stateFailed).
		OnEvent(eventRetry).
		Guard(limitAttempts(3)).
		Action(startJob()).
		To(stateRunning)

	// running -> success
	machine.State(stateRunning).
		OnEvent(eventFinish).
		Action(func(ctx context.Context, s *fsm.Session[jobState, jobEvent, *jobCtx]) error {
			if s.Ctx == nil {
				s.Ctx = &jobCtx{}
			}
			s.Ctx.FinishedAt = time.Now()
			s.Ctx.LastError = ""
			return nil
		}).
		To(stateSuccess)

	// running -> failed
	machine.State(stateRunning).
		OnEvent(eventFail).
		Action(func(ctx context.Context, s *fsm.Session[jobState, jobEvent, *jobCtx]) error {
			if s.Ctx == nil {
				s.Ctx = &jobCtx{}
			}
			if msg, ok := s.Data.(string); ok {
				s.Ctx.LastError = msg
			} else {
				s.Ctx.LastError = "falha desconhecida"
			}
			return nil
		}).
		To(stateFailed)

	// running -> timed out (by timer)
	machine.State(stateRunning).
		OnEvent(eventTimeout).
		Action(func(ctx context.Context, s *fsm.Session[jobState, jobEvent, *jobCtx]) error {
			if s.Ctx == nil {
				s.Ctx = &jobCtx{}
			}
			s.Ctx.LastError = "timeout atingido"
			return nil
		}).
		To(stateTimedOut)

	// configure timers on entering running
	machine.State(stateRunning).
		OnEnter(fsm.WithTimer[jobState, jobEvent](200*time.Millisecond, eventTimeout))
}

func limitAttempts(max int) fsm.GuardFn[jobState, jobEvent, *jobCtx] {
	return func(ctx context.Context, s *fsm.Session[jobState, jobEvent, *jobCtx]) error {
		if s.Ctx == nil {
			s.Ctx = &jobCtx{}
		}
		if s.Ctx.Attempts >= max {
			return fmt.Errorf("limite de tentativas atingido (%d)", max)
		}
		return nil
	}
}

func startJob() fsm.ActionFn[jobState, jobEvent, *jobCtx] {
	return func(ctx context.Context, s *fsm.Session[jobState, jobEvent, *jobCtx]) error {
		if s.Ctx == nil {
			s.Ctx = &jobCtx{}
		}
		s.Ctx.Attempts++
		s.Ctx.StartedAt = time.Now()
		s.Ctx.FinishedAt = time.Time{}
		s.Ctx.LastError = ""
		return nil
	}
}

func withTracing(next fsm.ActionFn[jobState, jobEvent, *jobCtx]) fsm.ActionFn[jobState, jobEvent, *jobCtx] {
	return func(ctx context.Context, s *fsm.Session[jobState, jobEvent, *jobCtx]) error {
		start := time.Now()
		log.Printf("[trace] %s --%s--> %s (sid=%s)", s.StateFrom, s.Event, s.StateTo, s.ID)
		err := next(ctx, s)
		log.Printf("[trace] concluído em %s (err=%v)", time.Since(start), err)
		return err
	}
}

func withRecover(next fsm.ActionFn[jobState, jobEvent, *jobCtx]) fsm.ActionFn[jobState, jobEvent, *jobCtx] {
	return func(ctx context.Context, s *fsm.Session[jobState, jobEvent, *jobCtx]) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic recuperado: %v", r)
			}
		}()
		return next(ctx, s)
	}
}

type verboseObserver struct{}

func (verboseObserver) OnTransition(ctx context.Context, s fsm.Session[jobState, jobEvent, *jobCtx]) {
	log.Printf("[obs] transição %s --%s--> %s (sid=%s)", s.StateFrom, s.Event, s.StateTo, s.ID)
}

func (verboseObserver) OnGuardRejected(ctx context.Context, s fsm.Session[jobState, jobEvent, *jobCtx], reason error) {
	log.Printf("[obs] guard rejeitado em %s --%s--> %s: %v", s.StateFrom, s.Event, s.StateTo, reason)
}

func (verboseObserver) OnActionError(ctx context.Context, s fsm.Session[jobState, jobEvent, *jobCtx], err error) {
	log.Printf("[obs] action erro %s --%s--> %s: %v", s.StateFrom, s.Event, s.StateTo, err)
}

func (verboseObserver) OnTimerSet(ctx context.Context, sessionID string, state jobState, d time.Duration, event jobEvent) {
	log.Printf("[obs] timer configurado para %s (sid=%s) em %s para '%s'", state, sessionID, d, event)
}

func (verboseObserver) OnTimerFired(ctx context.Context, sessionID string, event jobEvent) {
	log.Printf("[obs] timer disparou (sid=%s event=%s)", sessionID, event)
}

func (verboseObserver) OnTimerError(ctx context.Context, sessionID string, event jobEvent, err error) {
	log.Printf("[obs] erro ao processar timer (sid=%s event=%s): %v", sessionID, event, err)
}

type memoryStore struct {
	mu      sync.RWMutex
	records map[string]struct {
		state jobState
		ctx   *jobCtx
	}
	initial jobState
}

func newMemoryStore(initial jobState) *memoryStore {
	return &memoryStore{
		records: make(map[string]struct {
			state jobState
			ctx   *jobCtx
		}),
		initial: initial,
	}
}

func (m *memoryStore) Load(ctx context.Context, sessionID string) (jobState, *jobCtx, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.records[sessionID]; ok {
		return v.state, v.ctx, true, nil
	}
	return m.initial, nil, false, nil
}

func (m *memoryStore) Save(ctx context.Context, sessionID string, state jobState, userCtx *jobCtx) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[sessionID] = struct {
		state jobState
		ctx   *jobCtx
	}{state: state, ctx: userCtx}
	return nil
}

func runAdvancedDemo(machine *fsm.FSM[jobState, jobEvent, *jobCtx], store *memoryStore) {
	ctx := context.Background()

	fmt.Println("== job 1: sucesso antes do timeout ==")
	if err := machine.Send(ctx, "job-1", eventStart, nil); err != nil {
		log.Fatalf("job-1 start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := machine.Send(ctx, "job-1", eventFinish, nil); err != nil {
		log.Fatalf("job-1 finish: %v", err)
	}
	printSession(ctx, "job-1", store)

	fmt.Println("\n== job 2: timeout automático ==")
	if err := machine.Send(ctx, "job-2", eventStart, nil); err != nil {
		log.Fatalf("job-2 start: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // aguarda timer disparar
	printSession(ctx, "job-2", store)

	fmt.Println("\n== job 3: falha e retry ==")
	if err := machine.Send(ctx, "job-3", eventStart, nil); err != nil {
		log.Fatalf("job-3 start: %v", err)
	}
	if err := machine.Send(ctx, "job-3", eventFail, "erro externo"); err != nil {
		log.Fatalf("job-3 fail: %v", err)
	}
	if err := machine.Send(ctx, "job-3", eventRetry, nil); err != nil {
		log.Fatalf("job-3 retry: %v", err)
	}
	if err := machine.Send(ctx, "job-3", eventFinish, nil); err != nil {
		log.Fatalf("job-3 finish: %v", err)
	}
	printSession(ctx, "job-3", store)
}

func printSession(ctx context.Context, session string, store *memoryStore) {
	state, meta, _, err := store.Load(ctx, session)
	if err != nil {
		log.Printf("erro ao carregar %s: %v", session, err)
		return
	}
	fmt.Printf("estado atual de %s: %s\n", session, state)
	if meta != nil {
		fmt.Printf("contexto: attempts=%d started=%s finished=%s lastError=%q\n",
			meta.Attempts, meta.StartedAt.Format(time.RFC3339Nano), meta.FinishedAt.Format(time.RFC3339Nano), meta.LastError)
	}
}
