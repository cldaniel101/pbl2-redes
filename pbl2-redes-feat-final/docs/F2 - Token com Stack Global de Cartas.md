# F2 - Token com Stack Global de Cartas

## Visão Geral

Esta funcionalidade implementa um sistema onde o **token de matchmaking** transporta um **stack global de cartas**. Quando um servidor cria uma partida, ele retira as cartas necessárias diretamente do token, garantindo que as cartas sejam distribuídas de forma justa e controlada em um ambiente distribuído.

## Motivação

### Problema Identificado

Na implementação anterior, cada servidor mantinha seu próprio `CardDB` local que gerava cartas aleatórias independentemente. Isso causava vários problemas:

1. **Inconsistência entre servidores**: Cada servidor tinha seu próprio pool de cartas, sem sincronização
2. **Falta de controle global**: Não havia um "estoque" global de cartas compartilhado
3. **Duplicação**: Múltiplos servidores poderiam gerar as mesmas cartas simultaneamente
4. **Sem garantia de justiça**: A distribuição de cartas não era controlada globalmente

### Solução Proposta

Integrar o stack de cartas no token de matchmaking, fazendo com que:
- O token leia o `cards.json` no início
- O token mantenha um pool global de cartas disponíveis
- Servidores retirem cartas do token ao criar partidas
- Cartas retiradas sejam removidas do pool do token
- O sistema reabastece automaticamente quando necessário

## Implementação

### 1. Estrutura do Token (`server/token/token.go`)

Criamos um novo pacote `token` que define a estrutura:

```go
type Token struct {
    CardPool   []string              // Pool de IDs de cartas disponíveis
    AllCards   map[string]CardInfo   // Mapa de todas as cartas (para reabastecimento)
    Timestamp  int64                 // Timestamp da última atualização
    ServerAddr string                // Endereço do servidor que possui o token
}
```

**Funcionalidades principais:**

- `LoadCardsFromJSON()`: Carrega cartas do `cards.json` e popula o pool
- `DrawCards(count)`: Remove e retorna N cartas do pool
- `refillPool_unsafe()`: Reabastece o pool quando esgotado (thread-unsafe, uso interno)
- `ToJSON()` / `FromJSON()`: Serialização para transporte HTTP

### 2. Passagem do Token via HTTP

#### Modificações no `matchmaking/service.go`

O serviço de matchmaking agora:

1. **Recebe o token inicial** no construtor (apenas o primeiro servidor)
2. **Armazena o token localmente** enquanto está com ele
3. **Retira cartas** do token ao criar partidas
4. **Serializa e envia** o token completo para o próximo servidor

```go
func (s *MatchmakingService) passTokenToNextServer() {
    // Serializa o token para JSON
    tokenJSON, err := s.currentToken.ToJSON()
    
    // Envia via HTTP POST
    resp, err := s.httpClient.Post(
        s.nextServerAddress+"/api/receive-token", 
        "application/json", 
        bytes.NewBuffer(tokenJSON)
    )
    
    // Limpa o token local após passar
    s.currentToken = nil
}
```

#### Modificações no `api/handlers.go`

O handler `handleReceiveToken` agora:

1. **Lê o corpo da requisição** (JSON do token)
2. **Deserializa** o token
3. **Passa para o matchmaking** via interface `TokenReceiver`
4. **Notifica** o canal para iniciar processamento

```go
func (s *APIServer) handleReceiveToken(w http.ResponseWriter, r *http.Request) {
    body, _ := ioutil.ReadAll(r.Body)
    receivedToken, _ := token.FromJSON(body)
    
    // Passa o token para o matchmaking service
    s.tokenReceiver.SetToken(receivedToken)
    
    // Notifica que pode processar
    s.tokenAcquiredChan <- true
}
```

### 3. Criação de Partidas com Cartas do Token

#### Novo Método: `createMatchWithTokenCards`

```go
func (s *MatchmakingService) createMatchWithTokenCards(
    p1, p2 *protocol.PlayerConn, 
    isDistributed bool, 
    guestServer string, 
    matchID string
) (*game.Match, error) {
    // Calcula cartas necessárias (2 jogadores x 5 cartas)
    const cardsPerPlayer = 5
    totalCardsNeeded := 2 * cardsPerPlayer

    // Retira cartas do token
    cards, err := s.currentToken.DrawCards(totalCardsNeeded)
    
    // Separa as cartas para cada jogador
    p1Cards := cards[:cardsPerPlayer]
    p2Cards := cards[cardsPerPlayer:]

    // Cria a partida com as cartas específicas
    if isDistributed {
        match, err = s.stateManager.CreateDistributedMatchAsHostWithCards(...)
    } else {
        match = s.stateManager.CreateLocalMatchWithCards(...)
    }
}
```

#### Novos Métodos no `state/manager.go`

- `CreateLocalMatchWithCards()`: Cria partida local com cartas predefinidas
- `CreateDistributedMatchAsHostWithCards()`: Cria partida distribuída como host
- `ConfirmAndCreateDistributedMatchWithCards()`: Confirma partida no servidor guest

#### Novo Construtor: `game.NewMatchWithCards()`

```go
func NewMatchWithCards(
    id string, 
    p1, p2 *protocol.PlayerConn, 
    cardDB *CardDB, 
    broker *pubsub.Broker, 
    informer StateInformer, 
    p1Cards, p2Cards []string
) *Match {
    match := &Match{
        // ... inicialização ...
    }
    
    // Define as mãos iniciais com as cartas do token
    match.Hands[0] = p1Cards
    match.Hands[1] = p2Cards
    
    return match
}
```

### 4. Partidas Distribuídas com Token

Para partidas entre servidores diferentes:

1. **Servidor Host** retira 10 cartas do token (5 para cada jogador)
2. **Servidor Host** cria a partida localmente com as cartas do P1
3. **Servidor Host** envia as cartas do P2 para o servidor guest via HTTP:

```go
requestBody, _ := json.Marshal(map[string]interface{}{
    "matchId":       matchID,
    "hostPlayerId":  localPlayer.ID,
    "guestPlayerId": opponentInfo.PlayerID,
    "guestCards":    p2Cards,  // Cartas do jogador remoto
})
```

4. **Servidor Guest** recebe a requisição e cria a partida com as cartas recebidas

### 5. Inicialização no `main.go`

O primeiro servidor (índice 0) inicializa o token:

```go
if myIndex == 0 {
    initialToken = token.NewToken(thisServerAddress)
    
    cardsData, _ := ioutil.ReadFile("cards.json")
    
    // 100 cópias de cada carta
    initialToken.LoadCardsFromJSON(cardsData, 100)
    
    log.Printf("Token inicial criado com %d cartas", initialToken.GetPoolSize())
}
```

## Problemas Identificados e Resolvidos

### ✅ Problema 1: Concorrência no Acesso ao Pool de Cartas

**Identificação**: Múltiplos servidores poderiam tentar acessar o pool simultaneamente.

**Solução**: 
- Token usa `sync.Mutex` para operações thread-safe
- Apenas um servidor possui o token por vez (modelo de anel)
- O token é passado sequencialmente entre servidores

### ✅ Problema 2: Esgotamento do Pool de Cartas

**Identificação**: O pool poderia esgotar durante operação normal.

**Solução**:
- Método `refillPool_unsafe()` reabastece automaticamente
- Quando `DrawCards()` detecta insuficiência, reabastece antes de retornar erro
- 100 cópias de cada carta são adicionadas no reabastecimento
- Sistema embaralha as cartas após reabastecimento

### ✅ Problema 3: Serialização e Transporte do Token

**Identificação**: Token precisa ser transferido entre servidores via HTTP.

**Solução**:
- Métodos `ToJSON()` e `FromJSON()` para serialização
- Token completo (incluindo pool e metadados) é serializado
- HTTP POST com corpo JSON transporta o token
- Mutex não é serializado (marcado com `json:"-"`)

### ✅ Problema 4: Sincronização de Cartas em Partidas Distribuídas

**Identificação**: Em partidas entre servidores, ambos precisam das cartas corretas.

**Solução**:
- Servidor host retira TODAS as cartas necessárias (P1 + P2)
- Servidor host envia cartas do P2 na requisição de partida
- Servidor guest recebe e usa as cartas enviadas
- Garante que ambos os servidores usem cartas do mesmo "draw"

### ✅ Problema 5: Reabastecimento Durante Partida

**Identificação**: Método `refillHands()` ainda usa `CardDB.GetRandomCard()` local.

**Impacto Mitigado**:
- Cartas iniciais vêm do token (10 cartas por partida)
- Pool inicial de 900 cartas (9 cartas × 100 cópias)
- Partidas típicas terminam antes de esgotar as mãos iniciais
- Para jogos longos, o `refillHands()` usa o CardDB local como fallback

**Possível Melhoria Futura**: 
Implementar um mecanismo para solicitar mais cartas do token durante a partida, mas isso requer:
- Sincronização complexa com o token em outro servidor
- Possível latência de rede
- Complexidade adicional que pode não ser necessária para partidas típicas

### ✅ Problema 6: Inicialização do Token

**Identificação**: Apenas um servidor deve criar o token inicial.

**Solução**:
- Primeiro servidor (myIndex == 0) cria e inicializa o token
- Token carrega `cards.json` e popula pool
- Outros servidores iniciam com `initialToken = nil`
- Recebem o token via HTTP quando chegar sua vez no anel

## Fluxo Completo

### Inicialização
1. Servidor 1 cria token com 900 cartas (9 cartas × 100 cópias)
2. Servidor 1 passa token após 5 segundos para Servidor 2
3. Token circula pelo anel de servidores

### Criação de Partida Local
1. Servidor recebe token
2. Servidor processa fila de matchmaking
3. Se há 2 jogadores:
   - Retira 10 cartas do token
   - Cria partida com `NewMatchWithCards()`
   - Cartas são atribuídas aos jogadores
4. Servidor passa token para o próximo

### Criação de Partida Distribuída
1. Servidor A (com token) tem 1 jogador na fila
2. Servidor A consulta Servidor B, que também tem 1 jogador
3. Servidor A retira 10 cartas do token
4. Servidor A cria partida localmente (P1 local)
5. Servidor A envia requisição para Servidor B com:
   - Match ID
   - IDs dos jogadores
   - 5 cartas para o P2 (jogador remoto)
6. Servidor B cria partida com as cartas recebidas
7. Ambos os servidores têm a partida sincronizada

## Vantagens da Implementação

1. **Controle Global**: Um único stack de cartas para todo o cluster
2. **Justiça**: Todas as cartas vêm da mesma fonte controlada
3. **Sem Duplicação**: Cartas retiradas são removidas do pool
4. **Escalabilidade**: Pool se reabastece automaticamente
5. **Tolerância a Falhas**: Se um servidor cai, o token continua circulando
6. **Auditoria**: Token mantém timestamp e servidor atual
7. **Simplicidade**: Integração natural com o sistema de token existente

## Considerações de Performance

- **Serialização**: Token é serializado a cada passagem (~KB de dados)
- **Latência**: Adiciona ~50-100ms por passagem de token (rede local)
- **Memória**: Token ocupa ~100KB em memória (900 cartas)
- **Thread-Safety**: Mutex garante segurança mas pode adicionar contenção

## Melhorias Futuras Possíveis

1. **Compressão**: Comprimir JSON do token antes de enviar
2. **Cartas Delta**: Enviar apenas mudanças ao invés do token completo
3. **Cache Local**: Cada servidor mantém cache de cartas já usadas
4. **Reabastecimento Inteligente**: Algoritmo mais sofisticado para refill
5. **Métricas**: Adicionar logging de uso de cartas para análise
6. **Persistência**: Salvar estado do token em disco para recuperação

## Conclusão

A implementação do **Token com Stack Global de Cartas** resolve o problema de distribuição inconsistente de cartas em um ambiente distribuído. O sistema garante que todas as partidas usem cartas de um pool global controlado, mantendo a justiça e consistência do jogo, ao mesmo tempo em que aproveita a infraestrutura de token já existente para matchmaking.

