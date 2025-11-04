package main

import (
	"context"
	"errors"
	"testing"

	"github.com/neonlab-dev/fsm"
)

func TestSimulateOrder(t *testing.T) {
	machine, store := buildMachine()
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	results := simulateOrder(machine, store, []orderEvent{
		eventShip,
		eventPay,
		eventCancel,
	})

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if !errors.Is(results[0].Err, fsm.ErrInvalidTransition) {
		t.Fatalf("expected invalid transition on first event, got %v", results[0].Err)
	}
	if results[0].State != stateCreated {
		t.Fatalf("state after failed ship should remain %s, got %s", stateCreated, results[0].State)
	}

	if results[1].Err != nil {
		t.Fatalf("pay event should succeed, got %v", results[1].Err)
	}
	if results[1].State != stateShipped {
		t.Fatalf("expected state %s after pay+ship, got %s", stateShipped, results[1].State)
	}
	if results[1].Ctx == nil || results[1].Ctx.PaidAt.IsZero() || results[1].Ctx.ShippedAt.IsZero() {
		t.Fatalf("expected timestamps populated after pay+ship, ctx=%v", results[1].Ctx)
	}

	if results[2].State != stateShipped {
		t.Fatalf("state should stay shipped after cancel attempt, got %s", results[2].State)
	}
	if !errors.Is(results[2].Err, fsm.ErrNoSuchState) {
		t.Fatalf("expected ErrNoSuchState on cancel, got %v", results[2].Err)
	}
}
