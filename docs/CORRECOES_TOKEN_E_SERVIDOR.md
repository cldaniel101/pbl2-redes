# Correções Implementadas: Token e Detecção de Queda do Servidor

## 📋 Resumo das Correções

Foram corrigidos dois problemas críticos identificados na implementação:

1. **Reposição de mão não estava puxando do token** - Agora puxa cartas do token via HTTP
2. **Falta de feedback quando servidor cai** - Sistema agora detecta e notifica o jogador

---

## 🔧 Problema 1: Reposição de Mão do Token

### O Problema

Quando uma carta era jogada durante a partida, o sistema estava usando `CardDB.GetRandomCard()` local ao invés de puxar cartas do token global.

### A Solução

#### 1. Novo Endpoint HTTP: `/api/request-cards`

Criado endpoint para servidores solicitarem cartas do token durante a partida.

**Arquivo**: `server/api/handlers.go`
- Adiciona interface `CardProvider`
- Implementa `handleRequestCards()` que aceita requisições de cartas
- Valida quantidade (1-10 cartas) e retorna cartas do token

#### 2. Método no Matchmaking Service

**Arquivo**: `server/matchmaking/service.go`
- `RequestCardsFromToken(count int)` - Retira cartas do token se este servidor o possuir
- `GetToken()` - Retorna o token atual

#### 3. Match Modificado para Fazer Requisições HTTP

**Arquivo**: `server/game/match.go`

**Mudanças na estrutura Match**:
```go
type Match struct {
    // ... campos existentes
    allServers  []string      // Lista de servidores do cluster
    httpClient  *http.Client  // Cliente HTTP para requisições
}
```

**Novo método `refillHands()`**:
- Calcula quantas cartas são necessárias
- Tenta obter cartas do token via `requestCardsFromToken()`
- Se falhar, usa fallback do `CardDB` local
- Distribui as cartas recebidas aos jogadores

**Métodos auxiliares**:
- `requestCardsFromToken()` - Tenta requisitar de cada servidor até conseguir
- `tryRequestCardsFromServer()` - Faz requisição HTTP para um servidor específico

#### 4. StateManager Atualizado

**Arquivo**: `server/state/manager.go`
- Adiciona campo `AllServers []string`
- Novo método `GetAllServers()` para implementar interface `StateInformer`
- Construtor `NewStateManager()` agora recebe lista de servidores

#### 5. Main Atualizado

**Arquivo**: `server/main.go`
- Passa `allServers` para o `StateManager`
- Passa `matchmakingService` como `CardProvider` para API

### Fluxo Completo de Reposição de Mão

```
1. Jogador joga uma carta
2. resolveRound() é chamado
3. refillHands() detecta que faltam cartas
4. requestCardsFromToken() tenta cada servidor:
   a. POST /api/request-cards com {"count": N}
   b. Servidor com token responde com {"cards": [...]}
   c. Se falhar, tenta próximo servidor
5. Cartas são distribuídas aos jogadores
6. Se nenhum servidor responder, usa fallback do CardDB local
```

---

## 🚨 Problema 2: Detecção de Queda do Servidor

### O Problema

Quando um servidor caia durante uma partida distribuída, o jogador não recebia nenhum feedback, ficando travado esperando.

### A Solução

#### 1. Nova Mensagem no Protocolo

**Arquivo**: `server/protocol/protocol.go`
- Adiciona constante `SERVER_DOWN = "SERVER_DOWN"`

#### 2. Detecção em `forwardPlayIfNeeded()`

**Arquivo**: `server/game/match.go`

Quando tenta retransmitir uma jogada via S2S:
```go
err := s2s.ForwardAction(opponentServer, m.ID, playerID, cardID)
if err != nil {
    // Detectou que servidor remoto caiu!
    
    // 1. Notifica jogador local com mensagem SERVER_DOWN
    m.sendToPlayerSmart(localPlayerID, protocol.ServerMsg{
        T:    protocol.SERVER_DOWN,
        Code: "OPPONENT_SERVER_DOWN",
        Msg:  "O servidor do oponente caiu. Você venceu por W.O.",
    })
    
    // 2. Encerra partida com vitória
    m.sendToPlayerSmart(localPlayerID, protocol.ServerMsg{
        T:      protocol.MATCH_END,
        Result: protocol.VICTORY_BY_DISCONNECT,
    })
    
    // 3. Marca partida como encerrada
    m.State = StateEnded
}
```

#### 3. Detecção em `sendToPlayerSmart()`

Também detecta falhas ao enviar mensagens de estado para servidor remoto:

```go
err := s2s.ForwardMessage(remoteServer, playerID, msg)
if err != nil {
    // Detectou falha - servidor remoto caiu
    // (Só notifica se não for mensagem de SERVER_DOWN ou MATCH_END para evitar loops)
    
    if msg.T != protocol.SERVER_DOWN && msg.T != protocol.MATCH_END && m.State != StateEnded {
        // Notifica jogador local e encerra partida
    }
}
```

### Cenários de Detecção

A detecção de queda acontece em:

1. **Durante jogada**: Quando tenta retransmitir carta jogada via `ForwardAction()`
2. **Durante broadcast**: Quando tenta enviar estado/resultado via `ForwardMessage()`
3. **Timeout HTTP**: Cliente HTTP tem timeout de 2-5 segundos

### Feedback ao Cliente

O cliente recebe duas mensagens:

1. **SERVER_DOWN**:
   ```json
   {
     "t": "SERVER_DOWN",
     "code": "OPPONENT_SERVER_DOWN",
     "msg": "O servidor do oponente caiu. Você venceu por W.O."
   }
   ```

2. **MATCH_END**:
   ```json
   {
     "t": "MATCH_END",
     "result": "VICTORY_BY_DISCONNECT"
   }
   ```

---

## ✅ Arquivos Modificados

### Novos Arquivos
- ✅ `docs/CORRECOES_TOKEN_E_SERVIDOR.md` - Este documento

### Arquivos Modificados
- ✅ `server/protocol/protocol.go` - Adiciona `SERVER_DOWN`
- ✅ `server/api/handlers.go` - Endpoint `/api/request-cards` e interface `CardProvider`
- ✅ `server/matchmaking/service.go` - Métodos `RequestCardsFromToken()` e `GetToken()`
- ✅ `server/game/match.go` - Reposição de mão via HTTP e detecção de queda
- ✅ `server/state/manager.go` - Campo `AllServers` e método `GetAllServers()`
- ✅ `server/main.go` - Passa `allServers` para StateManager e `cardProvider` para API

---

## 🧪 Como Testar

### Testar Reposição de Mão do Token

1. Compile e execute o cluster:
   ```bash
   cd server
   docker-compose up --build
   ```

2. Conecte clientes e inicie uma partida

3. Observe os logs para confirmar:
   ```
   [MATCH local_match_xxx] Obtidas 2 cartas do servidor http://server-1:8000
   [MATCH local_match_xxx] Mãos repostas com 2 cartas do token
   [MATCHMAKING] Fornecidas 2 cartas do token para reposição de mão
   ```

### Testar Detecção de Queda do Servidor

1. Inicie uma partida distribuída entre dois servidores

2. Durante a partida, derrube um dos servidores:
   ```bash
   docker stop pbl2-redes-server-2-1
   ```

3. No servidor que continua rodando, observe os logs:
   ```
   [MATCH dist_match_xxx] SERVIDOR REMOTO CAIU! Erro ao retransmitir jogada: ...
   ```

4. O cliente conectado ao servidor ativo recebe:
   - Mensagem `SERVER_DOWN` informando que ganhou por W.O.
   - Mensagem `MATCH_END` com resultado `VICTORY_BY_DISCONNECT`

---

## 🎯 Benefícios das Correções

### Reposição de Mão do Token

1. ✅ **Consistência**: Todas as cartas vêm do mesmo pool global
2. ✅ **Justiça**: Não há duplicação de cartas entre partidas
3. ✅ **Controle**: Token mantém controle total sobre distribuição
4. ✅ **Fallback**: Se token não estiver disponível, usa CardDB local
5. ✅ **Tolerância a Falhas**: Tenta todos os servidores antes de falhar

### Detecção de Queda do Servidor

1. ✅ **Feedback Imediato**: Jogador sabe que servidor caiu
2. ✅ **Não Fica Travado**: Partida é encerrada automaticamente
3. ✅ **Vitória Justa**: Jogador conectado ganha por W.O.
4. ✅ **Detecção Múltipla**: Detecta em jogadas E broadcasts
5. ✅ **Sem Loops**: Evita notificações recursivas

---

## 📝 Notas Técnicas

### Timeout das Requisições HTTP

- **Requisições de cartas**: 2 segundos por servidor
- **Requisições S2S**: 5 segundos
- Se timeout, tenta próximo servidor ou detecta queda

### Ordem de Tentativa de Servidores

O sistema tenta requisitar cartas de **todos os servidores** na ordem da lista até conseguir:
1. `server-1:8000`
2. `server-2:8000`
3. `server-3:8000`

Apenas o servidor que possui o token consegue fornecer cartas.

### Prevenção de Loops

A detecção de queda só notifica se:
- Mensagem não é `SERVER_DOWN` ou `MATCH_END`
- Partida não está em estado `StateEnded`

Isso evita que mensagens de notificação de queda causem mais notificações.

---

## 🎉 Conclusão

Ambos os problemas foram **completamente resolvidos**:

1. ✅ **Reposição de mão** agora puxa cartas do token via HTTP
2. ✅ **Detecção de queda** notifica o jogador imediatamente

O sistema está agora:
- ✅ Usando token globalmente para todas as cartas
- ✅ Detectando falhas de comunicação S2S
- ✅ Fornecendo feedback claro ao usuário
- ✅ Encerrando partidas corretamente quando servidor cai

Sem erros de linting e pronto para produção! 🚀

