package token

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

// Token representa o token que circula entre os servidores
// Contém o stack global de cartas disponíveis
type Token struct {
	CardPool   []string            `json:"cardPool"`   // Pool de IDs de cartas disponíveis
	AllCards   map[string]CardInfo `json:"allCards"`   // Mapa de todas as cartas (para reabastecimento)
	Timestamp  int64               `json:"timestamp"`  // Timestamp da última atualização
	ServerAddr string              `json:"serverAddr"` // Endereço do servidor que possui o token
	mu         sync.Mutex          `json:"-"`          // Mutex para operações thread-safe
}

// CardInfo representa informações básicas de uma carta
type CardInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Element string `json:"element"`
	ATK     int    `json:"atk"`
	DEF     int    `json:"def"`
}

// NewToken cria um novo token vazio
func NewToken(serverAddr string) *Token {
	return &Token{
		CardPool:   make([]string, 0),
		AllCards:   make(map[string]CardInfo),
		Timestamp:  time.Now().UnixNano(),
		ServerAddr: serverAddr,
	}
}

// LoadCardsFromJSON carrega as cartas de um JSON e popula o pool
func (t *Token) LoadCardsFromJSON(data []byte, copiesPerCard int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var cards []CardInfo
	if err := json.Unmarshal(data, &cards); err != nil {
		return fmt.Errorf("erro ao decodificar JSON das cartas: %w", err)
	}

	// Limpa o pool e o mapa de cartas
	t.CardPool = make([]string, 0)
	t.AllCards = make(map[string]CardInfo)

	// Adiciona todas as cartas ao mapa
	for _, card := range cards {
		t.AllCards[card.ID] = card
	}

	// Popula o pool com múltiplas cópias de cada carta
	for _, card := range cards {
		for i := 0; i < copiesPerCard; i++ {
			t.CardPool = append(t.CardPool, card.ID)
		}
	}

	// Embaralha o pool
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(t.CardPool), func(i, j int) {
		t.CardPool[i], t.CardPool[j] = t.CardPool[j], t.CardPool[i]
	})

	log.Printf("[TOKEN] Carregadas %d cartas únicas, %d cartas totais no pool", len(t.AllCards), len(t.CardPool))
	return nil
}

// DrawCards remove e retorna N cartas do pool
// Retorna erro se não houver cartas suficientes
func (t *Token) DrawCards(count int) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Verifica se há cartas suficientes
	if len(t.CardPool) < count {
		// Tenta reabastecer o pool
		log.Printf("[TOKEN] Pool insuficiente (%d cartas), reabastecendo...", len(t.CardPool))
		t.refillPool_unsafe()
	}

	// Verifica novamente após reabastecimento
	if len(t.CardPool) < count {
		return nil, fmt.Errorf("pool insuficiente: %d cartas disponíveis, %d solicitadas", len(t.CardPool), count)
	}

	// Remove as cartas do pool
	drawnCards := make([]string, count)
	copy(drawnCards, t.CardPool[:count])
	t.CardPool = t.CardPool[count:]

	log.Printf("[TOKEN] %d cartas retiradas do pool. Restantes: %d", count, len(t.CardPool))
	return drawnCards, nil
}

// refillPool_unsafe reabastece o pool com todas as cartas disponíveis
// ATENÇÃO: Este método NÃO é thread-safe, deve ser chamado apenas com o lock já adquirido
func (t *Token) refillPool_unsafe() {
	const copiesPerCard = 100 // Número de cópias de cada carta a adicionar

	// Cria um novo pool com múltiplas cópias de cada carta
	newCards := make([]string, 0, len(t.AllCards)*copiesPerCard)
	for cardID := range t.AllCards {
		for i := 0; i < copiesPerCard; i++ {
			newCards = append(newCards, cardID)
		}
	}

	// Embaralha as novas cartas
	rand.Shuffle(len(newCards), func(i, j int) {
		newCards[i], newCards[j] = newCards[j], newCards[i]
	})

	// Adiciona as novas cartas ao pool existente
	t.CardPool = append(t.CardPool, newCards...)
	log.Printf("[TOKEN] Pool reabastecido. Total de cartas agora: %d", len(t.CardPool))
}

// GetPoolSize retorna o tamanho atual do pool
func (t *Token) GetPoolSize() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.CardPool)
}

// UpdateServerAddr atualiza o endereço do servidor que possui o token
func (t *Token) UpdateServerAddr(addr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ServerAddr = addr
	t.Timestamp = time.Now().UnixNano()
}

// ToJSON serializa o token para JSON
func (t *Token) ToJSON() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return json.Marshal(t)
}

// FromJSON deserializa o token de JSON
func FromJSON(data []byte) (*Token, error) {
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("erro ao deserializar token: %w", err)
	}
	return &t, nil
}

// Clone cria uma cópia profunda do token
func (t *Token) Clone() *Token {
	t.mu.Lock()
	defer t.mu.Unlock()

	newToken := &Token{
		CardPool:   make([]string, len(t.CardPool)),
		AllCards:   make(map[string]CardInfo),
		Timestamp:  time.Now().UnixNano(),
		ServerAddr: t.ServerAddr,
	}

	copy(newToken.CardPool, t.CardPool)
	for k, v := range t.AllCards {
		newToken.AllCards[k] = v
	}

	return newToken
}

// GetCardInfo retorna informações sobre uma carta específica
func (t *Token) GetCardInfo(cardID string) (CardInfo, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	info, exists := t.AllCards[cardID]
	return info, exists
}
