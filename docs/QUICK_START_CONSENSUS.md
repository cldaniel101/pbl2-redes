# 🚀 Guia Rápido - Sistema de Consenso

## 📖 Leitura Recomendada (em ordem)

1. **Este arquivo** - Guia rápido para começar
2. `IMPLEMENTACAO_ITEM1_RESUMO.md` - Resumo completo do que foi implementado
3. `docs/CONSENSUS_STRUCTURES.md` - Documentação técnica detalhada
4. `docs/CONSENSUS_PROGRESS.md` - Status e progresso

---

## ⚡ Uso Rápido das Estruturas

### 1. VectorialClock - Relógio Vetorial

```go
import "pingpong/server/consensus"

// Criar relógio para um servidor
serverIDs := []string{"server-1", "server-2", "server-3"}
clock := consensus.NewVectorialClock("server-1", serverIDs)

// Antes de enviar uma mensagem ou fazer uma operação local
clock.Tick()
timestamp := clock.GetSnapshot()

// Ao receber uma mensagem de outro servidor
receivedTimestamp := message.Timestamp
clock.Update(receivedTimestamp)

// Comparar dois timestamps
cmp := clock.Compare(otherTimestamp)
// -1 = aconteceu antes, 0 = concorrente, +1 = aconteceu depois
```

### 2. Operation - Operação Distribuída

```go
// Criar uma operação de jogada
op := consensus.NewOperation(
    consensus.OpTypePlay,
    "match-123",        // ID da partida
    "server-1",         // ID do servidor
    "player-456",       // ID do jogador
    "card-789",         // ID da carta
    clock.GetSnapshot(), // Timestamp vetorial
)

// Comparar duas operações
if op1.IsBefore(op2) {
    // op1 aconteceu antes de op2
}

// Ver informações
log.Println(op.ToString())
```

### 3. OperationQueue - Fila de Operações

```go
// Criar fila
queue := consensus.NewOperationQueue()

// Adicionar operações (em qualquer ordem)
queue.Add(op1)
queue.Add(op2)
queue.Add(op3)

// A fila ordena automaticamente por timestamp

// Processar operações na ordem correta
for !queue.IsEmpty() {
    op := queue.Remove() // Remove e retorna a próxima
    processOperation(op)
}

// Ou apenas consultar sem remover
if !queue.IsEmpty() {
    nextOp := queue.GetTop() // Não remove
    log.Printf("Próxima operação: %s", nextOp.ToString())
}
```

### 4. Match com Consenso

```go
// Inicializar consenso em uma partida (chamado pelo StateManager)
allServers := []string{"server-1", "server-2", "server-3"}
match.InitializeConsensus("server-1", allServers)

// Criar operação de jogada
op := match.CreatePlayOperation("player-123", "card-456")
if op != nil {
    // Partida distribuída - operação criada
    
    // Broadcast PREPARE para outros servidores
    broadcastPrepare(op)
    
    // Registrar que está esperando ACKs
    match.AddPendingACK(op.ID, []string{"server-2", "server-3"})
}

// Ao receber ACK
if match.MarkACK(op.ID, "server-2") {
    // Todos ACKs recebidos - pode executar
    broadcastCommit(op)
}

// Verificar status
pendingACKs := match.GetPendingACKCount()
queueSize := match.GetOperationQueueSize()
```

---

## 🔄 Fluxo Típico de Uma Jogada

### Server-1 (Coordenador - onde jogador está)

```go
// 1. Jogador faz jogada
func handlePlayCard(matchID, playerID, cardID string) {
    match := getMatch(matchID)
    
    // Criar operação com timestamp
    op := match.CreatePlayOperation(playerID, cardID)
    if op == nil {
        // Partida local - processa diretamente
        match.PlayCard(playerID, cardID)
        return
    }
    
    // Partida distribuída - inicia consenso
    
    // 2. Broadcast PREPARE para outros servidores
    for _, serverID := range []string{"server-2", "server-3"} {
        sendPrepare(serverID, op)
    }
    
    // 3. Registrar que está esperando ACKs
    match.AddPendingACK(op.ID, []string{"server-2", "server-3"})
}

// 4. Ao receber ACK
func handleACK(matchID, operationID, fromServer string) {
    match := getMatch(matchID)
    
    if match.MarkACK(operationID, fromServer) {
        // Todos ACKs recebidos!
        
        // 5. Broadcast COMMIT
        for _, serverID := range []string{"server-2", "server-3"} {
            sendCommit(serverID, operationID)
        }
        
        // 6. Executar localmente
        executeOperation(match, operationID)
    }
}
```

### Server-2 e Server-3 (Participantes)

```go
// 1. Ao receber PREPARE
func handlePrepare(matchID string, op *consensus.Operation) {
    match := getMatch(matchID)
    
    // Atualizar relógio vetorial
    match.VectorClock.Update(op.Timestamp)
    
    // Adicionar à fila de operações
    match.OperationQueue.Add(op)
    
    // Enviar ACK para coordenador
    sendACK(op.ServerID, matchID, op.ID, "server-2") // ou "server-3"
}

// 2. Ao receber COMMIT
func handleCommit(matchID, operationID string) {
    match := getMatch(matchID)
    
    // Executar a operação
    executeOperation(match, operationID)
}
```

### Execução de Operações (Todos os Servidores)

```go
func executeOperation(match *Match, operationID string) {
    // Processar fila na ordem correta
    for !match.OperationQueue.IsEmpty() {
        op := match.OperationQueue.GetTop()
        
        if op.ID != operationID {
            // Ainda há operações anteriores pendentes
            break
        }
        
        // Esta é a operação a executar
        match.RemoveOperation()
        
        // Aplicar mudanças no estado
        switch op.Type {
        case consensus.OpTypePlay:
            applyPlayOperation(match, op)
        case consensus.OpTypeResolve:
            applyResolveOperation(match, op)
        case consensus.OpTypeEndMatch:
            applyEndMatchOperation(match, op)
        }
    }
}
```

---

## 🧪 Como Testar

### Executar Testes Unitários

```bash
cd server
go test -v ./consensus/
```

**Resultado esperado**: 8 testes passando

### Testar Manualmente no Código

```go
package main

import (
    "fmt"
    "pingpong/server/consensus"
)

func main() {
    // Criar relógios para 3 servidores
    serverIDs := []string{"server-1", "server-2", "server-3"}
    
    clock1 := consensus.NewVectorialClock("server-1", serverIDs)
    clock2 := consensus.NewVectorialClock("server-2", serverIDs)
    
    // Simular eventos
    clock1.Tick() // server-1 faz algo
    fmt.Println("Clock 1:", clock1.ToString()) // {server-1:1, server-2:0, server-3:0}
    
    clock2.Tick() // server-2 faz algo
    clock2.Tick() // server-2 faz mais algo
    fmt.Println("Clock 2:", clock2.ToString()) // {server-1:0, server-2:2, server-3:0}
    
    // server-1 envia mensagem para server-2
    msg := clock1.GetSnapshot()
    clock2.Update(msg)
    fmt.Println("Clock 2 após receber msg:", clock2.ToString()) 
    // {server-1:1, server-2:3, server-3:0}
    
    // Criar e ordenar operações
    queue := consensus.NewOperationQueue()
    
    ts1 := map[string]int{"server-1": 1, "server-2": 0}
    ts2 := map[string]int{"server-1": 2, "server-2": 1}
    ts3 := map[string]int{"server-1": 1, "server-2": 1}
    
    op1 := consensus.NewOperation(consensus.OpTypePlay, "match-1", "server-1", "player-1", "card-1", ts1)
    op2 := consensus.NewOperation(consensus.OpTypePlay, "match-1", "server-2", "player-2", "card-2", ts2)
    op3 := consensus.NewOperation(consensus.OpTypePlay, "match-1", "server-1", "player-1", "card-3", ts3)
    
    // Adicionar em ordem aleatória
    queue.Add(op2)
    queue.Add(op1)
    queue.Add(op3)
    
    // Processar na ordem correta
    fmt.Println("\nProcessando operações:")
    for !queue.IsEmpty() {
        op := queue.Remove()
        fmt.Printf("- %s\n", op.ToString())
    }
    // Resultado: op1, op3, op2 (ordenado por timestamp)
}
```

---

## 🎓 Conceitos Importantes

### Ordem Causal vs. Ordem Total

**Ordem Causal**: Se evento A causou evento B, então A < B
**Concorrência**: Se A não causou B e B não causou A, então A || B

```
Exemplo:
Server-1: [a1] -----> [a2] -----> [a3]
                       ↓
Server-2: [b1] <------  ------> [b2]

Ordem causal:
- a1 < a2  (a1 aconteceu antes de a2)
- a2 < a3  (a2 aconteceu antes de a3)
- a2 < b2  (a2 causou b2 via mensagem)
- b1 || a1 (concorrentes - não relacionados)
- b1 || a2 (concorrentes)
```

### Desempate de Operações Concorrentes

Quando duas operações são concorrentes (|| ), precisamos de uma regra de desempate:

```go
// Operações concorrentes
ts1 := {s1:5, s2:1}  // de server-1
ts2 := {s1:2, s2:3}  // de server-2

// Não podemos determinar ordem causal, então:
// Desempate por ServerID (ordem lexicográfica)
if "server-1" < "server-2" {
    op1 vem antes
}
```

Isso garante que todos os servidores processam na **mesma ordem**.

---

## ⚠️ Pontos de Atenção

### 1. Inicialização do VectorClock

❌ **Errado**:
```go
match := NewMatch(...)
match.VectorClock.Tick() // PANIC! VectorClock é nil
```

✅ **Correto**:
```go
match := NewMatch(...)
match.InitializeConsensus("server-1", []string{"server-1", "server-2", "server-3"})
match.VectorClock.Tick() // OK!
```

### 2. Limpeza de PendingACKs

❌ **Memory Leak**:
```go
match.AddPendingACK(op.ID, serverIDs)
// ... nunca chama MarkACK ou limpa ...
// PendingACKs cresce indefinidamente
```

✅ **Correto**:
```go
match.AddPendingACK(op.ID, serverIDs)
// ...
if match.MarkACK(op.ID, serverID) {
    // MarkACK remove automaticamente quando completo
}
```

### 3. Thread-Safety

✅ **Todas as estruturas são thread-safe**:
```go
// Pode ser chamado de múltiplas goroutines
go match.CreatePlayOperation(p1, c1)
go match.CreatePlayOperation(p2, c2)
// Sem race conditions
```

### 4. Verificar se é Partida Distribuída

✅ **Sempre verificar**:
```go
op := match.CreatePlayOperation(playerID, cardID)
if op == nil {
    // Partida local - processar diretamente
} else {
    // Partida distribuída - usar consenso
}
```

---

## 📚 Recursos Adicionais

### Documentação Completa
- `docs/CONSENSUS_STRUCTURES.md` - Tutorial técnico detalhado
- `docs/CONSENSUS_PROGRESS.md` - Status e checklist
- `IMPLEMENTACAO_ITEM1_RESUMO.md` - Resumo executivo

### Código Fonte
- `server/consensus/vectorial_clock.go` - Implementação do relógio
- `server/consensus/operation.go` - Implementação de operações
- `server/consensus/queue.go` - Implementação da fila
- `server/game/match.go` - Integração com Match

### Testes
- `server/consensus/consensus_test.go` - 8 testes unitários

### Referências Acadêmicas
- [Vector Clocks](https://en.wikipedia.org/wiki/Vector_clock)
- [Two-Phase Commit](https://en.wikipedia.org/wiki/Two-phase_commit_protocol)
- [Lamport Timestamps](https://en.wikipedia.org/wiki/Lamport_timestamp)

---

## 🎯 Status

**Item 1**: ✅ **100% CONCLUÍDO**

Todas as estruturas implementadas, testadas e documentadas.

**Próximo passo**: Implementar handlers S2S para comunicação entre servidores.

---

**Última atualização**: 2025-10-22

