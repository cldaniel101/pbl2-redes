package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"pingpong/server/game"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"strings"
	"sync"
	"time"
)

// Variáveis globais para a topologia do anel
var (
	allServers        []string
	nextServerAddress string
)

// GameServer modificado para a lógica de token ring
type GameServer struct {
	// Atributos de estado local e comunicação com clientes
	cardDB           *game.CardDB
	packSystem       *game.PackSystem
	playersOnline    map[string]*protocol.PlayerConn
	matchmakingQueue []*protocol.PlayerConn
	activeMatches    map[string]*game.Match
	mu               sync.RWMutex
	httpClient       *http.Client
	serverAddress    string

	// Novos atributos para a gestão do Token Ring
	tokenMutex        sync.Mutex
	hasToken          bool
	tokenAcquiredChan chan bool // Canal para sinalizar que o token foi recebido
	broker            *pubsub.Broker
}

// NewGameServer cria um novo servidor de jogo.
func NewGameServer(serverAddress string) *GameServer {
	cardDB := game.NewCardDB()
	if err := cardDB.LoadFromFile("cards.json"); err != nil {
		log.Fatalf("[SERVER] Erro ao carregar cartas: %v", err)
	}

	packConfig := game.PackConfig{CardsPerPack: 3, Stock: 100}
	packSystem := game.NewPackSystem(packConfig, cardDB)

	return &GameServer{
		cardDB:            cardDB,
		packSystem:        packSystem,
		playersOnline:     make(map[string]*protocol.PlayerConn),
		matchmakingQueue:  make([]*protocol.PlayerConn, 0),
		activeMatches:     make(map[string]*game.Match),
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		serverAddress:     serverAddress,
		broker:            pubsub.NewBroker(),
		tokenAcquiredChan: make(chan bool, 1), // Canal com buffer para não bloquear
	}
}

// monitorMatch monitora uma partida até seu término
func (gs *GameServer) monitorMatch(match *game.Match) {
	<-match.Done()
	gs.mu.Lock()
	defer gs.mu.Unlock()
	delete(gs.activeMatches, match.ID)
	log.Printf("[SERVER] Partida %s finalizada e removida", match.ID)
}

// findPlayerMatch encontra a partida de um jogador
func (gs *GameServer) findPlayerMatch(playerID string) *game.Match {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	for _, match := range gs.activeMatches {
		if match.P1.ID == playerID || match.P2.ID == playerID {
			return match
		}
	}
	return nil
}

func main() {
	tcpAddr := getEnv("LISTEN_ADDR", ":9000")
	apiAddr := getEnv("API_ADDR", ":8000")
	thisServerAddress := "http://" + getEnv("HOSTNAME", "localhost") + apiAddr

	// Lógica para descobrir a topologia do anel a partir de variáveis de ambiente
	allServersEnv := getEnv("ALL_SERVERS", thisServerAddress)
	allServers = strings.Split(allServersEnv, ",")
	myIndex := -1
	for i, addr := range allServers {
		if addr == thisServerAddress {
			myIndex = i
			break
		}
	}
	if myIndex == -1 {
		log.Fatalf("Endereço do servidor %s não encontrado na lista ALL_SERVERS", thisServerAddress)
	}

	nextIndex := (myIndex + 1) % len(allServers)
	nextServerAddress = allServers[nextIndex]
	log.Printf("[TOKEN_RING] Este servidor é %s. O próximo no anel é %s.", thisServerAddress, nextServerAddress)

	gameServer := NewGameServer(thisServerAddress)

	// Inicia o servidor HTTP para a API inter-servidores numa goroutine
	go func() {
		http.HandleFunc("/api/find-opponent", gameServer.handleFindOpponent)
		http.HandleFunc("/api/request-match", gameServer.handleRequestMatch)
		http.HandleFunc("/api/receive-token", gameServer.handleReceiveToken)
		log.Printf("[API_SERVER] A escutar em %s", apiAddr)
		if err := http.ListenAndServe(apiAddr, nil); err != nil {
			log.Fatalf("[API_SERVER] Erro ao iniciar servidor HTTP: %v", err)
		}
	}()

	// Inicia o loop principal de matchmaking numa goroutine
	go gameServer.matchmakingLoop()

	// O primeiro servidor na lista (índice 0) é responsável por criar e iniciar o token
	if myIndex == 0 {
		log.Printf("[TOKEN_RING] Eu sou o nó inicial. A criar e a passar o token pela primeira vez.")
		// Dá o token a si mesmo para começar o ciclo
		go func() {
			time.Sleep(5 * time.Second) // Espera um pouco para os outros servidores estarem online
			gameServer.tokenAcquiredChan <- true
		}()
	}

	// Inicia o listener TCP para as conexões dos clientes
	ln, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		log.Fatalf("[SERVER] Erro ao escutar TCP: %v", err)
	}
	defer ln.Close()
	log.Printf("[SERVER] A escutar jogadores em %s", tcpAddr)

	// Goroutine para processar mensagens de clientes
	go gameServer.listenForClientMessages()

	log.Printf("[SERVER] Servidor pronto! Aguardando conexões...")

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[SERVER] Erro ao aceitar conexão: %v", err)
			continue
		}
		go gameServer.handleConn(conn)
	}
}

// handleConn processa uma nova conexão de cliente
func (gs *GameServer) handleConn(conn net.Conn) {
	peer := conn.RemoteAddr().String()
	log.Printf("[SERVER] Nova conexão de %s", peer)

	player := protocol.NewPlayerConn(peer, conn)

	gs.mu.Lock()
	gs.playersOnline[peer] = player
	gs.mu.Unlock()

	// Cria um subscriber para este jogador
	playerSub := gs.broker.Subscribe(fmt.Sprintf("player.%s", player.ID))

	// Goroutine para enviar mensagens para o cliente
	go gs.writeLoop(player, playerSub)

	defer func() {
		log.Printf("[SERVER] Desconectando %s", peer)
		gs.broker.Unsubscribe(playerSub)
		gs.cleanup(player)
		conn.Close()
	}()

	// Loop para ler mensagens do cliente e publicá-las
	for {
		msg, err := player.ReadMsg()
		if err != nil {
			break
		}
		if msg == nil {
			break // EOF
		}

		// Publica a mensagem do cliente para ser processada
		gs.broker.Publish("client.messages", protocol.ClientAction{Player: player, Msg: msg})
	}
}

// listenForClientMessages escuta e processa mensagens de clientes do broker
func (gs *GameServer) listenForClientMessages() {
	sub := gs.broker.Subscribe("client.messages")
	for msg := range sub {
		if action, ok := msg.Payload.(protocol.ClientAction); ok {
			log.Printf("[SERVER] <- %s: %s", action.Player.ID, action.Msg.T)
			gs.handleMessage(action.Player, action.Msg)
		}
	}
}

// sendToPlayer envia uma mensagem para um jogador específico via pub/sub
func (gs *GameServer) sendToPlayer(playerID string, msg protocol.ServerMsg) {
	gs.broker.Publish(fmt.Sprintf("player.%s", playerID), msg)
}

// writeLoop envia mensagens de um subscriber para o PlayerConn
func (gs *GameServer) writeLoop(player *protocol.PlayerConn, sub pubsub.Subscriber) {
	for msg := range sub {
		if serverMsg, ok := msg.Payload.(protocol.ServerMsg); ok {
			player.SendMsg(serverMsg)
		}
	}
}

// matchmakingLoop é o novo coração da lógica de matchmaking, baseado em token.
func (gs *GameServer) matchmakingLoop() {
	for {
		// 1. ESPERA - Bloqueia aqui até receber o token
		<-gs.tokenAcquiredChan
		log.Printf("[MATCHMAKING] Tenho o token. A verificar a fila...")

		// 2. AGE (Secção Crítica) - Executa a lógica de matchmaking
		gs.mu.Lock()
		if len(gs.matchmakingQueue) >= 2 {
			gs.createLocalMatch() // Lógica de partida local
		} else if len(gs.matchmakingQueue) == 1 {
			// player := gs.matchmakingQueue[0]
			gs.mu.Unlock() // Libera a trava local antes de chamadas de rede

			found := gs.findAndCreateDistributedMatch()
			if found {
				log.Printf("[MATCHMAKING] Partida distribuída criada com sucesso.")
			} else {
				log.Printf("[MATCHMAKING] Nenhum oponente distribuído encontrado.")
			}
		} else {
			gs.mu.Unlock()
			log.Printf("[MATCHMAKING] Fila vazia.")
		}

		// Pausa antes de passar o token para não sobrecarregar a rede
		time.Sleep(2 * time.Second)

		// 3. PASSA - Passa o token para o próximo servidor
		gs.passTokenToNextServer()
	}
}

// cleanup remove o jogador do sistema
func (gs *GameServer) cleanup(player *protocol.PlayerConn) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	// Remove da lista de jogadores online
	delete(gs.playersOnline, player.ID)

	// Limpa estados de rematch relacionados
	for _, p := range gs.playersOnline {
		if p.LastOpponent == player.ID {
			p.WantsRematch = false
		}
	}

	// Remove da fila de matchmaking
	for i, p := range gs.matchmakingQueue {
		if p.ID == player.ID {
			gs.matchmakingQueue = append(gs.matchmakingQueue[:i], gs.matchmakingQueue[i+1:]...)
			break
		}
	}

	// Notifica oponente se estava em partida
	for _, match := range gs.activeMatches {
		if match.P1.ID == player.ID || match.P2.ID == player.ID {
			var opponent *protocol.PlayerConn
			if match.P1.ID == player.ID {
				opponent = match.P2
			} else {
				opponent = match.P1
			}

			if opponent != nil {
				gs.sendToPlayer(opponent.ID, protocol.ServerMsg{
					T:    protocol.ERROR,
					Code: "OPPONENT_DISCONNECTED",
					Msg:  "Seu oponente desconectou",
				})
				gs.sendToPlayer(opponent.ID, protocol.ServerMsg{
					T:      protocol.MATCH_END,
					Result: protocol.WIN,
				})
			}

			// Remove a partida
			delete(gs.activeMatches, match.ID)
			break
		}
	}
}

func (gs *GameServer) createLocalMatch() {
	p1 := gs.matchmakingQueue[0]
	p2 := gs.matchmakingQueue[1]
	gs.matchmakingQueue = gs.matchmakingQueue[2:]
	gs.mu.Unlock()

	matchID := fmt.Sprintf("local_match_%d", time.Now().UnixNano())
	match := game.NewMatch(matchID, p1, p2, gs.cardDB, gs.broker)
	gs.mu.Lock()
	gs.activeMatches[matchID] = match
	gs.mu.Unlock()

	log.Printf("[SERVER] Partida local criada: %s", matchID)
	p1.SendMsg(protocol.ServerMsg{T: protocol.MATCH_FOUND, MatchID: matchID, OpponentID: p2.ID})
	p2.SendMsg(protocol.ServerMsg{T: protocol.MATCH_FOUND, MatchID: matchID, OpponentID: p1.ID})
	match.BroadcastState()
	go gs.monitorMatch(match)
}

func (gs *GameServer) findAndCreateDistributedMatch() bool {
	for _, serverAddr := range allServers {
		if serverAddr == gs.serverAddress {
			continue
		}
		resp, err := gs.httpClient.Get(serverAddr + "/api/find-opponent")
		if err == nil && resp.StatusCode == http.StatusOK {
			// ... Lógica para confirmar e criar a partida com POST para /api/request-match ...
			// Se a partida for criada, remover jogador da fila e retornar true
			resp.Body.Close()
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	return false
}

func (gs *GameServer) passTokenToNextServer() {
	gs.tokenMutex.Lock()
	gs.hasToken = false
	gs.tokenMutex.Unlock()

	log.Printf("[TOKEN_RING] A passar o token para %s...", nextServerAddress)
	_, err := gs.httpClient.Post(nextServerAddress+"/api/receive-token", "application/json", nil)
	if err != nil {
		log.Printf("[TOKEN_RING] ERRO ao passar o token: %v. O anel pode estar quebrado.", err)
		// Lógica de recuperação: tenta dar o token a si mesmo para recomeçar o ciclo
		time.Sleep(5 * time.Second)
		gs.tokenAcquiredChan <- true
	} else {
		log.Printf("[TOKEN_RING] Token passado com sucesso.")
	}
}

func (gs *GameServer) handleReceiveToken(w http.ResponseWriter, r *http.Request) {
	gs.tokenMutex.Lock()
	if gs.hasToken {
		gs.tokenMutex.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	gs.hasToken = true
	gs.tokenMutex.Unlock()

	log.Printf("[TOKEN_RING] Recebi o token do servidor anterior.")
	select {
	case gs.tokenAcquiredChan <- true:
	default: // Evita bloquear se o canal já estiver cheio
	}
	w.WriteHeader(http.StatusOK)
}

// handleMessage processa uma mensagem do cliente
func (gs *GameServer) handleMessage(player *protocol.PlayerConn, msg *protocol.ClientMsg) {
	switch msg.T {
	case protocol.FIND_MATCH:
		gs.handleFindMatch(player)
	case protocol.PLAY:
		gs.handlePlay(player, msg.CardID)
	case protocol.CHAT:
		gs.handleChat(player, msg.Text)
	case protocol.PING:
		gs.handlePing(player, msg.TS)
	case protocol.OPEN_PACK:
		gs.handleOpenPack(player)
	case protocol.LEAVE:
		gs.handleLeave(player)
	case protocol.AUTOPLAY:
		gs.handleAutoPlay(player, true)
	case protocol.NOAUTOPLAY:
		gs.handleAutoPlay(player, false)
	case protocol.REMATCH:
		gs.handleRematch(player)
	default:
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.INVALID_MESSAGE,
			Msg:  "Tipo de mensagem desconhecido",
		})
	}
}

// handleFindMatch adiciona jogador à fila de matchmaking
func (gs *GameServer) handleFindMatch(player *protocol.PlayerConn) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	for _, p := range gs.matchmakingQueue {
		if p.ID == player.ID {
			return
		}
	}
	gs.matchmakingQueue = append(gs.matchmakingQueue, player)
	log.Printf("[SERVER] %s entrou na fila de matchmaking", player.ID)
}

// handlePlay processa uma jogada
func (gs *GameServer) handlePlay(player *protocol.PlayerConn, cardID string) {
	match := gs.findPlayerMatch(player.ID)
	if match == nil {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.MATCH_NOT_FOUND,
			Msg:  "Você não está em uma partida",
		})
		return
	}

	if err := match.PlayCard(player.ID, cardID); err != nil {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.INVALID_CARD,
			Msg:  err.Error(),
		})
		return
	}

	log.Printf("[SERVER] %s jogou carta %s", player.ID, cardID)
}

// handleChat processa mensagens de chat
func (gs *GameServer) handleChat(player *protocol.PlayerConn, text string) {
	match := gs.findPlayerMatch(player.ID)
	if match == nil {
		return
	}

	var opponent *protocol.PlayerConn
	if match.P1.ID == player.ID {
		opponent = match.P2
	} else {
		opponent = match.P1
	}

	if opponent != nil {
		// Envia mensagem de chat para o oponente
		gs.sendToPlayer(opponent.ID, protocol.ServerMsg{
			T:        protocol.CHAT_MESSAGE,
			SenderID: player.ID,
			Text:     text,
		})
		log.Printf("[SERVER] Chat de %s para %s: %s", player.ID, opponent.ID, text)
	}
}

// handlePing responde com PONG
func (gs *GameServer) handlePing(player *protocol.PlayerConn, ts int64) {
	player.LastPing = time.Now().UnixMilli()
	rtt := player.LastPing - ts

	gs.sendToPlayer(player.ID, protocol.ServerMsg{
		T:     protocol.PONG,
		TS:    ts,
		RTTMs: rtt,
	})
}

// handleOpenPack processa abertura de pacote
func (gs *GameServer) handleOpenPack(player *protocol.PlayerConn) {
	cards, err := gs.packSystem.OpenPack(player.ID)
	if err != nil {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.OUT_OF_STOCK,
			Msg:  err.Error(),
		})
		return
	}

	gs.sendToPlayer(player.ID, protocol.ServerMsg{
		T:     protocol.PACK_OPENED,
		Cards: cards,
		Stock: gs.packSystem.GetStock(),
	})

	log.Printf("[SERVER] %s abriu pacote: %v", player.ID, cards)
}

// handleLeave remove jogador da fila ou partida
func (gs *GameServer) handleLeave(player *protocol.PlayerConn) {
	gs.cleanup(player)
}

// handleAutoPlay configura o modo autoplay para o jogador
func (gs *GameServer) handleAutoPlay(player *protocol.PlayerConn, enable bool) {
	player.AutoPlay = enable

	if enable {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "AUTOPLAY_ENABLED",
			Msg:  "Autoplay ativado - cartas serão jogadas automaticamente se não escolher em 12 segundos",
		})
		log.Printf("[SERVER] %s ativou autoplay", player.ID)
	} else {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "AUTOPLAY_DISABLED",
			Msg:  "Autoplay desativado - você tem tempo ilimitado para jogar",
		})
		log.Printf("[SERVER] %s desativou autoplay", player.ID)
	}
}

// handleRematch processa solicitação de rematch
func (gs *GameServer) handleRematch(player *protocol.PlayerConn) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	// Verifica se o jogador tem um último oponente
	if player.LastOpponent == "" {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "NO_LAST_OPPONENT",
			Msg:  "Você precisa terminar uma partida antes de solicitar rematch",
		})
		return
	}

	// Verifica se já está em uma partida
	if gs.findPlayerMatchUnsafe(player.ID) != nil {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "ALREADY_IN_MATCH",
			Msg:  "Você já está em uma partida",
		})
		return
	}

	// Encontra o oponente
	var opponent *protocol.PlayerConn
	for _, p := range gs.playersOnline {
		if p.ID == player.LastOpponent {
			opponent = p
			break
		}
	}

	if opponent == nil {
		gs.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "OPPONENT_NOT_ONLINE",
			Msg:  "Seu último oponente não está online",
		})
		return
	}

	// Marca que o jogador quer rematch
	player.WantsRematch = true

	// Notifica o oponente sobre a solicitação
	gs.sendToPlayer(opponent.ID, protocol.ServerMsg{
		T:        protocol.REMATCH_REQUEST,
		SenderID: player.ID,
		Msg:      fmt.Sprintf("%s quer jogar novamente! Digite /rematch para aceitar.", player.ID),
	})

	gs.sendToPlayer(player.ID, protocol.ServerMsg{
		T:    protocol.ERROR,
		Code: "REMATCH_REQUESTED",
		Msg:  "Solicitação de rematch enviada! Aguardando resposta do oponente...",
	})

	log.Printf("[SERVER] %s solicitou rematch com %s", player.ID, opponent.ID)

	// Verifica se ambos querem rematch
	if opponent.WantsRematch && opponent.LastOpponent == player.ID {
		// Ambos querem rematch, cria nova partida
		gs.createRematch(player, opponent)
	}
}

// createRematch cria uma nova partida entre os mesmos jogadores
func (gs *GameServer) createRematch(p1, p2 *protocol.PlayerConn) {
	// Gera ID único para a nova partida
	matchID := fmt.Sprintf("rematch_%d", time.Now().UnixNano())

	// Cria a partida
	match := game.NewMatch(matchID, p1, p2, gs.cardDB, gs.broker)
	gs.activeMatches[matchID] = match

	// Reseta estados de rematch
	p1.WantsRematch = false
	p2.WantsRematch = false

	log.Printf("[SERVER] Rematch criado: %s entre %s e %s", matchID, p1.ID, p2.ID)

	// Envia confirmação de rematch aceito
	gs.sendToPlayer(p1.ID, protocol.ServerMsg{
		T:          protocol.REMATCH_ACCEPTED,
		MatchID:    matchID,
		OpponentID: p2.ID,
		Msg:        "Rematch aceito! Nova partida iniciada!",
	})

	gs.sendToPlayer(p2.ID, protocol.ServerMsg{
		T:          protocol.REMATCH_ACCEPTED,
		MatchID:    matchID,
		OpponentID: p1.ID,
		Msg:        "Rematch aceito! Nova partida iniciada!",
	})

	// Envia estado inicial
	match.BroadcastState()

	// Monitora o fim da nova partida
	go gs.monitorMatch(match)
}

// findPlayerMatchUnsafe encontra a partida de um jogador (sem lock)
func (gs *GameServer) findPlayerMatchUnsafe(playerID string) *game.Match {
	for _, match := range gs.activeMatches {
		if match.P1.ID == playerID || match.P2.ID == playerID {
			return match
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// handleFindOpponent responde se este servidor tem um jogador na fila
func (gs *GameServer) handleFindOpponent(w http.ResponseWriter, r *http.Request) {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	if len(gs.matchmakingQueue) > 0 {
		player := gs.matchmakingQueue[0]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"playerId": player.ID,
		})
		log.Printf("[API_SERVER] Respondi a /api/find-opponent: tenho o jogador %s", player.ID)
	} else {
		http.NotFound(w, r)
		log.Printf("[API_SERVER] Respondi a /api/find-opponent: não tenho jogadores na fila")
	}
}

// handleRequestMatch processa o pedido final para criar uma partida
func (gs *GameServer) handleRequestMatch(w http.ResponseWriter, r *http.Request) {
	// Lógica para verificar se o jogador ainda está disponível,
	// remover o jogador da fila local e confirmar a criação da partida.
	// Se tudo estiver OK, responde com http.StatusOK.
	// Se o jogador não estiver mais disponível, responde com um erro (ex: http.StatusConflict).
}
