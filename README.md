Claro! Aqui está o texto convertido em **Markdown** pronto para ser usado diretamente em um arquivo `.md` (como `README.md`):

````markdown
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

```bash
Servidor 1: TCP:9000, HTTP:8000
Servidor 2: TCP:9001, HTTP:8001  
Servidor 3: TCP:9002, HTTP:8002
````

## 🚀 Instruções de Execução

### Pré-requisitos

* Docker e Docker Compose
* Go 1.19+ (para desenvolvimento local)

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
  -e SERVER_ADDR=server-2:9001 \
  pingpong-client:latest

# Conectar cliente ao servidor 3
docker run --rm -it --network pbl2-redes_game-net \
  -e SERVER_ADDR=server-3:9002 \
  pingpong-client:latest
```

---

## 🚀 Execução Multi-Host (Múltiplas Máquinas)

Esta seção descreve como executar os servidores em máquinas separadas (ou VMs) usando `docker run` e `--network=host`, assumindo que as máquinas estão na mesma rede e podem se comunicar pelos IPs listados.

### Pré-requisitos

1. As três máquinas (ex: `192.168.1.10`, `192.168.1.11`, `192.168.1.12`) devem ter o Docker instalado e a imagem `pingpong-server:latest` e `pingpong-client:latest` (construída, por exemplo, com `docker-compose build`).
2. As portas `8000` (API) e `9000` (TCP) devem estar acessíveis entre as máquinas.

---

### 1. Configurar Variável de Ambiente

```bash
export ALL_SERVERS_LIST="http://192.168.1.10:8000,http://192.168.1.11:8000,http://192.168.1.12:8000"
```

### 2. Iniciar os Servidores

**Na Máquina 1 (`192.168.1.10`):**

```bash
docker run -d --rm \
  --name pbl_server_1 \
  --network=host \
  -e "LISTEN_ADDR=:9000" \
  -e "API_ADDR=:8000" \
  -e "ALL_SERVERS=${ALL_SERVERS_LIST}" \
  -e "HOSTNAME=192.168.1.10" \
  pingpong-server:latest
```

**Na Máquina 2 (`192.168.1.11`):**

```bash
docker run -d --rm \
  --name pbl_server_2 \
  --network=host \
  -e "LISTEN_ADDR=:9000" \
  -e "API_ADDR=:8000" \
  -e "ALL_SERVERS=${ALL_SERVERS_LIST}" \
  -e "HOSTNAME=192.168.1.11" \
  pingpong-server:latest
```

**Na Máquina 3 (`192.168.1.12`):**

```bash
docker run -d --rm \
  --name pbl_server_3 \
  --network=host \
  -e "LISTEN_ADDR=:9000" \
  -e "API_ADDR=:8000" \
  -e "ALL_SERVERS=${ALL_SERVERS_LIST}" \
  -e "HOSTNAME=192.168.1.12" \
  pingpong-server:latest
```

### 3. Conectar Clientes

```bash
docker run --rm -it \
  --network=host \
  -e "SERVER_ADDR=192.168.1.10:9000" \
  pingpong-client:latest
```

---

## 🧪 Executar Testes

### Testes de Concorrência de Pacotes

```bash
cd tests
go test -v packs_test.go
go test -bench=BenchmarkPackStoreConcurrency -v
```

### Testes de Estresse do Cluster

```bash
cd tests
go test -v stress_cluster_test.go
go test -v stress_packs.go
```

## 📡 Variáveis de Ambiente

### Servidores

```bash
LISTEN_ADDR=:9000
API_ADDR=:8000
ALL_SERVERS=http://server-1:8000,http://server-2:8000,http://server-3:8000
HOSTNAME=server-1
PACK_REQUEST_TIMEOUT_SEC=10
```

### Cliente

```bash
SERVER_ADDR=server-1:9000
PING_INTERVAL_MS=2000
```

## 🔄 Exemplos de Requisições S2S (Server-to-Server)

### 1. Buscar Oponente

```bash
curl -X GET http://localhost:8001/api/find-opponent
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
  -d '{"packStock": 1000}'
```

---

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
  "you": {"hp": 20, "hand": ["c_001", "c_002", "c_003", "c_004", "c_005"]},
  "opponent": {"hp": 20, "handSize": 5},
  "round": 1,
  "deadlineMs": 12000
}
```

---

## 🎮 Comandos do Cliente

### Comandos Básicos

* `/play <número>` — Jogar carta
* `/hand` — Mostrar mão
* `/pack` — Abrir pacote
* `/ping` — Mostrar RTT
* `/autoplay` — Ativar autoplay
* `/rematch` — Nova partida
* `/help` — Ajuda
* `/quit` — Sair

---

## 🔧 Desenvolvimento Local

```bash
cd server
go run main.go

cd client
go run main.go

cd tests
go test -v ./...
```

---

## 📊 Monitoramento

```bash
docker-compose logs -f server-1
docker-compose logs -f server-2
docker-compose logs -f server-3
docker-compose ps
docker stats
```

---

## 🐛 Troubleshooting

1. **Servidor não inicia**

   * Verifique as portas
   * Use `docker-compose logs server-1`

2. **Cliente não conecta**

   * Confirme se o servidor está rodando

3. **Partidas não criam**

   * Certifique-se de que múltiplos servidores estão online

4. **Pacotes não abrem**

   * Verifique o token e logs da API

---

## 🛡️ Tolerância a Falhas e Melhorias

* **Regeneração Inteligente de Token:** watchdog no líder com timeout dinâmico (4s × nº de servidores)
* **Logs estruturados:** `[MATCHMAKING]`, `[HANDLER]`, `[MATCH]`, etc.
* **Timeout Configurável:**

```bash
PACK_REQUEST_TIMEOUT_SEC=10
```

---

## 📚 Documentação Adicional

* [Regras do Jogo](https://www.google.com/search?q=GAME_RULES.md)
* [Descrição do Problema](https://www.google.com/search?q=PROBLEM_DESCRIPTION.md)
* [Arquitetura Distribuída](https://www.google.com/search?q=docs/F0%2520-%2520arquitetura_distribuida.md)
* [API e Orquestração](https://www.google.com/search?q=docs/F1%2520-%2520API%2520e%2520Orquestra%C3%A7%C3%A3o%2520de%2520Jogadas%2520S2S.md)
* [Relatório de Verificação](https://www.google.com/search?q=VERIFICATION_REPORT.md)

---

## 🤝 Contribuição

1. Fork o projeto
2. Crie uma branch (`git checkout -b feature/AmazingFeature`)
3. Commit suas mudanças (`git commit -m 'Add some AmazingFeature'`)
4. Push (`git push origin feature/AmazingFeature`)
5. Abra um Pull Request

---
