package network

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"pingpong/server/state"
)

// TCPServer lida com todas as interações de rede TCP com os clientes.
type TCPServer struct {
	listenAddr   string
	stateManager *state.StateManager
	broker       *pubsub.Broker
}

// NewTCPServer cria uma nova instância do servidor TCP, injetando as dependências.
func NewTCPServer(addr string, sm *state.StateManager, broker *pubsub.Broker) *TCPServer {
	return &TCPServer{
		listenAddr:   addr,
		stateManager: sm,
		broker:       broker,
	}
}

// Listen inicia o listener TCP e aceita novas conexões, delegando-as ao client_manager.
func (s *TCPServer) Listen() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("erro ao escutar TCP em %s: %w", s.listenAddr, err)
	}
	defer ln.Close()
	log.Printf("[NETWORK] Servidor TCP a escutar jogadores em %s", s.listenAddr)

	go s.listenForClientMessages()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[NETWORK] Erro ao aceitar nova conexão: %v", err)
			continue
		}
		// Para cada nova conexão, inicia o gestor de ciclo de vida do cliente.
		go s.handleConn(conn)
	}
}

// listenForClientMessages escuta o tópico do broker e encaminha para o handler apropriado.
func (s *TCPServer) listenForClientMessages() {
	sub := s.broker.Subscribe("client.messages")
	for msg := range sub {
		if action, ok := msg.Payload.(protocol.ClientAction); ok {
			log.Printf("[HANDLER] <- %s: %s", action.Player.ID, action.Msg.T)
			s.handleMessage(action.Player, action.Msg)
		}
	}
}

// sendToPlayer publica uma mensagem para o tópico de um jogador específico no broker.
func (s *TCPServer) sendToPlayer(playerID string, msg protocol.ServerMsg) {
	s.broker.Publish(fmt.Sprintf("player.%s", playerID), msg)
}

// handleMessage é o roteador que direciona as mensagens para a função correta.
func (s *TCPServer) handleMessage(player *protocol.PlayerConn, msg *protocol.ClientMsg) {
	switch msg.T {
	case protocol.FIND_MATCH:
		s.handleFindMatch(player) // jogador entrou na fila para encontrar uma partida
	case protocol.PLAY:
		s.handlePlay(player, msg.CardID) // O jogador jogou uma carta numa partida
	case protocol.CHAT:
		s.handleChat(player, msg.Text) // O jogador enviou uma mensagem de chat.
	case protocol.PING:
		s.handlePing(player, msg.TS) // O cliente enviou uma mensagem PING para medir a latência.
	case protocol.OPEN_PACK:
		s.handleOpenPack(player) // O jogador quer abrir um pacote de cartas.
	case protocol.AUTOPLAY:
		s.handleAutoPlay(player, true) // O jogador ativou o autoplay.
	case protocol.NOAUTOPLAY:
		s.handleAutoPlay(player, false) // O jogador desativou o autoplay.
	default:
		s.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.INVALID_MESSAGE,
			Msg:  "Tipo de mensagem desconhecido",
		})
	}
}

// --- Handlers Específicos para cada Tipo de Mensagem ---

func (s *TCPServer) handleFindMatch(player *protocol.PlayerConn) {
	s.stateManager.AddPlayerToQueue(player)
	s.sendToPlayer(player.ID, protocol.ServerMsg{
		T:    protocol.ERROR,
		Code: "QUEUED",
		Msg:  "Você entrou na fila de matchmaking.",
	})
}

func (s *TCPServer) handlePlay(player *protocol.PlayerConn, cardID string) {
	match := s.stateManager.FindPlayerMatch(player.ID)
	if match == nil {
		s.sendToPlayer(player.ID, protocol.ServerMsg{T: protocol.ERROR, Code: protocol.MATCH_NOT_FOUND, Msg: "Você não está numa partida"})
		return
	}
	if err := match.PlayCard(player.ID, cardID); err != nil {
		s.sendToPlayer(player.ID, protocol.ServerMsg{T: protocol.ERROR, Code: protocol.INVALID_CARD, Msg: err.Error()})
		return
	}
	log.Printf("[HANDLER] Jogada de %s com a carta %s processada.", player.ID, cardID)
}

func (s *TCPServer) handleChat(player *protocol.PlayerConn, text string) {
	match := s.stateManager.FindPlayerMatch(player.ID)
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
		s.sendToPlayer(opponent.ID, protocol.ServerMsg{
			T:        protocol.CHAT_MESSAGE,
			SenderID: player.ID,
			Text:     text,
		})
	}
}

func (s *TCPServer) handlePing(player *protocol.PlayerConn, ts int64) {
	now := time.Now().UnixMilli()
	player.LastPing = now
	rtt := now - ts
	s.sendToPlayer(player.ID, protocol.ServerMsg{
		T:     protocol.PONG,
		TS:    ts,
		RTTMs: rtt,
	})
}

func (s *TCPServer) handleOpenPack(player *protocol.PlayerConn) {
	// 1. Criar um canal para receber o resultado de forma assíncrona.
	replyChan := make(chan state.PackResult, 1)

	// 2. Criar e enfileirar o pedido.
	request := &state.PackRequest{
		PlayerID:  player.ID,
		ReplyChan: replyChan,
	}
	s.stateManager.EnqueuePackRequest(request)

	// 3. Obter timeout configurável (padrão: 10 segundos)
	timeout := getPackRequestTimeout()

	// 4. Aguardar pelo resultado com um timeout.
	select {
	case result := <-replyChan:
		// O resultado chegou do processador do token.
		if result.Err != nil {
			s.sendToPlayer(player.ID, protocol.ServerMsg{
				T:    protocol.ERROR,
				Code: protocol.OUT_OF_STOCK, // Ou outro código de erro relevante
				Msg:  result.Err.Error(),
			})
		} else {
			s.sendToPlayer(player.ID, protocol.ServerMsg{
				T:     protocol.PACK_OPENED,
				Cards: result.Cards,
				// O stock atualizado virá no processamento do token.
				// Podemos omiti-lo aqui ou obter do resultado se o incluirmos.
			})
			log.Printf("[HANDLER] %s abriu um pacote (processado via token): %v", player.ID, result.Cards)
		}
	case <-time.After(timeout):
		// Timeout - o token pode estar demorando muito ou perdido.
		s.sendToPlayer(player.ID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: "REQUEST_TIMEOUT",
			Msg:  "O pedido para abrir o pacote demorou demasiado a ser processado.",
		})
		log.Printf("[HANDLER] Timeout ao aguardar resultado do pacote para %s (timeout: %v)", player.ID, timeout)
	}
}

// getPackRequestTimeout retorna o timeout configurado para pedidos de pacotes.
// Pode ser configurado via variável de ambiente PACK_REQUEST_TIMEOUT_SEC (padrão: 10 segundos).
func getPackRequestTimeout() time.Duration {
	timeoutStr := os.Getenv("PACK_REQUEST_TIMEOUT_SEC")
	if timeoutStr == "" {
		return 10 * time.Second
	}

	timeoutSec, err := strconv.Atoi(timeoutStr)
	if err != nil || timeoutSec <= 0 {
		log.Printf("[HANDLER] Valor inválido para PACK_REQUEST_TIMEOUT_SEC: %s, usando padrão de 10s", timeoutStr)
		return 10 * time.Second
	}

	return time.Duration(timeoutSec) * time.Second
}

func (s *TCPServer) handleAutoPlay(player *protocol.PlayerConn, enable bool) {
	player.AutoPlay = enable
	var msg, code string
	if enable {
		code, msg = "AUTOPLAY_ENABLED", "Autoplay ativado."
	} else {
		code, msg = "AUTOPLAY_DISABLED", "Autoplay desativado."
	}
	s.sendToPlayer(player.ID, protocol.ServerMsg{T: protocol.ERROR, Code: code, Msg: msg})
	log.Printf("[HANDLER] %s alterou autoplay para: %t", player.ID, enable)
}
