# Item 5: Sistema de Detecção de Falhas - Resumo da Implementação

## ✅ Checklist de Implementação

### 🔧 **5. Sistema de Detecção de Falhas**

- [x]  **Heartbeat System** (`server/consensus/heartbeat.go`)
    - [x]  Thread para pingar outros servidores periodicamente
    - [x]  Detectar servidores inativos
    - [x]  Atualizar flags de disponibilidade
- [x]  **Tratamento de Timeouts**
    - [x]  Timeout para ACKs não recebidos
    - [x]  Timeout para verificações não respondidas
    - [x]  Rollback automático em caso de falha

## 📁 Arquivos Criados

### Novos Arquivos

1. **`server/consensus/heartbeat.go`** (396 linhas)
   - Sistema de heartbeat com pings periódicos
   - Gerenciamento de status de servidores (vivo/morto)
   - Callbacks para eventos de morte e revivência
   - Registro de falhas consecutivas

2. **`server/consensus/timeout.go`** (334 linhas)
   - Gerenciador de timeouts para operações
   - Watchers com rollback automático
   - Estatísticas de timeouts (ACK e Check)
   - Helpers para timeout com rollback

3. **`server/consensus/failure_detector.go`** (411 linhas)
   - Detector integrado (heartbeat + timeout)
   - Rastreamento de operações e servidores envolvidos
   - Detecção de impacto quando servidor morre
   - Callbacks configuráveis para falhas

4. **`docs/FAILURE_DETECTION_SYSTEM.md`** (Documentação completa)
   - Arquitetura do sistema
   - Exemplos de uso
   - Fluxos de detecção
   - Guia de testes

5. **`docs/ITEM5_DETECCAO_FALHAS_RESUMO.md`** (Este arquivo)
   - Resumo da implementação
   - Checklist de funcionalidades
   - Lista de arquivos modificados

### Arquivos Modificados

1. **`server/api/handlers.go`**
   - Adicionado campo `failureDetector` ao `APIServer`
   - Método `SetFailureDetector()` para configuração
   - Endpoint `POST /api/health` para heartbeat
   - Endpoint `GET /api/failure-detector/status` para debug

2. **`server/game/match.go`**
   - Adicionado campo `FailureDetector` ao `Match`
   - Método `SetFailureDetector()` para configuração
   - Integração com `proposeOperation()` para timeout automático
   - Rastreamento de operações com servidores envolvidos

3. **`server/state/manager.go`**
   - Adicionado campo `failureDetector` ao `StateManager`
   - Método `SetFailureDetector()` para configuração

4. **`server/main.go`**
   - Inicialização do `FailureDetector` no startup
   - Registro de todos os servidores do cluster
   - Configuração de callbacks para falhas
   - Integração com `APIServer` e `StateManager`

## 🎯 Funcionalidades Implementadas

### 1. Heartbeat System

#### Características
- **Intervalo de Ping**: 2 segundos (configurável)
- **Timeout de Ping**: 3 segundos
- **Falhas Máximas**: 3 consecutivas
- **Endpoint**: `POST /api/health`

#### Funcionamento
```
1. Envia ping HTTP para /api/health a cada 2s
2. Se receber resposta 200 OK: marca servidor como vivo
3. Se timeout ou erro: incrementa contador de falhas
4. Se falhas >= 3: marca servidor como morto
5. Dispara callback onServerDeath()
```

#### Estruturas
```go
type HeartbeatSystem struct {
    serverID        string
    servers         map[string]*ServerStatus
    heartbeatTicker *time.Ticker
    checkInterval   time.Duration
    timeout         time.Duration
    maxFailures     int
    onServerDeath   func(serverID string)
    onServerRevive  func(serverID string)
}

type ServerStatus struct {
    ServerID      string
    Address       string
    IsAlive       bool
    LastHeartbeat time.Time
    LastCheck     time.Time
    FailureCount  int
}
```

### 2. Timeout Manager

#### Características
- **Timeout de ACK**: 5 segundos (configurável)
- **Timeout de Check**: 5 segundos (configurável)
- **Rollback Automático**: Sim
- **Estatísticas**: Total de timeouts por tipo

#### Funcionamento
```
1. Inicia watcher quando operação é proposta
2. Configura timer com callback de rollback
3. Se timer expira: executa rollback automático
4. Se operação completa: cancela timer
5. Registra estatísticas de timeouts
```

#### Estruturas
```go
type TimeoutManager struct {
    watchers         map[string]*TimeoutWatcher
    defaultACKTime   time.Duration
    defaultCheckTime time.Duration
    onTimeout        func(*TimeoutEvent)
    stats            TimeoutStats
}

type TimeoutWatcher struct {
    OperationID string
    Type        TimeoutType // ACK ou CHECK
    Timeout     time.Duration
    StartTime   time.Time
    timer       *time.Timer
    onTimeout   func(*TimeoutEvent)
}
```

### 3. Failure Detector

#### Características
- **Integrado**: Combina Heartbeat + Timeout
- **Rastreamento**: Operações → Servidores envolvidos
- **Impacto**: Detecta operações afetadas por servidor morto
- **Callbacks**: onServerFail, onOperationFail

#### Funcionamento
```
1. Inicia heartbeat para todos os servidores
2. Registra operações com servidores envolvidos
3. Se servidor morre:
   - Identifica operações afetadas
   - Marca operações como falhas
   - Dispara callbacks
4. Se operação tem timeout:
   - Executa rollback automático
   - Notifica jogador
```

#### Estruturas
```go
type FailureDetector struct {
    serverID         string
    heartbeatSystem  *HeartbeatSystem
    timeoutManager   *TimeoutManager
    onOperationFail  func(operationID, serverID, reason string)
    onServerFail     func(serverID string)
    operationServers map[string][]string // op -> servidores
    failedOperations map[string]string   // op -> reason
}
```

## 🔌 Pontos de Integração

### 1. APIServer
```go
// handlers.go
type APIServer struct {
    // ...
    failureDetector *consensus.FailureDetector
}

// Endpoints
POST /api/health                     // Recebe pings de heartbeat
GET  /api/failure-detector/status    // Status do detector (debug)
```

### 2. Match
```go
// match.go
type Match struct {
    // ...
    FailureDetector *consensus.FailureDetector
}

// Métodos
func (m *Match) SetFailureDetector(fd *FailureDetector)
func (m *Match) proposeOperation(op, servers) {
    // Inicia timeout watch automático
    if m.FailureDetector != nil {
        m.FailureDetector.WatchACKs(op.ID, servers, timeout, rollback)
    }
}
```

### 3. StateManager
```go
// manager.go
type StateManager struct {
    // ...
    failureDetector interface{ IsServerAlive(string) bool }
}

func (sm *StateManager) SetFailureDetector(fd)
```

### 4. Main
```go
// main.go
func main() {
    // 1. Criar FailureDetector
    failureDetector := consensus.NewFailureDetector(serverID, config)
    
    // 2. Registrar servidores
    for _, server := range allServers {
        failureDetector.RegisterServer(serverID, address)
    }
    
    // 3. Configurar callbacks
    failureDetector.SetOnServerFail(callback)
    failureDetector.SetOnOperationFail(callback)
    
    // 4. Iniciar
    failureDetector.Start()
    
    // 5. Injetar em componentes
    apiServer.SetFailureDetector(failureDetector)
    stateManager.SetFailureDetector(failureDetector)
}
```

## 📊 Fluxos Implementados

### Fluxo 1: Detecção de Servidor Morto
```
[Heartbeat System] → [Ping Periódico] → [Timeout/Erro]
                                              ↓
                                    [Incrementa Falhas]
                                              ↓
                                    [Falhas >= MaxFailures?]
                                              ↓ SIM
                                    [Marca Servidor Morto]
                                              ↓
                                    [onServerDeath Callback]
                                              ↓
                                    [Identifica Ops Afetadas]
                                              ↓
                                    [Marca Ops como Falhas]
                                              ↓
                                    [onOperationFail Callback]
```

### Fluxo 2: Timeout de ACK com Rollback
```
[Match] → [proposeOperation(op)]
              ↓
    [FailureDetector.WatchACKs()]
              ↓
    [Inicia Timer (5s)]
              ↓
         [Aguarda...]
              ↓
    [Timeout Dispara!]
              ↓
    [onTimeout Callback]
              ↓
    [RollbackOperation(op)]
              ↓
    [Notifica Jogador]
```

### Fluxo 3: Operação Bem-Sucedida
```
[Match] → [proposeOperation(op)]
              ↓
    [FailureDetector.WatchACKs()]
              ↓
    [Inicia Timer (5s)]
              ↓
    [Todos ACKs Recebidos!]
              ↓
    [FailureDetector.AllACKsReceived()]
              ↓
    [Cancela Timer]
              ↓
    [ExecuteOperation(op)]
```

## 🧪 Testes Realizáveis

### Teste 1: Heartbeat Funcionando
```bash
# Iniciar servidores
docker-compose up

# Verificar logs (a cada 2s):
# [HEARTBEAT] ✓ Servidor server-2 está vivo (ping: OK)
```

### Teste 2: Servidor Morto
```bash
# Derrubar servidor
docker stop pbl2-redes-server2-1

# Após ~6s, verificar logs:
# [HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 3/3)
# [HEARTBEAT] ✗ Servidor server-2 marcado como MORTO
# [MAIN] 🚨 ALERTA: Servidor server-2 falhou!
```

### Teste 3: Timeout de Operação
```bash
# Durante partida, derrubar servidor durante proposta
# Verificar logs após 5s:
# [TIMEOUT] ⏱️ Timeout de ACK para operação op-123
# [MATCH] Revertendo operação op-123 (tipo: PLAY)
```

### Teste 4: Status via API
```bash
curl http://localhost:8000/api/failure-detector/status | jq
```

## 📈 Estatísticas Disponíveis

### Via API
```json
{
  "stats": {
    "server_id": "server-1",
    "running": true,
    "alive_servers": 2,
    "dead_servers": 1,
    "active_operations": 0,
    "failed_operations": 2,
    "ack_timeouts": 2,
    "check_timeouts": 0,
    "active_watchers": 0
  },
  "aliveServers": ["server-2"],
  "deadServers": ["server-3"],
  "serversStatus": [...],
  "activeOps": [],
  "failedOps": {
    "op-123": "timeout aguardando ACKs após 5s",
    "op-456": "servidor server-3 morreu"
  }
}
```

### Via Código
```go
// Status de servidor
alive := failureDetector.IsServerAlive("server-2")

// Servidores vivos
aliveServers := failureDetector.GetAliveServers()

// Servidores mortos
deadServers := failureDetector.GetDeadServers()

// Estatísticas
stats := failureDetector.GetStats()

// Operações ativas/falhas
activeOps := failureDetector.GetActiveOperations()
failedOps := failureDetector.GetFailedOperations()
```

## 🎉 Benefícios Implementados

### 1. Detecção Automática
- ✅ Servidores mortos detectados em ~6 segundos
- ✅ Sem necessidade de intervenção manual
- ✅ Callbacks notificam componentes interessados

### 2. Rollback Automático
- ✅ Timeout de ACK dispara rollback (5s)
- ✅ Timeout de Check dispara rollback (5s)
- ✅ Servidor morto dispara rollback de operações afetadas
- ✅ Jogadores são notificados automaticamente

### 3. Tolerância a Falhas
- ✅ Sistema continua operando com servidores mortos
- ✅ Operações com servidores vivos não são afetadas
- ✅ Partidas locais continuam normalmente

### 4. Monitoramento
- ✅ Estatísticas detalhadas disponíveis
- ✅ Endpoint de debug para visualização
- ✅ Logs informativos de todos os eventos

### 5. Configurabilidade
- ✅ Intervalos de heartbeat configuráveis
- ✅ Timeouts de operação configuráveis
- ✅ Callbacks customizáveis
- ✅ Fácil integração com código existente

## 📝 Logs Importantes

### Heartbeat Normal
```
[HEARTBEAT] ✓ Servidor server-2 está vivo (ping: OK)
[API] ❤️ Heartbeat recebido de server-1
```

### Servidor Morto
```
[HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 1/3)
[HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 2/3)
[HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 3/3)
[HEARTBEAT] ✗ Servidor server-2 marcado como MORTO (falhas: 3)
[MAIN] 🚨 ALERTA: Servidor server-2 falhou!
[FAILURE_DETECTOR] ☠️ Servidor server-2 morreu - verificando ops afetadas
```

### Timeout de Operação
```
[TIMEOUT] ⏱️ Monitorando ACK para operação op-123 (timeout: 5s)
[TIMEOUT] ⏱️ Timeout de ACK para operação op-123 após 5.003s
[TIMEOUT] 🔄 Iniciando rollback automático para operação op-123
[MATCH match-456] [2PC] ⏱️ Timeout automático para operação op-123
[MATCH match-456] Revertendo operação op-123 (tipo: PLAY)
[FAILURE_DETECTOR] ✗ Operação op-123 falhou: timeout aguardando ACKs
```

### Operação Bem-Sucedida
```
[MATCH match-456] [2PC] Propondo operação op-789 para [http://server-2:8000]
[FAILURE_DETECTOR] 📝 Rastreando operação op-789 com servidores: [server-2]
[TIMEOUT] ⏱️ Monitorando ACK para operação op-789 (timeout: 5s)
[FAILURE_DETECTOR] ✓ ACK recebido de server-2 para operação op-789
[FAILURE_DETECTOR] ✓ Todos os ACKs recebidos para operação op-789
[TIMEOUT] ✓ Timeout cancelado para operação op-789
[MATCH match-456] [2PC] ✓ Operação op-789 executada com sucesso via consenso
[FAILURE_DETECTOR] ✓ Operação op-789 concluída com sucesso
```

## 🔧 Configuração Padrão

```go
// HeartbeatConfig
CheckInterval: 2 * time.Second   // Ping a cada 2s
Timeout:       10 * time.Second  // Considera morto após 10s sem resposta
MaxFailures:   3                 // 3 falhas consecutivas = morto

// TimeoutManager
ACKTimeout:   5 * time.Second    // 5s para receber ACKs
CheckTimeout: 5 * time.Second    // 5s para verificação cross-server
```

## ✅ Conclusão

O **Sistema de Detecção de Falhas** foi implementado com sucesso e está totalmente integrado ao sistema de consenso Two-Phase Commit. Todas as funcionalidades solicitadas foram implementadas:

1. ✅ **Heartbeat System** - Completo
2. ✅ **Timeout Management** - Completo
3. ✅ **Failure Detection** - Completo
4. ✅ **Automatic Rollback** - Completo

O sistema é:
- **Robusto**: Detecta e trata falhas automaticamente
- **Configurável**: Timeouts e intervalos ajustáveis
- **Monitorável**: Estatísticas e status disponíveis
- **Transparente**: Integração seamless com código existente
- **Tolerante**: Sistema continua operando mesmo com servidores mortos

---

**Data**: 22 de outubro de 2025  
**Status**: ✅ **ITEM 5 - IMPLEMENTAÇÃO COMPLETA**

