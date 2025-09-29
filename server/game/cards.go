package game

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"sync"
	"time"
)

// CardDB representa o banco de dados de cartas em memória
type CardDB struct {
	cards map[string]Card
	pool  []string // IDs das cartas para sorteio
	mu    sync.RWMutex
}

// NewCardDB cria um novo banco de dados de cartas
func NewCardDB() *CardDB {
	return &CardDB{
		cards: make(map[string]Card),
		pool:  make([]string, 0),
	}
}

// LoadFromFile carrega as cartas de um arquivo JSON
func (db *CardDB) LoadFromFile(filename string) error {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("erro ao ler arquivo de cartas: %w", err)
	}

	var cards []Card
	if err := json.Unmarshal(data, &cards); err != nil {
		return fmt.Errorf("erro ao decodificar JSON das cartas: %w", err)
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	for _, card := range cards {
		db.cards[card.ID] = card
		db.pool = append(db.pool, card.ID)
	}

	return nil
}

// GetCard retorna uma carta pelo ID
func (db *CardDB) GetCard(id string) (Card, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	card, exists := db.cards[id]
	return card, exists
}

// GetRandomCard retorna uma carta aleatória do pool
func (db *CardDB) GetRandomCard() string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if len(db.pool) == 0 {
		return ""
	}

	return db.pool[rand.Intn(len(db.pool))]
}

// GenerateHand gera uma mão inicial com cartas aleatórias
func (db *CardDB) GenerateHand(size int) Hand {
	hand := make(Hand, size)
	for i := 0; i < size; i++ {
		hand[i] = db.GetRandomCard()
	}
	return hand
}

// ValidateCard verifica se um ID de carta é válido
func (db *CardDB) ValidateCard(id string) bool {
	db.mu.RLock()
	defer db.mu.RUnlock()

	_, exists := db.cards[id]
	return exists
}

// GetAllCards retorna todas as cartas (para debug/admin)
func (db *CardDB) GetAllCards() map[string]Card {
	db.mu.RLock()
	defer db.mu.RUnlock()

	result := make(map[string]Card)
	for id, card := range db.cards {
		result[id] = card
	}
	return result
}

// PackSystem gerencia o sistema de pacotes de cartas
type PackSystem struct {
	stock    int
	config   PackConfig
	cardDB   *CardDB
	mu       sync.Mutex
	rng      *rand.Rand
	auditLog []PackAudit
}

// PackAudit representa um log de auditoria de abertura de pacote
type PackAudit struct {
	PackID    string    `json:"packId"`
	PlayerID  string    `json:"playerId"`
	Cards     []string  `json:"cards"`
	Timestamp time.Time `json:"timestamp"`
}

// NewPackSystem cria um novo sistema de pacotes
func NewPackSystem(config PackConfig, cardDB *CardDB) *PackSystem {
	seed := config.RNGSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	return &PackSystem{
		stock:    config.Stock,
		config:   config,
		cardDB:   cardDB,
		rng:      rand.New(rand.NewSource(seed)),
		auditLog: make([]PackAudit, 0),
	}
}

// OpenPack tenta abrir um pacote para um jogador (operação atômica)
func (ps *PackSystem) OpenPack(playerID string) ([]string, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Verifica se há estoque
	if ps.stock <= 0 {
		return nil, fmt.Errorf("estoque esgotado")
	}

	// Reserva uma unidade (decremento atômico)
	ps.stock--

	// Sorteia cartas
	cards := make([]string, ps.config.CardsPerPack)
	for i := 0; i < ps.config.CardsPerPack; i++ {
		cards[i] = ps.cardDB.GetRandomCard()
	}

	// Log de auditoria
	packID := fmt.Sprintf("pack_%d_%d", time.Now().Unix(), ps.rng.Int63())
	audit := PackAudit{
		PackID:    packID,
		PlayerID:  playerID,
		Cards:     cards,
		Timestamp: time.Now(),
	}
	ps.auditLog = append(ps.auditLog, audit)

	return cards, nil
}

// GetStock retorna o estoque atual
func (ps *PackSystem) GetStock() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.stock
}

// GetAuditLog retorna o log de auditoria (para admin/debug)
func (ps *PackSystem) GetAuditLog() []PackAudit {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	result := make([]PackAudit, len(ps.auditLog))
	copy(result, ps.auditLog)
	return result
}
