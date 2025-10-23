package matchmaking

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"pingpong/server/game"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"pingpong/server/state"
)

// MatchmakingService gere o processo de emparelhar jogadores,
// utilizando uma arquitetura de anel de token para coordenar entre múltiplos servidores.
type MatchmakingService struct {
	stateManager      *state.StateManager
	broker            *pubsub.Broker
	httpClient        *http.Client
	serverAddress     string                     // Endereço deste servidor (ex: http://server-1:8000)
	allServers        []string                   // Lista de todos os servidores no cluster
	nextServerAddress string                     // O próximo servidor no anel
	tokenAcquiredChan <-chan protocol.TokenState // Canal para receber a notificação do token
}

// NewService cria uma nova instância do serviço de matchmaking.
func NewService(sm *state.StateManager, broker *pubsub.Broker, tokenChan <-chan protocol.TokenState, selfAddr string, allAddrs []string, nextAddr string) *MatchmakingService {
	return &MatchmakingService{
		stateManager:      sm,
		broker:            broker,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		serverAddress:     selfAddr,
		allServers:        allAddrs,
		nextServerAddress: nextAddr,
		tokenAcquiredChan: tokenChan,
	}
}

// Run inicia o loop principal do serviço de matchmaking, que aguarda pelo token para agir.
func (s *MatchmakingService) Run() {
	log.Println("[MATCHMAKING] Serviço iniciado. A aguardar pelo token...")
	for {
		tokenState := <-s.tokenAcquiredChan
		log.Printf("[MATCHMAKING] Token recebido. Estado: %+v. A verificar a fila...", tokenState)

		// Processa a fila de pacotes ANTES de qualquer outra coisa.
		tokenState = s.processPackRequests(tokenState)

		s.processMatchmakingQueue()
		time.Sleep(2 * time.Second)
		s.passTokenToNextServer(tokenState)
	}
}

// processPackRequests processa a fila de pedidos de pacotes.
// Retorna o estado do token atualizado.
func (s *MatchmakingService) processPackRequests(currentState protocol.TokenState) protocol.TokenState {
	requests := s.stateManager.DequeueAllPackRequests()
	if len(requests) == 0 {
		return currentState // Sem pedidos, estado não muda.
	}

	log.Printf("[MATCHMAKING] A processar %d pedidos de pacotes...", len(requests))

	for _, req := range requests {
		if currentState.PackStock > 0 {
			// Há estoque, processa o pedido.
			currentState.PackStock--
			cards := s.stateManager.PackSystem.GenerateCardsForPack()

			// Envia o resultado de volta para a goroutine do jogador.
			req.ReplyChan <- state.PackResult{Cards: cards}

			log.Printf("[MATCHMAKING] Pacote aberto para %s. Cartas: %v. Estoque restante: %d", req.PlayerID, cards, currentState.PackStock)
		} else {
			// Estoque esgotado.
			req.ReplyChan <- state.PackResult{Err: errors.New("estoque de pacotes esgotado")}
			log.Printf("[MATCHMAKING] Pedido de pacote de %s rejeitado. Estoque esgotado.", req.PlayerID)
		}
	}

	return currentState
}

// processMatchmakingQueue verifica a fila de jogadores e tenta criar partidas.
func (s *MatchmakingService) processMatchmakingQueue() {
	playersInQueue := s.stateManager.GetMatchmakingQueueSnapshot()

	if len(playersInQueue) >= 2 {
		p1 := playersInQueue[0]
		p2 := playersInQueue[1]
		s.stateManager.RemovePlayersFromQueue(p1, p2)
		match := s.stateManager.CreateLocalMatch(p1, p2, s.broker)
		s.notifyPlayersOfMatch(match, p1, p2)
		go s.monitorMatch(match)
	} else if len(playersInQueue) == 1 {
		player := playersInQueue[0]
		log.Printf("[MATCHMAKING] A tentar encontrar um oponente distribuído para %s...", player.ID)
		if found := s.findAndCreateDistributedMatch(player); !found {
			log.Printf("[MATCHMAKING] Nenhum oponente distribuído encontrado para %s.", player.ID)
		}
	} else {
		log.Println("[MATCHMAKING] Fila vazia.")
	}
}

// findAndCreateDistributedMatch percorre outros servidores à procura de um oponente.
func (s *MatchmakingService) findAndCreateDistributedMatch(localPlayer *protocol.PlayerConn) bool {
	for _, serverAddr := range s.allServers {
		if serverAddr == s.serverAddress {
			continue
		}

		resp, err := s.httpClient.Get(serverAddr + "/api/find-opponent")
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				_ = resp.Body.Close()
			}
			continue
		}

		var opponentInfo struct {
			PlayerID string `json:"playerId"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&opponentInfo); err != nil {
			_ = resp.Body.Close()
			continue
		}
		_ = resp.Body.Close()

		log.Printf("[MATCHMAKING] Oponente %s encontrado em %s. A solicitar partida...", opponentInfo.PlayerID, serverAddr)
		matchID := fmt.Sprintf("dist_match_%d", time.Now().UnixNano())
		requestBody, _ := json.Marshal(map[string]string{
			"matchId":       matchID,
			"hostPlayerId":  localPlayer.ID,
			"guestPlayerId": opponentInfo.PlayerID,
		})

		resp, err = s.httpClient.Post(serverAddr+"/api/request-match", "application/json", bytes.NewBuffer(requestBody))
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				_ = resp.Body.Close()
			}
			log.Printf("[MATCHMAKING] Falha ao solicitar partida com %s.", serverAddr)
			return false
		}
		_ = resp.Body.Close()

		s.stateManager.RemovePlayersFromQueue(localPlayer)
		match, err := s.stateManager.CreateDistributedMatchAsHost(matchID, localPlayer, opponentInfo.PlayerID, s.serverAddress, serverAddr, s.broker)
		if err != nil {
			log.Printf("[MATCHMAKING] Erro ao criar partida distribuída localmente: %v", err)
			return false
		}

		log.Printf("[MATCHMAKING] Partida distribuída %s criada com sucesso!", matchID)
		s.notifyPlayersOfMatch(match, localPlayer, match.P2)
		go s.monitorMatch(match)
		return true
	}
	return false
}

// passTokenToNextServer envia uma requisição HTTP para passar o token.
func (s *MatchmakingService) passTokenToNextServer(currentState protocol.TokenState) {
	log.Printf("[MATCHMAKING] A passar o token para %s com estado: %+v...", s.nextServerAddress, currentState)

	requestBody, err := json.Marshal(currentState)
	if err != nil {
		log.Printf("[MATCHMAKING] ERRO ao serializar o estado do token: %v", err)
		// Decide o que fazer aqui - talvez passar um estado padrão ou tentar novamente?
		// Por agora, vamos apenas logar e não passar o token.
		return
	}

	_, err = s.httpClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		log.Printf("[MATCHMAKING] ERRO ao passar o token para %s: %v.", s.nextServerAddress, err)
	} else {
		log.Printf("[MATCHMAKING] Token passado com sucesso.")
	}
}

// notifyPlayersOfMatch envia a mensagem MATCH_FOUND para os jogadores envolvidos.
// O tipo do parâmetro 'match' foi corrigido para game.Match.
func (s *MatchmakingService) notifyPlayersOfMatch(match *game.Match, p1, p2 *protocol.PlayerConn) {
	s.broker.Publish("player."+p1.ID, protocol.ServerMsg{
		T:          protocol.MATCH_FOUND,
		MatchID:    match.ID,
		OpponentID: p2.ID,
	})
	s.broker.Publish("player."+p2.ID, protocol.ServerMsg{
		T:          protocol.MATCH_FOUND,
		MatchID:    match.ID,
		OpponentID: p1.ID,
	})
	match.BroadcastState()
}

// monitorMatch aguarda o fim de uma partida para a remover do estado.
// O tipo do parâmetro 'match' foi corrigido para game.Match.
func (s *MatchmakingService) monitorMatch(match *game.Match) {
	<-match.Done()
	s.stateManager.RemoveMatch(match.ID)
	log.Printf("[MATCHMAKING] Partida %s finalizada e removida do estado.", match.ID)
}
