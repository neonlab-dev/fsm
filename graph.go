package fsm

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// GraphSnapshot representa um snapshot imutável das transições conhecidas pela FSM.
type GraphSnapshot[StateT, EventT Comparable, CtxT any] struct {
	Name    string
	Initial StateT
	States  map[StateT]*GraphState[StateT, EventT]
}

// GraphState descreve timers e eventos saindo de um estado.
type GraphState[StateT, EventT Comparable] struct {
	OnEnterTimers []TimerConfig[StateT, EventT]
	Transitions   map[EventT]*GraphTransition[StateT]
}

// GraphTransition contém metadados de um arco no grafo.
type GraphTransition[StateT Comparable] struct {
	To          StateT
	Self        bool
	GuardCount  int
	ActionCount int
}

// SnapshotGraph percorre a definição atual da FSM e devolve um modelo navegável.
func (f *FSM[StateT, EventT, CtxT]) SnapshotGraph() GraphSnapshot[StateT, EventT, CtxT] {
	f.mu.RLock()
	defer f.mu.RUnlock()

	states := make(map[StateT]*GraphState[StateT, EventT], len(f.transitions))
	ensureState := func(state StateT) *GraphState[StateT, EventT] {
		if gs, ok := states[state]; ok {
			return gs
		}
		gs := &GraphState[StateT, EventT]{
			OnEnterTimers: nil,
			Transitions:   make(map[EventT]*GraphTransition[StateT]),
		}
		states[state] = gs
		return gs
	}

	for state, evMap := range f.transitions {
		gs := ensureState(state)
		for event, tr := range evMap {
			if tr == nil {
				continue
			}
			dest := state
			self := true
			if tr.to != nil {
				dest = *tr.to
				self = dest == state
			}
			gs.Transitions[event] = &GraphTransition[StateT]{
				To:          dest,
				Self:        self,
				GuardCount:  len(tr.guards),
				ActionCount: len(tr.actions),
			}
			ensureState(dest)
		}
	}

	for state, timers := range f.onEnterTimer {
		gs := ensureState(state)
		copied := make([]TimerConfig[StateT, EventT], len(timers))
		copy(copied, timers)
		gs.OnEnterTimers = append(gs.OnEnterTimers, copied...)
	}

	ensureState(f.initial)

	return GraphSnapshot[StateT, EventT, CtxT]{
		Name:    f.name,
		Initial: f.initial,
		States:  states,
	}
}

type graphDOTConfig[StateT, EventT Comparable] struct {
	formatState   func(StateT) string
	formatEvent   func(EventT) string
	includeMeta   bool
	includeTimers bool
}

// GraphDOTOption permite customizar a exportação DOT.
type GraphDOTOption[StateT, EventT Comparable] func(*graphDOTConfig[StateT, EventT])

// WithStateFormatter substitui o formato padrão (fmt.Sprint) utilizado para rotular estados.
func WithStateFormatter[StateT, EventT Comparable](fn func(StateT) string) GraphDOTOption[StateT, EventT] {
	return func(cfg *graphDOTConfig[StateT, EventT]) {
		if fn != nil {
			cfg.formatState = fn
		}
	}
}

// WithEventFormatter substitui o formato padrão (fmt.Sprint) utilizado para rotular eventos.
func WithEventFormatter[StateT, EventT Comparable](fn func(EventT) string) GraphDOTOption[StateT, EventT] {
	return func(cfg *graphDOTConfig[StateT, EventT]) {
		if fn != nil {
			cfg.formatEvent = fn
		}
	}
}

// WithoutMetadata remove contadores de guards/actions dos rótulos das arestas.
func WithoutMetadata[StateT, EventT Comparable]() GraphDOTOption[StateT, EventT] {
	return func(cfg *graphDOTConfig[StateT, EventT]) {
		cfg.includeMeta = false
	}
}

// WithoutTimers omite dados de timers dos rótulos de estados.
func WithoutTimers[StateT, EventT Comparable]() GraphDOTOption[StateT, EventT] {
	return func(cfg *graphDOTConfig[StateT, EventT]) {
		cfg.includeTimers = false
	}
}

// ExportDOT escreve uma representação DOT do grafo diretamente a partir da FSM.
func (f *FSM[StateT, EventT, CtxT]) ExportDOT(w io.Writer, opts ...GraphDOTOption[StateT, EventT]) error {
	snapshot := f.SnapshotGraph()
	return snapshot.WriteDOT(w, opts...)
}

// WriteDOT serializa o snapshot em formato DOT (Graphviz).
func (snap GraphSnapshot[StateT, EventT, CtxT]) WriteDOT(w io.Writer, opts ...GraphDOTOption[StateT, EventT]) error {
	cfg := graphDOTConfig[StateT, EventT]{
		formatState:   func(s StateT) string { return fmt.Sprint(s) },
		formatEvent:   func(e EventT) string { return fmt.Sprint(e) },
		includeMeta:   true,
		includeTimers: true,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	graphName := snap.Name
	if graphName == "" {
		graphName = "fsm"
	}
	if _, err := fmt.Fprintf(w, "digraph %s {\n", dotQuote(graphName)); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "  rankdir=LR;\n  node [shape=circle];\n  __start__ [shape=point];\n"); err != nil {
		return err
	}

	stateKeys := make([]StateT, 0, len(snap.States))
	for state := range snap.States {
		stateKeys = append(stateKeys, state)
	}
	sort.Slice(stateKeys, func(i, j int) bool {
		return cfg.formatState(stateKeys[i]) < cfg.formatState(stateKeys[j])
	})

	nodeIDs := make(map[StateT]string, len(stateKeys))
	for idx, state := range stateKeys {
		nodeIDs[state] = fmt.Sprintf("s%d", idx)
		label := cfg.formatState(state)
		if cfg.includeTimers && len(snap.States[state].OnEnterTimers) > 0 {
			timerParts := make([]string, 0, len(snap.States[state].OnEnterTimers))
			for _, tc := range snap.States[state].OnEnterTimers {
				timerParts = append(timerParts, fmt.Sprintf("%s @ %s", cfg.formatEvent(tc.Event), tc.After))
			}
			label = fmt.Sprintf("%s\\nTimers: %s", label, strings.Join(timerParts, ", "))
		}
		if _, err := fmt.Fprintf(w, "  %s [label=%s];\n", nodeIDs[state], dotQuote(label)); err != nil {
			return err
		}
	}

	initialID, ok := nodeIDs[snap.Initial]
	if !ok {
		return fmt.Errorf("fsm: estado inicial %v ausente no snapshot", snap.Initial)
	}
	if _, err := fmt.Fprintf(w, "  __start__ -> %s;\n", initialID); err != nil {
		return err
	}

	for _, state := range stateKeys {
		gs := snap.States[state]
		if len(gs.Transitions) == 0 {
			continue
		}
		events := make([]EventT, 0, len(gs.Transitions))
		for ev := range gs.Transitions {
			events = append(events, ev)
		}
		sort.Slice(events, func(i, j int) bool {
			return cfg.formatEvent(events[i]) < cfg.formatEvent(events[j])
		})

		for _, ev := range events {
			tr := gs.Transitions[ev]
			destID, ok := nodeIDs[tr.To]
			if !ok {
				return fmt.Errorf("fsm: estado destino %v ausente no snapshot", tr.To)
			}
			label := cfg.formatEvent(ev)
			if cfg.includeMeta {
				metaParts := make([]string, 0, 2)
				if tr.GuardCount > 0 {
					metaParts = append(metaParts, fmt.Sprintf("guards=%d", tr.GuardCount))
				}
				if tr.ActionCount > 0 {
					metaParts = append(metaParts, fmt.Sprintf("actions=%d", tr.ActionCount))
				}
				if len(metaParts) > 0 {
					label = fmt.Sprintf("%s\\n[%s]", label, strings.Join(metaParts, ", "))
				}
			}
			if _, err := fmt.Fprintf(w, "  %s -> %s [label=%s];\n", nodeIDs[state], destID, dotQuote(label)); err != nil {
				return err
			}
		}
	}

	_, err := io.WriteString(w, "}\n")
	return err
}

func dotQuote(s string) string {
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
