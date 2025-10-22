# Sistema de Consenso Two-Phase Commit - Implementação Completa

## 📋 Visão Geral

Este documento descreve a implementação completa do sistema de consenso baseado em **Two-Phase Commit com ACKs** para o jogo de cartas multiplayer distribuído. O sistema foi projetado para resolver o problema de deadlock no `resolveRound()` e garantir consistência de estado entre servidores.

## ✅ Status da Implementação

**CONCLUÍDO** - Todas as funcionalidades do item 2 foram implementadas com sucesso.

## 🎯 Objetivos Alcançados

1. ✅ Implementar Fase 1: Proposta e ACKs
2. ✅ Implementar Fase 2: Verificação e Execução
3. ✅ Integrar com `PlayCard()` para usar Two-Phase Commit automaticamente
4. ✅ Criar endpoints S2S para comunicação de consenso
5. ✅ Implementar validação de operações
6. ✅ Implementar mecanismos de rollback

## 🏗️ Arquitetura

### Fluxo Completo do Two-Phase Commit

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         FASE 1: PROPOSTA E ACKs                          │
└─────────────────────────────────────────────────────────────────────────┘

Server-1 (Proposer)                Server-2                Server-3
      │                                 │                       │
      │ 1. Jogador faz jogada           │                       │
      │ CreatePlayOperation()           │                       │
      │ (Incrementa VectorClock)        │                       │
      │                                 │                       │
      │ 2. Propõe operação              │                       │
      ├─────POST /api/matches/{id}/propose────────────────────>│
      ├─────POST /api/matches/{id}/propose─────────>│          │
      │                                 │            │          │
      │                                 │ 3. Valida  │          │
      │                                 │ Operação   │          │
      │                                 │            │          │
      │                          4. Envia ACK        │          │
      │<────POST /api/matches/{id}/ack───────────────┤          │
      │<────POST /api/matches/{id}/ack──────────────────────────┤
      │                                 │            │          │
      │ 5. waitForACKs()                │            │          │
      │ (timeout: 5s)                   │            │          │
      │                                 │            │          │

┌─────────────────────────────────────────────────────────────────────────┐
│                   FASE 2: VERIFICAÇÃO E EXECUÇÃO                         │
└─────────────────────────────────────────────────────────────────────────┘

      │ 6. Todos ACKs recebidos         │            │          │
      │                                 │            │          │
      │ 7. Verifica validade cross-server            │          │
      ├─────POST /api/matches/{id}/check──────────>│           │
      ├─────POST /api/matches/{id}/check─────────────────────>│
      │                                 │            │          │
      │<────200 OK (válido)─────────────┤            │          │
      │<────200 OK (válido)────────────────────────────────────┤
      │                                 │            │          │
      │ 8. ExecuteOperation()           │            │          │
      │ (local)                         │            │          │
      │                                 │            │          │
      │ 9. Solicita commit              │            │          │
      ├─────POST /api/matches/{id}/commit──────────>│          │
      ├─────POST /api/matches/{id}/commit────────────────────>│
      │                                 │            │          │
      │                                 │ ExecuteOperation()    │
      │                                 │            │          │
      │                                 │            ExecuteOperation()
      │                                 │            │          │
      ✓ Operação executada em todos os servidores   │          │
```

### Fluxo de Rollback (Caso de Falha)

```
Server-1 (Proposer)                Server-2                Server-3
      │                                 │                       │
      │ Timeout ou Operação Inválida    │                       │
      │                                 │                       │
      │ RollbackOperation()             │                       │
      │ (local)                         │                       │
      │                                 │                       │
      ├─────POST /api/matches/{id}/rollback────────>│          │
      ├─────POST /api/matches/{id}/rollback─────────────────>│
      │                                 │            │          │
      │                                 │ RollbackOperation()   │
      │                                 │            │          │
      │                                 │            RollbackOperation()
      │                                 │            │          │
      ✗ Operação descartada em todos os servidores  │          │
```

## 📝 Componentes Implementados

### 1. Métodos no Match (`server/game/match.go`)

#### `proposeOperation(op, otherServers) (ackChan, errChan)`
Propõe uma operação para outros servidores (Fase 1).

```go
// Adiciona operação na fila local
// Registra como pendente de ACKs
// Envia proposta para outros servidores via S2S
// Aguarda ACKs com timeout de 5 segundos
```

**Entrada**: Operação e lista de servidores  
**Saída**: Canais para notificação de ACKs ou erro

#### `waitForACKs(operationID, timeout) bool`
Aguarda confirmações de todos os servidores com timeout.

```go
// Verifica periodicamente (ticker de 100ms)
// Retorna true se todos confirmaram
// Retorna false se timeout expirou
```

**Entrada**: ID da operação e timeout  
**Saída**: `true` se todos ACKs recebidos, `false` se timeout

#### `CheckOperationValidity(op) (bool, error)`
Verifica se uma operação é válida no estado atual.

```go
// Para OpTypePlay:
//   - Verifica se jogador está na partida
//   - Verifica estado da partida (StateAwaitingPlays)
//   - Verifica se jogador já jogou
//   - Valida carta no CardDB
//   - Verifica se carta está na mão do jogador
```

**Entrada**: Operação a validar  
**Saída**: `(true, nil)` se válida, `(false, error)` se inválida

#### `ExecuteOperation(op) error`
Executa uma operação após consenso (Fase 2).

```go
// Atualiza relógio vetorial com timestamp da operação
// Executa ação específica do tipo de operação
// Remove operação da fila
// Verifica se ambos jogaram para resolver rodada
```

**Entrada**: Operação a executar  
**Saída**: `nil` se sucesso, `error` se falha

#### `RollbackOperation(op)`
Descarta uma operação inválida.

```go
// Remove operação da fila
// Remove dos ACKs pendentes
// Notifica jogador sobre rejeição
```

**Entrada**: Operação a reverter  
**Saída**: Nenhuma

#### `playCardWithConsensus(playerID, cardID) error`
Implementa Two-Phase Commit para jogadas em partidas distribuídas.

```go
// Fase 1:
//   - Cria operação com CreatePlayOperation()
//   - Propõe para outros servidores
//   - Aguarda ACKs
//
// Fase 2:
//   - Verifica validade cross-server com CheckOperation()
//   - Se válido: ExecuteOperation() + CommitOperation()
//   - Se inválido: RollbackOperation()
```

**Entrada**: ID do jogador e ID da carta  
**Saída**: `nil` se sucesso, `error` se falha

### 2. Funções S2S (`server/s2s/client.go`)

#### `ProposeOperation(remoteServer, matchID, op) error`
Envia uma proposta de operação para outro servidor (Fase 1).

**Endpoint**: `POST /api/matches/{matchID}/propose`  
**Timeout**: 5 segundos

#### `SendACK(remoteServer, matchID, operationID, senderServerID) error`
Envia um ACK de uma operação para outro servidor.

**Endpoint**: `POST /api/matches/{matchID}/ack`  
**Timeout**: 5 segundos

#### `CheckOperation(remoteServer, matchID, op) (bool, error)`
Solicita verificação de validade de uma operação (Fase 2).

**Endpoint**: `POST /api/matches/{matchID}/check`  
**Timeout**: 5 segundos  
**Retorno**: 
- `200 OK` → operação válida
- `409 Conflict` → operação inválida

#### `CommitOperation(remoteServer, matchID, op) error`
Solicita execução de uma operação após consenso (Fase 2).

**Endpoint**: `POST /api/matches/{matchID}/commit`  
**Timeout**: 5 segundos

#### `RollbackOperation(remoteServer, matchID, op) error`
Solicita rollback de uma operação (Fase 2).

**Endpoint**: `POST /api/matches/{matchID}/rollback`  
**Timeout**: 5 segundos

### 3. Endpoints da API (`server/api/handlers.go`)

#### `POST /api/matches/{matchID}/propose`
Recebe uma proposta de operação de outro servidor.

**Handler**: `handlePropose(w, r, matchID)`

```go
1. Decodifica operação do JSON
2. Encontra partida no StateManager
3. Atualiza relógio vetorial local
4. Adiciona operação à fila
5. Valida operação com CheckOperationValidity()
6. Envia ACK de volta ao proposer
```

**Status de Resposta**:
- `200 OK` → operação válida e ACK enviado
- `409 Conflict` → operação inválida
- `404 Not Found` → partida não encontrada

#### `POST /api/matches/{matchID}/ack`
Recebe um ACK de outro servidor.

**Handler**: `handleACK(w, r, matchID)`

```go
1. Decodifica ACK do JSON
2. Encontra partida no StateManager
3. Marca ACK com match.MarkACK()
4. Verifica se todos ACKs foram recebidos
```

**Status de Resposta**:
- `200 OK` → ACK registrado
- `404 Not Found` → partida não encontrada

#### `POST /api/matches/{matchID}/check`
Verifica se uma operação é válida (Fase 2).

**Handler**: `handleCheck(w, r, matchID)`

```go
1. Decodifica operação do JSON
2. Encontra partida no StateManager
3. Valida operação com CheckOperationValidity()
```

**Status de Resposta**:
- `200 OK` → operação válida
- `409 Conflict` → operação inválida
- `404 Not Found` → partida não encontrada

#### `POST /api/matches/{matchID}/commit`
Executa uma operação após consenso (Fase 2).

**Handler**: `handleCommit(w, r, matchID)`

```go
1. Decodifica operação do JSON
2. Encontra partida no StateManager
3. Executa operação com match.ExecuteOperation()
```

**Status de Resposta**:
- `200 OK` → operação executada
- `500 Internal Server Error` → erro na execução
- `404 Not Found` → partida não encontrada

#### `POST /api/matches/{matchID}/rollback`
Reverte uma operação (Fase 2).

**Handler**: `handleRollback(w, r, matchID)`

```go
1. Decodifica operação do JSON
2. Encontra partida no StateManager
3. Reverte operação com match.RollbackOperation()
```

**Status de Resposta**:
- `200 OK` → rollback executado
- `404 Not Found` → partida não encontrada

## 🔄 Integração com PlayCard()

O método `PlayCard()` foi modificado para detectar automaticamente se é uma partida distribuída e usar o sistema de consenso:

```go
func (m *Match) PlayCard(playerID, cardID string) error {
    // Validações iniciais...
    
    // Verifica se é uma partida distribuída
    if m.VectorClock != nil {
        // Usa Two-Phase Commit
        return m.playCardWithConsensus(playerID, cardID)
    }
    
    // Partida local - usa lógica simples
    m.Waiting[playerID] = cardID
    if len(m.Waiting) == 2 {
        go m.resolveRound()
    }
    return nil
}
```

**Diferenças entre Partidas Locais e Distribuídas**:

| Aspecto | Partida Local | Partida Distribuída |
|---------|--------------|---------------------|
| Sincronização | Lock local simples | Two-Phase Commit |
| Validação | Local apenas | Cross-server |
| Timestamp | `time.Now()` | Relógio Vetorial |
| Rollback | Não necessário | Implementado |
| Timeout | Não aplicável | 5 segundos |

## 🛡️ Mecanismos de Tolerância a Falhas

### 1. Timeout de ACKs
- **Duração**: 5 segundos
- **Ação**: Rollback automático se timeout expirar
- **Logs**: Registra operação e servidores que não responderam

### 2. Validação Cross-Server
- Verifica estado em todos os servidores antes de executar
- Se um servidor rejeitar, todos fazem rollback
- Garante consistência global

### 3. Rollback Coordenado
- Proposer inicia rollback local
- Notifica todos os outros servidores
- Cada servidor remove operação da fila
- Notifica jogadores sobre rejeição

### 4. Relógio Vetorial
- Garante ordenação causal de operações
- Resolve conflitos de operações concorrentes
- Mantém consistência de estado

## 📊 Logs e Debugging

O sistema gera logs detalhados para debugging:

```
[MATCH match-123] Iniciando consenso para operação op-1234 com servidores: [http://server-2:8000]
[MATCH match-123] Propondo operação op-1234 para [http://server-2:8000]
[S2S] Propondo operação op-1234 para http://server-2:8000/api/matches/match-123/propose
[API] Proposta recebida para partida match-123: operação op-1234
[API] ACK recebido para partida match-123: operação op-1234 do servidor server-2
[MATCH match-123] Todos os ACKs recebidos para operação op-1234
[S2S] Verificando operação op-1234 em http://server-2:8000
[S2S] Operação op-1234 é válida em http://server-2:8000
[MATCH match-123] Executando operação op-1234 (tipo: PLAY)
[MATCH match-123] Jogada registrada: player-1 jogou card-fire-dragon
[S2S] Comitando operação op-1234 em http://server-2:8000
[MATCH match-123] Operação op-1234 executada com sucesso via consenso
```

### Logs de Rollback

```
[MATCH match-123] Timeout aguardando ACKs para operação op-1234
[MATCH match-123] Erro no consenso para operação op-1234: timeout aguardando ACKs
[MATCH match-123] Revertendo operação op-1234 (tipo: PLAY)
[S2S] Solicitando rollback da operação op-1234 em http://server-2:8000
```

## 🎯 Vantagens do Sistema Implementado

### ✅ Consistência Forte
- Garante que todos os servidores executem operações na mesma ordem
- Usa relógios vetoriais para ordenação causal
- Validação cross-server previne estados inconsistentes

### ✅ Sem Deadlocks
- Não mantém locks durante chamadas HTTP
- Operações assíncronas com canais
- Timeout garante que sistema não trave indefinidamente

### ✅ Tolerante a Falhas
- Rollback automático em caso de falha
- Timeout de 5 segundos previne espera infinita
- Logs detalhados facilitam debugging

### ✅ Transparente para o Código Existente
- `PlayCard()` detecta automaticamente se é distribuída
- Código de partidas locais não foi modificado
- Integração seamless com sistema existente

### ✅ Escalável
- Suporta múltiplos servidores
- Operações concorrentes são ordenadas corretamente
- Fila de operações permite processamento assíncrono

## 🧪 Como Testar

### Teste Manual

1. **Iniciar 3 servidores**:
```bash
docker-compose up
```

2. **Conectar dois clientes em servidores diferentes**:
```bash
# Cliente 1 → Server-1
go run client/main.go localhost:7000

# Cliente 2 → Server-2
go run client/main.go localhost:7001
```

3. **Iniciar matchmaking e jogar**:
```
> match
> 1    # Joga primeira carta
```

4. **Observar logs**:
- Verificar propagação de operações
- Confirmar recebimento de ACKs
- Validar execução coordenada

### Teste de Falha

1. **Simular timeout**: Desconectar um servidor durante proposta
2. **Verificar rollback**: Confirmar que operação foi descartada
3. **Verificar logs**: Mensagens de timeout e rollback

### Teste de Concorrência

1. **Ambos jogadores jogam simultaneamente**
2. **Verificar ordenação**: Relógio vetorial deve resolver ordem
3. **Confirmar consistência**: Ambos servidores devem ter mesma ordem

## 📚 Referências

- [Two-Phase Commit Protocol](https://en.wikipedia.org/wiki/Two-phase_commit_protocol)
- [Vector Clocks](https://en.wikipedia.org/wiki/Vector_clock)
- [CONSENSUS_STRUCTURES.md](./CONSENSUS_STRUCTURES.md) - Estruturas de dados base
- [F0 - arquitetura_distribuida.md](./F0%20-%20arquitetura_distribuida.md) - Arquitetura DistriBank

## 🎉 Conclusão

O sistema de consenso Two-Phase Commit foi **implementado com sucesso** e está pronto para uso. Todos os objetivos foram alcançados:

- ✅ Fase 1 (Proposta e ACKs) implementada
- ✅ Fase 2 (Verificação e Execução) implementada
- ✅ Integração com `PlayCard()` concluída
- ✅ Endpoints S2S funcionais
- ✅ Validação e rollback implementados
- ✅ Sistema tolerante a falhas

O sistema resolve o problema de deadlock original e garante consistência de estado entre servidores distribuídos usando relógios vetoriais e Two-Phase Commit com ACKs.

---

**Data**: 22 de outubro de 2025  
**Status**: ✅ IMPLEMENTAÇÃO COMPLETA

