package fsm

import (
	"context"
	"sync"
)

// MemStorage is an in-memory Storage implementation for testing/dev.
type MemStorage[StateT, EventT Comparable, CtxT any] struct {
	mu   sync.RWMutex
	data map[string]struct {
		State StateT
		Ctx   CtxT
	}
	initial StateT
	zero    CtxT
}

// NewMemStorage creates a memory-backed storage with the provided initial state.
func NewMemStorage[StateT, EventT Comparable, CtxT any](initial StateT) *MemStorage[StateT, EventT, CtxT] {
	return &MemStorage[StateT, EventT, CtxT]{
		data: make(map[string]struct {
			State StateT
			Ctx   CtxT
		}),
		initial: initial,
	}
}

// Load implements Storage.
func (m *MemStorage[StateT, EventT, CtxT]) Load(ctx context.Context, sessionID string) (StateT, CtxT, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.data[sessionID]; ok {
		return v.State, v.Ctx, true, nil
	}
	return m.initial, m.zero, false, nil
}

// Save implements Storage.
func (m *MemStorage[StateT, EventT, CtxT]) Save(ctx context.Context, sessionID string, state StateT, userCtx CtxT) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[sessionID] = struct {
		State StateT
		Ctx   CtxT
	}{State: state, Ctx: userCtx}
	return nil
}
