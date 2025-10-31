# Arquitetura Distribuída e Sincronização de Estado

Este documento detalha a implementação de duas funcionalidades críticas para a operação robusta e distribuída do servidor do jogo: estabilidade contra desconexões de clientes e sincronização de estado de jogo entre múltiplos servidores.

## 1. Fase 0.1 — Estabilidade e Tolerância a Falhas

Para garantir que o servidor permaneça operacional mesmo quando os clientes se desconectam abruptamente (seja por perda de rede, fechamento do cliente, etc.), foi implementado um mecanismo de recuperação de *panics* e tratamento de erros de I/O.

### 1.1. Recuperação de Panics em Goroutines

As operações de leitura (`readLoop`) e escrita (`writeLoop`) para cada cliente são executadas em goroutines separadas. Um *panic* não tratado em qualquer uma dessas goroutines poderia derrubar todo o processo do servidor.

Para evitar isso, um `defer...recover()` foi adicionado ao topo de ambas as funções.

**Localização:** `server/network/client_manager.go`

**Exemplo em `readLoop`:**
```go
func (s *TCPServer) readLoop(player *protocol.PlayerConn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[CLIENT_MGR] Panic recuperado em readLoop para o cliente %s: %v", player.ID, r)
		}
	}()

	for {
		msg, err := player.ReadMsg()
		if err != nil {
			log.Printf("[CLIENT_MGR] Erro ao ler do cliente %s: %v. A encerrar a conexão.", player.ID, err)
			return // Encerra a goroutine de forma limpa
		}
		s.broker.Publish("client.messages", protocol.ClientAction{Player: player, Msg: msg})
	}
}
```

- **`defer func() { ... }()`**: Garante que o bloco de código seja executado ao final da função, não importa como ela termine (retorno normal, erro ou *panic*).
- **`recover()`**: Se a goroutine entrar em pânico, `recover()` captura o valor do pânico, permitindo que o programa continue a execução em vez de travar. O pânico é logado para depuração.

### 1.2. Limpeza de Recursos (`Cleanup`)

Quando um cliente se desconecta, é crucial liberar todos os recursos associados a ele para evitar vazamentos de memória e estado fantasma. A função `handleConn` agora possui um bloco `defer` robusto que orquestra essa limpeza.

**Localização:** `server/network/client_manager.go`

O processo de limpeza inclui:
1.  **Cancelar a Inscrição no Broker**: `s.broker.Unsubscribe(playerSub)` remove o listener de mensagens do jogador.
2.  **Remover Jogador do Estado**: `s.stateManager.CleanupPlayer(player)` remove o jogador da lista de jogadores online, da fila de matchmaking e de qualquer partida ativa.
3.  **Notificar Oponente**: Se o jogador estava em uma partida, o oponente é notificado da desconexão e declarado vencedor.
4.  **Fechar Conexão**: `conn.Close()` fecha a conexão TCP subjacente.

## 2. Fase 0.2 — Sincronização de Estado Inter-Servidores

Quando uma partida é formada entre jogadores conectados a servidores diferentes, o estado do jogo (HP, cartas jogadas, resultados de rodadas) precisa ser perfeitamente sincronizado. Isso é alcançado através de uma API S2S (Server-to-Server) e uma lógica de roteamento de mensagens "inteligente".

### 2.1. Fluxo de Comunicação S2S

A comunicação funciona em um fluxo de ida e volta:

1.  **Jogador Local Ação**: O Jogador A (no Servidor A) joga uma carta.
2.  **Retransmissão S2S**: O Servidor A detecta que o oponente (Jogador B) está em um servidor remoto (Servidor B). Ele envia a ação do Jogador A para um endpoint específico no Servidor B via HTTP POST.
3.  **Processamento Remoto**: O Servidor B recebe a ação através de sua API, a processa na lógica da partida e atualiza o estado do jogo.
4.  **Atualização de Estado**: Ambos os servidores calculam o resultado da rodada.
5.  **Envio para Clientes**: Cada servidor envia o estado atualizado para seu cliente local. Se o cliente for remoto, a mensagem é retransmitida de volta via API S2S.

### 2.2. Endpoints da API S2S

Dois novos endpoints foram criados para facilitar a comunicação S2S.

**Localização:** `server/api/handlers.go`

- **`POST /api/forward/play`**: Usado para retransmitir a jogada de um jogador (qual carta foi jogada) para o servidor do oponente.
  - **Handler**: `handleForwardPlay`
  - **Corpo da Requisição**: `{ "matchId": "...", "playerId": "...", "cardId": "..." }`

- **`POST /api/forward/message`**: Usado para retransmitir mensagens de estado do jogo (como resultados de rodada, atualizações de HP, fim de partida) para um jogador remoto.
  - **Handler**: `handleForwardMessage`
  - **Corpo da Requisição**: Objeto `protocol.ServerMsg`
  - **Cabeçalho Especial**: `X-Player-ID` é usado para identificar o jogador de destino no servidor remoto.

### 2.3. Lógica de Roteamento Inteligente

Para desacoplar a lógica do jogo da complexidade da rede, foram criadas funções "inteligentes" que decidem como enviar mensagens.

**Localização:** `server/game/match.go`

- **`sendToPlayerSmart(playerID, msg)`**:
  - **Verifica se a partida é distribuída**: Usa um `StateInformer` para obter detalhes da partida.
  - **Se o jogador for local**: A mensagem é publicada no broker Pub/Sub local (`m.broker.Publish`).
  - **Se o jogador for remoto**: A mensagem é encapsulada e enviada via HTTP para o servidor do oponente usando `s2s.ForwardMessage`.

- **`forwardPlayIfNeeded(playerID, cardID)`**:
  - Chamada dentro da função `PlayCard`.
  - Verifica se a partida é distribuída e se a ação partiu de um jogador local.
  - Se ambas as condições forem verdadeiras, a jogada é retransmitida para o servidor do oponente usando `s2s.ForwardPlay`. Isso garante que a jogada chegue ao servidor remoto *antes* de ser processada localmente, mantendo a sincronia.
