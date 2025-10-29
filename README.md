# Attribute War - Jogo de Cartas Multiplayer Distribuído

## 📋 Visão Geral

Este é um sistema de jogo de cartas multiplayer distribuído implementado em Go, onde múltiplos servidores colaboram para gerenciar jogadores e partidas. O sistema utiliza uma arquitetura distribuída com comunicação inter-servidores via API REST e comunicação servidor-cliente via TCP com protocolo JSON.

## 🏗️ Arquitetura

### Componentes Principais
- **3 Servidores de Jogo** (server-1, server-2, server-3)
- **Cliente de Teste** (para demonstração)
- **Sistema de Token Distribuído** (para gerenciamento de pacotes)
- **API Inter-Servidores** (comunicação REST)
- **Sistema de Matchmaking Distribuído**

### Portas e Serviços
```
Servidor 1: TCP:9000, HTTP:8000
Servidor 2: TCP:9001, HTTP:8001  
Servidor 3: TCP:9002, HTTP:8002
```

## 🚀 Instruções de Execução

### Pré-requisitos
- Docker e Docker Compose
- Go 1.19+ (para desenvolvimento local)

### 1. Subir o Cluster Completo

```bash
# Clone o repositório
git clone <repository-url>
cd pbl2-redes

# Subir todos os serviços
docker-compose up --build
```

### 2. Verificar Status dos Serviços

```bash
# Verificar containers em execução
docker-compose ps

# Verificar logs de um servidor específico
docker-compose logs server-1
docker-compose logs server-2
docker-compose logs server-3

# Verificar logs do cliente
docker-compose logs client
```

### 3. Conectar Clientes Adicionais

Para testar com múltiplos clientes:

```bash
# Conectar cliente ao servidor 1
docker run --rm -it --network pbl2-redes_game-net \
  -e SERVER_ADDR=server-1:9000 \
  pingpong-client:latest

# Conectar cliente ao servidor 2
docker run --rm -it --network pbl2-redes_game-net \
  -e SERVER_ADDR=server-2:9101 \
  pingpong-client:latest

# Conectar cliente ao servidor 3
docker run --rm -it --network pbl2-redes_game-net \
  -e SERVER_ADDR=server-3:9002 \
  pingpong-client:latest
```

## 🧪 Executar Testes

### Testes de Concorrência de Pacotes
```bash
# Executar testes de pacotes
cd tests
go test -v packs_test.go

# Executar benchmark de concorrência
go test -bench=BenchmarkPackStoreConcurrency -v
```

### Testes de Estresse do Cluster
```bash
# Executar teste de estresse do cluster
cd tests
go test -v stress_cluster_test.go

# Executar teste de estresse de pacotes
go test -v stress_packs.go
```

## 📡 Variáveis de Ambiente

### Servidores
```bash
LISTEN_ADDR=:9000          # Porta TCP para clientes
API_ADDR=:8000             # Porta HTTP para API inter-servidores
ALL_SERVERS=http://server-1:8000,http://server-2:8000,http://server-3:8000
HOSTNAME=server-1          # Nome do host (server-1, server-2, server-3)
PACK_REQUEST_TIMEOUT_SEC=10  # Timeout para pedidos de pacotes em segundos (padrão: 10)
```

### Cliente
```bash
SERVER_ADDR=server-1:9000  # Endereço do servidor para conectar
PING_INTERVAL_MS=2000      # Intervalo de ping em milissegundos
```

## 🔄 Exemplos de Requisições S2S (Server-to-Server)

### 1. Buscar Oponente
```bash
curl -X GET http://localhost:8001/api/find-opponent
```

**Resposta:**
```json
{
  "playerId": "player_123"
}
```

### 2. Solicitar Partida
```bash
curl -X POST http://localhost:8002/api/request-match \
  -H "Content-Type: application/json" \
  -d '{
    "matchId": "match_456",
    "hostPlayerId": "player_123",
    "guestPlayerId": "player_789"
  }'
```

### 3. Receber Token
```bash
curl -X POST http://localhost:8000/api/receive-token \
  -H "Content-Type: application/json" \
  -d '{
    "packStock": 1000
  }'
```

### 4. Ação de Partida (Jogar Carta)
```bash
curl -X POST http://localhost:8001/matches/match_456/action \
  -H "Content-Type: application/json" \
  -d '{
    "playerId": "player_123",
    "cardId": "c_001"
  }'
```

### 5. Encaminhar Mensagem
```bash
curl -X POST http://localhost:8002/api/forward/message \
  -H "Content-Type: application/json" \
  -H "X-Player-ID: player_789" \
  -d '{
    "t": "STATE",
    "you": {"hp": 20, "hand": ["c_001", "c_002"]},
    "opponent": {"hp": 20, "handSize": 5},
    "round": 1
  }'
```

## 💬 Exemplos de Mensagens S2C (Server-to-Client)

### 1. Partida Encontrada
```json
{
  "t": "MATCH_FOUND",
  "matchId": "match_456",
  "opponentId": "player_789"
}
```

### 2. Estado da Partida
```json
{
  "t": "STATE",
  "you": {
    "hp": 20,
    "hand": ["c_001", "c_002", "c_003", "c_004", "c_005"]
  },
  "opponent": {
    "hp": 20,
    "handSize": 5
  },
  "round": 1,
  "deadlineMs": 12000
}
```

### 3. Resultado da Rodada
```json
{
  "t": "ROUND_RESULT",
  "you": {
    "cardId": "c_001",
    "elementBonus": 3,
    "dmgDealt": 8,
    "dmgTaken": 2,
    "hp": 18
  },
  "opponent": {
    "cardId": "c_007",
    "elementBonus": 0,
    "hp": 12
  },
  "logs": [
    "Você jogou Fire Dragon (ATK 8+3). Oponente jogou Ice Mage (DEF 6)."
  ]
}
```

### 4. Pacote Aberto
```json
{
  "t": "PACK_OPENED",
  "cards": ["c_021", "c_088", "c_090"],
  "stock": 137
}
```

### 5. Fim da Partida
```json
{
  "t": "MATCH_END",
  "result": "WIN"
}
```

### 6. Erro
```json
{
  "t": "ERROR",
  "code": "OUT_OF_STOCK",
  "msg": "Não há mais pacotes disponíveis"
}
```

### 7. Pong (Resposta ao Ping)
```json
{
  "t": "PONG",
  "ts": 1694272000123,
  "rttMs": 42
}
```

## 🎮 Comandos do Cliente

### Comandos Básicos
- `/play <número>` - Jogar carta pelo índice (1-5)
- `/hand` - Mostrar sua mão atual
- `/pack` - Abrir pacote de cartas
- `/ping` - Liga/desliga exibição de RTT
- `/autoplay` - Ativar autoplay (cartas automáticas após 12s)
- `/noautoplay` - Desativar autoplay
- `/rematch` - Solicitar nova partida com último oponente
- `/help` - Mostrar ajuda
- `/quit` - Sair do jogo

### Atalhos
- `1-5` - Jogar carta diretamente pelo número
- `<mensagem>` - Enviar chat

## 🔧 Desenvolvimento Local

### Executar Servidor Individual
```bash
cd server
go run main.go
```

### Executar Cliente Individual
```bash
cd client
go run main.go
```

### Executar Testes
```bash
cd tests
go test -v ./...
```

## 📊 Monitoramento

### Logs dos Servidores
```bash
# Logs em tempo real
docker-compose logs -f server-1
docker-compose logs -f server-2
docker-compose logs -f server-3

# Logs de todos os serviços
docker-compose logs -f
```

### Status dos Containers
```bash
# Status detalhado
docker-compose ps

# Uso de recursos
docker stats
```

## 🐛 Troubleshooting

### Problemas Comuns

1. **Servidor não inicia**
   - Verificar se as portas estão disponíveis
   - Verificar logs: `docker-compose logs server-1`

2. **Cliente não conecta**
   - Verificar se o servidor está rodando
   - Verificar endereço: `SERVER_ADDR=server-1:9000`

3. **Partidas não são criadas**
   - Verificar se múltiplos servidores estão online
   - Verificar logs de matchmaking

4. **Pacotes não abrem**
   - Verificar se o token está circulando
   - Verificar logs de API inter-servidores

### Limpeza
```bash
# Parar todos os serviços
docker-compose down

# Remover volumes e imagens
docker-compose down -v --rmi all

# Limpeza completa
docker system prune -a
```

## 🛡️ Tolerância a Falhas e Melhorias

### Regeneração Inteligente de Token
O sistema implementa um watchdog no servidor líder que monitora a circulação do token:
- **Timeout dinâmico:** 4 segundos × número de servidores no anel
- **Regeneração com estado:** Usa o último estoque conhecido ao invés de resetar para valor inicial
- **Log de auditoria:** Rastreia total de pacotes abertos desde o início
- **Avisos de inconsistência:** Notifica quando regeneração pode causar estado inconsistente

### Timeout Configurável
O timeout para pedidos de pacotes pode ser ajustado via variável de ambiente:
```bash
PACK_REQUEST_TIMEOUT_SEC=10  # Padrão: 10 segundos
```

### Tratamento de Falhas em Partidas Distribuídas
- Falhas S2S (servidor-servidor) são detectadas com timeout de 5s
- Partidas afetadas são encerradas com mensagem clara: `VICTORY_BY_DISCONNECT`
- Estado do oponente remoto é limpo automaticamente
- Cliente recebe notificação compreensível do motivo do encerramento

### Logs Estruturados
- Todos os logs incluem tags de contexto: `[MATCHMAKING]`, `[HANDLER]`, `[MATCH]`, etc.
- Logs de auditoria com emoji 📦 para facilitar rastreamento de pacotes
- Logs de aviso ⚠️ para situações críticas de regeneração

## 📚 Documentação Adicional

- [Regras do Jogo](GAME_RULES.md)
- [Descrição do Problema](PROBLEM_DESCRIPTION.md)
- [Arquitetura Distribuída](docs/F0%20-%20arquitetura_distribuida.md)
- [API e Orquestração](docs/F1%20-%20API%20e%20Orquestração%20de%20Jogadas%20S2S.md)
- [Relatório de Verificação](VERIFICATION_REPORT.md) - Análise completa das funcionalidades implementadas

## 🤝 Contribuição

1. Fork o projeto
2. Crie uma branch para sua feature (`git checkout -b feature/AmazingFeature`)
3. Commit suas mudanças (`git commit -m 'Add some AmazingFeature'`)
4. Push para a branch (`git push origin feature/AmazingFeature`)
5. Abra um Pull Request

## 📄 Licença

Este projeto é parte de um trabalho acadêmico da disciplina de Redes e Sistemas Distribuídos.
