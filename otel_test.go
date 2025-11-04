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
