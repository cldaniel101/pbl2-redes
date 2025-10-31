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
    "pingpong/server/token"
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
    currentToken      *token.Token             // Token com pool de cartas
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
        s.ensureTokenInitialized()
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
	watchdogTimeout := time.Duration(len(s.allServers)*4) * time.Second
	timer := time.NewTimer(watchdogTimeout)

	for {
		select {
		case tokenState, ok := <-s.tokenChan:
			if !ok {
				log.Println("[MATCHMAKING] [LEADER] Canal do token fechado. Encerrando.")
				return
			}

			// Token recebido (do anel ou da injeção inicial)
			log.Printf("[MATCHMAKING] [LEADER] Token recebido. Watchdog resetado.")
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(watchdogTimeout)

			// Processa e passa o token
            s.ensureTokenInitialized()
			updatedTokenState := s.processPackRequests(tokenState)
			s.processMatchmakingQueue()
			time.Sleep(2 * time.Second) // Simula trabalho
			s.passTokenToNextServer(updatedTokenState)

		case <-timer.C: //
			// --- INÍCIO DA NOVA LÓGICA DE VERIFICAÇÃO ATIVA ---
			log.Printf("[MATCHMAKING] [LEADER] Watchdog timeout! O token não retornou.")
			log.Printf("[MATCHMAKING] [LEADER] A verificar ativamente o status do próximo nó: %s", s.nextServerAddress)

			// 1. Tenta "pingar" o próximo servidor para ver se ele está vivo.
			// Usamos um endpoint que sabemos que existe (/api/find-opponent) e um timeout curto.
			pingClient := http.Client{Timeout: 2 * time.Second}
			resp, err := pingClient.Get(s.nextServerAddress + "/api/find-opponent") //

			if err != nil {
				//
				// CASO 1: SERVIDOR CAIU (A requisição de ping falhou)
				//
				log.Printf("[MATCHMAKING] [LEADER] VERIFICAÇÃO FALHOU: O nó %s está inacessível (%v). Assumindo SERVIDOR CAIU.", s.nextServerAddress, err)
				log.Println("[MATCHMAKING] [LEADER] A reconfigurar anel para pular o nó falho.")

				// 1.A. Reconfigura o anel para pular o nó N+1 e ir para o N+2
				myIndex := -1
				for i, addr := range s.allServers {
					if addr == s.serverAddress {
						myIndex = i
						break
					}
				}

				if myIndex == -1 {
					log.Printf("[MATCHMAKING] [LEADER] ERRO CRÍTICO: Não foi possível encontrar o próprio endereço (%s) na lista de servidores.", s.serverAddress)
					timer.Reset(watchdogTimeout)
					continue
				}

				newNextIndex := (myIndex + 2) % len(s.allServers) // Lógica de pular (N+2)
				originalNext := s.nextServerAddress

				s.nextServerAddress = s.allServers[newNextIndex]

				log.Printf("[MATCHMAKING] [LEADER] Topologia reconfigurada.")
				log.Printf("[MATCHMAKING] [LEADER] Nó falho pulado: %s", originalNext)
				log.Printf("[MATCHMAKING] [LEADER] O próximo nó agora é: %s", s.nextServerAddress)

			} else {
				//
				// CASO 2: TOKEN SE PERDEU (A requisição de ping foi bem-sucedida)
				//
				_ = resp.Body.Close() // Importante fechar o corpo da resposta
				log.Printf("[MATCHMAKING] [LEADER] VERIFICAÇÃO OK: O nó %s está VIVO (respondeu). Assumindo TOKEN PERDIDO.", s.nextServerAddress)
				log.Println("[MATCHMAKING] [LEADER] O anel NÃO será reconfigurado.")
			}

			// 2. Regenera, processa e passa o token.
			// (Se o servidor caiu, 's.nextServerAddress' foi atualizado para o N+2).
			// (Se o token se perdeu, 's.nextServerAddress' continua o mesmo N+1).
			log.Println("[MATCHMAKING] [LEADER] A regenerar e processar token...")
			tokenState := protocol.TokenState{PackStock: s.lastKnownStock}
            s.ensureTokenInitialized()
			updatedTokenState := s.processPackRequests(tokenState)
			s.processMatchmakingQueue()
			time.Sleep(2 * time.Second) // Simula trabalho

			log.Println("[MATCHMAKING] [LEADER] A repassar token...")
			s.passTokenToNextServer(updatedTokenState)

			// 3. Reseta o watchdog.
			log.Println("[MATCHMAKING] [LEADER] Watchdog resetado após regeneração.")
			timer.Reset(watchdogTimeout)
			go s.returnTotheInitialNodes()
		}
	}
}

func (s *MatchmakingService) returnTotheInitialNodes() {
	time.Sleep(20 * time.Second)
	myIndex := -1
	for i, addr := range s.allServers {
		if addr == s.serverAddress {
			myIndex = i
			break
		}
	}
	if myIndex == -1 {
		log.Fatalf("[MAIN] Endereço do servidor %s não encontrado na lista ALL_SERVERS", s.serverAddress)
	}

	newNextIndex := (myIndex + 1) % len(s.allServers) 
	s.nextServerAddress = s.allServers[newNextIndex]

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
        match, err := s.createMatchWithTokenCards(p1, p2, false, "", "")
        if err != nil {
            log.Printf("[MATCHMAKING] Erro ao criar partida local com cartas do token: %v. A criar partida padrão.", err)
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

	// --- LÓGICA DE STRIKES REMOVIDA DAQUI ---
	// Agora simplesmente procura em todos os outros servidores.
	var serversToSearch []string
	for _, addr := range s.allServers { //
		if addr != s.serverAddress {
			serversToSearch = append(serversToSearch, addr)
		}
	}

	for _, serverAddr := range serversToSearch {
		// Primeira chamada S2S: encontrar um oponente
		resp, err := s.httpClient.Get(serverAddr + "/api/find-opponent") //
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
        // Prepara cartas do convidado a partir do token
        guestCards := []string{}
        if s.currentToken != nil {
            if cards, err := s.currentToken.DrawCards(game.HandSize); err == nil {
                guestCards = cards
            } else {
                log.Printf("[MATCHMAKING] Falha ao obter cartas do token para convidado: %v", err)
            }
        }

        requestBody, _ := json.Marshal(map[string]interface{}{
            "matchId":       matchID,
            "hostPlayerId":  localPlayer.ID,
            "guestPlayerId": opponentInfo.PlayerID,
            "guestCards":    guestCards,
        })

		// Segunda chamada S2S: solicitar a partida
		resp, err = s.httpClient.Post(serverAddr+"/api/request-match", "application/json", bytes.NewBuffer(requestBody)) //
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
        // Cria partida distribuída como host; tenta usar cartas do token para o host
        var match *game.Match
        if s.currentToken != nil {
            hostCards, derr := s.currentToken.DrawCards(game.HandSize)
            if derr == nil {
                match, err = s.stateManager.CreateDistributedMatchAsHostWithCards(matchID, localPlayer, opponentInfo.PlayerID, s.serverAddress, serverAddr, s.broker, hostCards, guestCards)
            } else {
                log.Printf("[MATCHMAKING] Falha ao obter cartas do token para host: %v", derr)
            }
        }
        if match == nil && err == nil {
            match, err = s.stateManager.CreateDistributedMatchAsHost(matchID, localPlayer, opponentInfo.PlayerID, s.serverAddress, serverAddr, s.broker)
        }
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
    // Envia o token de cartas junto (se existir) para “regeneração” do dono
    if s.currentToken != nil {
        s.currentToken.UpdateServerAddr(s.nextServerAddress)
        tokenJSON, err := s.currentToken.ToJSON()
        if err == nil {
            log.Printf("[MATCHMAKING] A passar o token de cartas (%d no pool) para %s...", s.currentToken.GetPoolSize(), s.nextServerAddress)
            if resp, err2 := s.httpClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(tokenJSON)); err2 == nil {
                if resp != nil {
                    _ = resp.Body.Close()
                }
                // Limpa token local após passar
                s.currentToken = nil
                return
            } else {
                log.Printf("[MATCHMAKING] ERRO ao passar token de cartas: %v", err2)
            }
        } else {
            log.Printf("[MATCHMAKING] ERRO ao serializar token de cartas: %v", err)
        }
    }

    // Fallback: envia apenas o estado de pacotes
    log.Printf("[MATCHMAKING] A passar o token para %s com estado: %+v...", s.nextServerAddress, currentState)
    requestBody, err := json.Marshal(currentState)
    if err != nil {
        log.Printf("[MATCHMAKING] ERRO ao serializar o estado do token: %v", err)
        return
    }
    _, err = s.httpClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(requestBody))
    if err != nil {
        log.Printf("[MATCHMAKING] ERRO ao passar o token para %s: %v.", s.nextServerAddress, err)
    } else {
        log.Printf("[MATCHMAKING] Token passado com sucesso.")
    }
}

// SetToken permite ao servidor de API injetar o token de cartas recebido
func (s *MatchmakingService) SetToken(t *token.Token) {
    s.currentToken = t
}

// ensureTokenInitialized cria e carrega o token a partir do CardDB caso ainda não exista
func (s *MatchmakingService) ensureTokenInitialized() {
    if s.currentToken != nil {
        return
    }
    s.currentToken = token.NewToken(s.serverAddress)
    all := s.stateManager.CardDB.GetAllCards()
    type cardInfo struct {
        ID      string `json:"id"`
        Name    string `json:"name"`
        Element string `json:"element"`
        ATK     int    `json:"atk"`
        DEF     int    `json:"def"`
    }
    buf := make([]cardInfo, 0, len(all))
    for _, c := range all {
        buf = append(buf, cardInfo{ID: c.ID, Name: c.Name, Element: string(c.Element), ATK: c.ATK, DEF: c.DEF})
    }
    raw, _ := json.Marshal(buf)
    _ = s.currentToken.LoadCardsFromJSON(raw, 10)
}

// createMatchWithTokenCards cria uma partida usando cartas do token
func (s *MatchmakingService) createMatchWithTokenCards(p1, p2 *protocol.PlayerConn, isDistributed bool, guestServer string, matchID string) (*game.Match, error) {
    if s.currentToken == nil {
        return nil, fmt.Errorf("token não disponível")
    }
    totalCardsNeeded := 2 * game.HandSize
    cards, err := s.currentToken.DrawCards(totalCardsNeeded)
    if err != nil {
        return nil, fmt.Errorf("erro ao pegar cartas do token: %w", err)
    }
    log.Printf("[MATCHMAKING] Pegou %d cartas do token para a partida", len(cards))
    p1Cards := cards[:game.HandSize]
    p2Cards := cards[game.HandSize:]
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
