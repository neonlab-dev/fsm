package fsm

import "time"

// stateBuilder is used for fluent state configuration.
type stateBuilder[StateT, EventT Comparable, CtxT any] struct {
	parent *FSM[StateT, EventT, CtxT]
	state  StateT
}

// eventBuilder captures the transition being configured.
type eventBuilder[StateT, EventT Comparable, CtxT any] struct {
	parent *FSM[StateT, EventT, CtxT]
	state  StateT
	event  EventT
	tr     *transition[StateT, EventT, CtxT]
}

type transition[StateT, EventT Comparable, CtxT any] struct {
	guards  []GuardFn[StateT, EventT, CtxT]
	actions []ActionFn[StateT, EventT, CtxT]
	to      *StateT // nil = self-loop
}

// State returns a builder for defining transitions from the given state.
func (f *FSM[StateT, EventT, CtxT]) State(state StateT) *stateBuilder[StateT, EventT, CtxT] {
	return &stateBuilder[StateT, EventT, CtxT]{parent: f, state: state}
}

// OnEvent begins a new transition definition for (state,event).
func (b *stateBuilder[StateT, EventT, CtxT]) OnEvent(ev EventT) *eventBuilder[StateT, EventT, CtxT] {
	b.parent.mu.Lock()
	defer b.parent.mu.Unlock()

	if _, ok := b.parent.transitions[b.state]; !ok {
		b.parent.transitions[b.state] = make(map[EventT]*transition[StateT, EventT, CtxT])
	}
	tr := &transition[StateT, EventT, CtxT]{}
	b.parent.transitions[b.state][ev] = tr
	return &eventBuilder[StateT, EventT, CtxT]{parent: b.parent, state: b.state, event: ev, tr: tr}
}

// Guard appends a guard to the transition.
func (e *eventBuilder[StateT, EventT, CtxT]) Guard(g GuardFn[StateT, EventT, CtxT]) *eventBuilder[StateT, EventT, CtxT] {
	e.tr.guards = append(e.tr.guards, g)
	return e
}

// Action appends an action to the transition (wrapped by middlewares).
func (e *eventBuilder[StateT, EventT, CtxT]) Action(a ActionFn[StateT, EventT, CtxT]) *eventBuilder[StateT, EventT, CtxT] {
	wrapped := a
	for i := len(e.parent.middlewares) - 1; i >= 0; i-- {
		wrapped = e.parent.middlewares[i](wrapped)
	}
	e.tr.actions = append(e.tr.actions, wrapped)
	return e
}

// To sets the destination state for the transition (omit for self-loop).
func (e *eventBuilder[StateT, EventT, CtxT]) To(dst StateT) *FSM[StateT, EventT, CtxT] {
	e.tr.to = &dst
	return e.parent
}

// OnEnter registers timers that will fire after entering the state.
func (b *stateBuilder[StateT, EventT, CtxT]) OnEnter(timers ...TimerConfig[StateT, EventT]) *FSM[StateT, EventT, CtxT] {
	b.parent.mu.Lock()
	defer b.parent.mu.Unlock()
	b.parent.onEnterTimer[b.state] = append(b.parent.onEnterTimer[b.state], timers...)
	return b.parent
}

// WithTimer is a helper to create a TimerConfig.
func WithTimer[StateT, EventT Comparable](after time.Duration, event EventT) TimerConfig[StateT, EventT] {
	return TimerConfig[StateT, EventT]{After: after, Event: event}
}
