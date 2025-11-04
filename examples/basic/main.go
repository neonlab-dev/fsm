package main

import (
	"context"
	"fmt"

	"github.com/neonlab-dev/fsm"
)

type lightState string
type lightEvent string

const (
	stateOff lightState = "off"
	stateOn  lightState = "on"

	eventToggle lightEvent = "toggle"
)

func main() {
	machine, store := buildMachine()
	defer func() {
		_ = machine.Close(context.Background())
	}()

	states, err := runSequence(machine, store, []lightEvent{
		eventToggle,
		eventToggle,
		eventToggle,
	})
	if err != nil {
		panic(err)
	}

	for idx, st := range states {
		fmt.Printf("step %d: estado atual %q\n", idx+1, st)
	}
}

func buildMachine() (*fsm.FSM[lightState, lightEvent, struct{}], *fsm.MemStorage[lightState, lightEvent, struct{}]) {
	store := fsm.NewMemStorage[lightState, lightEvent, struct{}](stateOff)
	machine := fsm.New(fsm.Config[lightState, lightEvent, struct{}]{
		Name:    "light-demo",
		Initial: stateOff,
		Storage: store,
	})

	machine.State(stateOff).
		OnEvent(eventToggle).
		Action(func(ctx context.Context, s *fsm.Session[lightState, lightEvent, struct{}]) error {
			fmt.Println("ligando a lâmpada")
			return nil
		}).
		To(stateOn)

	machine.State(stateOn).
		OnEvent(eventToggle).
		Action(func(ctx context.Context, s *fsm.Session[lightState, lightEvent, struct{}]) error {
			fmt.Println("desligando a lâmpada")
			return nil
		}).
		To(stateOff)

	return machine, store
}

func runSequence(machine *fsm.FSM[lightState, lightEvent, struct{}], store *fsm.MemStorage[lightState, lightEvent, struct{}], events []lightEvent) ([]lightState, error) {
	ctx := context.Background()
	states := make([]lightState, 0, len(events))

	for _, ev := range events {
		if err := machine.Send(ctx, "lamp", ev, nil); err != nil {
			return nil, err
		}
		state, _, ok, err := store.Load(ctx, "lamp")
		if err != nil {
			return nil, err
		}
		if !ok {
			state = stateOff
		}
		states = append(states, state)
	}
	return states, nil
}
