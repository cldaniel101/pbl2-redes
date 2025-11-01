package matchmaking

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
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
	serverAddress     string            // Endereço deste servidor (ex: http://server-1:8000)
	allServers        []string          // Lista de todos os servidores no cluster
	nextServerAddress string            // O próximo servidor no anel
	tokenChan         chan *token.Token // Canal para receber e (no líder) reinjetar o token
	myIndex           int               // Nosso índice na lista allServers
	isLeader          bool              //  Flag para indicar se este nó é o líder
	leaderMu          sync.Mutex        //  Mutex para proteger a flag isLeader
	watchdogTimer     *time.Timer       //  Timer do líder
	electionTimer     *time.Timer       //  Timer do seguidor
	lastKnownStock    int               // Último estoque conhecido (para regeneração inteligente)
	totalPacksOpened  int               // Total de pacotes abertos desde o início
	currentToken      *token.Token      // Token com pool de cartas
}

// NewService cria uma nova instância do serviço de matchmaking.
func NewService(sm *state.StateManager, broker *pubsub.Broker, tokenChan chan *token.Token, selfAddr string, allAddrs []string, nextAddr string) *MatchmakingService {
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
	return time.Duration(len(s.allServers)*16) * time.Second
}

// getElectionTimeout calcula a duração do timer de eleição do seguidor.
func (s *MatchmakingService) getElectionTimeout() time.Duration {
	// Deve ser significativamente mais longo que o watchdog para dar
	// tempo ao líder de regenerar o token antes que os seguidores
	// pensem que ele morreu.
	return s.getWatchdogTimeout() * 2
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
func (s *MatchmakingService) processPackRequests() {
	requests := s.stateManager.DequeueAllPackRequests()
	if len(requests) == 0 {
		return // Sem pedidos, sem trabalho.
	}

	// Se não tivermos o token de cartas, não podemos processar.
	// Os pedidos ficarão na fila para a próxima volta.
	if s.currentToken == nil {
		log.Printf("[MATCHMAKING] %d pedidos de pacote em espera, mas o token de cartas não está presente.", len(requests))
		return
	}

	log.Printf("[MATCHMAKING] A processar %d pedidos de pacotes. Pool de cartas atual: %d", len(requests), s.currentToken.GetPoolSize())

	// O número de cartas por pacote (deveria vir de uma config,
	// mas 3 é o valor no state/manager.go)
	const cardsPerPack = 3

	for _, req := range requests {
		// Tenta retirar 3 cartas do pool global
		cards, err := s.currentToken.DrawCards(cardsPerPack)

		if err != nil {
			// Erro (provavelmente pool insuficiente)
			req.ReplyChan <- state.PackResult{Err: errors.New("estoque de cartas esgotado")}
			log.Printf("[MATCHMAKING] Pedido de pacote de %s rejeitado: %v", req.PlayerID, err)
		} else {
			// Sucesso
			req.ReplyChan <- state.PackResult{Cards: cards}
			log.Printf("[MATCHMAKING] Pacote aberto para %s. Cartas: %v. Pool restante: %d", req.PlayerID, cards, s.currentToken.GetPoolSize())
		}
	}
	// s.currentToken foi modificado diretamente (DrawCards removeu cartas)
}

func (s *MatchmakingService) promoteToLeader() {
	s.leaderMu.Lock()
	s.isLeader = true
	s.leaderMu.Unlock()

	s.resetTimers()

	log.Println("[MATCHMAKING] [NEW LEADER] A regenerar e injetar o token...")
	s.regenerateAndSetToken() // Gera o token de cartas (s.currentToken)

	// Injeta o token que acabámos de criar no *nosso próprio* canal
	// para iniciar o ciclo.
	go func(tokenToInject *token.Token) {
		s.tokenChan <- tokenToInject
	}(s.currentToken)
}

// Run inicia o loop principal do serviço de matchmaking (agora unificado).
func (s *MatchmakingService) Run() {
	// Inicia o timer correto na inicialização (feito em NewService, mas garantimos aqui)
	s.resetTimers()

	for {
		select {
		// --- Caso 1: Token é recebido (Cenário Normal) ---
		case receivedToken, ok := <-s.tokenChan:
			if !ok {
				log.Println("[MATCHMAKING] Canal do token fechado. Encerrando.")
				return
			}

			log.Println("[MATCHMAKING] Token de cartas recebido. A processar...")
			s.currentToken = receivedToken // Armazena o token recebido

			// O anel está vivo. Reinicia o timer apropriado.
			s.resetTimers()

			// Processa as filas usando s.currentToken
			s.processPackRequests()
			s.processMatchmakingQueue()
			time.Sleep(2 * time.Second) // Simula trabalho

			// Passa o token (que está em s.currentToken)
			s.passTokenToNextServer()

		// --- Caso 2: Watchdog do LÍDER dispara (Token perdido) ---
		case <-s.watchdogTimer.C:
			s.leaderMu.Lock()
			if !s.isLeader {
				// Timer espúrio. Fomos rebaixados para seguidor,
				// mas o timer antigo disparou antes de ser parado.
				s.leaderMu.Unlock()

				log.Println("[MATCHMAKING] Watchdog espúrio (nó não é líder). Ignorando.")
				s.resetTimers() // Garante que o timer de eleição seja iniciado
				continue        // PULA O RESTO DA LÓGICA
			}
			s.leaderMu.Unlock() // Se chegamos aqui, somos o líder

			// --- Lógica de Falha do Líder (agora segura) ---
			log.Printf("[MATCHMAKING] [LEADER] Watchdog timeout! O token não retornou.")
			log.Printf("[MATCHMAKING] [LEADER] A verificar ativamente o status do próximo nó: %s", s.nextServerAddress)

			// Esta verificação de nó é opcional. O líder pode simplesmente
			// assumir que o token se perdeu e regenerar.
			// A lógica de falha de nó é tratada em passTokenToNextServer.
			resp, err := s.httpClient.Get(s.nextServerAddress + "/api/health-check")
			if err == nil {
				_ = resp.Body.Close()
				log.Printf("[MATCHMAKING] [LEADER] VERIFICAÇÃO OK: O nó %s está VIVO. Assumindo TOKEN PERDIDO.", s.nextServerAddress)
			} else {
				log.Printf("[MATCHMAKING] [LEADER] VERIFICAÇÃO FALHOU: O nó %s pode estar MORTO. A assumir TOKEN PERDIDO.", s.nextServerAddress)
				// A lógica em passTokenToNextServer tratará de reconfigurar o anel na próxima passagem.
			}

			log.Println("[MATCHMAKING] [LEADER] A regenerar e processar token...")
			s.regenerateAndSetToken() // Define s.currentToken

			// Processa as filas locais com o novo token
			s.processPackRequests()
			s.processMatchmakingQueue()
			time.Sleep(2 * time.Second) // Simula trabalho

			log.Println("[MATCHMAKING] [LEADER] A repassar token...")
			s.passTokenToNextServer() // Passa o s.currentToken

			// Reinicia o timer do líder
			s.resetTimers()

		// --- Caso 3: Timer de Eleição do SEGUIDOR dispara (Líder morreu) ---
		case <-s.electionTimer.C:
			s.leaderMu.Lock()
			if s.isLeader {
				// Timer espúrio. Fomos promovidos enquanto o timer corria.
				s.leaderMu.Unlock()
				log.Println("[MATCHMAKING] Timer de eleição espúrio (nó já é líder). Ignorando.")
				s.resetTimers() // Garante que o watchdog seja iniciado
				continue
			}
			s.leaderMu.Unlock()

			log.Println("[MATCHMAKING] [FOLLOWER] Timer de eleição disparou. Líder presumivelmente morto. A iniciar eleição...")

			// Algoritmo "Bully" Simplificado:
			highestPriorityNodeAlive := false
			for i := 0; i < s.myIndex; i++ {
				addr := s.allServers[i]
				log.Printf("[MATCHMAKING] [ELECTION] A verificar nó de prioridade mais alta: %s", addr)

				pingClient := http.Client{Timeout: 1 * time.Second}
				// Usamos /api/find-opponent como "health check"
				if resp, err := pingClient.Get(addr + "/api/find-opponent"); err == nil {
					_ = resp.Body.Close()
					log.Printf("[MATCHMAKING] [ELECTION] Nó %s está vivo. Não me tornarei líder.", addr)
					highestPriorityNodeAlive = true
					break
				}
			}

			if !highestPriorityNodeAlive {
				// Ninguém com prioridade mais alta (índice menor) está vivo.
				// Nós tornamo-nos o novo líder.
				// promoteToLeader irá regenerar o token perdido.
				s.promoteToLeader()
			} else {
				// Alguém com prioridade mais alta está vivo.
				// Apenas reiniciamos o nosso timer de eleição e esperamos.
				log.Println("[MATCHMAKING] [ELECTION] Outro nó deve tornar-se líder. A aguardar.")
				s.resetTimers() // Reinicia o electionTimer
			}
		}
	}
}

func (s *MatchmakingService) monitorFailedNode(failedNodeAddress string) {
	log.Printf("[MATCHMAKING] [LEADER] Iniciando monitoramento em background do nó falho: %s", failedNodeAddress)

	pingClient := http.Client{Timeout: 2 * time.Second}

	// Ticker é melhor para loops, para não recriar a goroutine
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	// Este é o endereço que queremos restaurar
	originalNextAddress := s.allServers[(s.myIndex+1)%len(s.allServers)]

	// Verificação de sanidade
	if failedNodeAddress != originalNextAddress {
		log.Printf("[MATCHMAKING] [LEADER] Lógica de monitoramento inconsistente. Abortando. Nó falho: %s, Nó original esperado: %s", failedNodeAddress, originalNextAddress)
		return
	}

	// Loop infinito de verificação
	for range ticker.C {
		// Pinga o nó que FALHOU (não s.nextServerAddress)
		_, err := pingClient.Get(failedNodeAddress + "/api/health-check")

		if err == nil {
			// SUCESSO! O nó falho original voltou!
			log.Printf("[MATCHMAKING] [LEADER] Nó original %s voltou! A reconfigurar anel.", failedNodeAddress)

			// Reconfigura o 'nextServerAddress' para o nó que acabou de voltar
			// É seguro fazer isso, pois o líder é o único que muda s.nextServerAddress
			s.nextServerAddress = failedNodeAddress

			// A goroutine termina seu trabalho e sai do loop/função.
			return
		}

		// FALHA: O servidor ainda não voltou
		log.Printf("[MATCHMAKING] [LEADER] Monitoramento: Nó %s ainda offline.", failedNodeAddress)
	}
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
func (s *MatchmakingService) passTokenToNextServer() {
	if s.currentToken == nil {
		// Isto pode acontecer se formos um seguidor e o token
		// ainda não tiver chegado. Não é um erro.
		log.Printf("[MATCHMAKING] Sem token para passar. A aguardar a próxima volta.")
		return
	}

	// Atualiza o dono do token e serializa
	s.currentToken.UpdateServerAddr(s.nextServerAddress)
	tokenJSON, err := s.currentToken.ToJSON()
	if err != nil {
		log.Printf("[MATCHMAKING] ERRO ao serializar token de cartas: %v", err)
		return
	}

	log.Printf("[MATCHMAKING] A passar o token de cartas (%d no pool) para %s...", s.currentToken.GetPoolSize(), s.nextServerAddress)

	// Envia via HTTP
	postClient := &http.Client{Timeout: 5 * time.Second}
	if resp, err2 := postClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(tokenJSON)); err2 == nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		// Limpa o token local APÓS passar com sucesso
		s.currentToken = nil
		log.Printf("[MATCHMAKING] Token passado com sucesso.")
	} else {
		log.Printf("[MATCHMAKING] ERRO ao passar token de cartas: %v", err2)
		_, errHealth := http.Get(s.nextServerAddress + "/api/health-check")
		time.Sleep(10 * time.Second)
		if errHealth != nil {
			log.Printf("[MATCHMAKING] [LEADER] VERIFICAÇÃO FALHOU: O nó %s está inacessível (%v).", s.nextServerAddress, err2) // 'err' aqui pode ser 'err2' do post
			log.Println("[MATCHMAKING] [LEADER] A reconfigurar anel para pular o nó falho.")

			newNextIndex := (s.myIndex + 2) % len(s.allServers) // Lógica de pular (N+2)

			// 1. Salve o endereço do nó que acabou de falhar
			originalNextFailedNode := s.nextServerAddress

			// 2. Configure o novo 'nextServerAddress' para pular o nó
			s.nextServerAddress = s.allServers[newNextIndex]

			log.Printf("[MATCHMAKING] [LEADER] Topologia reconfigurada. Próximo nó é: %s (pulado: %s)", s.nextServerAddress, originalNextFailedNode)

			log.Println("[MATCHMAKING] [LEADER] A repassar token...")
			// Tenta repassar para o novo nó imediatamente
			postClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(tokenJSON))

			// 3. Reseta o watchdog.
			s.watchdogTimer.Reset(s.getWatchdogTimeout())

			// 4. Inicie o monitoramento passando o nó FALHO como argumento
			go s.monitorFailedNode(originalNextFailedNode)
		} else {
			postClient.Post(s.nextServerAddress+"/api/receive-token", "application/json", bytes.NewBuffer(tokenJSON))
		}
	}
}

func (s *MatchmakingService) regenerateAndSetToken() {
	log.Println("[MATCHMAKING] [REGENERATION] A regenerar token de cartas global...")

	// Esta lógica é movida de 'main.go'
	newToken := token.NewToken(s.serverAddress)
	cardsData, err := ioutil.ReadFile("cards.json") // (precisa importar "io/ioutil")
	if err != nil {
		log.Printf("[MATCHMAKING] [REGENERATION] ERRO FATAL: não foi possível ler cards.json para regenerar token: %v", err)
		// O servidor ficará num estado degradado sem token
		return
	}

	// Usando 10 cópias, como em main.go
	if err := newToken.LoadCardsFromJSON(cardsData, 10); err != nil {
		log.Printf("[MATCHMAKING] [REGENERATION] ERRO FATAL: não foi possível carregar cartas no token regenerado: %v", err)
		return
	}

	log.Printf("[MATCHMAKING] [REGENERATION] Novo token regenerado com %d cartas.", newToken.GetPoolSize())
	s.currentToken = newToken // Define o token regenerado
}

// SetToken permite ao servidor de API injetar o token de cartas recebido
func (s *MatchmakingService) SetToken(t *token.Token) {
	s.currentToken = t
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
