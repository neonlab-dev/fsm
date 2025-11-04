package main

import (
	"context"
	"fmt"
	"time"

	"github.com/neonlab-dev/fsm"
)

type orderState string
type orderEvent string

const (
	stateCreated  orderState = "created"
	statePaid     orderState = "paid"
	stateShipped  orderState = "shipped"
	stateCanceled orderState = "canceled"

	eventPay    orderEvent = "pay"
	eventShip   orderEvent = "ship"
	eventCancel orderEvent = "cancel"
)

type orderCtx struct {
	PaidAt     time.Time
	ShippedAt  time.Time
	CancelNote string
}

func main() {
	machine, store := buildMachine()
	defer func() {
		_ = machine.Close(context.Background())
	}()

	results := simulateOrder(machine, store, []orderEvent{
		eventShip,
		eventPay,
		eventCancel,
	})

	for _, res := range results {
		fmt.Printf("\n== enviando evento %q ==\n", res.Event)
		if res.Err != nil {
			fmt.Printf("erro: %v\n", res.Err)
		}
		fmt.Printf("novo estado: %s\n", res.State)
		if res.Ctx != nil {
			fmt.Printf("ctx: %+v\n", *res.Ctx)
		}
	}
}

type stepResult struct {
	Event orderEvent
	State orderState
	Ctx   *orderCtx
	Err   error
}

func buildMachine() (*fsm.FSM[orderState, orderEvent, *orderCtx], *fsm.MemStorage[orderState, orderEvent, *orderCtx]) {
	store := fsm.NewMemStorage[orderState, orderEvent, *orderCtx](stateCreated)
	machine := fsm.New(fsm.Config[orderState, orderEvent, *orderCtx]{
		Name:        "orders-medio",
		Initial:     stateCreated,
		Storage:     store,
		Middlewares: []fsm.Middleware[orderState, orderEvent, *orderCtx]{logAction},
	})

	machine.State(stateCreated).
		OnEvent(eventPay).
		Action(func(ctx context.Context, s *fsm.Session[orderState, orderEvent, *orderCtx]) error {
			if s.Ctx == nil {
				s.Ctx = &orderCtx{}
			}
			s.Ctx.PaidAt = time.Now()
			return s.Dispatch(ctx, eventShip, nil)
		}).
		To(statePaid)

	machine.State(statePaid).
		OnEvent(eventShip).
		Guard(func(ctx context.Context, s *fsm.Session[orderState, orderEvent, *orderCtx]) error {
			if s.Ctx == nil || s.Ctx.PaidAt.IsZero() {
				return fmt.Errorf("pedido não pago")
			}
			return nil
		}).
		Action(func(ctx context.Context, s *fsm.Session[orderState, orderEvent, *orderCtx]) error {
			s.Ctx.ShippedAt = time.Now()
			return nil
		}).
		To(stateShipped)

	machine.State(stateCreated).
		OnEvent(eventCancel).
		Action(cancelWithNote("cancelado antes do pagamento")).
		To(stateCanceled)

	machine.State(statePaid).
		OnEvent(eventCancel).
		Action(cancelWithNote("cancelado após pagamento")).
		To(stateCanceled)

	return machine, store
}

func logAction(next fsm.ActionFn[orderState, orderEvent, *orderCtx]) fsm.ActionFn[orderState, orderEvent, *orderCtx] {
	return func(ctx context.Context, s *fsm.Session[orderState, orderEvent, *orderCtx]) error {
		fmt.Printf("executando action %s --%s--> %s (sid=%s)\n", s.StateFrom, s.Event, s.StateTo, s.ID)
		if err := next(ctx, s); err != nil {
			fmt.Printf("action falhou: %v\n", err)
			return err
		}
		fmt.Println("action concluída")
		return nil
	}
}

func cancelWithNote(note string) fsm.ActionFn[orderState, orderEvent, *orderCtx] {
	return func(ctx context.Context, s *fsm.Session[orderState, orderEvent, *orderCtx]) error {
		if s.Ctx == nil {
			s.Ctx = &orderCtx{}
		}
		s.Ctx.CancelNote = note
		return nil
	}
}

func simulateOrder(machine *fsm.FSM[orderState, orderEvent, *orderCtx], store *fsm.MemStorage[orderState, orderEvent, *orderCtx], events []orderEvent) []stepResult {
	session := "order-42"
	ctx := context.Background()
	results := make([]stepResult, 0, len(events))

	for _, ev := range events {
		res := stepResult{Event: ev}
		res.Err = machine.Send(ctx, session, ev, nil)
		state, meta, _, loadErr := store.Load(ctx, session)
		if loadErr == nil {
			res.State = state
			if meta != nil {
				cp := *meta
				res.Ctx = &cp
			}
		}
		results = append(results, res)
	}
	return results
}
