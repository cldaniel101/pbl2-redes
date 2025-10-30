package token

import (
    "encoding/json"
    "fmt"
    "log"
    "math/rand"
    "sync"
    "time"
)

// Token representa o token que circula no anel, carregando um estoque de cartas
// e o endereço do servidor "dono" atual.
type Token struct {
    mu         sync.Mutex
    ServerAddr string   `json:"serverAddr"`
    CardPool   []string `json:"cardPool"`
    Timestamp  int64    `json:"ts"`
}

// New cria um novo token com um pool inicial de cartas.
func New(serverAddr string, initialPool []string) *Token {
    return &Token{
        ServerAddr: serverAddr,
        CardPool:   append([]string{}, initialPool...),
        Timestamp:  time.Now().UnixNano(),
    }
}

// UpdateServerAddr atualiza o endereço do servidor no token e renova o timestamp.
func (t *Token) UpdateServerAddr(addr string) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.ServerAddr = addr
    t.Timestamp = time.Now().UnixNano()
}

// ToJSON serializa o token para JSON.
func (t *Token) ToJSON() ([]byte, error) {
    t.mu.Lock()
    defer t.mu.Unlock()
    return json.Marshal(t)
}

// FromJSON desserializa JSON num Token.
func FromJSON(b []byte) (*Token, error) {
    var tok Token
    if err := json.Unmarshal(b, &tok); err != nil {
        return nil, err
    }
    return &tok, nil
}

// DrawCards remove e retorna N cartas do pool. Reabastece se necessário.
func (t *Token) DrawCards(count int) ([]string, error) {
    t.mu.Lock()
    defer t.mu.Unlock()

    if len(t.CardPool) < count {
        log.Printf("[TOKEN] Pool insuficiente (%d cartas), reabastecendo...", len(t.CardPool))
        t.refillPoolUnsafe()
    }

    if len(t.CardPool) < count {
        return nil, fmt.Errorf("pool insuficiente: %d cartas disponiveis, %d solicitadas", len(t.CardPool), count)
    }

    drawn := make([]string, count)
    copy(drawn, t.CardPool[:count])
    t.CardPool = t.CardPool[count:]
    log.Printf("[TOKEN] %d cartas retiradas do pool. Restantes: %d", count, len(t.CardPool))
    return drawn, nil
}

// refillPoolUnsafe reabastece o pool com cartas pseudo-aleatorias.
// Esta  uma vers3o simples que gera IDs sint9ticos quando o DB n3o est1 acessdvel aqui.
func (t *Token) refillPoolUnsafe() {
    // Reabastece com um lote pequeno para n3o crescer indefinidamente
    const batch = 50
    for i := 0; i < batch; i++ {
        // IDs sint9ticos (alternativamente, poderdamos integrar um CardDB aqui)
        t.CardPool = append(t.CardPool, randomCardID())
    }
    t.Timestamp = time.Now().UnixNano()
}

func randomCardID() string {
    // Exemplo simples: CARD_001..CARD_100
    n := rand.Intn(100) + 1
    return fmt.Sprintf("CARD_%03d", n)
}


