package matchmaking

import (
	"bytes"
	"encoding/json"
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
	serverAddress     string   // Endereço deste servidor (ex: http://server-1:8000)
	allServers        []string // Lista de todos os servidores no cluster
	nextServerAddress string   // O próximo servidor no anel
	tokenAcquiredChan <-chan bool // Canal para receber a notificação do token
}

// NewService cria uma nova instância do serviço de matchmaking.
func NewService(sm *state.StateManager, broker *pubsub.Broker, tokenChan <-chan bool, selfAddr string, allAddrs []string, nextAddr string) *MatchmakingService {
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
		<-s.tokenAcquiredChan
		log.Println("[MATCHMAKING] Token recebido. A verificar a fila...")
		s.processMatchmakingQueue()
		time.Sleep(2 * time.Second)
		s.passTokenToNextServer()
	}
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
func (s *MatchmakingService) passTokenToNextServer() {
	log.Printf("[MATCHMAKING] A passar o token para %s...", s.nextServerAddress)
	_, err := s.httpClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", nil)
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