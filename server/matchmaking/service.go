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
	serverAddress     string                   // Endereço deste servidor (ex: http://server-1:8000)
	allServers        []string                 // Lista de todos os servidores no cluster
	nextServerAddress string                   // O próximo servidor no anel
	tokenChan         chan protocol.TokenState // Canal para receber e (no líder) reinjetar o token
	isLeader          bool                     // Flag para indicar se este nó é o líder
	lastKnownStock    int                      // Último estoque conhecido (para regeneração inteligente)
	totalPacksOpened  int                      // Total de pacotes abertos desde o início
}

// NewService cria uma nova instância do serviço de matchmaking.
func NewService(sm *state.StateManager, broker *pubsub.Broker, tokenChan chan protocol.TokenState, selfAddr string, allAddrs []string, nextAddr string) *MatchmakingService {
	isLeader := selfAddr == allAddrs[0]
	log.Printf("[MATCHMAKING] Configurado como líder: %t", isLeader)

	return &MatchmakingService{
		stateManager:      sm,
		broker:            broker,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		serverAddress:     selfAddr,
		allServers:        allAddrs,
		nextServerAddress: nextAddr,
		tokenChan:         tokenChan,
		isLeader:          isLeader,
		lastKnownStock:    1000, // Estoque inicial padrão
		totalPacksOpened:  0,
	}
}

// Run inicia o loop principal do serviço de matchmaking, que aguarda pelo token para agir.
func (s *MatchmakingService) Run() {
	if !s.isLeader {
		s.runFollower()
		return
	}
	s.runLeader()
}

// runFollower é o loop para servidores que não são líderes. Apenas aguardam e processam o token.
func (s *MatchmakingService) runFollower() {
	log.Println("[MATCHMAKING] Serviço (Seguidor) iniciado. A aguardar pelo token...")
	for tokenState := range s.tokenChan {
		log.Printf("[MATCHMAKING] Token recebido. Estado: %+v. A verificar a fila...", tokenState)
		updatedTokenState := s.processPackRequests(tokenState)
		s.processMatchmakingQueue()
		time.Sleep(2 * time.Second) // Simula trabalho
		s.passTokenToNextServer(updatedTokenState)
	}
	log.Println("[MATCHMAKING] Canal do token fechado. Encerrando (Seguidor).")
}

// runLeader é o loop para o servidor líder, que inclui o watchdog para regenerar o token.
func (s *MatchmakingService) runLeader() {
	log.Println("[MATCHMAKING] Serviço (Líder) iniciado com watchdog de token.")
	// Timeout generoso: 4 segundos por servidor no anel.
	watchdogTimeout := time.Duration(len(s.allServers)*4) * time.Second
	timer := time.NewTimer(watchdogTimeout)

	for {
		select {
		case tokenState, ok := <-s.tokenChan:
			if !ok {
				log.Println("[MATCHMAKING] [LEADER] Canal do token fechado. Encerrando.")
				return
			}

			log.Printf("[MATCHMAKING] [LEADER] Token recebido. Watchdog resetado.")
			if !timer.Stop() {
				// Esvazia o canal do timer se Stop() retornar false, o que é necessário
				// se o timer já tiver disparado e o evento estiver pendente.
				<-timer.C
			}
			timer.Reset(watchdogTimeout)

			// Processa e passa o token
			updatedTokenState := s.processPackRequests(tokenState)
			s.processMatchmakingQueue()
			time.Sleep(2 * time.Second) // Simula trabalho
			s.passTokenToNextServer(updatedTokenState)

		case <-timer.C:
			log.Println("[MATCHMAKING] [LEADER] Watchdog timeout: Token perdido! A regenerar...")
			// Regenera o token com o último estado conhecido.
			// Usa o lastKnownStock como base para a regeneração.
			recoveredStock := s.lastKnownStock
			log.Printf("[MATCHMAKING] [LEADER] A regenerar token com último estoque conhecido: %d pacotes", recoveredStock)
			log.Printf("[MATCHMAKING] [LEADER] Total de pacotes abertos desde o início: %d", s.totalPacksOpened)
			log.Printf("[MATCHMAKING] [LEADER] ⚠️  AVISO: Regeneração de token pode causar inconsistência se houver pacotes em processamento.")
			s.tokenChan <- protocol.TokenState{PackStock: recoveredStock}
			// O timer será resetado na case de recepção do token.
		}
	}
}

// processPackRequests processa a fila de pedidos de pacotes.
// Retorna o estado do token atualizado.
func (s *MatchmakingService) processPackRequests(currentState protocol.TokenState) protocol.TokenState {
	requests := s.stateManager.DequeueAllPackRequests()
	if len(requests) == 0 {
		// Atualiza o último estoque conhecido mesmo sem pedidos
		s.lastKnownStock = currentState.PackStock
		return currentState // Sem pedidos, estado não muda.
	}

	log.Printf("[MATCHMAKING] A processar %d pedidos de pacotes. Estoque atual: %d", len(requests), currentState.PackStock)

	packsBefore := currentState.PackStock
	for _, req := range requests {
		if currentState.PackStock > 0 {
			// Há estoque, processa o pedido.
			currentState.PackStock--
			s.totalPacksOpened++ // Incrementa contador de auditoria
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

	// Atualiza o último estoque conhecido e registra auditoria
	s.lastKnownStock = currentState.PackStock
	packsOpened := packsBefore - currentState.PackStock
	if packsOpened > 0 {
		log.Printf("[MATCHMAKING] 📦 Auditoria: %d pacotes abertos nesta rodada. Total acumulado: %d. Estoque atual: %d",
			packsOpened, s.totalPacksOpened, currentState.PackStock)
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

		// Primeira chamada S2S: encontrar um oponente
		resp, err := s.httpClient.Get(serverAddr + "/api/find-opponent")
		if err != nil {
			log.Printf("[MATCHMAKING] Erro ao contactar %s para encontrar oponente: %v", serverAddr, err)
			continue // Tenta o próximo servidor
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue // Nenhum jogador encontrado, tenta o próximo servidor
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

		// Segunda chamada S2S: solicitar a partida
		resp, err = s.httpClient.Post(serverAddr+"/api/request-match", "application/json", bytes.NewBuffer(requestBody))
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				_ = resp.Body.Close()
			}
			log.Printf("[MATCHMAKING] Falha S2S ao solicitar partida com %s. Notificando jogador.", serverAddr)

			// Remove o jogador da fila e notifica-o do erro.
			s.stateManager.RemovePlayersFromQueue(localPlayer)
			s.broker.Publish("player."+localPlayer.ID, protocol.ServerMsg{
				T:    protocol.ERROR,
				Code: "MATCH_SETUP_FAILED",
				Msg:  "Não foi possível criar a partida com o oponente. Por favor, tente procurar novamente.",
			})
			return true // Retorna true para parar de procurar outros oponentes.
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
