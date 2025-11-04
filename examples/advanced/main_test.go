package main

import (
	"context"
	"io"
	"log"
	"testing"
	"time"
)

func silenceLogs(t *testing.T) func() {
	t.Helper()
	orig := log.Writer()
	log.SetOutput(io.Discard)
	return func() {
		log.SetOutput(orig)
	}
}

func TestJobCompletesBeforeTimeout(t *testing.T) {
	t.Cleanup(silenceLogs(t))

	timeout := 50 * time.Millisecond
	machine, store := buildAdvancedMachine(timeout)
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	results := simulateJob(machine, store, "job-success", []jobCommand{
		{Event: eventStart},
		{Event: eventFinish},
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].State != stateRunning {
		t.Fatalf("after start, expected state %s, got %s", stateRunning, results[0].State)
	}
	if results[0].Ctx == nil || results[0].Ctx.Attempts != 1 {
		t.Fatalf("start should increment attempts, ctx=%v", results[0].Ctx)
	}
	if results[1].Err != nil {
		t.Fatalf("finish should succeed, got %v", results[1].Err)
	}
	if results[1].State != stateSuccess {
		t.Fatalf("expected success state, got %s", results[1].State)
	}
	if results[1].Ctx == nil || results[1].Ctx.FinishedAt.IsZero() {
		t.Fatalf("finish should populate FinishedAt, ctx=%v", results[1].Ctx)
	}
	if results[1].Ctx.LastError != "" {
		t.Fatalf("expected empty LastError, got %q", results[1].Ctx.LastError)
	}
}

func TestJobTimeoutFires(t *testing.T) {
	t.Cleanup(silenceLogs(t))

	timeout := 20 * time.Millisecond
	machine, store := buildAdvancedMachine(timeout)
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	results := simulateJob(machine, store, "job-timeout", []jobCommand{
		{Event: eventStart},
	})
	if len(results) != 1 {
		t.Fatalf("expected single result, got %d", len(results))
	}
	if results[0].State != stateRunning {
		t.Fatalf("after start expected running, got %s", results[0].State)
	}

	time.Sleep(timeout + timeout/2)

	state, ctx, _, err := store.Load(context.Background(), "job-timeout")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if state != stateTimedOut {
		t.Fatalf("expected stateTimedOut, got %s", state)
	}
	if ctx == nil || ctx.LastError != "timeout atingido" {
		t.Fatalf("expected timeout error message, ctx=%v", ctx)
	}
}

func TestJobRetryFlow(t *testing.T) {
	t.Cleanup(silenceLogs(t))

	timeout := 30 * time.Millisecond
	machine, store := buildAdvancedMachine(timeout)
	t.Cleanup(func() {
		_ = machine.Close(context.Background())
	})

	results := simulateJob(machine, store, "job-retry", []jobCommand{
		{Event: eventStart},
		{Event: eventFail, Data: "erro externo"},
		{Event: eventRetry},
		{Event: eventFinish},
	})

	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	if results[1].State != stateFailed {
		t.Fatalf("expected failed state after fail, got %s", results[1].State)
	}
	if results[1].Ctx == nil || results[1].Ctx.LastError != "erro externo" {
		t.Fatalf("expected context with failure message, ctx=%v", results[1].Ctx)
	}
	if results[2].State != stateRunning {
		t.Fatalf("retry should move back to running, got %s", results[2].State)
	}
	if results[2].Ctx == nil || results[2].Ctx.Attempts != 2 {
		t.Fatalf("retry should increment attempts, ctx=%v", results[2].Ctx)
	}
	if results[3].State != stateSuccess {
		t.Fatalf("finish after retry should succeed, got %s", results[3].State)
	}
	if results[3].Ctx == nil || results[3].Ctx.LastError != "" {
		t.Fatalf("finish should clear errors, ctx=%v", results[3].Ctx)
	}
}
