package matchmaking

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"pingpong/server/game"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"pingpong/server/state"
	"pingpong/server/token"
	"sync"
	"time"
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
	myIndex           int                      // Nosso índice na lista allServers
	isLeader          bool                     //  Flag para indicar se este nó é o líder
	leaderMu          sync.Mutex               //  Mutex para proteger a flag isLeader
	watchdogTimer     *time.Timer              //  Timer do líder
	electionTimer     *time.Timer              //  Timer do seguidor
	lastKnownStock    int                      // Último estoque conhecido (para regeneração inteligente)
	totalPacksOpened  int                      // Total de pacotes abertos desde o início
	currentToken      *token.Token             // Token com pool de cartas
}

// NewService cria uma nova instância do serviço de matchmaking.
func NewService(sm *state.StateManager, broker *pubsub.Broker, tokenChan chan protocol.TokenState, selfAddr string, allAddrs []string, nextAddr string) *MatchmakingService {
	// Encontra o nosso próprio índice.
	myIndex := -1
	for i, addr := range allAddrs {
		if addr == selfAddr {
			myIndex = i
			break
		}
	}
	if myIndex == -1 {
		log.Fatalf("[MATCHMAKING] Não foi possível encontrar o próprio endereço %s na lista de servidores", selfAddr)
	}

	isLeader := (myIndex == 0) // Nó 0 é o líder inicial
	log.Printf("[MATCHMAKING] Configurado como líder: %t (Índice: %d)", isLeader, myIndex)

	s := &MatchmakingService{
		stateManager:      sm,
		broker:            broker,
		httpClient:        &http.Client{Timeout: 2 * time.Second}, // Timeout curto para pings/health checks
		serverAddress:     selfAddr,
		allServers:        allAddrs,
		nextServerAddress: nextAddr,
		tokenChan:         tokenChan,
		myIndex:           myIndex,
		isLeader:          isLeader,
		lastKnownStock:    1000,
		totalPacksOpened:  0,
	}

	// Calcula durações dos timers
	watchdogTimeout := s.getWatchdogTimeout()
	electionTimeout := s.getElectionTimeout()

	// Inicializa os timers
	s.watchdogTimer = time.NewTimer(watchdogTimeout)
	s.electionTimer = time.NewTimer(electionTimeout)

	// Para o timer que não está em uso
	if !isLeader {
		s.watchdogTimer.Stop()
	} else {
		s.electionTimer.Stop()
	}

	return s
}

// getWatchdogTimeout calcula a duração do watchdog do líder.
func (s *MatchmakingService) getWatchdogTimeout() time.Duration {
	// O timeout do líder deve ser dinâmico e razoavelmente curto
	return time.Duration(len(s.allServers)*4) * time.Second
}

// getElectionTimeout calcula a duração do timer de eleição do seguidor.
func (s *MatchmakingService) getElectionTimeout() time.Duration {
	// Deve ser significativamente mais longo que o watchdog para dar
	// tempo ao líder de regenerar o token antes que os seguidores
	// pensem que ele morreu.
	return s.getWatchdogTimeout() * 3
}

// resetTimers reinicia o timer apropriado com base no estado de líder.
func (s *MatchmakingService) resetTimers() {
	s.leaderMu.Lock()
	defer s.leaderMu.Unlock()

	// Garante que ambos os timers estejam parados antes de reiniciar o correto
	if !s.watchdogTimer.Stop() {
		select {
		case <-s.watchdogTimer.C: // Esvazia o canal se o timer disparou
		default:
		}
	}
	if !s.electionTimer.Stop() {
		select {
		case <-s.electionTimer.C: // Esvazia o canal se o timer disparou
		default:
		}
	}

	// Reinicia o timer correto
	if s.isLeader {
		s.watchdogTimer.Reset(s.getWatchdogTimeout())
	} else {
		s.electionTimer.Reset(s.getElectionTimeout())
	}
}

// promoteToLeader promove este nó a líder.
func (s *MatchmakingService) promoteToLeader() {
	s.leaderMu.Lock()
	if s.isLeader {
		s.leaderMu.Unlock()
		return // Já somos o líder
	}

	log.Println("[MATCHMAKING] [ELECTION] A promover este nó a LÍDER.")
	s.isLeader = true
	s.leaderMu.Unlock()

	// Transição de timers: para o de eleição e inicia o de watchdog
	s.resetTimers()

	// Como novo líder, devemos regenerar e injetar o token imediatamente
	log.Println("[MATCHMAKING] [NEW LEADER] A regenerar e injetar o token...")
	tokenState := protocol.TokenState{
		PackStock:            s.lastKnownStock,
		GeneratedByLeaderIdx: s.myIndex,
	}

	// A injeção é feita enviando para o nosso próprio canal
	go func() {
		s.tokenChan <- tokenState
	}()
}

// Run inicia o loop principal do serviço de matchmaking (agora unificado).
func (s *MatchmakingService) Run() {
	// Inicia o timer correto na inicialização (feito em NewService, mas garantimos aqui)
	s.resetTimers()

	for {
		select {
		// --- Caso 1: Token é recebido (Cenário Normal) ---
		case tokenState, ok := <-s.tokenChan:
			if !ok {
				log.Println("[MATCHMAKING] Canal do token fechado. Encerrando.")
				return
			}

			log.Println("[MATCHMAKING] Token recebido. A processar...")

			s.leaderMu.Lock()
			if s.isLeader && tokenState.GeneratedByLeaderIdx < s.myIndex {
				log.Printf("[MATCHMAKING] Recebido token do líder %d (prioridade >). A demitir-me para seguidor.", tokenState.GeneratedByLeaderIdx)
				s.isLeader = false
			}
			s.leaderMu.Unlock()

			// O anel está vivo. Reinicia o timer apropriado.
			s.resetTimers()

			// Processa e passa o token (lógica original)
			s.ensureTokenInitialized()
			updatedTokenState := s.processPackRequests(tokenState)
			s.processMatchmakingQueue()
			time.Sleep(2 * time.Second) // Simula trabalho

			// Adiciona o nosso índice de líder ao passar, se formos o líder
			/* // DESCOMENTE QUANDO protocol.TokenState FOR ATUALIZADO
			s.leaderMu.Lock()
			if s.isLeader {
				updatedTokenState.GeneratedByLeaderIdx = s.myIndex
			}
			s.leaderMu.Unlock()
			*/

			s.passTokenToNextServer(updatedTokenState)

		// --- Caso 2: Watchdog do LÍDER dispara (Token perdido) ---
		case <-s.watchdogTimer.C:
			s.leaderMu.Lock()
			if !s.isLeader {
				// Timer espúrio. Fomos demitidos enquanto o timer corria.
				s.leaderMu.Unlock()
				log.Println("[MATCHMAKING] Watchdog espúrio. Ignorando.")
				s.resetTimers() // Apenas reinicia (vai iniciar o timer de eleição)
				continue
			}
			s.leaderMu.Unlock()

			// --- Lógica de Falha do Líder (código original de runLeader) ---
			log.Printf("[MATCHMAKING] [LEADER] Watchdog timeout! O token não retornou.")
			log.Printf("[MATCHMAKING] [LEADER] A verificar ativamente o status do próximo nó: %s", s.nextServerAddress)

			// 1. Tenta "pingar" o próximo servidor
			// Usamos /api/find-opponent pois sabemos que ele existe
			resp, err := s.httpClient.Get(s.nextServerAddress + "/api/find-opponent")

			if err != nil {
				// CASO 1: SERVIDOR CAIU
				log.Printf("[MATCHMAKING] [LEADER] VERIFICAÇÃO FALHOU: O nó %s está inacessível (%v).", s.nextServerAddress, err)
				log.Println("[MATCHMAKING] [LEADER] A reconfigurar anel para pular o nó falho.")

				// Reconfigura o anel para pular o nó N+1 e ir para o N+2
				myIndexInList := -1
				for i, addr := range s.allServers {
					if addr == s.serverAddress {
						myIndexInList = i
						break
					}
				}

				if myIndexInList != -1 {
					newNextIndex := (myIndexInList + 2) % len(s.allServers) // Lógica de pular (N+2)
					originalNext := s.nextServerAddress
					s.nextServerAddress = s.allServers[newNextIndex]
					log.Printf("[MATCHMAKING] [LEADER] Topologia reconfigurada. Próximo nó é: %s (pulado: %s)", s.nextServerAddress, originalNext)
				} else {
					log.Printf("[MATCHMAKING] [LEADER] ERRO CRÍTICO: Não foi possível encontrar o próprio endereço.")
				}

			} else {
				// CASO 2: TOKEN SE PERDEU
				_ = resp.Body.Close()
				log.Printf("[MATCHMAKING] [LEADER] VERIFICAÇÃO OK: O nó %s está VIVO. Assumindo TOKEN PERDIDO.", s.nextServerAddress)
			}

			// 2. Regenera, processa e passa o token.
			log.Println("[MATCHMAKING] [LEADER] A regenerar e processar token...")
			tokenState := protocol.TokenState{
				PackStock:            s.lastKnownStock,
				GeneratedByLeaderIdx: s.myIndex,
			}
			s.ensureTokenInitialized()
			updatedTokenState := s.processPackRequests(tokenState)
			s.processMatchmakingQueue()
			time.Sleep(2 * time.Second) // Simula trabalho

			log.Println("[MATCHMAKING] [LEADER] A repassar token...")
			s.passTokenToNextServer(updatedTokenState)

			// 3. Reseta o watchdog.
			log.Println("[MATCHMAKING] [LEADER] Watchdog resetado após regeneração.")
			s.watchdogTimer.Reset(s.getWatchdogTimeout())
			go s.returnTotheInitialNodes()

		// --- Caso 3: Timer de Eleição do SEGUIDOR dispara (Líder morreu) ---
		case <-s.electionTimer.C:
			s.leaderMu.Lock()
			if s.isLeader {
				// Timer espúrio. Fomos promovidos enquanto o timer corria.
				s.leaderMu.Unlock()
				log.Println("[MATCHMAKING] Timer de eleição espúrio. Ignorando.")
				s.resetTimers() // Apenas reinicia (vai iniciar o watchdog)
				continue
			}
			s.leaderMu.Unlock()

			log.Println("[MATCHMAKING] [FOLLOWER] Timer de eleição disparou. Líder presumivelmente morto. A iniciar eleição...")

			// Algoritmo "Bully" Simplificado:
			// Verifica se algum nó com índice MENOR (prioridade maior) está vivo.
			highestPriorityNodeAlive := false
			for i := 0; i < s.myIndex; i++ {
				addr := s.allServers[i]
				log.Printf("[MATCHMAKING] [ELECTION] A verificar nó de prioridade mais alta: %s", addr)

				pingClient := http.Client{Timeout: 1 * time.Second}
				// Usamos /api/find-opponent como "health check"
				if resp, err := pingClient.Get(addr + "/api/find-opponent"); err == nil {
					// Nó de prioridade mais alta está VIVO.
					_ = resp.Body.Close()
					log.Printf("[MATCHMAKING] [ELECTION] Nó %s está vivo. Não me tornarei líder.", addr)
					highestPriorityNodeAlive = true
					break // Encerra a verificação
				}
			}

			if !highestPriorityNodeAlive {
				// Ninguém com prioridade mais alta (índice menor) está vivo.
				// Nós tornamo-nos o novo líder.
				s.promoteToLeader()
			} else {
				// Alguém com prioridade mais alta está vivo.
				// Apenas reiniciamos o nosso timer de eleição e esperamos.
				log.Println("[MATCHMAKING] [ELECTION] Outro nó deve tornar-se líder. A aguardar.")
				s.electionTimer.Reset(s.getElectionTimeout())
			}
		}
	}
}

// runFollower E runLeader SÃO AGORA OBSOLETOS.
// A lógica está toda unificada em Run().

func (s *MatchmakingService) returnTotheInitialNodes() {
	time.Sleep(20 * time.Second)
	pingClient := http.Client{Timeout: 2 * time.Second}
	_, err := pingClient.Get(s.nextServerAddress + "/api/find-opponent")
	if err == nil {
		log.Println("[MATCHMAKING] [LEADER] Voltando para o nó inicial, servidor voltou!.")
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

	var serversToSearch []string
	for _, addr := range s.allServers {
		if addr != s.serverAddress {
			serversToSearch = append(serversToSearch, addr)
		}
	}

	for _, serverAddr := range serversToSearch {
		// Primeira chamada S2S: encontrar um oponente
		// (usamos o httpClient com timeout curto)
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

		// Segunda chamada S2S: solicitar a partida (usamos um cliente com timeout maior)
		postClient := &http.Client{Timeout: 5 * time.Second}
		resp, err = postClient.Post(serverAddr+"/api/request-match", "application/json", bytes.NewBuffer(requestBody))
		if err != nil || (resp != nil && resp.StatusCode != http.StatusOK) {
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

			// Usar um cliente com timeout maior para a passagem do token
			postClient := &http.Client{Timeout: 5 * time.Second}
			if resp, err2 := postClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(tokenJSON)); err2 == nil {
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

	postClient := &http.Client{Timeout: 5 * time.Second}
	_, err = postClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(requestBody))
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
