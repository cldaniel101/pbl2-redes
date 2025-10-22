package consensus

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// TimeoutType define o tipo de timeout
type TimeoutType string

const (
	TimeoutACK   TimeoutType = "ACK"   // Timeout aguardando ACKs
	TimeoutCheck TimeoutType = "CHECK" // Timeout aguardando verificação
)

// TimeoutEvent representa um evento de timeout
type TimeoutEvent struct {
	OperationID string      // ID da operação que sofreu timeout
	Type        TimeoutType // Tipo do timeout
	StartTime   time.Time   // Momento em que a operação começou
	TimeoutAt   time.Time   // Momento em que o timeout ocorreu
	Duration    time.Duration
}

// TimeoutWatcher monitora timeouts de operações
type TimeoutWatcher struct {
	OperationID string      // ID da operação sendo monitorada
	Type        TimeoutType // Tipo de timeout
	Timeout     time.Duration
	StartTime   time.Time
	timer       *time.Timer
	onTimeout   func(*TimeoutEvent) // Callback quando timeout ocorre
	cancelled   bool
	mu          sync.Mutex
}

// TimeoutManager gerencia todos os timeouts de operações de consenso
type TimeoutManager struct {
	mu               sync.RWMutex
	watchers         map[string]*TimeoutWatcher // operationID -> watcher
	defaultACKTime   time.Duration              // Timeout padrão para ACKs (5s)
	defaultCheckTime time.Duration              // Timeout padrão para checks (5s)
	onTimeout        func(*TimeoutEvent)        // Callback global de timeout
	stats            TimeoutStats               // Estatísticas de timeouts
}

// TimeoutStats armazena estatísticas de timeouts
type TimeoutStats struct {
	TotalACKTimeouts   int // Total de timeouts de ACK
	TotalCheckTimeouts int // Total de timeouts de verificação
	mu                 sync.RWMutex
}

// NewTimeoutManager cria um novo gerenciador de timeouts
func NewTimeoutManager(defaultACKTime, defaultCheckTime time.Duration) *TimeoutManager {
	if defaultACKTime == 0 {
		defaultACKTime = 5 * time.Second
	}
	if defaultCheckTime == 0 {
		defaultCheckTime = 5 * time.Second
	}

	return &TimeoutManager{
		watchers:         make(map[string]*TimeoutWatcher),
		defaultACKTime:   defaultACKTime,
		defaultCheckTime: defaultCheckTime,
		stats:            TimeoutStats{},
	}
}

// WatchACK inicia o monitoramento de timeout para ACKs de uma operação
func (tm *TimeoutManager) WatchACK(operationID string, timeout time.Duration, onTimeout func(*TimeoutEvent)) {
	if timeout == 0 {
		timeout = tm.defaultACKTime
	}

	tm.startWatcher(operationID, TimeoutACK, timeout, onTimeout)
}

// WatchCheck inicia o monitoramento de timeout para verificação de uma operação
func (tm *TimeoutManager) WatchCheck(operationID string, timeout time.Duration, onTimeout func(*TimeoutEvent)) {
	if timeout == 0 {
		timeout = tm.defaultCheckTime
	}

	tm.startWatcher(operationID, TimeoutCheck, timeout, onTimeout)
}

// startWatcher cria e inicia um watcher de timeout
func (tm *TimeoutManager) startWatcher(operationID string, timeoutType TimeoutType, timeout time.Duration, onTimeout func(*TimeoutEvent)) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Cancela watcher existente se houver
	if existing, exists := tm.watchers[operationID]; exists {
		existing.Cancel()
	}

	watcher := &TimeoutWatcher{
		OperationID: operationID,
		Type:        timeoutType,
		Timeout:     timeout,
		StartTime:   time.Now(),
		onTimeout:   onTimeout,
		cancelled:   false,
	}

	// Cria timer que dispara o timeout
	watcher.timer = time.AfterFunc(timeout, func() {
		watcher.mu.Lock()
		if watcher.cancelled {
			watcher.mu.Unlock()
			return
		}
		watcher.mu.Unlock()

		// Cria evento de timeout
		event := &TimeoutEvent{
			OperationID: operationID,
			Type:        timeoutType,
			StartTime:   watcher.StartTime,
			TimeoutAt:   time.Now(),
			Duration:    time.Since(watcher.StartTime),
		}

		// Atualiza estatísticas
		tm.updateStats(timeoutType)

		// Log do timeout
		log.Printf("[TIMEOUT] ⏱️ Timeout de %s para operação %s após %v",
			timeoutType, operationID, event.Duration)

		// Chama callback específico do watcher
		if watcher.onTimeout != nil {
			watcher.onTimeout(event)
		}

		// Chama callback global
		if tm.onTimeout != nil {
			tm.onTimeout(event)
		}

		// Remove watcher da lista
		tm.mu.Lock()
		delete(tm.watchers, operationID)
		tm.mu.Unlock()
	})

	tm.watchers[operationID] = watcher

	log.Printf("[TIMEOUT] ⏱️ Monitorando %s para operação %s (timeout: %v)",
		timeoutType, operationID, timeout)
}

// Cancel cancela o monitoramento de timeout para uma operação
func (tm *TimeoutManager) Cancel(operationID string) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if watcher, exists := tm.watchers[operationID]; exists {
		watcher.Cancel()
		delete(tm.watchers, operationID)
		log.Printf("[TIMEOUT] ✓ Timeout cancelado para operação %s", operationID)
		return true
	}

	return false
}

// Cancel cancela um watcher individual
func (tw *TimeoutWatcher) Cancel() {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if !tw.cancelled {
		tw.cancelled = true
		if tw.timer != nil {
			tw.timer.Stop()
		}
	}
}

// IsActive verifica se um watcher está ativo para uma operação
func (tm *TimeoutManager) IsActive(operationID string) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	watcher, exists := tm.watchers[operationID]
	if !exists {
		return false
	}

	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return !watcher.cancelled
}

// GetActiveWatchers retorna o número de watchers ativos
func (tm *TimeoutManager) GetActiveWatchers() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.watchers)
}

// GetWatcherInfo retorna informações sobre um watcher específico
func (tm *TimeoutManager) GetWatcherInfo(operationID string) (TimeoutType, time.Duration, time.Duration, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	if watcher, exists := tm.watchers[operationID]; exists {
		watcher.mu.Lock()
		defer watcher.mu.Unlock()

		elapsed := time.Since(watcher.StartTime)
		remaining := watcher.Timeout - elapsed
		if remaining < 0 {
			remaining = 0
		}

		return watcher.Type, elapsed, remaining, true
	}

	return "", 0, 0, false
}

// SetOnTimeout registra um callback global para todos os timeouts
func (tm *TimeoutManager) SetOnTimeout(callback func(*TimeoutEvent)) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onTimeout = callback
}

// updateStats atualiza as estatísticas de timeouts
func (tm *TimeoutManager) updateStats(timeoutType TimeoutType) {
	tm.stats.mu.Lock()
	defer tm.stats.mu.Unlock()

	switch timeoutType {
	case TimeoutACK:
		tm.stats.TotalACKTimeouts++
	case TimeoutCheck:
		tm.stats.TotalCheckTimeouts++
	}
}

// GetStats retorna as estatísticas de timeouts
func (tm *TimeoutManager) GetStats() (ackTimeouts, checkTimeouts int) {
	tm.stats.mu.RLock()
	defer tm.stats.mu.RUnlock()
	return tm.stats.TotalACKTimeouts, tm.stats.TotalCheckTimeouts
}

// ResetStats reseta as estatísticas
func (tm *TimeoutManager) ResetStats() {
	tm.stats.mu.Lock()
	defer tm.stats.mu.Unlock()
	tm.stats.TotalACKTimeouts = 0
	tm.stats.TotalCheckTimeouts = 0
}

// CancelAll cancela todos os watchers ativos
func (tm *TimeoutManager) CancelAll() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	count := len(tm.watchers)
	for _, watcher := range tm.watchers {
		watcher.Cancel()
	}
	tm.watchers = make(map[string]*TimeoutWatcher)

	log.Printf("[TIMEOUT] ✓ Todos os %d watchers foram cancelados", count)
	return count
}

// ToString retorna uma representação em string do gerenciador de timeouts
func (tm *TimeoutManager) ToString() string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	ackTimeouts, checkTimeouts := tm.GetStats()

	result := "TimeoutManager{\n"
	result += fmt.Sprintf("  Watchers ativos: %d\n", len(tm.watchers))
	result += fmt.Sprintf("  Timeout padrão ACK: %v\n", tm.defaultACKTime)
	result += fmt.Sprintf("  Timeout padrão Check: %v\n", tm.defaultCheckTime)
	result += fmt.Sprintf("  Total de timeouts ACK: %d\n", ackTimeouts)
	result += fmt.Sprintf("  Total de timeouts Check: %d\n", checkTimeouts)
	result += "  Watchers: [\n"

	for opID, watcher := range tm.watchers {
		watcher.mu.Lock()
		elapsed := time.Since(watcher.StartTime)
		remaining := watcher.Timeout - elapsed
		result += fmt.Sprintf("    %s: %s (elapsed: %v, remaining: %v)\n",
			opID, watcher.Type, elapsed, remaining)
		watcher.mu.Unlock()
	}

	result += "  ]\n}"
	return result
}

// ========================================
// Helpers para criar timeouts com rollback automático
// ========================================

// WatchACKWithRollback monitora ACKs e executa rollback automático em caso de timeout
func (tm *TimeoutManager) WatchACKWithRollback(
	operationID string,
	timeout time.Duration,
	rollbackFunc func(operationID string, reason string),
) {
	tm.WatchACK(operationID, timeout, func(event *TimeoutEvent) {
		reason := fmt.Sprintf("timeout aguardando ACKs após %v", event.Duration)
		log.Printf("[TIMEOUT] 🔄 Iniciando rollback automático para operação %s: %s",
			operationID, reason)
		if rollbackFunc != nil {
			rollbackFunc(operationID, reason)
		}
	})
}

// WatchCheckWithRollback monitora verificação e executa rollback automático em caso de timeout
func (tm *TimeoutManager) WatchCheckWithRollback(
	operationID string,
	timeout time.Duration,
	rollbackFunc func(operationID string, reason string),
) {
	tm.WatchCheck(operationID, timeout, func(event *TimeoutEvent) {
		reason := fmt.Sprintf("timeout aguardando verificação após %v", event.Duration)
		log.Printf("[TIMEOUT] 🔄 Iniciando rollback automático para operação %s: %s",
			operationID, reason)
		if rollbackFunc != nil {
			rollbackFunc(operationID, reason)
		}
	})
}
