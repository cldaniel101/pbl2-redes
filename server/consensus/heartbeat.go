package consensus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// ServerStatus representa o status de disponibilidade de um servidor
type ServerStatus struct {
	ServerID      string    // ID do servidor
	Address       string    // Endereço HTTP do servidor
	IsAlive       bool      // Flag de disponibilidade
	LastHeartbeat time.Time // Timestamp do último heartbeat recebido
	LastCheck     time.Time // Timestamp da última verificação enviada
	FailureCount  int       // Contador de falhas consecutivas
	mu            sync.RWMutex
}

// HeartbeatSystem gerencia o sistema de detecção de falhas
type HeartbeatSystem struct {
	mu              sync.RWMutex
	serverID        string                   // ID deste servidor
	servers         map[string]*ServerStatus // Mapa de status dos servidores
	heartbeatTicker *time.Ticker             // Ticker para pings periódicos
	checkInterval   time.Duration            // Intervalo entre verificações
	timeout         time.Duration            // Timeout para considerar servidor morto
	maxFailures     int                      // Número máximo de falhas antes de marcar como morto
	stopChan        chan bool                // Canal para parar o sistema
	onServerDeath   func(serverID string)    // Callback quando servidor morre
	onServerRevive  func(serverID string)    // Callback quando servidor revive
	running         bool                     // Flag indicando se o sistema está rodando
}

// HeartbeatConfig contém as configurações do sistema de heartbeat
type HeartbeatConfig struct {
	CheckInterval time.Duration // Intervalo entre verificações (padrão: 2s)
	Timeout       time.Duration // Timeout para considerar servidor morto (padrão: 10s)
	MaxFailures   int           // Número máximo de falhas consecutivas (padrão: 3)
}

// DefaultHeartbeatConfig retorna a configuração padrão
func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{
		CheckInterval: 2 * time.Second,
		Timeout:       10 * time.Second,
		MaxFailures:   3,
	}
}

// NewHeartbeatSystem cria um novo sistema de heartbeat
func NewHeartbeatSystem(serverID string, config HeartbeatConfig) *HeartbeatSystem {
	if config.CheckInterval == 0 {
		config.CheckInterval = 2 * time.Second
	}
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}
	if config.MaxFailures == 0 {
		config.MaxFailures = 3
	}

	return &HeartbeatSystem{
		serverID:      serverID,
		servers:       make(map[string]*ServerStatus),
		checkInterval: config.CheckInterval,
		timeout:       config.Timeout,
		maxFailures:   config.MaxFailures,
		stopChan:      make(chan bool, 1),
		running:       false,
	}
}

// RegisterServer registra um servidor para monitoramento
func (hs *HeartbeatSystem) RegisterServer(serverID, address string) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	// Não registra o próprio servidor
	if serverID == hs.serverID {
		return
	}

	if _, exists := hs.servers[serverID]; !exists {
		hs.servers[serverID] = &ServerStatus{
			ServerID:      serverID,
			Address:       address,
			IsAlive:       true, // Assume que está vivo inicialmente
			LastHeartbeat: time.Now(),
			LastCheck:     time.Now(),
			FailureCount:  0,
		}
		log.Printf("[HEARTBEAT] Servidor %s registrado para monitoramento: %s", serverID, address)
	}
}

// UnregisterServer remove um servidor do monitoramento
func (hs *HeartbeatSystem) UnregisterServer(serverID string) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	delete(hs.servers, serverID)
	log.Printf("[HEARTBEAT] Servidor %s removido do monitoramento", serverID)
}

// Start inicia o sistema de heartbeat
func (hs *HeartbeatSystem) Start() {
	hs.mu.Lock()
	if hs.running {
		hs.mu.Unlock()
		log.Printf("[HEARTBEAT] Sistema já está rodando")
		return
	}
	hs.running = true
	hs.heartbeatTicker = time.NewTicker(hs.checkInterval)
	hs.mu.Unlock()

	log.Printf("[HEARTBEAT] Sistema iniciado (intervalo: %v, timeout: %v, max falhas: %d)",
		hs.checkInterval, hs.timeout, hs.maxFailures)

	// Goroutine para pingar servidores periodicamente
	go hs.heartbeatLoop()
}

// Stop para o sistema de heartbeat
func (hs *HeartbeatSystem) Stop() {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if !hs.running {
		return
	}

	hs.running = false
	if hs.heartbeatTicker != nil {
		hs.heartbeatTicker.Stop()
	}
	hs.stopChan <- true
	log.Printf("[HEARTBEAT] Sistema parado")
}

// heartbeatLoop é a goroutine principal que pinga os servidores periodicamente
func (hs *HeartbeatSystem) heartbeatLoop() {
	for {
		select {
		case <-hs.heartbeatTicker.C:
			hs.checkAllServers()

		case <-hs.stopChan:
			return
		}
	}
}

// checkAllServers verifica o status de todos os servidores registrados
func (hs *HeartbeatSystem) checkAllServers() {
	hs.mu.RLock()
	serversCopy := make(map[string]*ServerStatus)
	for k, v := range hs.servers {
		serversCopy[k] = v
	}
	hs.mu.RUnlock()

	for _, server := range serversCopy {
		go hs.checkServer(server)
	}
}

// checkServer verifica o status de um servidor específico
func (hs *HeartbeatSystem) checkServer(server *ServerStatus) {
	server.mu.Lock()
	server.LastCheck = time.Now()
	server.mu.Unlock()

	// Envia ping para o servidor
	alive := hs.pingServer(server.Address)

	server.mu.Lock()
	defer server.mu.Unlock()

	wasAlive := server.IsAlive

	if alive {
		// Servidor respondeu
		server.IsAlive = true
		server.LastHeartbeat = time.Now()
		server.FailureCount = 0

		if !wasAlive {
			// Servidor reviveu
			log.Printf("[HEARTBEAT] ✓ Servidor %s reviveu (resposta recebida)", server.ServerID)
			if hs.onServerRevive != nil {
				go hs.onServerRevive(server.ServerID)
			}
		} else {
			log.Printf("[HEARTBEAT] ✓ Servidor %s está vivo (ping: OK)", server.ServerID)
		}
	} else {
		// Servidor não respondeu
		server.FailureCount++
		log.Printf("[HEARTBEAT] ✗ Servidor %s não respondeu (falha %d/%d)",
			server.ServerID, server.FailureCount, hs.maxFailures)

		// Verifica se deve marcar como morto
		if server.FailureCount >= hs.maxFailures {
			if wasAlive {
				// Marca como morto
				server.IsAlive = false
				log.Printf("[HEARTBEAT] ✗ Servidor %s marcado como MORTO (falhas consecutivas: %d)",
					server.ServerID, server.FailureCount)
				if hs.onServerDeath != nil {
					go hs.onServerDeath(server.ServerID)
				}
			}
		}
	}
}

// pingServer envia um ping HTTP para o servidor e retorna true se respondeu
func (hs *HeartbeatSystem) pingServer(address string) bool {
	url := fmt.Sprintf("%s/api/health", address)

	client := &http.Client{
		Timeout: 3 * time.Second, // Timeout curto para pings
	}

	// Envia ping com ID do servidor para o servidor saber quem está pingando
	payload := map[string]string{
		"serverId": hs.serverID,
	}
	jsonPayload, _ := json.Marshal(payload)

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// IsServerAlive verifica se um servidor está vivo
func (hs *HeartbeatSystem) IsServerAlive(serverID string) bool {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	if server, exists := hs.servers[serverID]; exists {
		server.mu.RLock()
		defer server.mu.RUnlock()
		return server.IsAlive
	}

	// Servidor não registrado - assume que está vivo
	return true
}

// GetAliveServers retorna a lista de IDs dos servidores vivos
func (hs *HeartbeatSystem) GetAliveServers() []string {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	aliveServers := []string{}
	for _, server := range hs.servers {
		server.mu.RLock()
		if server.IsAlive {
			aliveServers = append(aliveServers, server.ServerID)
		}
		server.mu.RUnlock()
	}

	return aliveServers
}

// GetDeadServers retorna a lista de IDs dos servidores mortos
func (hs *HeartbeatSystem) GetDeadServers() []string {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	deadServers := []string{}
	for _, server := range hs.servers {
		server.mu.RLock()
		if !server.IsAlive {
			deadServers = append(deadServers, server.ServerID)
		}
		server.mu.RUnlock()
	}

	return deadServers
}

// GetServerStatus retorna o status de um servidor específico
func (hs *HeartbeatSystem) GetServerStatus(serverID string) (*ServerStatus, bool) {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	if server, exists := hs.servers[serverID]; exists {
		server.mu.RLock()
		defer server.mu.RUnlock()

		// Retorna uma cópia para evitar race conditions
		return &ServerStatus{
			ServerID:      server.ServerID,
			Address:       server.Address,
			IsAlive:       server.IsAlive,
			LastHeartbeat: server.LastHeartbeat,
			LastCheck:     server.LastCheck,
			FailureCount:  server.FailureCount,
		}, true
	}

	return nil, false
}

// GetAllServersStatus retorna o status de todos os servidores
func (hs *HeartbeatSystem) GetAllServersStatus() []*ServerStatus {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	statuses := []*ServerStatus{}
	for _, server := range hs.servers {
		server.mu.RLock()
		statuses = append(statuses, &ServerStatus{
			ServerID:      server.ServerID,
			Address:       server.Address,
			IsAlive:       server.IsAlive,
			LastHeartbeat: server.LastHeartbeat,
			LastCheck:     server.LastCheck,
			FailureCount:  server.FailureCount,
		})
		server.mu.RUnlock()
	}

	return statuses
}

// SetOnServerDeath registra um callback para quando um servidor morre
func (hs *HeartbeatSystem) SetOnServerDeath(callback func(serverID string)) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.onServerDeath = callback
}

// SetOnServerRevive registra um callback para quando um servidor revive
func (hs *HeartbeatSystem) SetOnServerRevive(callback func(serverID string)) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.onServerRevive = callback
}

// RecordHeartbeat registra um heartbeat recebido de outro servidor
// Esta função é chamada pelo endpoint de health quando recebe um ping
func (hs *HeartbeatSystem) RecordHeartbeat(serverID string) {
	hs.mu.RLock()
	server, exists := hs.servers[serverID]
	hs.mu.RUnlock()

	if exists {
		server.mu.Lock()
		server.LastHeartbeat = time.Now()
		server.mu.Unlock()
	}
}

// ToString retorna uma representação em string do sistema de heartbeat
func (hs *HeartbeatSystem) ToString() string {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	result := fmt.Sprintf("HeartbeatSystem{ServerID: %s, Running: %v, Servers: [\n",
		hs.serverID, hs.running)

	for _, server := range hs.servers {
		server.mu.RLock()
		status := "VIVO"
		if !server.IsAlive {
			status = "MORTO"
		}
		result += fmt.Sprintf("  %s (%s): %s (falhas: %d, último ping: %v)\n",
			server.ServerID, server.Address, status, server.FailureCount,
			time.Since(server.LastHeartbeat))
		server.mu.RUnlock()
	}

	result += "]}"
	return result
}
