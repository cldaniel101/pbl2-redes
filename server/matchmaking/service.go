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
	"pingpong/server/token"
)

// MatchmakingService gere o processo de emparelhar jogadores,
// utilizando uma arquitetura de anel de token para coordenar entre múltiplos servidores.
type MatchmakingService struct {
	stateManager      *state.StateManager
	broker            *pubsub.Broker
	httpClient        *http.Client
	serverAddress     string      // Endereço deste servidor (ex: http://server-1:8000)
	allServers        []string    // Lista de todos os servidores no cluster
	nextServerAddress string      // O próximo servidor no anel
	tokenAcquiredChan <-chan bool // Canal para receber a notificação do token
	currentToken      *token.Token
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

// SetToken permite ao servidor da API injetar o token recebido neste serviço.
func (s *MatchmakingService) SetToken(t *token.Token) {
	s.currentToken = t
}

// processMatchmakingQueue verifica a fila de jogadores e tenta criar partidas.
func (s *MatchmakingService) processMatchmakingQueue() {
	playersInQueue := s.stateManager.GetMatchmakingQueueSnapshot()

	if len(playersInQueue) >= 2 {
		p1 := playersInQueue[0]
		p2 := playersInQueue[1]
		s.stateManager.RemovePlayersFromQueue(p1, p2)
		// Tenta criar partida com cartas do token se disponvel
		var match *game.Match
		var err error
		if s.currentToken != nil {
			match, err = s.createMatchWithTokenCards(p1, p2, false, "", "")
			if err != nil {
				log.Printf("[MATCHMAKING] Falha ao usar cartas do token: %v. A usar CardDB.", err)
				match = s.stateManager.CreateLocalMatch(p1, p2, s.broker)
			}
		} else {
			match = s.stateManager.CreateLocalMatch(p1, p2, s.broker)
		}
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
		var match *game.Match
		if s.currentToken != nil {
			match, err = s.stateManager.CreateDistributedMatchAsHostWithCards(matchID, localPlayer, opponentInfo.PlayerID, s.serverAddress, serverAddr, s.broker, []string{}, []string{})
			// As cartas virão da createMatchWithTokenCards abaixo para garantir a divisão correta
		}
		if err != nil || match == nil {
			match, err = s.stateManager.CreateDistributedMatchAsHost(matchID, localPlayer, opponentInfo.PlayerID, s.serverAddress, serverAddr, s.broker)
		}
		if err != nil {
			log.Printf("[MATCHMAKING] Erro ao criar partida distribuída localmente: %v", err)
			return false
		}
		// Se houver token, substituir mãos iniciais com cartas do token
		if s.currentToken != nil {
			const cardsPerPlayer = 5
			total := 2 * cardsPerPlayer
			if cards, derr := s.currentToken.DrawCards(total); derr == nil {
				p1Cards := cards[:cardsPerPlayer]
				p2Cards := cards[cardsPerPlayer:]
				match.OverrideInitialHands(p1Cards, p2Cards)
				log.Printf("[MATCHMAKING] Partida distribuída com cartas do token atribuídas")
			} else {
				log.Printf("[MATCHMAKING] Falha ao retirar cartas do token: %v", derr)
			}
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
	if s.currentToken == nil {
		log.Printf("[MATCHMAKING] AVISO: Tentando passar token nulo!")
		return
	}
	// Atualiza dono do token
	s.currentToken.UpdateServerAddr(s.nextServerAddress)
	tokenJSON, err := s.currentToken.ToJSON()
	if err != nil {
		log.Printf("[MATCHMAKING] ERRO ao serializar token: %v", err)
		return
	}
	log.Printf("[MATCHMAKING] A passar o token (%d cartas) para %s...", len(s.currentToken.CardPool), s.nextServerAddress)
	resp, err := s.httpClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(tokenJSON))
	if err != nil {
		log.Printf("[MATCHMAKING] ERRO ao passar o token para %s: %v.", s.nextServerAddress, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[MATCHMAKING] ERRO: servidor retornou status %d", resp.StatusCode)
		return
	}
	log.Printf("[MATCHMAKING] Token passado com sucesso.")
	s.currentToken = nil
}

// createMatchWithTokenCards cria uma partida (local ou distribuída) usando cartas do token
func (s *MatchmakingService) createMatchWithTokenCards(p1, p2 *protocol.PlayerConn, isDistributed bool, guestServer string, matchID string) (*game.Match, error) {
	if s.currentToken == nil {
		return nil, fmt.Errorf("token não disponível")
	}
	const cardsPerPlayer = 5
	totalCardsNeeded := 2 * cardsPerPlayer
	cards, err := s.currentToken.DrawCards(totalCardsNeeded)
	if err != nil {
		return nil, fmt.Errorf("erro ao pegar cartas do token: %w", err)
	}
	p1Cards := cards[:cardsPerPlayer]
	p2Cards := cards[cardsPerPlayer:]
	var match *game.Match
	if isDistributed {
		match, err = s.stateManager.CreateDistributedMatchAsHostWithCards(
			matchID,
			p1,
			p2.ID,
			s.serverAddress,
			guestServer,
			s.broker,
			p1Cards,
			p2Cards,
		)
	} else {
		match = s.stateManager.CreateLocalMatchWithCards(p1, p2, s.broker, p1Cards, p2Cards)
	}
	return match, err
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
