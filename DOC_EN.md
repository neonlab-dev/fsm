# FSM (Finite-State Machine) for Go

This project provides a strongly typed finite-state machine built with Go generics. It keeps things simple but gives enough hooks so you can plug in storage, logging, and metrics without writing everything by hand.

## Key features

- States and events use type-safe enums (`~int`, `~string`, etc.)
- Guards and actions run in order and can mutate a session context
- Per-session locking keeps concurrent `Send` calls safe
- Timers can trigger follow-up events after a state entry
- Observers and middlewares let you add logging, tracing, or metrics
- In-memory storage helper included for tests and demos
- Optional `Session.Dispatch` queues events that should run right after the current transition
- `WithOTelActionSpans` middleware creates OpenTelemetry spans around actions

## Quick start

```go
type State string
const (
    StateInit  State = "init"
    StateReady State = "ready"
)

type Event string
const (
    EventStart Event = "start"
    EventDone  Event = "done"
)

type Context struct {
    Count int
}

import "github.com/neonlab-dev/fsm"

store := fsm.NewMemStorage[State, Event, *Context](StateInit)
machine := fsm.New(fsm.Config[State, Event, *Context]{
    Name:    "demo",
    Initial: StateInit,
    Storage: store,
})

machine.State(StateInit).OnEvent(EventStart).
    Action(func(ctx context.Context, s *fsm.Session[State, Event, *Context]) error {
        if s.Ctx == nil {
            s.Ctx = &Context{}
        }
        s.Ctx.Count++
        return nil
    }).
    To(StateReady)

if err := machine.Send(context.Background(), "session-1", EventStart, nil); err != nil {
    log.Fatal(err)
}
defer machine.Close(context.Background())
```

## Testing

The repo ships with a `Makefile` so you can exercise the suite offline:

```bash
make test        # go test ./... with GOPROXY=off, local caches
make coverage    # writes coverage.out and shows current coverage (≈97% for core)
make clean-cache # removes .gocache/ after you're done
```

Prefer manual commands?

```bash
GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache \
    GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
    go test ./...
```

Examples under `examples/{basic,medium,advanced}` now expose helper functions plus `*_test.go`, so `go test ./examples/...` exercises the progressive demos as part of CI.
