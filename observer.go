package fsm

import (
	"context"
	"log"
	"time"
)

// NoopObserver discards all observer events.
type NoopObserver[StateT, EventT Comparable, CtxT any] struct{}

func (NoopObserver[StateT, EventT, CtxT]) OnTransition(context.Context, Session[StateT, EventT, CtxT]) {
}
func (NoopObserver[StateT, EventT, CtxT]) OnGuardRejected(context.Context, Session[StateT, EventT, CtxT], error) {
}
func (NoopObserver[StateT, EventT, CtxT]) OnActionError(context.Context, Session[StateT, EventT, CtxT], error) {
}
func (NoopObserver[StateT, EventT, CtxT]) OnTimerSet(context.Context, string, StateT, time.Duration, EventT) {
}
func (NoopObserver[StateT, EventT, CtxT]) OnTimerFired(context.Context, string, EventT) {}
func (NoopObserver[StateT, EventT, CtxT]) OnTimerError(context.Context, string, EventT, error) {
}

// LogObserver writes simple logs via the standard library log package.
type LogObserver[StateT, EventT Comparable, CtxT any] struct{ Prefix string }

func (o LogObserver[StateT, EventT, CtxT]) pfx() string {
	if o.Prefix == "" {
		return "[FSM]"
	}
	return o.Prefix
}

func (o LogObserver[StateT, EventT, CtxT]) OnTransition(_ context.Context, s Session[StateT, EventT, CtxT]) {
	log.Printf("%s %v --%v--> %v (sid=%s)", o.pfx(), s.StateFrom, s.Event, s.StateTo, s.ID)
}
func (o LogObserver[StateT, EventT, CtxT]) OnGuardRejected(_ context.Context, s Session[StateT, EventT, CtxT], reason error) {
	log.Printf("%s guard rejected in %v on %v (sid=%s): %v", o.pfx(), s.StateFrom, s.Event, s.ID, reason)
}
func (o LogObserver[StateT, EventT, CtxT]) OnActionError(_ context.Context, s Session[StateT, EventT, CtxT], err error) {
	log.Printf("%s action error in %v on %v (sid=%s): %v", o.pfx(), s.StateFrom, s.Event, s.ID, err)
}
func (o LogObserver[StateT, EventT, CtxT]) OnTimerSet(_ context.Context, sid string, st StateT, d time.Duration, _ EventT) {
	log.Printf("%s timer set: sid=%s state=%v after=%s", o.pfx(), sid, st, d)
}
func (o LogObserver[StateT, EventT, CtxT]) OnTimerFired(_ context.Context, sid string, ev EventT) {
	log.Printf("%s timer fired: sid=%s event=%v", o.pfx(), sid, ev)
}
func (o LogObserver[StateT, EventT, CtxT]) OnTimerError(_ context.Context, sid string, ev EventT, err error) {
	log.Printf("%s timer error: sid=%s event=%v err=%v", o.pfx(), sid, ev, err)
}
