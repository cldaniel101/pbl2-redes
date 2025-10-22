# Estruturas de Dados do Sistema de Consenso

Este documento descreve as estruturas de dados implementadas para o sistema de consenso distribuído baseado em Two-Phase Commit com ACKs e relógios vetoriais.

## 📋 Visão Geral

O sistema de consenso foi implementado para resolver o problema de deadlock no `resolveRound()` e garantir consistência de estado entre servidores distribuídos. A solução utiliza:

- **Relógios Vetoriais** para ordenação causal de eventos
- **Fila de Operações** ordenada por timestamp vetorial
- **Sistema de ACKs** para garantir que todos os servidores confirmaram as operações

## 🕐 VectorialClock

### Localização
`server/consensus/vectorial_clock.go`

### Descrição
Implementa um relógio vetorial (Vector Clock) para ordenação causal de eventos em sistemas distribuídos. Cada servidor mantém um timestamp vetorial com contadores para todos os servidores do cluster.

### Estrutura
```go
type VectorialClock struct {
    mu      sync.RWMutex
    clocks  map[string]int // serverID -> timestamp
    ownerID string         // ID do servidor que possui este relógio
}
```

### Métodos Principais

#### `NewVectorialClock(ownerID string, serverIDs []string) *VectorialClock`
Cria um novo relógio vetorial inicializado com zeros para todos os servidores.

#### `Tick()`
Incrementa o contador do próprio servidor. Deve ser chamado antes de enviar uma mensagem ou executar uma operação local.

```go
clock.Tick() // Incrementa o contador local
```

#### `Update(other map[string]int)`
Atualiza o relógio vetorial com base em outro relógio (merge). Usado quando recebe uma mensagem de outro servidor.

```go
// Ao receber mensagem, atualiza com o timestamp da mensagem
clock.Update(messageTimestamp)
```

#### `Compare(other map[string]int) int`
Compara dois relógios vetoriais para determinar ordenação causal:
- Retorna `-1` se este relógio aconteceu **antes** do outro
- Retorna `0` se são **concorrentes** (incomparáveis)
- Retorna `+1` se este relógio aconteceu **depois** do outro

```go
cmp := clock1.Compare(clock2.GetSnapshot())
if cmp < 0 {
    // clock1 aconteceu antes de clock2
}
```

#### `GetSnapshot() map[string]int`
Retorna uma cópia do estado atual do relógio (thread-safe).

#### `ToString() string`
Retorna representação em string: `{server-1:5, server-2:3, server-3:7}`

### Exemplo de Uso

```go
// Inicialização
serverIDs := []string{"server-1", "server-2", "server-3"}
clock := consensus.NewVectorialClock("server-1", serverIDs)

// Evento local
clock.Tick()
timestamp := clock.GetSnapshot()

// Enviar timestamp com a mensagem
sendMessage(data, timestamp)

// Ao receber mensagem
receivedTimestamp := message.Timestamp
clock.Update(receivedTimestamp)
```

## 📝 Operation

### Localização
`server/consensus/operation.go`

### Descrição
Representa uma operação em uma partida distribuída. Todas as operações são ordenadas por timestamp vetorial para garantir consistência.

### Estrutura
```go
type Operation struct {
    // Identificação
    ID       string        // ID único da operação (UUID)
    Type     OperationType // Tipo da operação (PLAY, RESOLVE, END_MATCH)
    MatchID  string        // ID da partida
    ServerID string        // ID do servidor que criou a operação

    // Dados da operação
    PlayerID string // ID do jogador que executou a ação
    CardID   string // ID da carta jogada (apenas para PLAY)

    // Timestamp vetorial para ordenação
    Timestamp map[string]int // Relógio vetorial no momento da operação

    // Metadados
    CreatedAt time.Time // Timestamp real da criação (apenas para debug)
}
```

### Tipos de Operação
```go
const (
    OpTypePlay     OperationType = "PLAY"      // Jogada de carta
    OpTypeResolve  OperationType = "RESOLVE"   // Resolução de rodada
    OpTypeEndMatch OperationType = "END_MATCH" // Fim de partida
)
```

### Métodos Principais

#### `NewOperation(opType, matchID, serverID, playerID, cardID string, timestamp map[string]int) *Operation`
Cria uma nova operação com os dados fornecidos.

#### `CompareTimestamp(other *Operation) int`
Compara o timestamp vetorial de duas operações:
- Retorna `-1` se esta operação aconteceu **antes**
- Retorna `0` se são **concorrentes**
- Retorna `+1` se esta operação aconteceu **depois**

Em caso de operações concorrentes, usa o `ServerID` como desempate (ordem lexicográfica) para garantir ordem total determinística.

#### `IsBefore(other *Operation) bool`
Verifica se esta operação aconteceu antes de outra.

#### `IsAfter(other *Operation) bool`
Verifica se esta operação aconteceu depois de outra.

#### `IsConcurrent(other *Operation) bool`
Verifica se esta operação é concorrente com outra.

### Exemplo de Uso

```go
// Criar operação de jogada
op := consensus.NewOperation(
    consensus.OpTypePlay,
    "match-123",
    "server-1",
    "player-456",
    "card-789",
    vectorClock.GetSnapshot(),
)

// Comparar operações
if op1.IsBefore(op2) {
    // Processar op1 primeiro
}
```

## 📊 OperationQueue

### Localização
`server/consensus/queue.go`

### Descrição
Fila de prioridade ordenada por timestamp vetorial. Usa um heap para manter as operações ordenadas, garantindo que operações anteriores sejam processadas primeiro.

### Estrutura
```go
type OperationQueue struct {
    mu    sync.RWMutex
    items *operationHeap
}
```

### Métodos Principais

#### `NewOperationQueue() *OperationQueue`
Cria uma nova fila de operações vazia.

#### `Add(op *Operation)`
Adiciona uma operação à fila. A operação é inserida na posição correta baseada no timestamp vetorial.

#### `Remove() *Operation`
Remove e retorna a operação com menor timestamp (a que aconteceu primeiro). Retorna `nil` se a fila estiver vazia.

#### `GetTop() *Operation`
Retorna (sem remover) a operação com menor timestamp. Retorna `nil` se a fila estiver vazia.

#### `IsEmpty() bool`
Verifica se a fila está vazia.

#### `Size() int`
Retorna o número de operações na fila.

#### `Contains(operationID string) bool`
Verifica se uma operação com determinado ID está na fila.

#### `RemoveByID(operationID string) bool`
Remove uma operação específica pelo ID. Retorna `true` se encontrou e removeu.

#### `GetAll() []*Operation`
Retorna todas as operações na fila (sem remover).

#### `Clear()`
Remove todas as operações da fila.

### Exemplo de Uso

```go
// Criar fila
queue := consensus.NewOperationQueue()

// Adicionar operações (podem ser em qualquer ordem)
queue.Add(op1)
queue.Add(op2)
queue.Add(op3)

// Processar operações na ordem causal
for !queue.IsEmpty() {
    op := queue.Remove()
    processOperation(op)
}

// Verificar próxima operação sem remover
if !queue.IsEmpty() {
    nextOp := queue.GetTop()
    log.Printf("Próxima operação: %s", nextOp.ToString())
}
```

## 🎮 Modificações no Match

### Novos Campos

```go
type Match struct {
    // ... campos existentes ...

    // Campos para o sistema de consenso distribuído
    OperationQueue *consensus.OperationQueue     // Fila de operações ordenada
    VectorClock    *consensus.VectorialClock     // Relógio vetorial da partida
    PendingACKs    map[string]map[string]bool    // operationID -> {serverID -> acked}
    ackMu          sync.RWMutex                  // Mutex para PendingACKs
}
```

### Novos Métodos

#### `InitializeConsensus(serverID string, allServerIDs []string)`
Inicializa o relógio vetorial e estruturas de consenso. Deve ser chamado pelo `StateManager` ao criar partidas distribuídas.

```go
match.InitializeConsensus("server-1", []string{"server-1", "server-2", "server-3"})
```

#### `AddPendingACK(operationID string, serverIDs []string)`
Registra que uma operação está aguardando ACKs de determinados servidores.

```go
match.AddPendingACK(op.ID, []string{"server-2", "server-3"})
```

#### `MarkACK(operationID, serverID string) bool`
Marca que um servidor confirmou uma operação. Retorna `true` se todos os ACKs foram recebidos.

```go
if match.MarkACK(op.ID, "server-2") {
    // Todos os servidores confirmaram, pode executar a operação
}
```

#### `CreatePlayOperation(playerID, cardID string) *Operation`
Cria uma operação de jogada com timestamp vetorial atualizado.

```go
op := match.CreatePlayOperation("player-123", "card-456")
if op != nil {
    // Operação criada (partida distribuída)
    broadcastOperation(op)
}
```

#### `ProcessOperation() *Operation`
Retorna a próxima operação da fila (sem remover).

#### `RemoveOperation() *Operation`
Remove e retorna a próxima operação da fila.

#### `GetOperationQueueSize() int`
Retorna o tamanho da fila de operações.

#### `GetPendingACKCount() int`
Retorna o número de operações aguardando ACKs.

## 🔄 Fluxo de Consenso (Resumo)

### 1. Jogador faz uma jogada
```go
// Server-1 recebe jogada
match.CreatePlayOperation(playerID, cardID) // Cria operação com timestamp
// Envia operação para outros servidores
```

### 2. Outros servidores recebem a operação
```go
// Server-2 e Server-3 recebem
match.VectorClock.Update(op.Timestamp) // Atualiza relógio
match.OperationQueue.Add(op)           // Adiciona à fila
// Envia ACK para Server-1
```

### 3. Server-1 recebe ACKs
```go
if match.MarkACK(op.ID, "server-2") {
    // Todos confirmaram
    match.RemoveOperation() // Remove da fila
    executeOperation(op)    // Executa
}
```

### 4. Execução ordenada
```go
// Todos os servidores processam operações na mesma ordem
// graças aos timestamps vetoriais
for !match.OperationQueue.IsEmpty() {
    op := match.OperationQueue.GetTop()
    if allACKsReceived(op) {
        match.RemoveOperation()
        executeOperation(op)
    }
}
```

## ✅ Testes

Todos os testes estão em `server/consensus/consensus_test.go` e cobrem:

- ✓ Funcionamento básico do relógio vetorial (Tick, Update)
- ✓ Comparação de relógios vetoriais
- ✓ Ordenação de operações
- ✓ Fila de operações (Add, Remove, GetTop)
- ✓ Remoção por ID
- ✓ Clonagem de estruturas

Execute os testes com:
```bash
cd server
go test -v ./consensus/
```

## 📚 Referências

- [Vector Clocks - Lamport Timestamps](https://en.wikipedia.org/wiki/Vector_clock)
- [Two-Phase Commit Protocol](https://en.wikipedia.org/wiki/Two-phase_commit_protocol)
- [DistriBank - Arquitetura de Referência](../docs/F0%20-%20arquitetura_distribuida.md)

## 🎯 Próximos Passos

1. **Integração com StateManager** - Inicializar consenso ao criar partidas distribuídas
2. **Handlers S2S** - Criar endpoints para PREPARE, ACK, COMMIT
3. **Coordinator** - Implementar lógica de coordenação do Two-Phase Commit
4. **Timeout & Rollback** - Implementar tratamento de falhas e rollback
5. **Testes de Integração** - Testar com múltiplos servidores simultâneos

---

**Status**: ✅ Item 1 (Estruturas de Dados e Estado) - **CONCLUÍDO**

