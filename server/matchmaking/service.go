package matchmaking

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
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
	serverAddress     string       // Endereço deste servidor (ex: http://server-1:8000)
	allServers        []string     // Lista de todos os servidores no cluster
	nextServerAddress string       // O próximo servidor no anel
	tokenAcquiredChan <-chan bool  // Canal para receber a notificação do token
	currentToken      *token.Token // Token atual com o stack de cartas
}

// NewService cria uma nova instância do serviço de matchmaking.
func NewService(sm *state.StateManager, broker *pubsub.Broker, tokenChan <-chan bool, selfAddr string, allAddrs []string, nextAddr string, initialToken *token.Token) *MatchmakingService {
	return &MatchmakingService{
		stateManager:      sm,
		broker:            broker,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		serverAddress:     selfAddr,
		allServers:        allAddrs,
		nextServerAddress: nextAddr,
		tokenAcquiredChan: tokenChan,
		currentToken:      initialToken,
	}
}

// Run inicia o loop principal do serviço de matchmaking, que aguarda pelo token para agir.
func (s *MatchmakingService) Run() {
	log.Println("[MATCHMAKING] Serviço iniciado. A aguardar pelo token...")
	for {
		<-s.tokenAcquiredChan
		if s.currentToken != nil {
			log.Printf("[MATCHMAKING] Token recebido com %d cartas no pool. A verificar a fila...", s.currentToken.GetPoolSize())
		} else {
			log.Println("[MATCHMAKING] Token recebido (vazio). A verificar a fila...")
		}
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

		// Tenta criar a partida com cartas do token
		match, err := s.createMatchWithTokenCards(p1, p2, false, "", "")
		if err != nil {
			log.Printf("[MATCHMAKING] Erro ao criar partida: %v", err)
			return
		}

		s.stateManager.RemovePlayersFromQueue(p1, p2)
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

// createMatchWithTokenCards cria uma partida usando cartas do token
func (s *MatchmakingService) createMatchWithTokenCards(p1, p2 *protocol.PlayerConn, isDistributed bool, guestServer string, matchID string) (*game.Match, error) {
	// Verifica se o token está disponível
	if s.currentToken == nil {
		return nil, fmt.Errorf("token não disponível")
	}

	// Calcula quantas cartas são necessárias (2 jogadores x 5 cartas iniciais)
	const cardsPerPlayer = 5
	totalCardsNeeded := 2 * cardsPerPlayer

	// Tenta pegar as cartas do token
	cards, err := s.currentToken.DrawCards(totalCardsNeeded)
	if err != nil {
		return nil, fmt.Errorf("erro ao pegar cartas do token: %w", err)
	}

	log.Printf("[MATCHMAKING] Pegou %d cartas do token para a partida", len(cards))

	// Separa as cartas para cada jogador
	p1Cards := cards[:cardsPerPlayer]
	p2Cards := cards[cardsPerPlayer:]

	// Cria a partida
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

		// Cria PlayerConn temporária para o oponente remoto
		opponentConn := &protocol.PlayerConn{ID: opponentInfo.PlayerID}

		// Cria a partida usando cartas do token
		match, err := s.createMatchWithTokenCards(localPlayer, opponentConn, true, serverAddr, matchID)
		if err != nil {
			log.Printf("[MATCHMAKING] Erro ao criar partida com cartas do token: %v", err)
			return false
		}

		// Envia as cartas do jogador remoto para o servidor dele
		p2Cards := match.Hands[1] // Mão do jogador 2 (oponente)
		requestBody, _ := json.Marshal(map[string]interface{}{
			"matchId":       matchID,
			"hostPlayerId":  localPlayer.ID,
			"guestPlayerId": opponentInfo.PlayerID,
			"guestCards":    p2Cards,
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

		log.Printf("[MATCHMAKING] Partida distribuída %s criada com sucesso!", matchID)
		s.notifyPlayersOfMatch(match, localPlayer, match.P2)
		go s.monitorMatch(match)
		return true
	}
	return false
}

// passTokenToNextServer envia o token com as cartas para o próximo servidor.
func (s *MatchmakingService) passTokenToNextServer() {
	if s.currentToken == nil {
		log.Printf("[MATCHMAKING] AVISO: Tentando passar token nulo!")
		return
	}

	// Atualiza o endereço do servidor no token
	s.currentToken.UpdateServerAddr(s.nextServerAddress)

	// Serializa o token para JSON
	tokenJSON, err := s.currentToken.ToJSON()
	if err != nil {
		log.Printf("[MATCHMAKING] ERRO ao serializar token: %v", err)
		return
	}

	log.Printf("[MATCHMAKING] A passar o token (%d cartas) para %s...", s.currentToken.GetPoolSize(), s.nextServerAddress)

	resp, err := s.httpClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(tokenJSON))
	if err != nil {
		log.Printf("[MATCHMAKING] ERRO ao passar o token para %s: %v.", s.nextServerAddress, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		log.Printf("[MATCHMAKING] ERRO: servidor retornou status %d: %s", resp.StatusCode, string(body))
		return
	}

	log.Printf("[MATCHMAKING] Token passado com sucesso.")

	// Limpa o token local após passar
	s.currentToken = nil
}

// SetToken define o token recebido de outro servidor
func (s *MatchmakingService) SetToken(t *token.Token) {
	s.currentToken = t
	log.Printf("[MATCHMAKING] Token recebido e definido com %d cartas no pool", t.GetPoolSize())
}

// GetToken retorna o token atual (se este servidor possui o token)
func (s *MatchmakingService) GetToken() *token.Token {
	return s.currentToken
}

// RequestCardsFromToken retira cartas do token se este servidor o possuir
func (s *MatchmakingService) RequestCardsFromToken(count int) ([]string, error) {
	if s.currentToken == nil {
		return nil, fmt.Errorf("este servidor não possui o token")
	}

	cards, err := s.currentToken.DrawCards(count)
	if err != nil {
		return nil, fmt.Errorf("erro ao retirar cartas do token: %w", err)
	}

	log.Printf("[MATCHMAKING] Fornecidas %d cartas do token para reposição de mão", len(cards))
	return cards, nil
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
