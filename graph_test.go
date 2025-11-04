package fsm

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

type graphState string
type graphEvent string

const (
	stateA graphState = "A"
	stateB graphState = "B"
	stateC graphState = "C"

	eventToB  graphEvent = "toB"
	eventStay graphEvent = "stay"
	eventToC  graphEvent = "toC"
)

func buildGraphMachine() *FSM[graphState, graphEvent, struct{}] {
	machine := New(Config[graphState, graphEvent, struct{}]{
		Name:    "graph-demo",
		Initial: stateA,
	})

	machine.State(stateA).
		OnEvent(eventToB).
		Guard(func(ctx context.Context, s *Session[graphState, graphEvent, struct{}]) error { return nil }).
		Action(func(ctx context.Context, s *Session[graphState, graphEvent, struct{}]) error { return nil }).
		To(stateB)

	machine.State(stateB).
		OnEvent(eventStay).
		Action(func(ctx context.Context, s *Session[graphState, graphEvent, struct{}]) error { return nil }).
		To(stateB)

	machine.State(stateB).
		OnEnter(WithTimer[graphState, graphEvent](2*time.Second, eventToB))

	return machine
}

func TestSnapshotGraphCapturesDefinition(t *testing.T) {
	machine := buildGraphMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	snapshot := machine.SnapshotGraph()

	if snapshot.Name != "graph-demo" {
		t.Fatalf("expected snapshot name graph-demo, got %s", snapshot.Name)
	}
	if snapshot.Initial != stateA {
		t.Fatalf("expected initial state %s, got %s", stateA, snapshot.Initial)
	}
	if len(snapshot.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(snapshot.States))
	}

	origin := snapshot.States[stateA]
	if origin == nil {
		t.Fatalf("state A missing in snapshot")
	}
	tr, ok := origin.Transitions[eventToB]
	if !ok {
		t.Fatalf("transition A --toB--> ? missing")
	}
	if tr.To != stateB {
		t.Fatalf("expected destination stateB, got %s", tr.To)
	}
	if tr.GuardCount != 1 {
		t.Fatalf("expected 1 guard, got %d", tr.GuardCount)
	}
	if tr.ActionCount != 1 {
		t.Fatalf("expected 1 action, got %d", tr.ActionCount)
	}

	dest := snapshot.States[stateB]
	if dest == nil {
		t.Fatalf("state B missing in snapshot")
	}
	if len(dest.OnEnterTimers) != 1 {
		t.Fatalf("expected 1 timer, got %d", len(dest.OnEnterTimers))
	}
	timer := dest.OnEnterTimers[0]
	if timer.Event != eventToB || timer.After != 2*time.Second {
		t.Fatalf("unexpected timer payload: %+v", timer)
	}
	self, ok := dest.Transitions[eventStay]
	if !ok {
		t.Fatalf("transition stay missing on state B")
	}
	if !self.Self {
		t.Fatalf("expected self-loop on state B for stay event")
	}
	if self.ActionCount != 1 || self.GuardCount != 0 {
		t.Fatalf("unexpected metadata on self transition: %+v", self)
	}
}

func TestExportDOTProducesDeterministicOutput(t *testing.T) {
	machine := buildGraphMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	var buf bytes.Buffer
	if err := machine.ExportDOT(&buf); err != nil {
		t.Fatalf("ExportDOT failed: %v", err)
	}

	got := buf.String()
	want := "digraph \"graph-demo\" {\n" +
		"  rankdir=LR;\n" +
		"  node [shape=circle];\n" +
		"  __start__ [shape=point];\n" +
		"  s0 [label=\"A\"];\n" +
		"  s1 [label=\"B\\nTimers: toB @ 2s\"];\n" +
		"  __start__ -> s0;\n" +
		"  s0 -> s1 [label=\"toB\\n[guards=1, actions=1]\"];\n" +
		"  s1 -> s1 [label=\"stay\\n[actions=1]\"];\n" +
		"}\n"

	if got != want {
		t.Fatalf("unexpected DOT output:\n%s", got)
	}
}

func TestWriteDOTWithCustomOptions(t *testing.T) {
	machine := New(Config[graphState, graphEvent, struct{}]{
		Name:    "custom-graph",
		Initial: stateA,
	})
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	machine.State(stateA).
		OnEvent(eventToB).
		To(stateB)

	machine.State(stateB).
		OnEvent(eventStay).
		To(stateB)

	machine.State(stateB).
		OnEvent(eventToC).
		To(stateC)

	machine.State(stateB).
		OnEnter(WithTimer[graphState, graphEvent](time.Second, eventToB))

	snapshot := machine.SnapshotGraph()

	var buf bytes.Buffer
	err := snapshot.WriteDOT(&buf,
		WithStateFormatter[graphState, graphEvent](func(s graphState) string {
			return "STATE_" + string(s)
		}),
		WithEventFormatter[graphState, graphEvent](func(ev graphEvent) string {
			return strings.ToUpper(string(ev))
		}),
		WithoutMetadata[graphState, graphEvent](),
		WithoutTimers[graphState, graphEvent](),
	)
	if err != nil {
		t.Fatalf("WriteDOT with custom options failed: %v", err)
	}

	got := buf.String()
	want := "digraph \"custom-graph\" {\n" +
		"  rankdir=LR;\n" +
		"  node [shape=circle];\n" +
		"  __start__ [shape=point];\n" +
		"  s0 [label=\"STATE_A\"];\n" +
		"  s1 [label=\"STATE_B\"];\n" +
		"  s2 [label=\"STATE_C\"];\n" +
		"  __start__ -> s0;\n" +
		"  s0 -> s1 [label=\"TOB\"];\n" +
		"  s1 -> s1 [label=\"STAY\"];\n" +
		"  s1 -> s2 [label=\"TOC\"];\n" +
		"}\n"

	if got != want {
		t.Fatalf("unexpected DOT output with custom options:\n%s", got)
	}
}
