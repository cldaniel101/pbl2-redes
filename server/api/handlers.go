package api

import (
	"encoding/json"
	"log"
	"net/http"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"pingpong/server/state"
)

// APIServer lida com as requisições HTTP da API inter-servidores.
type APIServer struct {
	stateManager *state.StateManager
	broker       *pubsub.Broker
	serverAddr   string
	// O canal de token é injetado para que o handler possa notificar
	// o serviço de matchmaking quando o token for recebido.
	tokenAcquiredChan chan<- bool
}

// NewServer cria uma nova instância do servidor da API.
func NewServer(sm *state.StateManager, broker *pubsub.Broker, tokenChan chan<- bool, serverAddr string) *APIServer {
	return &APIServer{
		stateManager:      sm,
		broker:            broker,
		tokenAcquiredChan: tokenChan,
		serverAddr:        serverAddr,
	}
}

// Router configura e retorna um novo router HTTP com todos os endpoints da API.
func (s *APIServer) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/find-opponent", s.handleFindOpponent)
	mux.HandleFunc("/api/request-match", s.handleRequestMatch)
	mux.HandleFunc("/api/receive-token", s.handleReceiveToken)
	mux.HandleFunc("/api/forward/play", s.handleForwardPlay)
	mux.HandleFunc("/api/forward/message", s.handleForwardMessage)
	return mux
}

// sendToPlayer publica uma mensagem para um jogador específico através do broker.
func (s *APIServer) sendToPlayer(playerID string, msg protocol.ServerMsg) {
	s.broker.Publish("player."+playerID, msg)
}

// handleFindOpponent responde se este servidor tem um jogador na fila de matchmaking.
func (s *APIServer) handleFindOpponent(w http.ResponseWriter, r *http.Request) {
	// A lógica de aceder à fila foi movida para o StateManager
	// para garantir a segurança de concorrência.
	player := s.stateManager.GetFirstPlayerInQueue()

	if player != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"playerId": player.ID,
		})
		log.Printf("[API] Respondeu a /api/find-opponent: tenho o jogador %s", player.ID)
	} else {
		http.NotFound(w, r)
		log.Printf("[API] Respondeu a /api/find-opponent: não tenho jogadores na fila")
	}
}

// handleRequestMatch processa o pedido final para criar uma partida distribuída.
func (s *APIServer) handleRequestMatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MatchID       string `json:"matchId"`
		HostPlayerID  string `json:"hostPlayerId"`
		GuestPlayerID string `json:"guestPlayerId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Pedido inválido", http.StatusBadRequest)
		return
	}

	// Delega a lógica de confirmação e criação da partida para o StateManager.
	guestPlayer, err := s.stateManager.ConfirmAndCreateDistributedMatch(
		req.MatchID,
		req.GuestPlayerID,
		req.HostPlayerID,
		r.RemoteAddr, // O endereço do servidor anfitrião
		s.serverAddr, // O endereço deste servidor (convidado)
		s.broker,
	)

	if err != nil {
		log.Printf("[API] Pedido de partida para %s rejeitado: %v", req.GuestPlayerID, err)
		http.Error(w, err.Error(), http.StatusConflict) // 409 Conflict
		return
	}

	log.Printf("[API] Partida distribuída %s aceite para o jogador local %s.", req.MatchID, req.GuestPlayerID)

	// Notifica o jogador local (convidado) de que a partida foi encontrada.
	s.sendToPlayer(guestPlayer.ID, protocol.ServerMsg{
		T:          protocol.MATCH_FOUND,
		MatchID:    req.MatchID,
		OpponentID: req.HostPlayerID,
	})

	w.WriteHeader(http.StatusOK)
}

// handleReceiveToken recebe o token de outro servidor e notifica o serviço de matchmaking.
func (s *APIServer) handleReceiveToken(w http.ResponseWriter, r *http.Request) {
	log.Printf("[API] Recebi o token do servidor anterior.")
	select {
	case s.tokenAcquiredChan <- true:
		// Notifica o matchmaking que pode começar a sua verificação.
	default:
		// Evita bloquear se o serviço de matchmaking não estiver pronto para receber.
	}
	w.WriteHeader(http.StatusOK)
}

// handleForwardPlay recebe uma jogada de um jogador remoto e aplica-a à partida local.
func (s *APIServer) handleForwardPlay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlayerID string `json:"playerId"`
		CardID   string `json:"cardId"`
		MatchID  string `json:"matchId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Pedido inválido", http.StatusBadRequest)
		return
	}

	match := s.stateManager.FindPlayerMatch(req.PlayerID)
	if match == nil || match.ID != req.MatchID {
		log.Printf("[API] Jogada recebida para partida %s não encontrada para o jogador %s", req.MatchID, req.PlayerID)
		http.NotFound(w, r)
		return
	}

	if err := match.PlayCard(req.PlayerID, req.CardID); err != nil {
		log.Printf("[API] Erro ao processar jogada retransmitida de %s: %v", req.PlayerID, err)
		// Opcional: poderia retornar um erro para o servidor anfitrião.
	}

	log.Printf("[API] Jogada retransmitida de %s processada com sucesso.", req.PlayerID)
	w.WriteHeader(http.StatusOK)
}

// handleForwardMessage recebe uma mensagem de estado do servidor anfitrião e a envia para o jogador local.
func (s *APIServer) handleForwardMessage(w http.ResponseWriter, r *http.Request) {
	var msg protocol.ServerMsg
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	// O servidor que retransmite adiciona o ID do jogador alvo num cabeçalho.
	playerID := r.Header.Get("X-Player-ID")
	if playerID == "" {
		log.Printf("[API] Não foi possível retransmitir a mensagem, ID do jogador em falta no cabeçalho X-Player-ID.")
		http.Error(w, "ID do jogador em falta", http.StatusBadRequest)
		return
	}

	log.Printf("[API] Retransmitindo mensagem do tipo %s para o jogador %s", msg.T, playerID)
	s.sendToPlayer(playerID, msg)
	w.WriteHeader(http.StatusOK)
}