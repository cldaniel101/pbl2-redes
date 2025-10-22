# Quick Start: Sistema de Detecção de Falhas

## 🚀 Início Rápido

### 1. Iniciar o Sistema

O sistema de detecção de falhas é **iniciado automaticamente** quando o servidor inicia:

```bash
# Compilar servidor
cd server
go build -o ../bin/server main.go

# Iniciar com Docker Compose (3 servidores)
docker-compose up
```

### 2. Verificar Logs

O sistema gera logs informativos sobre o status:

```
[MAIN] ID deste servidor: server-0
[MAIN] Registrado servidor server-1 (http://server-1:8000) para monitoramento
[MAIN] Registrado servidor server-2 (http://server-2:8000) para monitoramento
[MAIN] ✓ Sistema de detecção de falhas iniciado
[HEARTBEAT] ✓ Servidor server-1 está vivo (ping: OK)
[HEARTBEAT] ✓ Servidor server-2 está vivo (ping: OK)
```

### 3. Consultar Status

Use o endpoint de debug para ver o status em tempo real:

```bash
# Consultar status do detector de falhas
curl http://localhost:8000/api/failure-detector/status | jq

# Exemplo de resposta:
{
  "stats": {
    "server_id": "server-0",
    "running": true,
    "alive_servers": 2,
    "dead_servers": 0,
    "active_operations": 0,
    "failed_operations": 0,
    "ack_timeouts": 0,
    "check_timeouts": 0,
    "active_watchers": 0
  },
  "aliveServers": ["server-1", "server-2"],
  "deadServers": [],
  "serversStatus": [
    {
      "ServerID": "server-1",
      "Address": "http://server-1:8000",
      "IsAlive": true,
      "LastHeartbeat": "2025-10-22T10:30:45Z",
      "LastCheck": "2025-10-22T10:30:45Z",
      "FailureCount": 0
    }
  ],
  "activeOps": [],
  "failedOps": {}
}
```

## 🧪 Testar Detecção de Falhas

### Teste 1: Simular Morte de Servidor

```bash
# Terminal 1: Iniciar servidores
docker-compose up

# Terminal 2: Derrubar um servidor
docker stop pbl2-redes-server2-1

# Observar logs no Terminal 1 (após ~6 segundos):
# [HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 1/3)
# [HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 2/3)
# [HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 3/3)
# [HEARTBEAT] ✗ Servidor server-2 marcado como MORTO
# [MAIN] 🚨 ALERTA: Servidor server-2 falhou!
```

### Teste 2: Verificar Timeout de Operação

```bash
# 1. Conectar dois clientes em servidores diferentes
# Cliente 1 → Server-1
go run client/main.go localhost:7000

# Cliente 2 → Server-2
go run client/main.go localhost:7001

# 2. Iniciar partida distribuída
> match

# 3. Durante jogada, derrubar Server-2
docker stop pbl2-redes-server2-1

# 4. Cliente 1 tenta jogar carta
> 1

# 5. Observar logs - timeout após 5s:
# [TIMEOUT] ⏱️ Monitorando ACK para operação op-123 (timeout: 5s)
# [TIMEOUT] ⏱️ Timeout de ACK para operação op-123 após 5s
# [TIMEOUT] 🔄 Iniciando rollback automático para operação op-123
# [MATCH] Revertendo operação op-123 (tipo: PLAY)
```

### Teste 3: Reviver Servidor

```bash
# 1. Derrubar servidor
docker stop pbl2-redes-server2-1

# 2. Aguardar ser marcado como morto (~6s)
# [HEARTBEAT] ✗ Servidor server-2 marcado como MORTO

# 3. Reviver servidor
docker start pbl2-redes-server2-1

# 4. Observar logs - servidor revive em ~2s:
# [HEARTBEAT] ✓ Servidor server-2 reviveu (resposta recebida)
```

## 📊 Monitorar em Tempo Real

### Opção 1: Via API (Recomendado)

```bash
# Loop para monitorar continuamente
while true; do 
  clear
  echo "=== Status do Detector de Falhas ==="
  curl -s http://localhost:8000/api/failure-detector/status | jq '.stats'
  sleep 2
done
```

### Opção 2: Via Logs

```bash
# Filtrar logs do heartbeat
docker-compose logs -f server1 | grep HEARTBEAT

# Filtrar logs de timeout
docker-compose logs -f server1 | grep TIMEOUT

# Filtrar logs de falhas
docker-compose logs -f server1 | grep "ALERTA"
```

## 🔧 Configuração Customizada

Se você quiser ajustar os parâmetros do sistema, edite `server/main.go`:

```go
// Configuração customizada
fdConfig := consensus.FailureDetectorConfig{
    HeartbeatConfig: consensus.HeartbeatConfig{
        CheckInterval: 1 * time.Second,  // Pings mais frequentes
        Timeout:       5 * time.Second,  // Timeout menor
        MaxFailures:   2,                // Menos tolerância
    },
    ACKTimeout:   3 * time.Second,       // Timeout de ACK menor
    CheckTimeout: 3 * time.Second,       // Timeout de Check menor
}
failureDetector := consensus.NewFailureDetector(serverID, fdConfig)
```

**Valores Padrão:**
- Intervalo de Heartbeat: 2 segundos
- Timeout de Ping: 3 segundos
- Falhas Máximas: 3
- Timeout de ACK: 5 segundos
- Timeout de Check: 5 segundos

## 🎯 Casos de Uso Comuns

### 1. Detectar Servidor Indisponível

**Cenário**: Um servidor fica indisponível (rede, crash, etc.)

**Comportamento**:
1. Heartbeat falha 3 vezes consecutivas (~6 segundos)
2. Servidor marcado como morto
3. Callback `onServerDeath` é chamado
4. Operações envolvendo esse servidor são marcadas como falhas
5. Rollback automático é executado

**Logs Esperados**:
```
[HEARTBEAT] ✗ Servidor server-2 não respondeu (falha 3/3)
[HEARTBEAT] ✗ Servidor server-2 marcado como MORTO
[MAIN] 🚨 ALERTA: Servidor server-2 falhou!
[FAILURE_DETECTOR] ☠️ Servidor server-2 morreu - verificando ops afetadas
```

### 2. Timeout Aguardando ACKs

**Cenário**: Servidor não responde à proposta de operação no tempo esperado

**Comportamento**:
1. Operação é proposta para outros servidores
2. Timeout de 5s é iniciado
3. ACKs não chegam no prazo
4. Timeout dispara rollback automático
5. Jogador é notificado da rejeição

**Logs Esperados**:
```
[TIMEOUT] ⏱️ Monitorando ACK para operação op-123 (timeout: 5s)
[TIMEOUT] ⏱️ Timeout de ACK para operação op-123 após 5.003s
[MATCH] Revertendo operação op-123 (tipo: PLAY)
```

### 3. Operação Bem-Sucedida

**Cenário**: Todos os servidores respondem no tempo esperado

**Comportamento**:
1. Operação é proposta
2. ACKs chegam dentro do prazo
3. Timeout é cancelado
4. Operação é executada com sucesso

**Logs Esperados**:
```
[MATCH] [2PC] Propondo operação op-123 para [http://server-2:8000]
[FAILURE_DETECTOR] ✓ ACK recebido de server-2 para operação op-123
[FAILURE_DETECTOR] ✓ Todos os ACKs recebidos para operação op-123
[TIMEOUT] ✓ Timeout cancelado para operação op-123
[MATCH] [2PC] ✓ Operação op-123 executada com sucesso via consenso
```

## 📈 Métricas Importantes

### Via Status API

```bash
curl -s http://localhost:8000/api/failure-detector/status | jq '.stats'
```

**Métricas Principais**:
- `alive_servers`: Número de servidores vivos
- `dead_servers`: Número de servidores mortos
- `active_operations`: Operações em andamento
- `failed_operations`: Operações que falharam
- `ack_timeouts`: Total de timeouts de ACK
- `check_timeouts`: Total de timeouts de verificação
- `active_watchers`: Watchers de timeout ativos

### Alertas a Monitorar

🔴 **Crítico**:
- `dead_servers > 0` → Servidor(es) morto(s)
- `failed_operations > 10` → Muitas operações falhando

🟡 **Aviso**:
- `ack_timeouts > 5` → Latência alta ou problemas de rede
- `check_timeouts > 5` → Problemas de validação cross-server

🟢 **Normal**:
- `alive_servers == total_servers - 1` (excluindo próprio servidor)
- `failed_operations == 0`
- `active_operations` variando conforme partidas ativas

## 🐛 Troubleshooting

### Problema: "Servidor não morre mesmo offline"

**Causa**: MaxFailures muito alto ou CheckInterval muito longo

**Solução**:
```go
// Configuração mais agressiva
HeartbeatConfig{
    CheckInterval: 1 * time.Second,
    MaxFailures:   2,
}
```

### Problema: "Muitos timeouts de ACK"

**Causa**: Rede lenta ou timeout muito curto

**Solução**:
```go
// Aumentar timeout
fdConfig := FailureDetectorConfig{
    ACKTimeout: 10 * time.Second,  // Aumentar para 10s
}
```

### Problema: "Servidor marcado como morto incorretamente"

**Causa**: Picos de latência ou sobrecarga temporária

**Solução**:
```go
// Aumentar tolerância
HeartbeatConfig{
    MaxFailures: 5,  // Permitir mais falhas
    Timeout:     15 * time.Second,  // Timeout mais generoso
}
```

## 📚 Referências

- [FAILURE_DETECTION_SYSTEM.md](./FAILURE_DETECTION_SYSTEM.md) - Documentação completa
- [ITEM5_DETECCAO_FALHAS_RESUMO.md](./ITEM5_DETECCAO_FALHAS_RESUMO.md) - Resumo da implementação
- [CONSENSUS_TWO_PHASE_COMMIT.md](./CONSENSUS_TWO_PHASE_COMMIT.md) - Sistema de consenso

## 🎉 Conclusão

O sistema de detecção de falhas está **pronto para uso** e funciona automaticamente. Principais benefícios:

✅ **Detecção Automática**: Servidores mortos detectados em ~6s  
✅ **Rollback Automático**: Timeouts disparam rollback  
✅ **Tolerante a Falhas**: Sistema continua operando  
✅ **Monitorável**: Status disponível via API  
✅ **Configurável**: Parâmetros ajustáveis  

---

**Status**: ✅ **SISTEMA ATIVO E FUNCIONAL**

