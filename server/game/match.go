package game

import (
	"fmt"
	"log"
	"math/rand"
	"pingpong/server/protocol"
	"sync"
	"time"
)

// SenderFunc é um tipo para a função de envio de mensagens
type SenderFunc func(player *protocol.PlayerConn, msg protocol.ServerMsg)

// Match representa uma partida 1v1
type Match struct {
	ID       string
	P1       *protocol.PlayerConn
	P2       *protocol.PlayerConn
	HP       [2]int
	Hands    [2]Hand
	Discard  [2][]string
	Round    int
	State    MatchState
	Waiting  map[string]string
	Deadline time.Time
	CardDB   *CardDB
	mu       sync.Mutex
	done     chan bool
	sender   SenderFunc // Armazena a função de envio seguro
}

// NewMatch cria uma nova partida
func NewMatch(id string, p1, p2 *protocol.PlayerConn, cardDB *CardDB, sender SenderFunc) *Match {
	match := &Match{
		ID:      id,
		P1:      p1,
		P2:      p2,
		HP:      [2]int{HPStart, HPStart},
		Hands:   [2]Hand{},
		Discard: [2][]string{{}, {}},
		Round:   1,
		State:   StateAwaitingPlays,
		Waiting: make(map[string]string),
		CardDB:  cardDB,
		done:    make(chan bool, 1),
		sender:  sender, // Atribui a função de envio
	}
	match.DealInitialHands()
	return match
}

// DealInitialHands distribui as mãos iniciais
func (m *Match) DealInitialHands() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Hands[0] = m.CardDB.GenerateHand(HandSize)
	m.Hands[1] = m.CardDB.GenerateHand(HandSize)
}

// GetPlayerIndex retorna o índice do jogador (0 ou 1)
func (m *Match) GetPlayerIndex(playerID string) int {
	if m.P1.ID == playerID {
		return 0
	}
	return 1
}

// PlayCard registra uma carta jogada por um jogador
func (m *Match) PlayCard(playerID, cardID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	playerIndex := m.GetPlayerIndex(playerID)
	found := false
	for _, handCardID := range m.Hands[playerIndex] {
		if handCardID == cardID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("carta não está na mão do jogador")
	}

	m.Waiting[playerID] = cardID
	if len(m.Waiting) == 2 {
		go m.resolveRound()
	}
	return nil
}

// resolveRound resolve uma rodada quando ambos jogadores jogaram
func (m *Match) resolveRound() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.State == StateResolving {
		return // Evita dupla resolução
	}
	m.State = StateResolving

	p1Card, _ := m.CardDB.GetCard(m.Waiting[m.P1.ID])
	p2Card, _ := m.CardDB.GetCard(m.Waiting[m.P2.ID])

	p1Bonus := ElementalBonus(p1Card.Element, p2Card.Element)
	p2Bonus := ElementalBonus(p2Card.Element, p1Card.Element)

	p1DamageDealt := max(0, (p1Card.ATK+p1Bonus)-p2Card.DEF)
	p2DamageDealt := max(0, (p2Card.ATK+p2Bonus)-p1Card.DEF)

	m.HP[1] -= p1DamageDealt
	m.HP[0] -= p2DamageDealt

	m.removeCardFromHand(0, p1Card.ID)
	m.removeCardFromHand(1, p2Card.ID)
	m.refillHands()

	m.broadcastRoundResult(p1Card, p2Card, p1Bonus, p2Bonus, p1DamageDealt, p2DamageDealt)

	m.Waiting = make(map[string]string)
	m.Round++

	if m.EndIfGameOver() {
		return
	}

	m.State = StateAwaitingPlays
	m.BroadcastState()
}

func (m *Match) removeCardFromHand(playerIndex int, cardID string) {
	for i, handCardID := range m.Hands[playerIndex] {
		if handCardID == cardID {
			m.Hands[playerIndex] = append(m.Hands[playerIndex][:i], m.Hands[playerIndex][i+1:]...)
			return
		}
	}
}

func (m *Match) refillHands() {
	for i := 0; i < 2; i++ {
		for len(m.Hands[i]) < HandSize {
			m.Hands[i] = append(m.Hands[i], m.CardDB.GetRandomCard())
		}
	}
}

func (m *Match) broadcastRoundResult(p1Card, p2Card Card, p1Bonus, p2Bonus, p1Dmg, p2Dmg int) {
	p1Msg := protocol.ServerMsg{
		T: protocol.ROUND_RESULT,
		You: &protocol.PlayerView{HP: m.HP[0], CardID: p1Card.ID, ElementBonus: p1Bonus, DmgDealt: p1Dmg, DmgTaken: p2Dmg},
		Opponent: &protocol.PlayerView{HP: m.HP[1], CardID: p2Card.ID},
	}
	p2Msg := protocol.ServerMsg{
		T: protocol.ROUND_RESULT,
		You: &protocol.PlayerView{HP: m.HP[1], CardID: p2Card.ID, ElementBonus: p2Bonus, DmgDealt: p2Dmg, DmgTaken: p1Dmg},
		Opponent: &protocol.PlayerView{HP: m.HP[0], CardID: p1Card.ID},
	}
	m.sender(m.P1, p1Msg)
	m.sender(m.P2, p2Msg)
}

func (m *Match) BroadcastState() {
	p1Msg := protocol.ServerMsg{
		T:        protocol.STATE,
		You:      &protocol.PlayerView{HP: m.HP[0], Hand: m.Hands[0]},
		Opponent: &protocol.PlayerView{HP: m.HP[1], HandSize: len(m.Hands[1])},
		Round:    m.Round,
	}
	p2Msg := protocol.ServerMsg{
		T:        protocol.STATE,
		You:      &protocol.PlayerView{HP: m.HP[1], Hand: m.Hands[1]},
		Opponent: &protocol.PlayerView{HP: m.HP[0], HandSize: len(m.Hands[0])},
		Round:    m.Round,
	}
	m.sender(m.P1, p1Msg)
	m.sender(m.P2, p2Msg)
}

func (m *Match) EndIfGameOver() bool {
	if m.HP[0] <= 0 || m.HP[1] <= 0 {
		m.State = StateEnded
		var p1Result, p2Result string

		if m.HP[0] <= 0 && m.HP[1] <= 0 {
			p1Result, p2Result = protocol.DRAW, protocol.DRAW
		} else if m.HP[0] <= 0 {
			p1Result, p2Result = protocol.LOSE, protocol.WIN
		} else {
			p1Result, p2Result = protocol.WIN, protocol.LOSE
		}

		m.sender(m.P1, protocol.ServerMsg{T: protocol.MATCH_END, Result: p1Result})
		m.sender(m.P2, protocol.ServerMsg{T: protocol.MATCH_END, Result: p2Result})
		log.Printf("[MATCH %s] Partida finalizada.", m.ID)
		return true
	}
	return false
}

func ElementalBonus(a, b Element) int {
	if (a == FIRE && b == PLANT) || (a == PLANT && b == WATER) || (a == WATER && b == FIRE) {
		return ElementalATKBonus
	}
	return 0
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
