# API e Orquestração de Jogadas S2S

Para refinar e padronizar a comunicação entre servidores, a Fase 1 focou em adotar uma abordagem mais RESTful para as ações de jogo e consolidar a lógica de orquestração.

## 1. Endpoint RESTful para Ações de Jogo

O endpoint S2S para encaminhar jogadas foi refatorado para seguir uma semântica mais clara e alinhada com os padrões REST.

**Localização:** `server/api/handlers.go`

- **Endpoint Anterior**: `POST /api/forward/play`
- **Novo Endpoint**: `POST /matches/{matchID}/action`

**Handler**: `handleMatchAction`
- **Extração de Parâmetros**:
  - O `matchID` agora é extraído diretamente da URL da requisição, tornando o endpoint um recurso bem definido.
  - O corpo da requisição foi simplificado para conter apenas os dados da ação.
- **Corpo da Requisição**: `{ "playerId": "...", "cardId": "..." }`

**Exemplo de Chamada no Cliente S2S:**
A função no cliente S2S foi renomeada para refletir a nova natureza da ação.

**Localização:** `server/s2s/client.go`

```go
// s2s/client.go
func ForwardAction(opponentServer, matchID, playerID, cardID string) {
    // ...
	url := fmt.Sprintf("%s/matches/%s/action", opponentServer, matchID)
	// ... faz a requisição HTTP POST
}
```

## 2. Fluxo de Orquestração da Jogada

O fluxo de processamento de uma ação de um jogador em uma partida distribuída foi consolidado para garantir consistência e evitar condições de corrida ou duplicação de lógica.

1.  **Ação do Cliente**: Um jogador local envia uma mensagem `PLAY_CARD` para o seu servidor (Servidor A).
2.  **Validação e Encaminhamento**:
    - A função `match.PlayCard` no Servidor A é chamada.
    - **Antes de qualquer alteração de estado local**, a função `forwardPlayIfNeeded` é invocada.
    - `forwardPlayIfNeeded` verifica:
      - Se a partida é distribuída.
      - Se o jogador que fez a ação está online *neste servidor*.
    - Se ambas as condições forem verdadeiras, a ação é encaminhada para o servidor do oponente (Servidor B) através de `s2s.ForwardAction`.
3.  **Processamento Remoto**:
    - O Servidor B recebe a requisição no endpoint `POST /matches/{matchID}/action`.
    - O handler `handleMatchAction` decodifica a ação e chama `match.PlayCard` na sua cópia local da partida.
    - **Importante**: Quando `forwardPlayIfNeeded` é chamada no Servidor B, a verificação `isPlayerLocal` falha (pois o jogador original da ação não está no Servidor B), o que **impede um re-encaminhamento em loop (eco)**.
4.  **Sincronização do Estado**:
    - Em ambos os servidores, a função `PlayCard` registra a ação do jogador no mapa `Waiting`.
    - Quando o mapa `Waiting` contém as jogadas de ambos os jogadores, a função `resolveRound` é disparada de forma independente em cada servidor.
    - Como a lógica de `resolveRound` é determinística, **ambos os servidores chegam ao mesmo resultado de estado** (HP, cartas na mão, etc.).
5.  **Notificação aos Clientes**:
    - Após a resolução da rodada, cada servidor utiliza a função `sendToPlayerSmart` para notificar os jogadores.
    - O Servidor A envia o resultado para o seu jogador local.
    - O Servidor B envia o resultado para o seu jogador local.
    - Esta abordagem garante que cada servidor é responsável por se comunicar apenas com os clientes diretamente conectados a ele.