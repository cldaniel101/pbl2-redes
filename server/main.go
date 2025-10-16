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
	"strings"
	"sync"
	"time"
)

// Variáveis globais para a topologia do anel
var (
	allServers        []string
	nextServerAddress string
)

// FindOpponentResponse é a resposta do endpoint /api/find-opponent
type FindOpponentResponse struct {
	PlayerID      string `json:"playerId"`
	ServerAddress string `json:"serverAddress"` // O endereço do servidor que tem o oponente
}

// RequestMatchPayload é o corpo da requisição para /api/request-match
type RequestMatchPayload struct {
	RequestingPlayerID string `json:"requestingPlayerId"`
	RequestingServer   string `json:"requestingServer"`
}

// GameServer modificado para a lógica de token ring
type GameServer struct {
	cardDB           *game.CardDB
	packSystem       *game.PackSystem
	playersOnline    map[string]*protocol.PlayerConn
	matchmakingQueue []*protocol.PlayerConn
	activeMatches    map[string]*game.Match
	mu               sync.RWMutex
	httpClient       *http.Client
	serverAddress    string
	tokenMutex        sync.Mutex
	hasToken          bool
	tokenAcquiredChan chan bool
	remoteMatches     map[string]string // playerId -> hostServerURL
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
		tokenAcquiredChan: make(chan bool, 1),
		remoteMatches:     make(map[string]string),
	}
}

// safeSend é o novo método central para enviar mensagens.
// Ele verifica se o jogador é local ou remoto e age de acordo.
func (gs *GameServer) safeSend(player *protocol.PlayerConn, msg protocol.ServerMsg) {
	if player == nil {
		return
	}

	if player.IsRemote {
		// LÓGICA PARA JOGADOR REMOTO
		msgBytes, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[SAFE_SEND] Erro ao codificar msg para jogador remoto %s: %v", player.ID, err)
			return
		}

		forwardURL := player.RemoteServerURL + "/api/forward"
		payload := map[string]string{
			"targetPlayerId": player.ID,
			"message":        string(msgBytes),
		}
		payloadBytes, _ := json.Marshal(payload)

		resp, err := gs.httpClient.Post(forwardURL, "application/json", strings.NewReader(string(payloadBytes)))
		if err != nil {
			log.Printf("[SAFE_SEND] Erro ao encaminhar msg para jogador remoto %s: %v", player.ID, err)
		} else if resp != nil {
			resp.Body.Close()
		}
	} else if player.Encoder != nil {
		// LÓGICA PARA JOGADOR LOCAL
		player.SendMsg(msg)
	} else {
		log.Printf("[SAFE_SEND] Aviso: Tentou enviar mensagem para jogador local sem encoder: %s", player.ID)
	}
}

func main() {
	tcpAddr := getEnv("LISTEN_ADDR", ":9000")
	apiAddr := getEnv("API_ADDR", ":8000")
	hostname := getEnv("HOSTNAME", "localhost")
	thisServerAddress := fmt.Sprintf("http://%s%s", hostname, apiAddr)

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

	go func() {
		http.HandleFunc("/api/find-opponent", gameServer.handleFindOpponent)
		http.HandleFunc("/api/request-match", gameServer.handleRequestMatch)
		http.HandleFunc("/api/receive-token", gameServer.handleReceiveToken)
		http.HandleFunc("/api/match/action", gameServer.handleMatchAction)
		http.HandleFunc("/api/forward", gameServer.handleForwardMessage)
		log.Printf("[API_SERVER] A escutar em %s", apiAddr)
		if err := http.ListenAndServe(apiAddr, nil); err != nil {
			log.Fatalf("[API_SERVER] Erro ao iniciar servidor HTTP: %v", err)
		}
	}()

	go gameServer.matchmakingLoop()

	if myIndex == 0 {
		log.Printf("[TOKEN_RING] Eu sou o nó inicial. A criar e a passar o token.")
		go func() {
			time.Sleep(5 * time.Second)
			gameServer.tokenAcquiredChan <- true
		}()
	}

	ln, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		log.Fatalf("[SERVER] Erro ao escutar TCP: %v", err)
	}
	defer ln.Close()
	log.Printf("[SERVER] A escutar jogadores em %s", tcpAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[SERVER] Erro ao aceitar conexão: %v", err)
			continue
		}
		go gameServer.handleConn(conn)
	}
}

func (gs *GameServer) handleConn(conn net.Conn) {
	peer := conn.RemoteAddr().String()
	log.Printf("[SERVER] Nova conexão de %s", peer)
	player := protocol.NewPlayerConn(peer, conn)
	gs.mu.Lock()
	gs.playersOnline[player.ID] = player
	gs.mu.Unlock()

	defer func() {
		log.Printf("[SERVER] Desconectando %s", peer)
		gs.cleanup(player)
		conn.Close()
	}()

	for {
		msg, err := player.ReadMsg()
		if err != nil {
			break
		}
		if msg == nil {
			break
		}

		gs.mu.RLock()
		hostURL, isRemote := gs.remoteMatches[player.ID]
		gs.mu.RUnlock()

		if isRemote {
			msgBytes, _ := json.Marshal(msg)
			payload := map[string]string{"playerId": player.ID, "message": string(msgBytes)}
			payloadBytes, _ := json.Marshal(payload)
			gs.httpClient.Post(hostURL+"/api/match/action", "application/json", strings.NewReader(string(payloadBytes)))
		} else {
			gs.handleMessage(player, msg)
		}
	}
}

func (gs *GameServer) matchmakingLoop() {
	for {
		<-gs.tokenAcquiredChan
		log.Printf("[MATCHMAKING] Tenho o token. A verificar a fila...")

		gs.mu.Lock()
		if len(gs.matchmakingQueue) >= 2 {
			gs.createLocalMatch()
		} else if len(gs.matchmakingQueue) == 1 {
			gs.mu.Unlock()
			if !gs.findAndCreateDistributedMatch() {
				log.Printf("[MATCHMAKING] Nenhum oponente distribuído encontrado.")
			}
		} else {
			gs.mu.Unlock()
		}

		time.Sleep(2 * time.Second)
		gs.passTokenToNextServer()
	}
}

func (gs *GameServer) cleanup(player *protocol.PlayerConn) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	delete(gs.playersOnline, player.ID)

	for i, p := range gs.matchmakingQueue {
		if p.ID == player.ID {
			gs.matchmakingQueue = append(gs.matchmakingQueue[:i], gs.matchmakingQueue[i+1:]...)
			break
		}
	}

	match := gs.findPlayerMatchUnsafe(player.ID)
	if match != nil {
		var opponent *protocol.PlayerConn
		if match.P1.ID == player.ID {
			opponent = match.P2
		} else {
			opponent = match.P1
		}

		// CORREÇÃO: Usa safeSend para notificar o oponente
		gs.safeSend(opponent, protocol.ServerMsg{T: protocol.ERROR, Code: "OPPONENT_DISCONNECTED", Msg: "Seu oponente desconectou"})
		gs.safeSend(opponent, protocol.ServerMsg{T: protocol.MATCH_END, Result: protocol.WIN})

		delete(gs.activeMatches, match.ID)
	}
}

func (gs *GameServer) createLocalMatch() {
	p1 := gs.matchmakingQueue[0]
	p2 := gs.matchmakingQueue[1]
	gs.matchmakingQueue = gs.matchmakingQueue[2:]
	gs.mu.Unlock()

	matchID := fmt.Sprintf("local_match_%d", time.Now().UnixNano())
	match := game.NewMatch(matchID, p1, p2, gs.cardDB, gs.safeSend)

	gs.mu.Lock()
	gs.activeMatches[matchID] = match
	gs.mu.Unlock()

	log.Printf("[SERVER] Partida local criada: %s", matchID)
	gs.safeSend(p1, protocol.ServerMsg{T: protocol.MATCH_FOUND, MatchID: matchID, OpponentID: p2.ID})
	gs.safeSend(p2, protocol.ServerMsg{T: protocol.MATCH_FOUND, MatchID: matchID, OpponentID: p1.ID})
	match.BroadcastState()
}

func (gs *GameServer) findAndCreateDistributedMatch() bool {
	gs.mu.Lock()
	if len(gs.matchmakingQueue) == 0 {
		gs.mu.Unlock()
		return false
	}
	player := gs.matchmakingQueue[0]
	gs.mu.Unlock()

	for _, serverAddr := range allServers {
		if serverAddr == gs.serverAddress {
			continue
		}

		findURL := serverAddr + "/api/find-opponent"
		resp, err := gs.httpClient.Get(findURL)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}

		var opponent FindOpponentResponse
		if json.NewDecoder(resp.Body).Decode(&opponent) != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		requestURL := opponent.ServerAddress + "/api/request-match"
		payload := RequestMatchPayload{RequestingPlayerID: player.ID, RequestingServer: gs.serverAddress}
		body, _ := json.Marshal(payload)

		postResp, err := gs.httpClient.Post(requestURL, "application/json", strings.NewReader(string(body)))
		if err == nil && postResp.StatusCode == http.StatusOK {
			postResp.Body.Close()

			gs.mu.Lock()
			if len(gs.matchmakingQueue) == 0 || gs.matchmakingQueue[0].ID != player.ID {
				gs.mu.Unlock()
				return false // Player left queue while we were matching
			}
			p1 := gs.matchmakingQueue[0]
			gs.matchmakingQueue = gs.matchmakingQueue[1:]
			gs.mu.Unlock()

			p2_remote := &protocol.PlayerConn{ID: opponent.PlayerID, IsRemote: true, RemoteServerURL: opponent.ServerAddress}
			matchID := fmt.Sprintf("dist_match_%d", time.Now().UnixNano())
			match := game.NewMatch(matchID, p1, p2_remote, gs.cardDB, gs.safeSend)

			gs.mu.Lock()
			gs.activeMatches[matchID] = match
			gs.mu.Unlock()

			log.Printf("[SERVER] Partida distribuída %s criada. Host: local %s, Remoto: %s", matchID, p1.ID, p2_remote.ID)
			gs.safeSend(p1, protocol.ServerMsg{T: protocol.MATCH_FOUND, MatchID: matchID, OpponentID: p2_remote.ID})
			match.BroadcastState()
			return true
		}
		if postResp != nil {
			postResp.Body.Close()
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
	default:
	}
	w.WriteHeader(http.StatusOK)
}

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
	case protocol.AUTOPLAY:
		gs.handleAutoPlay(player, true)
	case protocol.NOAUTOPLAY:
		gs.handleAutoPlay(player, false)
	default:
		gs.safeSend(player, protocol.ServerMsg{T: protocol.ERROR, Code: protocol.INVALID_MESSAGE, Msg: "Tipo de mensagem desconhecido"})
	}
}

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

func (gs *GameServer) handlePlay(player *protocol.PlayerConn, cardID string) {
	match := gs.findPlayerMatch(player.ID)
	if match == nil {
		gs.safeSend(player, protocol.ServerMsg{T: protocol.ERROR, Code: protocol.MATCH_NOT_FOUND, Msg: "Você não está em uma partida"})
		return
	}
	if err := match.PlayCard(player.ID, cardID); err != nil {
		gs.safeSend(player, protocol.ServerMsg{T: protocol.ERROR, Code: protocol.INVALID_CARD, Msg: err.Error()})
	}
}

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
	gs.safeSend(opponent, protocol.ServerMsg{T: protocol.CHAT_MESSAGE, SenderID: player.ID, Text: text})
}

func (gs *GameServer) handlePing(player *protocol.PlayerConn, ts int64) {
	rtt := time.Now().UnixMilli() - ts
	gs.safeSend(player, protocol.ServerMsg{T: protocol.PONG, TS: ts, RTTMs: rtt})
}

func (gs *GameServer) handleOpenPack(player *protocol.PlayerConn) {
	cards, err := gs.packSystem.OpenPack(player.ID)
	if err != nil {
		gs.safeSend(player, protocol.ServerMsg{T: protocol.ERROR, Code: protocol.OUT_OF_STOCK, Msg: err.Error()})
		return
	}
	gs.safeSend(player, protocol.ServerMsg{T: protocol.PACK_OPENED, Cards: cards, Stock: gs.packSystem.GetStock()})
}

func (gs *GameServer) handleAutoPlay(player *protocol.PlayerConn, enable bool) {
	player.AutoPlay = enable
	msg := protocol.ServerMsg{T: protocol.ERROR}
	if enable {
		msg.Code = "AUTOPLAY_ENABLED"
		msg.Msg = "Autoplay ativado"
	} else {
		msg.Code = "AUTOPLAY_DISABLED"
		msg.Msg = "Autoplay desativado"
	}
	gs.safeSend(player, msg)
}

func (gs *GameServer) findPlayerMatch(playerID string) *game.Match {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return gs.findPlayerMatchUnsafe(playerID)
}

func (gs *GameServer) findPlayerMatchUnsafe(playerID string) *game.Match {
	for _, match := range gs.activeMatches {
		if (match.P1 != nil && match.P1.ID == playerID) || (match.P2 != nil && match.P2.ID == playerID) {
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

func (gs *GameServer) handleFindOpponent(w http.ResponseWriter, r *http.Request) {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	if len(gs.matchmakingQueue) > 0 {
		player := gs.matchmakingQueue[0]
		response := FindOpponentResponse{PlayerID: player.ID, ServerAddress: gs.serverAddress}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	} else {
		http.NotFound(w, r)
	}
}

func (gs *GameServer) handleRequestMatch(w http.ResponseWriter, r *http.Request) {
	var payload RequestMatchPayload
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		http.Error(w, "Corpo inválido", http.StatusBadRequest)
		return
	}

	gs.mu.Lock()
	defer gs.mu.Unlock()

	if len(gs.matchmakingQueue) == 0 {
		http.Error(w, "Jogador não disponível", http.StatusConflict)
		return
	}

	p2 := gs.matchmakingQueue[0]
	gs.matchmakingQueue = gs.matchmakingQueue[1:]

	log.Printf("[API_SERVER] Match distribuído aceito! Jogador local %s pareado com %s.", p2.ID, payload.RequestingPlayerID)
	gs.remoteMatches[p2.ID] = payload.RequestingServer
	gs.safeSend(p2, protocol.ServerMsg{T: protocol.MATCH_FOUND, OpponentID: payload.RequestingPlayerID})
	w.WriteHeader(http.StatusOK)
}

func (gs *GameServer) handleMatchAction(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		PlayerID string `json:"playerId"`
		Message  string `json:"message"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		http.Error(w, "Corpo inválido", http.StatusBadRequest)
		return
	}
	var clientMsg protocol.ClientMsg
	if json.Unmarshal([]byte(payload.Message), &clientMsg) != nil {
		http.Error(w, "Mensagem inválida", http.StatusBadRequest)
		return
	}

	match := gs.findPlayerMatch(payload.PlayerID)
	if match == nil {
		http.NotFound(w, r)
		return
	}
	var player *protocol.PlayerConn
	if match.P1.ID == payload.PlayerID {
		player = match.P1
	} else if match.P2.ID == payload.PlayerID {
		player = match.P2
	}

	if player != nil {
		gs.handleMessage(player, &clientMsg)
		w.WriteHeader(http.StatusOK)
	} else {
		http.NotFound(w, r)
	}
}

func (gs *GameServer) handleForwardMessage(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		TargetPlayerID string `json:"targetPlayerId"`
		Message        string `json:"message"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		http.Error(w, "Corpo inválido", http.StatusBadRequest)
		return
	}
	var serverMsg protocol.ServerMsg
	if json.Unmarshal([]byte(payload.Message), &serverMsg) != nil {
		http.Error(w, "Mensagem inválida", http.StatusBadRequest)
		return
	}

	gs.mu.RLock()
	player, ok := gs.playersOnline[payload.TargetPlayerID]
	gs.mu.RUnlock()

	if ok && player != nil && !player.IsRemote {
		gs.safeSend(player, serverMsg)
		w.WriteHeader(http.StatusOK)
	} else {
		http.NotFound(w, r)
	}
}
