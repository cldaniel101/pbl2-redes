package api

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"pingpong/server/state"
	"pingpong/server/token"
	"strings"
)

// TokenReceiver define a interface para receber o token
type TokenReceiver interface {
	SetToken(t *token.Token)
}

// APIServer lida com as requisições HTTP da API inter-servidores.
type APIServer struct {
	stateManager *state.StateManager
	broker       *pubsub.Broker
	serverAddr   string
	// O canal de token é injetado para que o handler possa notificar
	// o serviço de matchmaking quando o token for recebido.
	tokenAcquiredChan chan<- *token.Token
	tokenReceiver     TokenReceiver
}

// NewServer cria uma nova instância do servidor da API.
func NewServer(sm *state.StateManager, broker *pubsub.Broker, tokenChan chan<- *token.Token, serverAddr string) *APIServer {
	return &APIServer{
		stateManager:      sm,
		broker:            broker,
		tokenAcquiredChan: tokenChan,
		serverAddr:        serverAddr,
	}
}

// SetTokenReceiver configura o destino que receberá o token ao chegar via HTTP
func (s *APIServer) SetTokenReceiver(r TokenReceiver) {
    s.tokenReceiver = r
}

// Router configura e retorna um novo router HTTP com todos os endpoints da API.
func (s *APIServer) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/find-opponent", s.handleFindOpponent)
	mux.HandleFunc("/api/request-match", s.handleRequestMatch)
	mux.HandleFunc("/api/receive-token", s.handleReceiveToken)
	mux.HandleFunc("/matches/", s.handleMatchAction)
	mux.HandleFunc("/api/forward/message", s.handleForwardMessage)
	mux.HandleFunc("/api/health-check", s.handleHealthCheck)
	return mux
}

// handleHealthCheck é um endpoint simples para verificar se o servidor está online.
func (s *APIServer) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
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
		MatchID       string   `json:"matchId"`
		HostPlayerID  string   `json:"hostPlayerId"`
		GuestPlayerID string   `json:"guestPlayerId"`
		GuestCards    []string `json:"guestCards"` // Cartas para o jogador convidado
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Pedido inválido", http.StatusBadRequest)
		return
	}

	// Delega a lógica de confirmação e criação da partida para o StateManager.
	// Agora passa também as cartas do jogador convidado
	guestPlayer, err := s.stateManager.ConfirmAndCreateDistributedMatchWithCards(
		req.MatchID,
		req.GuestPlayerID,
		req.HostPlayerID,
		r.RemoteAddr, // O endereço do servidor anfitrião
		s.serverAddr, // O endereço deste servidor (convidado)
		s.broker,
		req.GuestCards,
	)

	if err != nil {
		log.Printf("[API] Pedido de partida para %s rejeitado: %v", req.GuestPlayerID, err)
		http.Error(w, err.Error(), http.StatusConflict) // 409 Conflict
		return
	}

	log.Printf("[API] Partida distribuída %s aceite para o jogador local %s com %d cartas.",
		req.MatchID, req.GuestPlayerID, len(req.GuestCards))

	// Notifica o jogador local (convidado) de que a partida foi encontrada.
	s.sendToPlayer(guestPlayer.ID, protocol.ServerMsg{
		T:          protocol.MATCH_FOUND,
		MatchID:    req.MatchID,
		OpponentID: req.HostPlayerID,
	})

	// Envia o estado inicial (Rodada 1) para o jogador convidado (local).
	// Isto é crucial para que ele receba a sua mão inicial.
	// (Usamos FindMatchByID porque ConfirmAndCreateDistributedMatchWithCards já o criou)
	if match := s.stateManager.FindMatchByID(req.MatchID); match != nil {
		match.BroadcastState()
	}

	w.WriteHeader(http.StatusOK)
}

// handleReceiveToken recebe o token de outro servidor e notifica o serviço de matchmaking.
func (s *APIServer) handleReceiveToken(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
        return
    }

    // Ler o corpo da requisição
    data, errRead := ioutil.ReadAll(r.Body)
    if errRead != nil {
        log.Printf("[API] Erro ao ler corpo do token: %v", errRead)
        http.Error(w, "Corpo do pedido inválido", http.StatusBadRequest)
        return
    }

    // Tentar deserializar como o token de cartas
    receivedToken, err := token.FromJSON(data)
    if err != nil {
        log.Printf("[API] Erro ao deserializar token de cartas: %v", err)
        http.Error(w, "Formato de token inválido", http.StatusBadRequest)
        return
    }

    log.Printf("[API] Recebi token de cartas com %d no pool.", receivedToken.GetPoolSize())

    // Envia o token *real* para o canal do matchmaking
    select {
    case s.tokenAcquiredChan <- receivedToken:
        log.Printf("[API] Token enviado para o serviço de matchmaking.")
    default:
        log.Println("[API] Aviso: canal do token bloqueado, não foi possível enviar o token.")
    }

    w.WriteHeader(http.StatusOK)
}

// handleMatchAction recebe uma ação de um jogador remoto (ex: jogar uma carta)
// e aplica-a à partida local. A URL deve ser no formato /matches/{matchID}/action.
func (s *APIServer) handleMatchAction(w http.ResponseWriter, r *http.Request) {
	// Extrai o ID da partida da URL.
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/matches/"), "/")
	if len(pathParts) < 2 || pathParts[1] != "action" {
		log.Printf("[API] URL de ação inválida: %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	matchID := pathParts[0]

	// Decodifica o corpo da requisição para obter os detalhes da ação.
	var req struct {
		PlayerID string `json:"playerId"` // ID real do jogador
		CardID   string `json:"cardId"`   // ID da carta jogada
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Pedido inválido", http.StatusBadRequest)
		return
	}

	// Encontra a partida através do gestor de estado.
	match := s.stateManager.FindMatchByID(matchID)
	if match == nil {
		log.Printf("[API] Ação recebida para partida %s não encontrada", matchID)
		http.NotFound(w, r)
		return
	}

	// Aplica a jogada na partida
	if err := match.PlayCard(req.PlayerID, req.CardID); err != nil {
		log.Printf("[API] Erro ao processar jogada retransmitida de %s: %v", req.PlayerID, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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

	// Apenas encaminha a mensagem se o jogador estiver realmente online neste servidor.
	// Isso evita que um servidor processe mensagens para jogadores que não são seus.
	if s.stateManager.IsPlayerOnline(playerID) {
		log.Printf("[API] Retransmitindo mensagem do tipo %s para o jogador %s", msg.T, playerID)
		s.sendToPlayer(playerID, msg)
	} else {
		log.Printf("[API] Mensagem para %s ignorada, jogador não está online neste servidor.", playerID)
	}

	w.WriteHeader(http.StatusOK)
}
