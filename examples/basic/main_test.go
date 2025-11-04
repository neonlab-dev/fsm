package main

import (
	"context"
	"testing"
)

func TestRunSequence(t *testing.T) {
	machine, store := buildMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	events := []lightEvent{eventToggle, eventToggle, eventToggle}
	states, err := runSequence(machine, store, events)
	if err != nil {
		t.Fatalf("runSequence failed: %v", err)
	}
	want := []lightState{stateOn, stateOff, stateOn}
	if len(states) != len(want) {
		t.Fatalf("expected %d states, got %d", len(want), len(states))
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("state at %d = %s, want %s", i, states[i], want[i])
		}
	}
}
