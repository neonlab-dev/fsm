package fsm

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

type (
	testOTelState string
	testOTelEvent string
	testOTelCtx   struct{}
)

func TestWithOTelActionSpansWrapsError(t *testing.T) {
	tracer := trace.NewNoopTracerProvider().Tracer("test")
	mw := WithOTelActionSpans[testOTelState, testOTelEvent, *testOTelCtx](tracer)

	fsmInstance := &FSM[testOTelState, testOTelEvent, *testOTelCtx]{name: "otel-test"}

	wrapped := mw(func(ctx context.Context, s *Session[testOTelState, testOTelEvent, *testOTelCtx]) error {
		return errors.New("boom")
	})

	session := &Session[testOTelState, testOTelEvent, *testOTelCtx]{
		ID:        "sid",
		StateFrom: "from",
		StateTo:   "to",
		Event:     "event",
		fsm:       fsmInstance,
	}

	if err := wrapped(context.Background(), session); err == nil || err.Error() != "boom" {
		t.Fatalf("expected propagated error, got %v", err)
	}
}

func TestWithOTelActionSpansNilTracer(t *testing.T) {
	mw := WithOTelActionSpans[testOTelState, testOTelEvent, *testOTelCtx](nil)
	fsmInstance := &FSM[testOTelState, testOTelEvent, *testOTelCtx]{name: "otel-test"}
	wrapped := mw(func(ctx context.Context, s *Session[testOTelState, testOTelEvent, *testOTelCtx]) error {
		if s.Data == nil {
			t.Fatalf("expected data to propagate")
		}
		return nil
	})

	session := &Session[testOTelState, testOTelEvent, *testOTelCtx]{
		ID:        "sid",
		StateFrom: "from",
		StateTo:   "to",
		Event:     "event",
		Data:      struct{ X int }{X: 42},
		fsm:       fsmInstance,
	}

	if err := wrapped(context.Background(), session); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
