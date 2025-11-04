package fsm

import (
	"context"
	"errors"
)

// Dispatch queues a follow-up event to be processed after the current Send finishes.
// The event inherits no cancellation from ctx to avoid cascading cancels.
func (s *Session[StateT, EventT, CtxT]) Dispatch(ctx context.Context, event EventT, data any) error {
	if s == nil {
		return errors.New("fsm: dispatch on nil session")
	}
	if s.fsm == nil {
		return errors.New("fsm: dispatch unavailable outside transition")
	}
	if s.fsm.closed.Load() {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	safeCtx := context.WithoutCancel(ctx)
	s.postEvents = append(s.postEvents, queuedEvent[StateT, EventT, CtxT]{
		ctx:   safeCtx,
		event: event,
		data:  data,
	})
	return nil
}
