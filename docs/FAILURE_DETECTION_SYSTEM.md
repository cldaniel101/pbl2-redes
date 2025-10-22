# Sistema de Detecção de Falhas

## 📋 Visão Geral

Este documento descreve a implementação completa do **Sistema de Detecção de Falhas** para o jogo de cartas multiplayer distribuído. O sistema foi projetado para detectar servidores inativos, gerenciar timeouts de operações de consenso e executar rollback automático em caso de falhas.

## ✅ Status da Implementação

**CONCLUÍDO** - Todas as funcionalidades do item 5 foram implementadas com sucesso.

## 🎯 Objetivos Alcançados

1. ✅ **Heartbeat System** - Sistema de ping periódico para servidores
2. ✅ **Timeout Management** - Gerenciamento de timeouts para ACKs e verificações
3. ✅ **Failure Detection** - Detecção de servidores inativos
4. ✅ **Automatic Rollback** - Rollback automático em caso de falha

## 🏗️ Arquitetura

O sistema é composto por três componentes principais:

```
┌─────────────────────────────────────────────────────────────────┐
│                   Sistema de Detecção de Falhas                  │
└─────────────────────────────────────────────────────────────────┘
                                 │
                ┌────────────────┼────────────────┐
                │                │                │
                ▼                ▼                ▼
        ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
        │  Heartbeat   │  │   Timeout    │  │   Failure    │
        │   System     │  │   Manager    │  │   Detector   │
        └──────────────┘  └──────────────┘  └──────────────┘
```

## 📝 Componentes

### 1. HeartbeatSystem (`server/consensus/heartbeat.go`)

Sistema de ping periódico para monitorar a disponibilidade de servidores.

#### Funcionalidades

- **Pings Periódicos**: Envia requisições HTTP para `/api/health` a cada 2 segundos (configurável)
- **Contagem de Falhas**: Registra falhas consecutivas de cada servidor
- **Marcação de Status**: Marca servidores como vivos ou mortos baseado em falhas consecutivas
- **Callbacks**: Notifica quando servidores morrem ou revivem

#### Configuração

```go
type HeartbeatConfig struct {
    CheckInterval time.Duration // Intervalo entre verificações (padrão: 2s)
    Timeout       time.Duration // Timeout para considerar servidor morto (padrão: 10s)
    MaxFailures   int           // Número máximo de falhas consecutivas (padrão: 3)
}
```

#### Exemplo de Uso

```go
// Criar sistema de heartbeat
config := consensus.DefaultHeartbeatConfig()
heartbeat := consensus.NewHeartbeatSystem("server-1", config)

// Registrar servidores para monitoramento
heartbeat.RegisterServer("server-2", "http://server-2:8000")
heartbeat.RegisterServer("server-3", "http://server-3:8000")

// Configurar callbacks
heartbeat.SetOnServerDeath(func(serverID string) {
    log.Printf("Servidor %s morreu!", serverID)
})

heartbeat.SetOnServerRevive(func(serverID string) {
    log.Printf("Servidor %s reviveu!", serverID)
})

// Iniciar o sistema
heartbeat.Start()
```

#### Estrutura de Dados

```go
type ServerStatus struct {
    ServerID      string    // ID do servidor
    Address       string    // Endereço HTTP do servidor
    IsAlive       bool      // Flag de disponibilidade
    LastHeartbeat time.Time // Timestamp do último heartbeat recebido
    LastCheck     time.Time // Timestamp da última verificação enviada
    FailureCount  int       // Contador de falhas consecutivas
}
```

### 2. TimeoutManager (`server/consensus/timeout.go`)

Gerenciador de timeouts para operações de consenso com rollback automático.

#### Funcionalidades

- **Monitoramento de ACKs**: Detecta timeout aguardando confirmações de servidores
- **Monitoramento de Checks**: Detecta timeout aguardando verificações cross-server
- **Rollback Automático**: Executa callback de rollback quando timeout ocorre
- **Estatísticas**: Rastreia número de timeouts de ACK e Check

#### Tipos de Timeout

```go
const (
    TimeoutACK   TimeoutType = "ACK"   // Timeout aguardando ACKs
    TimeoutCheck TimeoutType = "CHECK" // Timeout aguardando verificação
)
```

#### Exemplo de Uso

```go
// Criar gerenciador de timeouts
timeoutManager := consensus.NewTimeoutManager(
    5*time.Second, // Timeout para ACKs
    5*time.Second, // Timeout para Checks
)

// Monitorar ACKs com rollback automático
timeoutManager.WatchACKWithRollback(
    operationID,
    5*time.Second,
    func(opID, reason string) {
        log.Printf("Timeout! Executando rollback: %s", reason)
        match.RollbackOperation(operation)
    },
)

// Cancelar timeout quando ACKs recebidos
timeoutManager.Cancel(operationID)
```

#### Estrutura de Dados

```go
type TimeoutWatcher struct {
    OperationID   string      // ID da operação sendo monitorada
    Type          TimeoutType // Tipo de timeout
    Timeout       time.Duration
    StartTime     time.Time
    timer         *time.Timer
    onTimeout     func(*TimeoutEvent) // Callback quando timeout ocorre
    cancelled     bool
}
```

### 3. FailureDetector (`server/consensus/failure_detector.go`)

Detector integrado que combina heartbeat e timeout para detecção abrangente de falhas.

#### Funcionalidades

- **Integração Heartbeat + Timeout**: Combina ambos os sistemas
- **Rastreamento de Operações**: Monitora operações e os servidores envolvidos
- **Detecção de Impacto**: Identifica operações afetadas quando servidor morre
- **Callbacks Configuráveis**: Notifica sobre falhas de servidor e operação

#### Exemplo de Uso

```go
// Criar detector de falhas
config := consensus.DefaultFailureDetectorConfig()
detector := consensus.NewFailureDetector("server-1", config)

// Registrar servidores
detector.RegisterServer("server-2", "http://server-2:8000")

// Configurar callbacks
detector.SetOnServerFail(func(serverID string) {
    log.Printf("Servidor %s falhou!", serverID)
})

detector.SetOnOperationFail(func(opID, serverID, reason string) {
    log.Printf("Operação %s falhou: %s", opID, reason)
})

// Iniciar detector
detector.Start()

// Monitorar operação com timeout automático
detector.WatchACKs(
    operationID,
    []string{"server-2", "server-3"},
    5*time.Second,
    func(opID, reason string) {
        // Rollback automático
        match.RollbackOperation(operation)
    },
)
```

## 🔌 Integração com o Sistema de Consenso

### 1. Endpoint de Health Check

Adicionado endpoint `/api/health` para responder aos pings de heartbeat:

```go
// handleHealthCheck responde aos pings de heartbeat de outros servidores
func (s *APIServer) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
    // Decodifica o ping
    var ping struct {
        ServerID string `json:"serverId"`
    }
    json.NewDecoder(r.Body).Decode(&ping)
    
    // Registra heartbeat no detector de falhas
    if s.failureDetector != nil {
        s.failureDetector.RecordHeartbeat(ping.ServerID)
    }
    
    // Responde com status OK
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(map[string]string{
        "status": "ok",
    })
}
```

### 2. Endpoint de Debug

Adicionado endpoint `/api/failure-detector/status` para visualizar status do sistema:

```http
GET /api/failure-detector/status

Response:
{
  "stats": {
    "server_id": "server-1",
    "running": true,
    "alive_servers": 2,
    "dead_servers": 0,
    "active_operations": 1,
    "failed_operations": 0,
    "ack_timeouts": 0,
    "check_timeouts": 0,
    "active_watchers": 1
  },
  "aliveServers": ["server-2", "server-3"],
  "deadServers": [],
  "serversStatus": [...],
  "activeOps": ["op-123"],
  "failedOps": {}
}
```

### 3. Integração com Match

O `FailureDetector` foi integrado ao `Match` para monitoramento automático:

```go
type Match struct {
    // ... campos existentes ...
    
    // Sistema de detecção de falhas (opcional)
    FailureDetector *consensus.FailureDetector
}

// Uso no Two-Phase Commit
func (m *Match) proposeOperation(op *Operation, otherServers []string) {
    // Se o FailureDetector está disponível, inicia monitoramento
    if m.FailureDetector != nil {
        // Rastreia operação com os servidores envolvidos
        m.FailureDetector.TrackOperation(op.ID, otherServers)
        
        // Configura timeout com rollback automático
        timeout := time.Duration(ConsensusACKTimeout) * time.Millisecond
        m.FailureDetector.WatchACKs(op.ID, otherServers, timeout, 
            func(opID, reason string) {
                // Rollback automático em caso de timeout
                log.Printf("Timeout! Executando rollback: %s", reason)
                m.RollbackOperation(op)
                errChan <- fmt.Errorf("%s", reason)
            })
    }
    
    // ... resto da lógica de proposta ...
}
```

### 4. Inicialização no Main

O sistema é inicializado automaticamente no `main.go`:

```go
// Gera ID único para este servidor
serverID := fmt.Sprintf("server-%d", myIndex)

// Cria o sistema de detecção de falhas
fdConfig := consensus.DefaultFailureDetectorConfig()
failureDetector := consensus.NewFailureDetector(serverID, fdConfig)

// Registra todos os outros servidores
for i, serverAddr := range allServers {
    if serverAddr != thisServerAddress {
        otherServerID := fmt.Sprintf("server-%d", i)
        failureDetector.RegisterServer(otherServerID, serverAddr)
    }
}

// Configura callbacks
failureDetector.SetOnServerFail(func(deadServerID string) {
    log.Printf("🚨 ALERTA: Servidor %s falhou!", deadServerID)
})

failureDetector.SetOnOperationFail(func(operationID, deadServerID, reason string) {
    log.Printf("🚨 ALERTA: Operação %s falhou: %s", operationID, reason)
})

// Inicia o sistema
failureDetector.Start()

// Configura no API Server e StateManager
apiServer.SetFailureDetector(failureDetector)
stateManager.SetFailureDetector(failureDetector)
```

## 📊 Fluxo de Detecção de Falhas

### Fluxo Normal (Servidor Vivo)

```
Server-1 (Monitor)           Server-2 (Monitorado)
      │                             │
      │ 1. Envia Ping               │
      ├─── POST /api/health ───────>│
      │    {serverId: "server-1"}   │
      │                             │
      │                             │ 2. Processa Ping
      │                             │ RecordHeartbeat()
      │                             │
      │ 3. Recebe Resposta OK       │
      │<──── 200 OK ─────────────────┤
      │                             │
      │ 4. Atualiza Status          │
      │ IsAlive = true              │
      │ FailureCount = 0            │
      │                             │
      ✓ Servidor-2 está vivo        │
```

### Fluxo de Falha (Servidor Morto)

```
Server-1 (Monitor)           Server-2 (Morto)
      │                             ✗
      │ 1. Envia Ping               │
      ├─── POST /api/health ───X    │
      │                             │
      │ 2. Timeout                  │
      │ FailureCount++              │
      │                             │
      │ ... (repete)                │
      │                             │
      │ 3. MaxFailures atingido     │
      │ IsAlive = false             │
      │ onServerDeath("server-2")   │
      │                             │
      │ 4. Verifica ops afetadas    │
      │ MarkOperationFailed()       │
      │ Executa rollback            │
      │                             │
      ☠ Servidor-2 morreu           │
      🔄 Operações revertidas        │
```

### Fluxo de Timeout de Operação

```
Server-1 (Proposer)          Server-2
      │                             │
      │ 1. Propõe Operação          │
      ├─── ProposeOperation ───────>│
      │                             │
      │ 2. Inicia Timeout Watch     │
      │ WatchACKs(op, 5s)           │
      │                             │
      │ 3. Aguarda ACK...           │
      │ (5 segundos)                │
      │                             ✗ Não responde
      │                             │
      │ 4. Timeout Dispara!         │
      │ onTimeout() chamado         │
      │                             │
      │ 5. Rollback Automático      │
      │ RollbackOperation(op)       │
      │ Notifica jogador            │
      │                             │
      ⏱️ Timeout detectado           │
      🔄 Operação revertida          │
```

## 🛡️ Mecanismos de Tolerância a Falhas

### 1. Detecção de Servidor Morto

- **Heartbeats Periódicos**: Pinga servidores a cada 2 segundos
- **Contagem de Falhas**: 3 falhas consecutivas marca servidor como morto
- **Timeout de Ping**: 3 segundos por ping
- **Ação**: Marca operações afetadas como falhas e executa rollback

### 2. Timeout de ACKs

- **Duração**: 5 segundos (configurável via `ConsensusACKTimeout`)
- **Ação**: Rollback automático da operação
- **Notificação**: Jogador é notificado da rejeição

### 3. Timeout de Verificações

- **Duração**: 5 segundos (configurável via `ConsensusCheckTimeout`)
- **Ação**: Rollback automático da operação
- **Propagação**: Solicita rollback em todos os servidores

### 4. Operações Afetadas por Servidor Morto

- **Rastreamento**: Mantém mapa de operações → servidores envolvidos
- **Impacto**: Quando servidor morre, marca todas as operações afetadas
- **Ação**: Executa rollback em operações pendentes

## 📈 Estatísticas e Monitoramento

### Estatísticas do HeartbeatSystem

```go
// Consultar status de servidores
aliveServers := failureDetector.GetAliveServers()
deadServers := failureDetector.GetDeadServers()
allStatus := failureDetector.GetAllServersStatus()

for _, status := range allStatus {
    fmt.Printf("Server: %s, Alive: %v, LastHeartbeat: %v, Failures: %d\n",
        status.ServerID, status.IsAlive, 
        time.Since(status.LastHeartbeat), status.FailureCount)
}
```

### Estatísticas do TimeoutManager

```go
// Consultar estatísticas de timeout
ackTimeouts, checkTimeouts := failureDetector.GetStats()
fmt.Printf("ACK Timeouts: %d, Check Timeouts: %d\n", 
    ackTimeouts, checkTimeouts)

// Watchers ativos
activeWatchers := timeoutManager.GetActiveWatchers()
fmt.Printf("Watchers ativos: %d\n", activeWatchers)
```

### Estatísticas do FailureDetector

```go
// Estatísticas completas
stats := failureDetector.GetStats()
fmt.Printf("Stats: %+v\n", stats)

// Operações ativas e falhas
activeOps := failureDetector.GetActiveOperations()
failedOps := failureDetector.GetFailedOperations()
fmt.Printf("Active: %v, Failed: %v\n", activeOps, failedOps)
```

## 🧪 Como Testar

### Teste 1: Heartbeat Normal

```bash
# Iniciar 3 servidores
docker-compose up

# Verificar logs - deve ver heartbeats a cada 2 segundos:
# [HEARTBEAT] ✓ Servidor server-2 está vivo (ping: OK)
```

### Teste 2: Servidor Morto

```bash
# Iniciar servidores
docker-compose up

# Derrubar um servidor
docker stop pbl2-redes-server2-1

# Observar logs - após ~6 segundos (3 falhas * 2s):
# [HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 1/3)
# [HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 2/3)
# [HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 3/3)
# [HEARTBEAT] ✗ Servidor server-2 marcado como MORTO
# [MAIN] 🚨 ALERTA: Servidor server-2 falhou!
```

### Teste 3: Timeout de ACKs

```bash
# Simular timeout desconectando servidor durante proposta
# 1. Iniciar partida distribuída
# 2. Jogador 1 joga carta
# 3. Derrubar servidor do jogador 2 imediatamente

# Observar logs:
# [MATCH match-123] [2PC] Propondo operação op-123 para [http://server-2:8000]
# [TIMEOUT] ⏱️ Monitorando ACK para operação op-123 (timeout: 5s)
# [TIMEOUT] ⏱️ Timeout de ACK para operação op-123 após 5s
# [MATCH match-123] [2PC] ⏱️ Timeout automático para operação op-123
# [MATCH match-123] Revertendo operação op-123 (tipo: PLAY)
```

### Teste 4: Status do Detector

```bash
# Consultar status via API
curl http://localhost:8000/api/failure-detector/status | jq

# Resposta mostra:
# - Servidores vivos e mortos
# - Operações ativas e falhas
# - Estatísticas de timeouts
```

## 🔧 Configuração

### Configurações Padrão

```go
// HeartbeatConfig padrão
CheckInterval: 2 * time.Second   // Intervalo entre pings
Timeout:       10 * time.Second  // Timeout para considerar morto
MaxFailures:   3                 // Falhas consecutivas antes de marcar como morto

// TimeoutManager padrão
ACKTimeout:   5 * time.Second    // Timeout para ACKs
CheckTimeout: 5 * time.Second    // Timeout para verificações
```

### Configuração Customizada

```go
// Criar configuração customizada
config := consensus.FailureDetectorConfig{
    HeartbeatConfig: consensus.HeartbeatConfig{
        CheckInterval: 1 * time.Second,  // Mais agressivo
        Timeout:       5 * time.Second,
        MaxFailures:   2,
    },
    ACKTimeout:   3 * time.Second,       // Timeout menor
    CheckTimeout: 3 * time.Second,
}

detector := consensus.NewFailureDetector("server-1", config)
```

## 📚 Referências

- [Two-Phase Commit Protocol](https://en.wikipedia.org/wiki/Two-phase_commit_protocol)
- [Failure Detector](https://en.wikipedia.org/wiki/Failure_detector)
- [Heartbeat (computing)](https://en.wikipedia.org/wiki/Heartbeat_(computing))
- [CONSENSUS_TWO_PHASE_COMMIT.md](./CONSENSUS_TWO_PHASE_COMMIT.md)

## 🎉 Conclusão

O sistema de detecção de falhas foi **implementado com sucesso** e está totalmente integrado ao sistema de consenso. Principais benefícios:

- ✅ **Detecção Automática**: Servidores mortos são detectados automaticamente
- ✅ **Timeouts Gerenciados**: ACKs e verificações têm timeout com rollback automático
- ✅ **Rollback Coordenado**: Falhas disparam rollback em todos os servidores
- ✅ **Monitoramento**: Estatísticas e status disponíveis via API
- ✅ **Tolerante a Falhas**: Sistema continua operando mesmo com servidores mortos
- ✅ **Transparente**: Integração seamless com código existente

O sistema resolve completamente os problemas de timeout e detecção de falhas no consenso distribuído!

---

**Data**: 22 de outubro de 2025  
**Status**: ✅ IMPLEMENTAÇÃO COMPLETA

