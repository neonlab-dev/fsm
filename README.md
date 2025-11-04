# FSM: Finite-State Machine Toolkit for Go

`fsm` é uma biblioteca de máquina de estados finitos fortemente tipada escrita em Go, com suporte a **generics**, **storage plugável**, **timers**, **middlewares** e **observabilidade extensível**. Ela foca em dar ergonomia de alto nível ao mesmo tempo em que mantém controle total sobre concorrência e persistência por sessão.

## Por que usar

- **Tipos fortes de ponta a ponta**: estados e eventos são enums (`~int`, `~string`, etc.), evitando erros de string solta.
- **Contexto mutável com persistência**: transições recebem um ponteiro de sessão, permitindo atualizar o contexto de usuário e o estado alvo antes de persistir.
- **Concorrência segura por sessão**: múltiplas goroutines podem emitir eventos; a biblioteca serializa `Send` por `sessionID`.
- **Timers de estado**: agende eventos automáticos na entrada do estado (`OnEnter`) sem precisar rodar goroutines externas.
- **Observabilidade pluggável**: hooks para transições, rejeições de guard, erros de action, timers e falhas ao reenfileirar eventos.
- **Storage configurável**: use `MemStorage` em testes ou plugue seu próprio repositório (banco SQL, Redis, etc.).
- **Middlewares de action**: envolva actions com logging, tracing, recoveries personalizados.
- **Encerramento previsível**: `Close` aborta timers pendentes e fecha novas entradas de eventos.
- **Eventos encadeados**: `Session.Dispatch` agenda follow-ups mantendo a ordem de processamento por sessão.
- **Instrumentação pronta**: helper `WithOTelActionSpans` cria spans de OpenTelemetry automaticamente para cada action.

## Instalação

No `go.mod` do seu projeto, adicione a dependência apontando para o caminho da sua cópia do repositório (ajuste `github.com/seu-org/fsm` conforme necessário):

```bash
go get github.com/seu-org/fsm
```

## Conceitos básicos

### 1. Defina estados, eventos e contexto

```go
type OrderState string

const (
    OrderPending  OrderState = "pending"
    OrderPaid     OrderState = "paid"
    OrderCanceled OrderState = "canceled"
)

type OrderEvent string

const (
    EventAuthorize OrderEvent = "authorize"
    EventTimeout   OrderEvent = "timeout"
    EventCancel    OrderEvent = "cancel"
)

type OrderCtx struct {
    PaymentReference string
    AuthorizedAt     time.Time
    FailedAttempts   int
}
```

### 2. Construa a máquina

```go
import (
    "github.com/neonlab-dev/fsm"
)

store := fsm.NewMemStorage[OrderState, OrderEvent, *OrderCtx](OrderPending)
machine := fsm.New(fsm.Config[OrderState, OrderEvent, *OrderCtx]{
    Name:     "orders",
    Initial:  OrderPending,
    Storage:  store, // plugue seu repositório aqui
    Observer: fsm.LogObserver[OrderState, OrderEvent, *OrderCtx]{Prefix: "[orders]"},
})
```

### 3. Cadastre transições

```go
import (
    "context"
    "errors"
    "time"
)

machine.State(OrderPending).
    OnEvent(EventAuthorize).
    Guard(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
        if s.Ctx == nil {
            s.Ctx = &OrderCtx{}
        }
        if s.Ctx.FailedAttempts >= 3 {
            return errors.New("too many retries")
        }
        return nil
    }).
    Action(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
        s.Ctx.AuthorizedAt = time.Now()
        s.Ctx.PaymentReference = s.Data.(string) // payload opcional passado em Send
        return nil
    }).
    To(OrderPaid)

machine.State(OrderPending).
    OnEvent(EventCancel).
    Action(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
        s.Ctx = nil // limpando contexto antes de persistir
        return nil
    }).
    To(OrderCanceled)
```

> **Nota**: `OnEvent` agora falha cedo se você registrar duas vezes o mesmo par (estado, evento). Para substituições conscientes durante refactors, utilize `State(...).OnEventReplace(...)`.

### 4. Timers e eventos automáticos

```go
import "time"

machine.State(OrderPending).
    OnEnter(
        fsm.WithTimer[OrderState, OrderEvent](30*time.Minute, EventTimeout),
    )

machine.State(OrderPending).
    OnEvent(EventTimeout).
    Action(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
        s.Ctx.FailedAttempts++
        return nil
    })
```

Quando `OrderPending` é atingido, a FSM agenda um timer que disparará `EventTimeout` em 30 minutos. O observer receberá `OnTimerFired` e, caso o `Send` automático falhe, também `OnTimerError`.

### 5. Disparando eventos

```go
import (
    "context"
    "errors"
    "log"
)

if err := machine.Send(context.Background(), "order-123", EventAuthorize, "ref-ABC"); err != nil {
    if errors.Is(err, fsm.ErrGuardRejected) {
        // trate conforme necessário
    }
    log.Fatalf("transition failed: %v", err)
}
```

`Send` é seguro para concorrência: se várias goroutines chamarem `Send` com o mesmo `sessionID`, a biblioteca processará uma de cada vez e persistirá o contexto atualizado antes de liberar a próxima.
Os erros de transição agora incluem o estado/evento problemático para facilitar o debugging.

### Encadeando follow-ups durante uma transição

Se uma action precisar disparar outro evento na sequência, use `Session.Dispatch`. Os eventos são enfileirados e executados logo após a transição atual concluir, respeitando a mesma ordem e bloqueio por sessão.

```go
machine.State(OrderPending).
    OnEvent(EventAuthorize).
    Action(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
        // ... lógica principal ...
        return s.Dispatch(ctx, EventTimeout, nil) // agenda um follow-up imediato
    }).To(OrderPaid)
```

### Tratando erros de persistência (`OnPersistError`)

Ao configurar a FSM, você pode fornecer `OnPersistError`. Ele será chamado quando o `Storage.Save` falhar, já com `StateTo` revertido para `StateFrom` e o contexto restaurado ao valor previamente carregado.

```go
machine := fsm.New(fsm.Config[OrderState, OrderEvent, *OrderCtx]{
    Name:           "orders",
    Initial:        OrderPending,
    Storage:        store,
    OnPersistError: func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx], err error) error {
        log.Printf("falha persistindo sessão %s: %v", s.ID, err)
        return nil // retorne erro customizado se quiser abortar com outra mensagem
    },
})
```

### Encerrando a máquina e limpando timers

Quando terminar de usar a FSM (por exemplo, no shutdown da aplicação), chame `Close` para cancelar timers ativos, aguardar sessões em progresso e rejeitar novos `Send`.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if err := machine.Close(ctx); err != nil {
    log.Printf("shutdown incompleto: %v", err)
}
```

### Integração com OpenTelemetry

Use o helper `WithOTelActionSpans` para gerar spans automaticamente em cada action. Ele adiciona atributos como `fsm.name`, estado origem/destino, evento e `sessionID`, marcando o span com `codes.Error` quando a action retorna erro.

```go
import (
    "go.opentelemetry.io/otel"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

tp := sdktrace.NewTracerProvider()
otel.SetTracerProvider(tp)

machine := fsm.New(fsm.Config[OrderState, OrderEvent, *OrderCtx]{
    Name:    "orders",
    Initial: OrderPending,
    Storage: store,
    Middlewares: []fsm.Middleware[OrderState, OrderEvent, *OrderCtx]{
        fsm.WithOTelActionSpans[OrderState, OrderEvent, *OrderCtx](tp.Tracer("orders-fsm")),
    },
})
```

Se você passar `nil` como tracer, o helper usa `otel.Tracer("fsm")`.

## Testes e cobertura

Todos os testes residem no pacote raiz. Para evitar depender do proxy oficial (útil em ambientes sem rede), execute com `GOPROXY` e `GOSUMDB` desativados e force o toolchain local:

```bash
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test ./...
```

Para medir cobertura (atual ~98% das instruções do pacote `fsm`):

```bash
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test -cover ./...
```

> Dica: caso precise inspecionar o relatório completo, gere `-coverprofile=coverage.out` e abra com `go tool cover -html=coverage.out`.

### Exemplo completo

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/neonlab-dev/fsm"
)

type OrderState string
type OrderEvent string

const (
    OrderPending OrderState = "pending"
    OrderPaid    OrderState = "paid"
    OrderExpired OrderState = "expired"

    EventAuthorize OrderEvent = "authorize"
    EventTimeout   OrderEvent = "timeout"
)

type OrderCtx struct {
    AuthorizedAt time.Time
    TimedOutAt   time.Time
}

func main() {
    store := fsm.NewMemStorage[OrderState, OrderEvent, *OrderCtx](OrderPending)
    machine := fsm.New(fsm.Config[OrderState, OrderEvent, *OrderCtx]{
        Name:    "orders",
        Initial: OrderPending,
        Storage: store,
        Observer: fsm.LogObserver[OrderState, OrderEvent, *OrderCtx]{
            Prefix: "[orders]",
        },
    })
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), time.Second)
        defer cancel()
        if err := machine.Close(ctx); err != nil {
            log.Printf("erro ao fechar FSM: %v", err)
        }
    }()

    machine.State(OrderPending).
        OnEvent(EventAuthorize).
        Guard(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
            if s.Ctx == nil {
                s.Ctx = &OrderCtx{}
            }
            return nil
        }).
        Action(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
            s.Ctx.AuthorizedAt = time.Now()
            return nil
        }).
        To(OrderPaid)

    machine.State(OrderPending).
        OnEnter(fsm.WithTimer[OrderState, OrderEvent](30*time.Second, EventTimeout))

    machine.State(OrderPending).
        OnEvent(EventTimeout).
        Action(func(ctx context.Context, s *fsm.Session[OrderState, OrderEvent, *OrderCtx]) error {
            s.Ctx.TimedOutAt = time.Now()
            log.Printf("order %s timed out", s.ID)
            return nil
        }).
        To(OrderExpired)

    if err := machine.Send(context.Background(), "order-123", EventAuthorize, nil); err != nil {
        log.Fatalf("transition failed: %v", err)
    }
}
```

## Middlewares

`Config.Middlewares` aceita uma lista de funções que envolvem cada Action. Exemplo de middleware para tempo de execução e logging:

```go
func LogDuration[StateT, EventT fsm.Comparable, CtxT any](logf func(string, ...any)) fsm.Middleware[StateT, EventT, CtxT] {
    return func(next fsm.ActionFn[StateT, EventT, CtxT]) fsm.ActionFn[StateT, EventT, CtxT] {
        return func(ctx context.Context, s *fsm.Session[StateT, EventT, CtxT]) error {
            start := time.Now()
            err := next(ctx, s)
            logf("transition %v --%v--> %v took %s (err=%v)", s.StateFrom, s.Event, s.StateTo, time.Since(start), err)
            return err
        }
    }
}

machine := fsm.New(fsm.Config[OrderState, OrderEvent, *OrderCtx]{
    Initial:     OrderPending,
    Storage:     store,
    Middlewares: []fsm.Middleware[OrderState, OrderEvent, *OrderCtx]{LogDuration(log.Printf)},
})
```

## Observers personalizados

Implemente `fsm.Observer` para integrar com sistemas de métricas ou tracing. Todos os hooks recebem o snapshot da `Session`; timers contam com os métodos adicionais `OnTimerSet`, `OnTimerFired` e `OnTimerError`.

```go
type MetricsObserver struct {
    counter *prometheus.CounterVec // exemplo com Prometheus
}

func (o MetricsObserver) OnTransition(ctx context.Context, s fsm.Session[OrderState, OrderEvent, *OrderCtx]) {
    o.counter.WithLabelValues(string(s.StateFrom), string(s.Event), string(s.StateTo)).Inc()
}

func (o MetricsObserver) OnTimerError(ctx context.Context, sessionID string, event OrderEvent, err error) {
    log.Printf("timer failed for %s (event=%s): %v", sessionID, event, err)
}

// demais métodos (OnGuardRejected, OnActionError...) podem ser vazios se não forem usados.
```

### Conectando seu storage

Implemente a interface:

```go
type Storage[StateT, EventT fsm.Comparable, CtxT any] interface {
    Load(ctx context.Context, sessionID string) (state StateT, userCtx CtxT, ok bool, err error)
    Save(ctx context.Context, sessionID string, state StateT, userCtx CtxT) error
}
```

Para armazenar em Redis ou banco SQL, basta devolver o estado/ctx persistido em `Load` e armazenar em `Save`. Se `Load` retornar `ok=false`, a FSM assume estado inicial (`Config.Initial`).

## Testes

`fsm_test.go` (e arquivos complementares) cobrem:

- Persistência do contexto e disponibilidade antecipada de `StateTo`.
- Serialização de `Send` por sessão em cenário concorrente e timers com erro.
- Fechamento via `Close`, fila iterativa de `Dispatch` e limpeza de timers (`fsm_runtime_test.go`).

Cobertura atual (pacote principal) ≥ 97%. Gere um relatório completo com:

```bash
make coverage
go tool cover -html=coverage.out
```

### Como rodar a suíte

Targets prontos no `Makefile` já exportam `GOPROXY=off`, `GOSUMDB=off` e usam caches locais:

- `make test` — executa `go test ./...`.
- `make coverage` — grava `coverage.out` com os mesmos parâmetros offline.
- `make clean-cache` — remove `.gocache/` após a execução, mantendo o workspace limpo.

Caso prefira manualmente:

```bash
GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache go test ./...
```

## Exemplos

Exemplos incrementais estão em `examples/`:

- `examples/basic`: demonstra uma FSM on/off e expõe `buildMachine`/`runSequence` para reaproveitamento em testes.
- `examples/medium`: fluxo de pedidos com guards, middleware e `Dispatch`; `simulateOrder` retorna snapshots e é coberto por `go test`.
- `examples/advanced`: cenários com timers, retries, observer verboso e middlewares (`simulateJob` auxilia testes).

Todos os diretórios possuem `main.go` (demonstração CLI) e `main_test.go`, permitindo:

```bash
go test ./examples/...
go run ./examples/basic
```

Os testes evitam a necessidade de execuções manuais e ajudam a manter os exemplos em sincronia com a biblioteca principal.

## Roadmap / ideias

- Adicionar helpers para exportar diagramas de estados.
- Implementar storage pronto para Redis/PostgreSQL.
- Criar exemplos completos (cmd) com REST/CLI para demonstrar FSM em apps reais.

Pull requests e issues são bem-vindos! Ajuste o módulo e os exemplos conforme o caminho real do repositório na sua organização.
