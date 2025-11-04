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
	store := fsm.NewMemStorage[lightState, lightEvent, struct{}](stateOff)
	machine := fsm.New(fsm.Config[lightState, lightEvent, struct{}]{
		Name:    "light-demo",
		Initial: stateOff,
		Storage: store,
	})
	defer func() {
		_ = machine.Close(context.Background())
	}()

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

	run(machine, store, stateOff)
}

func run(machine *fsm.FSM[lightState, lightEvent, struct{}], store *fsm.MemStorage[lightState, lightEvent, struct{}], initial lightState) {
	ctx := context.Background()
	steps := []lightEvent{eventToggle, eventToggle, eventToggle}

	for idx, ev := range steps {
		if err := machine.Send(ctx, "lamp", ev, nil); err != nil {
			panic(err)
		}
		state, _, ok, _ := store.Load(ctx, "lamp")
		if !ok {
			state = initial
		}
		fmt.Printf("step %d: estado atual %q\n", idx+1, state)
	}
}
