package packs

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

var (
	ErrOutOfStock = errors.New("estoque esgotado")
)

// PackStore gerencia o estoque global de pacotes de cartas
type PackStore struct {
	mu           sync.Mutex
	Stock        int
	Rng          *rand.Rand
	RarityTable  []string // IDs das cartas ponderados simples para MVP
	CardsPerPack int
	AuditLog     []PackAudit
}

// PackAudit representa um log de auditoria de abertura de pacote
type PackAudit struct {
	PackID    string    `json:"packId"`
	PlayerID  string    `json:"playerId"`
	Cards     []string  `json:"cards"`
	Timestamp time.Time `json:"timestamp"`
}

// PlayerConn interface simplificada para o sistema de packs
type PlayerConn interface {
	GetID() string
}

// NewPackStore cria uma nova instância do PackStore
func NewPackStore(stock int, cardsPerPack int, rarityTable []string, seed int64) *PackStore {
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	return &PackStore{
		Stock:        stock,
		CardsPerPack: cardsPerPack,
		RarityTable:  rarityTable,
		Rng:          rand.New(rand.NewSource(seed)),
		AuditLog:     make([]PackAudit, 0),
	}
}

// OpenPack tenta abrir um pacote para um jogador (operação atômica)
func (ps *PackStore) OpenPack(player PlayerConn) ([]string, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Verifica se há estoque
	if ps.Stock <= 0 {
		return nil, ErrOutOfStock
	}

	// Reserva uma unidade (decremento atômico)
	ps.Stock--

	// Sorteia N=3 cardIds
	cards := make([]string, ps.CardsPerPack)
	for i := 0; i < ps.CardsPerPack; i++ {
		if len(ps.RarityTable) > 0 {
			cards[i] = ps.RarityTable[ps.Rng.Intn(len(ps.RarityTable))]
		} else {
			cards[i] = fmt.Sprintf("c_%03d", ps.Rng.Intn(9)+1) // fallback
		}
	}

	// Garantir que não há IDs duplicados no mesmo pack
	cards = removeDuplicates(cards, ps.RarityTable, ps.Rng)

	// Log de auditoria
	packID := fmt.Sprintf("pack_%d_%d", time.Now().Unix(), ps.Rng.Int63())
	audit := PackAudit{
		PackID:    packID,
		PlayerID:  player.GetID(),
		Cards:     cards,
		Timestamp: time.Now(),
	}
	ps.AuditLog = append(ps.AuditLog, audit)

	return cards, nil
}

// removeDuplicates garante que não há cartas duplicadas no pack
func removeDuplicates(cards []string, rarityTable []string, rng *rand.Rand) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(cards))

	for _, card := range cards {
		if !seen[card] {
			seen[card] = true
			result = append(result, card)
		}
	}

	// Se removemos duplicatas, precisamos preencher até o tamanho original
	for len(result) < len(cards) {
		attempts := 0
		for attempts < 100 && len(result) < len(cards) {
			var newCard string
			if len(rarityTable) > 0 {
				newCard = rarityTable[rng.Intn(len(rarityTable))]
			} else {
				newCard = fmt.Sprintf("c_%03d", rng.Intn(9)+1)
			}

			if !seen[newCard] {
				seen[newCard] = true
				result = append(result, newCard)
			}
			attempts++
		}
		// Se não conseguiu encontrar cartas únicas suficientes, para
		if attempts >= 100 {
			break
		}
	}

	return result
}

// GetStock retorna o estoque atual de forma thread-safe
func (ps *PackStore) GetStock() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.Stock
}

// GetAuditLog retorna uma cópia do log de auditoria
func (ps *PackStore) GetAuditLog() []PackAudit {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	result := make([]PackAudit, len(ps.AuditLog))
	copy(result, ps.AuditLog)
	return result
}

// SetStock define o estoque (usado principalmente para testes)
func (ps *PackStore) SetStock(stock int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.Stock = stock
}
