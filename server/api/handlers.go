package api

import (
	"encoding/json"
	"log"
	"net/http"
	"pingpong/server/consensus"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"pingpong/server/s2s"
	"pingpong/server/state"
	"strings"
)

// APIServer lida com as requisições HTTP da API inter-servidores.
type APIServer struct {
	stateManager *state.StateManager
	broker       *pubsub.Broker
	serverAddr   string
	// O canal de token é injetado para que o handler possa notificar
	// o serviço de matchmaking quando o token for recebido.
	tokenAcquiredChan chan<- bool
	// Sistema de detecção de falhas
	failureDetector *consensus.FailureDetector
}

// NewServer cria uma nova instância do servidor da API.
func NewServer(sm *state.StateManager, broker *pubsub.Broker, tokenChan chan<- bool, serverAddr string) *APIServer {
	return &APIServer{
		stateManager:      sm,
		broker:            broker,
		tokenAcquiredChan: tokenChan,
		serverAddr:        serverAddr,
		failureDetector:   nil, // Será configurado externamente
	}
}

// SetFailureDetector configura o sistema de detecção de falhas
func (s *APIServer) SetFailureDetector(fd *consensus.FailureDetector) {
	s.failureDetector = fd
}

// Router configura e retorna um novo router HTTP com todos os endpoints da API.
func (s *APIServer) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/find-opponent", s.handleFindOpponent)
	mux.HandleFunc("/api/request-match", s.handleRequestMatch)
	mux.HandleFunc("/api/receive-token", s.handleReceiveToken)
	mux.HandleFunc("/matches/", s.handleMatchAction) // Endpoint para ações da partida
	mux.HandleFunc("/api/forward/message", s.handleForwardMessage)

	// Endpoints do sistema de consenso
	mux.HandleFunc("/api/matches/", s.handleConsensusEndpoints)

	// Endpoint de health check para o sistema de heartbeat
	mux.HandleFunc("/api/health", s.handleHealthCheck)

	// Endpoint de status do detector de falhas (debug)
	mux.HandleFunc("/api/failure-detector/status", s.handleFailureDetectorStatus)

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

// ========================================
// Handlers do Sistema de Consenso
// ========================================

// handleConsensusEndpoints roteia requisições para endpoints de consenso
func (s *APIServer) handleConsensusEndpoints(w http.ResponseWriter, r *http.Request) {
	// Parse da URL: /api/matches/{matchID}/{action}
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/matches/"), "/")
	if len(pathParts) < 2 {
		http.NotFound(w, r)
		return
	}

	matchID := pathParts[0]
	action := pathParts[1]

	switch action {
	case "propose":
		s.handlePropose(w, r, matchID)
	case "ack":
		s.handleACK(w, r, matchID)
	case "check":
		s.handleCheck(w, r, matchID)
	case "commit", "execute": // Alias: ambos executam a operação após consenso
		s.handleCommit(w, r, matchID)
	case "rollback":
		s.handleRollback(w, r, matchID)
	default:
		http.NotFound(w, r)
	}
}

// handlePropose recebe uma proposta de operação de outro servidor (Fase 1)
func (s *APIServer) handlePropose(w http.ResponseWriter, r *http.Request, matchID string) {
	var op consensus.Operation
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	// Encontra a partida
	match := s.stateManager.FindMatchByID(matchID)
	if match == nil {
		log.Printf("[API] Proposta recebida para partida %s não encontrada", matchID)
		http.NotFound(w, r)
		return
	}

	log.Printf("[API] Proposta recebida para partida %s: operação %s", matchID, op.ID)

	// Atualiza o relógio vetorial local com o timestamp da operação
	if match.VectorClock != nil {
		match.VectorClock.Update(op.Timestamp)
	}

	// Adiciona operação à fila local
	match.OperationQueue.Add(&op)

	// Verifica se a operação é válida localmente
	valid, err := match.CheckOperationValidity(&op)
	if !valid {
		log.Printf("[API] Operação %s inválida: %v", op.ID, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Envia ACK de volta para o servidor que propôs
	distMatch, isDistributed := s.stateManager.GetDistributedMatchInfo(matchID)
	if isDistributed {
		// Determina o servidor que propôs (é o servidor que enviou a requisição)
		// Podemos usar o ServerID da operação
		proposerServer := op.ServerID

		// Converte o ServerID para o endereço real
		var proposerAddr string
		if proposerServer == distMatch.HostServer {
			proposerAddr = distMatch.HostServer
		} else {
			proposerAddr = distMatch.GuestServer
		}

		// Envia ACK
		go s2s.SendACK(proposerAddr, matchID, op.ID, s.serverAddr)
	}

	w.WriteHeader(http.StatusOK)
}

// handleACK recebe um ACK de outro servidor (Fase 1)
func (s *APIServer) handleACK(w http.ResponseWriter, r *http.Request, matchID string) {
	var req struct {
		OperationID string `json:"operationId"`
		ServerID    string `json:"serverId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	// Encontra a partida
	match := s.stateManager.FindMatchByID(matchID)
	if match == nil {
		log.Printf("[API] ACK recebido para partida %s não encontrada", matchID)
		http.NotFound(w, r)
		return
	}

	log.Printf("[API] ACK recebido para partida %s: operação %s do servidor %s", matchID, req.OperationID, req.ServerID)

	// Marca o ACK
	allACKsReceived := match.MarkACK(req.OperationID, req.ServerID)

	if allACKsReceived {
		log.Printf("[API] Todos os ACKs recebidos para operação %s", req.OperationID)
	}

	w.WriteHeader(http.StatusOK)
}

// handleCheck verifica se uma operação é válida (Fase 2)
func (s *APIServer) handleCheck(w http.ResponseWriter, r *http.Request, matchID string) {
	var op consensus.Operation
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	// Encontra a partida
	match := s.stateManager.FindMatchByID(matchID)
	if match == nil {
		log.Printf("[API] Check recebido para partida %s não encontrada", matchID)
		http.NotFound(w, r)
		return
	}

	log.Printf("[API] Check recebido para partida %s: operação %s", matchID, op.ID)

	// Verifica validade
	valid, err := match.CheckOperationValidity(&op)
	if !valid {
		log.Printf("[API] Operação %s inválida: %v", op.ID, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleCommit executa uma operação após consenso (Fase 2)
func (s *APIServer) handleCommit(w http.ResponseWriter, r *http.Request, matchID string) {
	var op consensus.Operation
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	// Encontra a partida
	match := s.stateManager.FindMatchByID(matchID)
	if match == nil {
		log.Printf("[API] Commit recebido para partida %s não encontrada", matchID)
		http.NotFound(w, r)
		return
	}

	log.Printf("[API] Commit recebido para partida %s: operação %s", matchID, op.ID)

	// Executa a operação
	if err := match.ExecuteOperation(&op); err != nil {
		log.Printf("[API] Erro ao executar operação %s: %v", op.ID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleRollback reverte uma operação (Fase 2)
func (s *APIServer) handleRollback(w http.ResponseWriter, r *http.Request, matchID string) {
	var op consensus.Operation
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	// Encontra a partida
	match := s.stateManager.FindMatchByID(matchID)
	if match == nil {
		log.Printf("[API] Rollback recebido para partida %s não encontrada", matchID)
		http.NotFound(w, r)
		return
	}

	log.Printf("[API] Rollback recebido para partida %s: operação %s", matchID, op.ID)

	// Reverte a operação
	match.RollbackOperation(&op)

	w.WriteHeader(http.StatusOK)
}

// ========================================
// Endpoints do Sistema de Detecção de Falhas
// ========================================

// handleHealthCheck responde aos pings de heartbeat de outros servidores
func (s *APIServer) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	// Decodifica o ping (contém o ID do servidor que está pingando)
	var ping struct {
		ServerID string `json:"serverId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&ping); err != nil {
		// Se não conseguir decodificar, aceita mesmo assim (ping simples)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
		return
	}

	// Registra o heartbeat no detector de falhas
	if s.failureDetector != nil && ping.ServerID != "" {
		s.failureDetector.RecordHeartbeat(ping.ServerID)
		log.Printf("[API] ❤️ Heartbeat recebido de %s", ping.ServerID)
	}

	// Responde com status OK
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

// handleFailureDetectorStatus retorna o status do sistema de detecção de falhas (debug)
func (s *APIServer) handleFailureDetectorStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	if s.failureDetector == nil {
		http.Error(w, "Sistema de detecção de falhas não configurado", http.StatusServiceUnavailable)
		return
	}

	// Coleta informações do sistema
	stats := s.failureDetector.GetStats()
	aliveServers := s.failureDetector.GetAliveServers()
	deadServers := s.failureDetector.GetDeadServers()
	allServersStatus := s.failureDetector.GetAllServersStatus()
	activeOps := s.failureDetector.GetActiveOperations()
	failedOps := s.failureDetector.GetFailedOperations()

	// Prepara resposta
	response := map[string]interface{}{
		"stats":         stats,
		"aliveServers":  aliveServers,
		"deadServers":   deadServers,
		"serversStatus": allServersStatus,
		"activeOps":     activeOps,
		"failedOps":     failedOps,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)

	log.Printf("[API] Status do detector de falhas consultado")
}
