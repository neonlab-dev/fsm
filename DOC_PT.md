# FSM (Máquina de Estados) em Go

Biblioteca leve para construir máquinas de estados tipadas usando generics. A API é direta, mas oferece pontos de extensão para storage, logs e métricas quando você precisar.

## Destaques

- Estados e eventos como enums tipados (`~int`, `~string`, etc.)
- Guards e actions encadeadas com acesso ao contexto da sessão
- Concorrência segura: `Send` é serializado por `sessionID`
- Timers que disparam eventos após entrar em um estado
- Observers e middlewares para logs, tracing ou métricas
- `MemStorage` pronto para testes ou protótipos
- `Session.Dispatch` agenda eventos logo após a transição atual
- Middleware `WithOTelActionSpans` cria spans OpenTelemetry para cada action

## Exemplo rápido

```go
type Estado string
const (
    EstadoInit  Estado = "init"
    EstadoPronto Estado = "ready"
)

type Evento string
const (
    EventoComecar Evento = "start"
)

type Ctx struct {
    Nome string
}

import "github.com/neonlab-dev/fsm"

store := fsm.NewMemStorage[Estado, Evento, *Ctx](EstadoInit)
maquina := fsm.New(fsm.Config[Estado, Evento, *Ctx]{
    Name:    "demo",
    Initial: EstadoInit,
    Storage: store,
})

maquina.State(EstadoInit).OnEvent(EventoComecar).
    Action(func(ctx context.Context, s *fsm.Session[Estado, Evento, *Ctx]) error {
        if s.Ctx == nil {
            s.Ctx = &Ctx{}
        }
        s.Ctx.Nome = "Usuário"
        return nil
    }).
    To(EstadoPronto)

if err := maquina.Send(context.Background(), "sessao-1", EventoComecar, nil); err != nil {
    log.Fatal(err)
}
defer maquina.Close(context.Background())
```

## Testes

Use o `Makefile` incluído para rodar tudo offline:

```bash
make test        # executa go test ./... com GOPROXY=off e caches locais
make coverage    # gera coverage.out (cobertura ≈97% no pacote principal)
make clean-cache # remove .gocache/ depois da execução
```

Se preferir rodar manualmente:

```bash
GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache \
    GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
    go test ./...
```

Os exemplos em `examples/basic`, `examples/medium` e `examples/advanced` possuem `*_test.go`, então `go test ./examples/...` valida os cenários incrementalmente.
