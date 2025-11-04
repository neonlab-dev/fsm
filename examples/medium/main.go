package main

import (
	"context"
	"fmt"
	"time"

	"fsm"
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
	store := fsm.NewMemStorage[orderState, orderEvent, *orderCtx](stateCreated)

	machine := fsm.New(fsm.Config[orderState, orderEvent, *orderCtx]{
		Name:        "orders-medio",
		Initial:     stateCreated,
		Storage:     store,
		Middlewares: []fsm.Middleware[orderState, orderEvent, *orderCtx]{logAction},
	})
	defer func() {
		_ = machine.Close(context.Background())
	}()

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

	runDemo(machine, store)
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

func runDemo(machine *fsm.FSM[orderState, orderEvent, *orderCtx], store *fsm.MemStorage[orderState, orderEvent, *orderCtx]) {
	session := "order-42"
	ctx := context.Background()

	send := func(ev orderEvent, payload any) {
		fmt.Printf("\n== enviando evento %q ==\n", ev)
		if err := machine.Send(ctx, session, ev, payload); err != nil {
			fmt.Printf("erro: %v\n", err)
		}
		state, meta, _, _ := store.Load(ctx, session)
		fmt.Printf("novo estado: %s\n", state)
		if meta != nil {
			fmt.Printf("ctx: %+v\n", *meta)
		}
	}

	send(eventShip, nil)   // falha porque ainda não está pago
	send(eventPay, nil)    // paga o pedido e despacha automaticamente via Dispatch
	send(eventCancel, nil) // não tem efeito porque já despachado, mas mostra serialização
}
