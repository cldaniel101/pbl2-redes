package consensus

import (
	"fmt"
	"strings"
	"sync"
)

// VectorialClock representa um relógio vetorial para ordenação de eventos distribuídos
// Cada servidor mantém um timestamp vetorial com contadores para todos os servidores
type VectorialClock struct {
	mu      sync.RWMutex
	clocks  map[string]int // serverID -> timestamp
	ownerID string         // ID do servidor que possui este relógio
}

// NewVectorialClock cria um novo relógio vetorial
func NewVectorialClock(ownerID string, serverIDs []string) *VectorialClock {
	vc := &VectorialClock{
		clocks:  make(map[string]int),
		ownerID: ownerID,
	}

	// Inicializa todos os relógios com 0
	for _, serverID := range serverIDs {
		vc.clocks[serverID] = 0
	}

	return vc
}

// Tick incrementa o contador do próprio servidor
func (vc *VectorialClock) Tick() {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	vc.clocks[vc.ownerID]++
}

// Update atualiza o relógio vetorial com base em outro relógio (merge)
// Usado quando recebe uma mensagem de outro servidor
func (vc *VectorialClock) Update(other map[string]int) {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	// Para cada servidor, pega o máximo entre o timestamp local e o recebido
	for serverID, otherTimestamp := range other {
		if currentTimestamp, exists := vc.clocks[serverID]; exists {
			if otherTimestamp > currentTimestamp {
				vc.clocks[serverID] = otherTimestamp
			}
		} else {
			// Se for um novo servidor, adiciona
			vc.clocks[serverID] = otherTimestamp
		}
	}

	// Incrementa o próprio contador após o merge
	vc.clocks[vc.ownerID]++
}

// Compare compara dois relógios vetoriais
// Retorna:
//
//	-1 se vc aconteceu antes de other (vc < other)
//	 0 se são concorrentes (incomparáveis)
//	+1 se vc aconteceu depois de other (vc > other)
func (vc *VectorialClock) Compare(other map[string]int) int {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	lessOrEqual := true    // vc <= other
	greaterOrEqual := true // vc >= other

	// Verifica todos os servidores
	allServers := make(map[string]bool)
	for serverID := range vc.clocks {
		allServers[serverID] = true
	}
	for serverID := range other {
		allServers[serverID] = true
	}

	for serverID := range allServers {
		vcTime := vc.clocks[serverID]
		otherTime := other[serverID]

		if vcTime > otherTime {
			lessOrEqual = false
		}
		if vcTime < otherTime {
			greaterOrEqual = false
		}
	}

	// vc aconteceu antes se vc <= other e existe pelo menos um timestamp menor
	if lessOrEqual && !greaterOrEqual {
		return -1
	}

	// vc aconteceu depois se vc >= other e existe pelo menos um timestamp maior
	if greaterOrEqual && !lessOrEqual {
		return 1
	}

	// Se vc == other (todos timestamps iguais), considera igual (retorna 0)
	// Se são incomparáveis (alguns maiores, outros menores), também retorna 0
	return 0
}

// GetSnapshot retorna uma cópia do estado atual do relógio
func (vc *VectorialClock) GetSnapshot() map[string]int {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	snapshot := make(map[string]int)
	for serverID, timestamp := range vc.clocks {
		snapshot[serverID] = timestamp
	}

	return snapshot
}

// ToString retorna uma representação em string do relógio vetorial
func (vc *VectorialClock) ToString() string {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	parts := []string{}
	for serverID, timestamp := range vc.clocks {
		parts = append(parts, fmt.Sprintf("%s:%d", serverID, timestamp))
	}

	return fmt.Sprintf("{%s}", strings.Join(parts, ", "))
}

// GetValue retorna o valor do timestamp para um servidor específico
func (vc *VectorialClock) GetValue(serverID string) int {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	return vc.clocks[serverID]
}

// SetValue define o valor do timestamp para um servidor específico (usado em testes)
func (vc *VectorialClock) SetValue(serverID string, value int) {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	vc.clocks[serverID] = value
}

// Clone cria uma cópia do relógio vetorial
func (vc *VectorialClock) Clone() *VectorialClock {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	clone := &VectorialClock{
		clocks:  make(map[string]int),
		ownerID: vc.ownerID,
	}

	for serverID, timestamp := range vc.clocks {
		clone.clocks[serverID] = timestamp
	}

	return clone
}
