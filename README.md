# FSM (Finite-State Machine Toolkit for Go)

Typed, concurrent-safe state machine helpers with timers, middleware, observers, and optional OpenTelemetry integration.

## Highlights

- Generics-powered states/events and pluggable storage backends
- Per-session locking with timer scheduling and queued follow-up events
- Middleware/observer hooks for logging, tracing, or metrics
- Optional Graphviz export (DOT/PNG) for visualising transitions

## Documentation

- English reference: [`DOC_EN.md`](DOC_EN.md)
- Referência em português: [`DOC_PT.md`](DOC_PT.md)

Both guides include complete examples (basic, medium, advanced), OpenTelemetry helpers, graph export instructions, and testing notes.

## Repository Tour

- `examples/basic`, `examples/medium`, `examples/advanced` — runnable demos plus matching tests
- `graph.go`, `graphviz.go` — graph snapshot/export utilities
- `otel.go` — helper middleware for OpenTelemetry spans

## Contributing / Feedback

Issues and pull requests are welcome. Check the Makefile targets (`test`, `coverage`, `clean-cache`) or run `go test ./...` directly to validate changes.
