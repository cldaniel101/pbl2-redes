package consensus

import (
	"fmt"
	"time"
)

// OperationType define o tipo de operação
type OperationType string

const (
	OpTypePlay     OperationType = "PLAY"      // Jogada de carta
	OpTypeResolve  OperationType = "RESOLVE"   // Resolução de rodada
	OpTypeEndMatch OperationType = "END_MATCH" // Fim de partida
)

// Operation representa uma operação em uma partida distribuída
// Todas as operações são ordenadas por timestamp vetorial para garantir consistência
type Operation struct {
	// Identificação
	ID       string        // ID único da operação (UUID)
	Type     OperationType // Tipo da operação
	MatchID  string        // ID da partida
	ServerID string        // ID do servidor que criou a operação

	// Dados da operação
	PlayerID string // ID do jogador que executou a ação
	CardID   string // ID da carta jogada (apenas para PLAY)

	// Timestamp vetorial para ordenação
	Timestamp map[string]int // Relógio vetorial no momento da operação

	// Metadados
	CreatedAt time.Time // Timestamp real da criação (apenas para debug)
}

// NewOperation cria uma nova operação
func NewOperation(opType OperationType, matchID, serverID, playerID, cardID string, timestamp map[string]int) *Operation {
	return &Operation{
		ID:        generateOperationID(),
		Type:      opType,
		MatchID:   matchID,
		ServerID:  serverID,
		PlayerID:  playerID,
		CardID:    cardID,
		Timestamp: copyTimestamp(timestamp),
		CreatedAt: time.Now(),
	}
}

// CompareTimestamp compara o timestamp vetorial desta operação com outra
// Retorna:
//
//	-1 se esta operação aconteceu antes
//	 0 se são concorrentes
//	+1 se esta operação aconteceu depois
func (op *Operation) CompareTimestamp(other *Operation) int {
	lessOrEqual := true
	greaterOrEqual := true

	// Obtém todos os IDs de servidores
	allServers := make(map[string]bool)
	for serverID := range op.Timestamp {
		allServers[serverID] = true
	}
	for serverID := range other.Timestamp {
		allServers[serverID] = true
	}

	for serverID := range allServers {
		opTime := op.Timestamp[serverID]
		otherTime := other.Timestamp[serverID]

		if opTime > otherTime {
			lessOrEqual = false
		}
		if opTime < otherTime {
			greaterOrEqual = false
		}
	}

	if lessOrEqual && !greaterOrEqual {
		return -1
	}
	if greaterOrEqual && !lessOrEqual {
		return 1
	}

	// Se são concorrentes, usa o ServerID como desempate (ordem lexicográfica)
	// Isso garante ordem total determinística
	if op.ServerID < other.ServerID {
		return -1
	} else if op.ServerID > other.ServerID {
		return 1
	}

	return 0
}

// IsAfter verifica se esta operação aconteceu depois de outra
func (op *Operation) IsAfter(other *Operation) bool {
	return op.CompareTimestamp(other) > 0
}

// IsBefore verifica se esta operação aconteceu antes de outra
func (op *Operation) IsBefore(other *Operation) bool {
	return op.CompareTimestamp(other) < 0
}

// IsConcurrent verifica se esta operação é concorrente com outra
func (op *Operation) IsConcurrent(other *Operation) bool {
	return op.CompareTimestamp(other) == 0
}

// ToString retorna uma representação em string da operação
func (op *Operation) ToString() string {
	return fmt.Sprintf("Op{ID:%s, Type:%s, Match:%s, Server:%s, Player:%s, Card:%s, TS:%v}",
		op.ID, op.Type, op.MatchID, op.ServerID, op.PlayerID, op.CardID, op.Timestamp)
}

// Clone cria uma cópia da operação
func (op *Operation) Clone() *Operation {
	return &Operation{
		ID:        op.ID,
		Type:      op.Type,
		MatchID:   op.MatchID,
		ServerID:  op.ServerID,
		PlayerID:  op.PlayerID,
		CardID:    op.CardID,
		Timestamp: copyTimestamp(op.Timestamp),
		CreatedAt: op.CreatedAt,
	}
}

// copyTimestamp cria uma cópia de um timestamp vetorial
func copyTimestamp(ts map[string]int) map[string]int {
	copy := make(map[string]int)
	for k, v := range ts {
		copy[k] = v
	}
	return copy
}

// generateOperationID gera um ID único para a operação
// Por simplicidade, usamos timestamp + contador
// Em produção, seria melhor usar UUID
var operationCounter = 0

func generateOperationID() string {
	operationCounter++
	return fmt.Sprintf("op-%d-%d", time.Now().UnixNano(), operationCounter)
}
