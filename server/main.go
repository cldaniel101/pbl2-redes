package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"pingpong/server/game"
	"pingpong/server/protocol"
	"sync"
	"time"
)

// GameServer representa o servidor do jogo
type GameServer struct {
	cardDB           *game.CardDB
	packSystem       *game.PackSystem
	playersOnline    map[string]*protocol.PlayerConn
	matchmakingQueue []*protocol.PlayerConn
	activeMatches    map[string]*game.Match
	mu               sync.RWMutex
}

// NewGameServer cria um novo servidor do jogo
func NewGameServer() *GameServer {
	// Inicializa CardDB
	cardDB := game.NewCardDB()
	if err := cardDB.LoadFromFile("cards.json"); err != nil {
		log.Fatalf("[SERVER] Erro ao carregar cartas: %v", err)
	}

	// Inicializa sistema de pacotes
	packConfig := game.PackConfig{
		CardsPerPack: 3,
		Stock:        100,
		RNGSeed:      0, // seed aleatório
	}
	packSystem := game.NewPackSystem(packConfig, cardDB)

	return &GameServer{
		cardDB:           cardDB,
		packSystem:       packSystem,
		playersOnline:    make(map[string]*protocol.PlayerConn),
		matchmakingQueue: make([]*protocol.PlayerConn, 0),
		activeMatches:    make(map[string]*game.Match),
	}
}

// tryCreateMatch verifica a fila de matchmaking e cria partidas
func (gs *GameServer) tryCreateMatch() {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	// Precisa de pelo menos 2 jogadores na fila
	if len(gs.matchmakingQueue) < 2 {
		return
	}

	// Pega os dois primeiros jogadores da fila
	p1 := gs.matchmakingQueue[0]
	p2 := gs.matchmakingQueue[1]
	gs.matchmakingQueue = gs.matchmakingQueue[2:]

	// Gera ID único para a partida
	matchID := fmt.Sprintf("match_%d", time.Now().UnixNano())

	// Cria a partida
	match := game.NewMatch(matchID, p1, p2, gs.cardDB)
	gs.activeMatches[matchID] = match

	log.Printf("[SERVER] Partida criada: %s entre %s e %s", matchID, p1.ID, p2.ID)

	// Envia MATCH_FOUND para ambos jogadores
	p1.SendMsg(protocol.ServerMsg{
		T:          protocol.MATCH_FOUND,
		MatchID:    matchID,
		OpponentID: p2.ID,
	})

	p2.SendMsg(protocol.ServerMsg{
		T:          protocol.MATCH_FOUND,
		MatchID:    matchID,
		OpponentID: p1.ID,
	})

	// Envia estado inicial
	match.BroadcastState()

	// Monitora o fim da partida
	go gs.monitorMatch(match)
}

// monitorMatch monitora uma partida até seu término
func (gs *GameServer) monitorMatch(match *game.Match) {
	<-match.Done()

	gs.mu.Lock()
	defer gs.mu.Unlock()

	// Armazena informações do último oponente para possível rematch
	match.P1.LastOpponent = match.P2.ID
	match.P2.LastOpponent = match.P1.ID
	match.P1.WantsRematch = false
	match.P2.WantsRematch = false

	// Remove a partida da lista de partidas ativas
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
	addr := getEnv("LISTEN_ADDR", ":9000")

	log.Printf("[SERVER] Iniciando servidor Attribute War em %s ...", addr)

	// Cria o servidor do jogo
	gameServer := NewGameServer()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("[SERVER] Erro ao escutar: %v", err)
	}
	defer ln.Close()

	// Goroutine para matchmaking contínuo
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			gameServer.tryCreateMatch()
		}
	}()

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

	// Cria PlayerConn
	player := protocol.NewPlayerConn(peer, conn)

	// Registra o jogador
	gs.mu.Lock()
	gs.playersOnline[peer] = player
	gs.mu.Unlock()

	// Limpeza quando desconectar
	defer func() {
		log.Printf("[SERVER] Desconectando %s", peer)
		gs.cleanup(player)
		conn.Close()
	}()

	// Loop principal de mensagens
	for {
		msg, err := player.ReadMsg()
		if err != nil {
			log.Printf("[SERVER] Erro ao ler de %s: %v", peer, err)
			break
		}
		if msg == nil {
			break // EOF
		}

		log.Printf("[SERVER] <- %s: %s", peer, msg.T)
		gs.handleMessage(player, msg)
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
				opponent.SendMsg(protocol.ServerMsg{
					T:    protocol.ERROR,
					Code: "OPPONENT_DISCONNECTED",
					Msg:  "Seu oponente desconectou",
				})
				opponent.SendMsg(protocol.ServerMsg{
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
		player.SendMsg(protocol.ServerMsg{
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

	// Verifica se já está na fila
	for _, p := range gs.matchmakingQueue {
		if p.ID == player.ID {
			return
		}
	}

	// Adiciona à fila
	gs.matchmakingQueue = append(gs.matchmakingQueue, player)
	log.Printf("[SERVER] %s entrou na fila de matchmaking", player.ID)
}

// handlePlay processa uma jogada
func (gs *GameServer) handlePlay(player *protocol.PlayerConn, cardID string) {
	match := gs.findPlayerMatch(player.ID)
	if match == nil {
		player.SendMsg(protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.MATCH_NOT_FOUND,
			Msg:  "Você não está em uma partida",
		})
		return
	}

	if err := match.PlayCard(player.ID, cardID); err != nil {
		player.SendMsg(protocol.ServerMsg{
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
		opponent.SendMsg(protocol.ServerMsg{
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

	player.SendMsg(protocol.ServerMsg{
		T:     protocol.PONG,
		TS:    ts,
		RTTMs: rtt,
	})
}

// handleOpenPack processa abertura de pacote
func (gs *GameServer) handleOpenPack(player *protocol.PlayerConn) {
	cards, err := gs.packSystem.OpenPack(player.ID)
	if err != nil {
		player.SendMsg(protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.OUT_OF_STOCK,
			Msg:  err.Error(),
		})
		return
	}

	player.SendMsg(protocol.ServerMsg{
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
		player.SendMsg(protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "AUTOPLAY_ENABLED",
			Msg:  "Autoplay ativado - cartas serão jogadas automaticamente se não escolher em 12 segundos",
		})
		log.Printf("[SERVER] %s ativou autoplay", player.ID)
	} else {
		player.SendMsg(protocol.ServerMsg{
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
		player.SendMsg(protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "NO_LAST_OPPONENT",
			Msg:  "Você precisa terminar uma partida antes de solicitar rematch",
		})
		return
	}

	// Verifica se já está em uma partida
	if gs.findPlayerMatchUnsafe(player.ID) != nil {
		player.SendMsg(protocol.ServerMsg{
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
		player.SendMsg(protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "OPPONENT_NOT_ONLINE",
			Msg:  "Seu último oponente não está online",
		})
		return
	}

	// Marca que o jogador quer rematch
	player.WantsRematch = true

	// Notifica o oponente sobre a solicitação
	opponent.SendMsg(protocol.ServerMsg{
		T:        protocol.REMATCH_REQUEST,
		SenderID: player.ID,
		Msg:      fmt.Sprintf("%s quer jogar novamente! Digite /rematch para aceitar.", player.ID),
	})

	player.SendMsg(protocol.ServerMsg{
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
	match := game.NewMatch(matchID, p1, p2, gs.cardDB)
	gs.activeMatches[matchID] = match

	// Reseta estados de rematch
	p1.WantsRematch = false
	p2.WantsRematch = false

	log.Printf("[SERVER] Rematch criado: %s entre %s e %s", matchID, p1.ID, p2.ID)

	// Envia confirmação de rematch aceito
	p1.SendMsg(protocol.ServerMsg{
		T:          protocol.REMATCH_ACCEPTED,
		MatchID:    matchID,
		OpponentID: p2.ID,
		Msg:        "Rematch aceito! Nova partida iniciada!",
	})

	p2.SendMsg(protocol.ServerMsg{
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

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
