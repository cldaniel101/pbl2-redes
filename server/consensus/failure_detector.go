package consensus

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// FailureDetector combina heartbeat e timeout para detecção abrangente de falhas
type FailureDetector struct {
	mu               sync.RWMutex
	serverID         string
	heartbeatSystem  *HeartbeatSystem
	timeoutManager   *TimeoutManager
	onOperationFail  func(operationID, serverID, reason string) // Callback para falha de operação
	onServerFail     func(serverID string)                      // Callback para falha de servidor
	operationServers map[string][]string                        // operationID -> lista de servidores envolvidos
	failedOperations map[string]string                          // operationID -> reason
	running          bool
}

// FailureDetectorConfig contém as configurações do detector de falhas
type FailureDetectorConfig struct {
	HeartbeatConfig HeartbeatConfig
	ACKTimeout      time.Duration
	CheckTimeout    time.Duration
}

// DefaultFailureDetectorConfig retorna a configuração padrão
func DefaultFailureDetectorConfig() FailureDetectorConfig {
	return FailureDetectorConfig{
		HeartbeatConfig: DefaultHeartbeatConfig(),
		ACKTimeout:      5 * time.Second,
		CheckTimeout:    5 * time.Second,
	}
}

// NewFailureDetector cria um novo detector de falhas
func NewFailureDetector(serverID string, config FailureDetectorConfig) *FailureDetector {
	heartbeatSystem := NewHeartbeatSystem(serverID, config.HeartbeatConfig)
	timeoutManager := NewTimeoutManager(config.ACKTimeout, config.CheckTimeout)

	fd := &FailureDetector{
		serverID:         serverID,
		heartbeatSystem:  heartbeatSystem,
		timeoutManager:   timeoutManager,
		operationServers: make(map[string][]string),
		failedOperations: make(map[string]string),
		running:          false,
	}

	// Configura callbacks do heartbeat
	heartbeatSystem.SetOnServerDeath(func(deadServerID string) {
		fd.handleServerDeath(deadServerID)
	})

	heartbeatSystem.SetOnServerRevive(func(revivedServerID string) {
		fd.handleServerRevive(revivedServerID)
	})

	return fd
}

// Start inicia o detector de falhas
func (fd *FailureDetector) Start() {
	fd.mu.Lock()
	if fd.running {
		fd.mu.Unlock()
		log.Printf("[FAILURE_DETECTOR] Detector já está rodando")
		return
	}
	fd.running = true
	fd.mu.Unlock()

	log.Printf("[FAILURE_DETECTOR] ⚡ Detector de falhas iniciado para servidor %s", fd.serverID)
	fd.heartbeatSystem.Start()
}

// Stop para o detector de falhas
func (fd *FailureDetector) Stop() {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	if !fd.running {
		return
	}

	fd.running = false
	fd.heartbeatSystem.Stop()
	fd.timeoutManager.CancelAll()

	log.Printf("[FAILURE_DETECTOR] Detector de falhas parado")
}

// RegisterServer registra um servidor para monitoramento
func (fd *FailureDetector) RegisterServer(serverID, address string) {
	fd.heartbeatSystem.RegisterServer(serverID, address)
}

// UnregisterServer remove um servidor do monitoramento
func (fd *FailureDetector) UnregisterServer(serverID string) {
	fd.heartbeatSystem.UnregisterServer(serverID)
}

// ========================================
// Monitoramento de Operações
// ========================================

// TrackOperation inicia o rastreamento de uma operação com os servidores envolvidos
func (fd *FailureDetector) TrackOperation(operationID string, serverIDs []string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	fd.operationServers[operationID] = serverIDs
	log.Printf("[FAILURE_DETECTOR] 📝 Rastreando operação %s com servidores: %v", operationID, serverIDs)
}

// UntrackOperation para o rastreamento de uma operação (sucesso)
func (fd *FailureDetector) UntrackOperation(operationID string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	delete(fd.operationServers, operationID)
	delete(fd.failedOperations, operationID)
	log.Printf("[FAILURE_DETECTOR] ✓ Operação %s concluída com sucesso", operationID)
}

// MarkOperationFailed marca uma operação como falha
func (fd *FailureDetector) MarkOperationFailed(operationID, reason string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	fd.failedOperations[operationID] = reason
	log.Printf("[FAILURE_DETECTOR] ✗ Operação %s falhou: %s", operationID, reason)
}

// IsOperationFailed verifica se uma operação falhou
func (fd *FailureDetector) IsOperationFailed(operationID string) (bool, string) {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	if reason, exists := fd.failedOperations[operationID]; exists {
		return true, reason
	}
	return false, ""
}

// ========================================
// Monitoramento de ACKs com Timeout
// ========================================

// WatchACKs inicia o monitoramento de ACKs com timeout e rollback automático
func (fd *FailureDetector) WatchACKs(
	operationID string,
	serverIDs []string,
	timeout time.Duration,
	rollbackFunc func(operationID string, reason string),
) {
	// Registra os servidores envolvidos
	fd.TrackOperation(operationID, serverIDs)

	// Inicia timeout com rollback automático
	fd.timeoutManager.WatchACKWithRollback(operationID, timeout, func(opID, reason string) {
		// Marca operação como falha
		fd.MarkOperationFailed(opID, reason)

		// Executa rollback
		if rollbackFunc != nil {
			rollbackFunc(opID, reason)
		}

		// Notifica callback global
		if fd.onOperationFail != nil {
			fd.onOperationFail(opID, "", reason)
		}
	})
}

// ACKReceived notifica que um ACK foi recebido de um servidor
func (fd *FailureDetector) ACKReceived(operationID, serverID string) {
	log.Printf("[FAILURE_DETECTOR] ✓ ACK recebido de %s para operação %s", serverID, operationID)

	// Verifica se o servidor está vivo
	if !fd.heartbeatSystem.IsServerAlive(serverID) {
		log.Printf("[FAILURE_DETECTOR] ⚠️ ACK recebido de servidor morto %s para operação %s",
			serverID, operationID)
	}
}

// AllACKsReceived cancela o timeout de ACKs quando todos forem recebidos
func (fd *FailureDetector) AllACKsReceived(operationID string) {
	fd.timeoutManager.Cancel(operationID)
	log.Printf("[FAILURE_DETECTOR] ✓ Todos os ACKs recebidos para operação %s", operationID)
}

// ========================================
// Monitoramento de Verificações com Timeout
// ========================================

// WatchCheck inicia o monitoramento de verificação com timeout e rollback automático
func (fd *FailureDetector) WatchCheck(
	operationID string,
	timeout time.Duration,
	rollbackFunc func(operationID string, reason string),
) {
	fd.timeoutManager.WatchCheckWithRollback(operationID, timeout, func(opID, reason string) {
		// Marca operação como falha
		fd.MarkOperationFailed(opID, reason)

		// Executa rollback
		if rollbackFunc != nil {
			rollbackFunc(opID, reason)
		}

		// Notifica callback global
		if fd.onOperationFail != nil {
			fd.onOperationFail(opID, "", reason)
		}
	})
}

// CheckCompleted cancela o timeout de verificação quando concluída
func (fd *FailureDetector) CheckCompleted(operationID string) {
	fd.timeoutManager.Cancel(operationID)
	log.Printf("[FAILURE_DETECTOR] ✓ Verificação concluída para operação %s", operationID)
}

// ========================================
// Tratamento de Falhas de Servidor
// ========================================

// handleServerDeath lida com a morte de um servidor
func (fd *FailureDetector) handleServerDeath(deadServerID string) {
	log.Printf("[FAILURE_DETECTOR] ☠️ Servidor %s morreu - verificando operações afetadas", deadServerID)

	fd.mu.RLock()
	affectedOps := []string{}
	for opID, servers := range fd.operationServers {
		for _, serverID := range servers {
			if serverID == deadServerID {
				affectedOps = append(affectedOps, opID)
				break
			}
		}
	}
	fd.mu.RUnlock()

	// Marca operações afetadas como falhas
	reason := fmt.Sprintf("servidor %s morreu", deadServerID)
	for _, opID := range affectedOps {
		fd.MarkOperationFailed(opID, reason)
		log.Printf("[FAILURE_DETECTOR] ✗ Operação %s afetada pela morte de %s", opID, deadServerID)

		// Notifica callback
		if fd.onOperationFail != nil {
			fd.onOperationFail(opID, deadServerID, reason)
		}
	}

	// Notifica callback de falha de servidor
	if fd.onServerFail != nil {
		fd.onServerFail(deadServerID)
	}

	log.Printf("[FAILURE_DETECTOR] Total de operações afetadas pela morte de %s: %d",
		deadServerID, len(affectedOps))
}

// handleServerRevive lida com a revivência de um servidor
func (fd *FailureDetector) handleServerRevive(revivedServerID string) {
	log.Printf("[FAILURE_DETECTOR] ✨ Servidor %s reviveu", revivedServerID)
	// Por enquanto apenas loga, mas pode implementar lógica de recuperação no futuro
}

// ========================================
// Consultas de Estado
// ========================================

// IsServerAlive verifica se um servidor está vivo
func (fd *FailureDetector) IsServerAlive(serverID string) bool {
	return fd.heartbeatSystem.IsServerAlive(serverID)
}

// GetAliveServers retorna a lista de servidores vivos
func (fd *FailureDetector) GetAliveServers() []string {
	return fd.heartbeatSystem.GetAliveServers()
}

// GetDeadServers retorna a lista de servidores mortos
func (fd *FailureDetector) GetDeadServers() []string {
	return fd.heartbeatSystem.GetDeadServers()
}

// GetServerStatus retorna o status de um servidor
func (fd *FailureDetector) GetServerStatus(serverID string) (*ServerStatus, bool) {
	return fd.heartbeatSystem.GetServerStatus(serverID)
}

// GetAllServersStatus retorna o status de todos os servidores
func (fd *FailureDetector) GetAllServersStatus() []*ServerStatus {
	return fd.heartbeatSystem.GetAllServersStatus()
}

// GetActiveOperations retorna a lista de operações sendo rastreadas
func (fd *FailureDetector) GetActiveOperations() []string {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	operations := []string{}
	for opID := range fd.operationServers {
		operations = append(operations, opID)
	}
	return operations
}

// GetFailedOperations retorna a lista de operações que falharam
func (fd *FailureDetector) GetFailedOperations() map[string]string {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	// Retorna uma cópia
	failed := make(map[string]string)
	for opID, reason := range fd.failedOperations {
		failed[opID] = reason
	}
	return failed
}

// GetStats retorna estatísticas do detector de falhas
func (fd *FailureDetector) GetStats() map[string]interface{} {
	ackTimeouts, checkTimeouts := fd.timeoutManager.GetStats()
	aliveServers := len(fd.GetAliveServers())
	deadServers := len(fd.GetDeadServers())

	fd.mu.RLock()
	activeOps := len(fd.operationServers)
	failedOps := len(fd.failedOperations)
	fd.mu.RUnlock()

	return map[string]interface{}{
		"server_id":         fd.serverID,
		"running":           fd.running,
		"alive_servers":     aliveServers,
		"dead_servers":      deadServers,
		"active_operations": activeOps,
		"failed_operations": failedOps,
		"ack_timeouts":      ackTimeouts,
		"check_timeouts":    checkTimeouts,
		"active_watchers":   fd.timeoutManager.GetActiveWatchers(),
	}
}

// ========================================
// Callbacks
// ========================================

// SetOnOperationFail registra um callback para quando uma operação falha
func (fd *FailureDetector) SetOnOperationFail(callback func(operationID, serverID, reason string)) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.onOperationFail = callback
}

// SetOnServerFail registra um callback para quando um servidor falha
func (fd *FailureDetector) SetOnServerFail(callback func(serverID string)) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.onServerFail = callback
}

// ========================================
// Debug e Informações
// ========================================

// ToString retorna uma representação em string do detector de falhas
func (fd *FailureDetector) ToString() string {
	stats := fd.GetStats()
	aliveServers := fd.GetAliveServers()
	deadServers := fd.GetDeadServers()
	activeOps := fd.GetActiveOperations()
	failedOps := fd.GetFailedOperations()

	result := "FailureDetector{\n"
	result += fmt.Sprintf("  ServerID: %s\n", fd.serverID)
	result += fmt.Sprintf("  Running: %v\n", stats["running"])
	result += "  \n"
	result += "  Servidores:\n"
	result += fmt.Sprintf("    Vivos: %d - %v\n", len(aliveServers), aliveServers)
	result += fmt.Sprintf("    Mortos: %d - %v\n", len(deadServers), deadServers)
	result += "  \n"
	result += "  Operações:\n"
	result += fmt.Sprintf("    Ativas: %d - %v\n", len(activeOps), activeOps)
	result += fmt.Sprintf("    Falhadas: %d\n", len(failedOps))
	for opID, reason := range failedOps {
		result += fmt.Sprintf("      %s: %s\n", opID, reason)
	}
	result += "  \n"
	result += "  Timeouts:\n"
	result += fmt.Sprintf("    ACK: %d\n", stats["ack_timeouts"])
	result += fmt.Sprintf("    Check: %d\n", stats["check_timeouts"])
	result += fmt.Sprintf("    Watchers ativos: %d\n", stats["active_watchers"])
	result += "}"

	return result
}

// RecordHeartbeat registra um heartbeat recebido (chamado pelo endpoint de health)
func (fd *FailureDetector) RecordHeartbeat(serverID string) {
	fd.heartbeatSystem.RecordHeartbeat(serverID)
}
