# FSM (Finite-State Machine Toolkit for Go)

Build production-ready workflows faster with a typed, concurrency-safe FSM engine that plugs directly into your Go services.

## Why Leading Teams Pick FSM

- Ship reliable automation: deterministic transitions with per-session locking prevent race conditions under heavy load.
- Shorten integration time: composable middleware, observers, and optional OpenTelemetry hooks fit your existing logging and tracing stacks.
- Visualise & iterate quickly: Graphviz export keeps stakeholders aligned while engineers refine journeys.
- Scale with confidence: pluggable storage keeps state where you need it—memory for blazing prototypes or your own persistence layer for mission-critical flows.

## Key Capabilities

- Generic state/event definitions, so you model real business enums without casting.
- Queued follow-up events and timer scheduling for rich, long-lived sessions.
- Built-in middleware chain to add metrics, auditing, or custom guarantees.
- Observer interface to broadcast guard rejections, action errors, and timer activity.
- Extensive built-in test suite (96% coverage) keeps regressions out of your releases.

## Use It For

- Customer onboarding or KYC journeys with branching approval steps.
- Payment and billing retries that demand precise timing guarantees.
- Device or IoT lifecycle control where visibility and traceability matter.
- Any workflow where deterministic state management beats scattered conditionals.

## Documentation

- English reference: [`DOC_EN.md`](DOC_EN.md)
- Referência em português: [`DOC_PT.md`](DOC_PT.md)

Each guide walks through basic, medium, and advanced examples, OpenTelemetry helpers, graph export instructions, and testing tools.

## Quick Repository Tour

- `examples/basic`, `examples/medium`, `examples/advanced`: runnable demos with matching tests—perfect for onboarding new engineers.
- `graph.go`, `graphviz.go`: generate DOT/PNG diagrams to share in design reviews.
- `otel.go`: drop-in middleware to instrument spans without boilerplate.

## Getting Started

```bash
go get github.com/neonlab-dev/fsm
```

```go
// Define your state and event enums, wire storage, then send events.
```

Run `make test` for unit tests, `make coverage` for coverage reports, or invoke `go test .` directly. Our core library ships with ~96% coverage and a GitHub Actions pipeline that enforces formatting (`gofmt`), static analysis (`go vet`), and fresh coverage on every push—so you start with a safety net.

## Contributing & Feedback

We love real-world stories. Open an issue, share a win, or send a PR—let's grow the toolkit together.
