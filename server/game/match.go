package game

import (
	"fmt"
	"log"
	"math/rand"
	"pingpong/server/protocol"
	"sync"
	"time"
	"encoding/json"
	"strings"
)

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
	Waiting  map[string]string // playerID -> cardID jogado
	Deadline time.Time
	CardDB   *CardDB
	mu       sync.Mutex
	done     chan bool
}

// NewMatch cria uma nova partida
func NewMatch(id string, p1, p2 *protocol.PlayerConn, cardDB *CardDB) *Match {
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
	}

	// Gera mãos iniciais
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

// GetOpponentIndex retorna o índice do oponente
func (m *Match) GetOpponentIndex(playerID string) int {
	if m.P1.ID == playerID {
		return 1
	}
	return 0
}

// PlayCard registra uma carta jogada por um jogador
func (m *Match) PlayCard(playerID, cardID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Valida se o jogador está na partida
	playerIndex := m.GetPlayerIndex(playerID)
	if (playerIndex == 0 && m.P1.ID != playerID) || (playerIndex == 1 && m.P2.ID != playerID) {
		return fmt.Errorf("jogador não está nesta partida")
	}

	// Valida se a carta está na mão do jogador
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

	// Valida se a carta existe no CardDB
	if !m.CardDB.ValidateCard(cardID) {
		return fmt.Errorf("carta inválida")
	}

	// Registra a jogada
	m.Waiting[playerID] = cardID

	// Verifica se ambos jogaram
	if len(m.Waiting) == 2 {
		// Resolve a rodada
		go m.resolveRound()
	}

	return nil
}

// resolveRound resolve uma rodada quando ambos jogadores jogaram
func (m *Match) resolveRound() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.State = StateResolving

	// Pega as cartas jogadas
	p1CardID := m.Waiting[m.P1.ID]
	p2CardID := m.Waiting[m.P2.ID]

	p1Card, _ := m.CardDB.GetCard(p1CardID)
	p2Card, _ := m.CardDB.GetCard(p2CardID)

	// Calcula bônus elemental
	p1Bonus := ElementalBonus(p1Card.Element, p2Card.Element)
	p2Bonus := ElementalBonus(p2Card.Element, p1Card.Element)

	// Calcula danos
	p1DamageDealt := max(0, (p1Card.ATK+p1Bonus)-p2Card.DEF)
	p2DamageDealt := max(0, (p2Card.ATK+p2Bonus)-p1Card.DEF)

	// Aplica danos
	m.HP[1] -= p1DamageDealt // P1 causa dano em P2
	m.HP[0] -= p2DamageDealt // P2 causa dano em P1

	// Remove cartas das mãos e adiciona ao descarte
	m.removeCardFromHand(0, p1CardID)
	m.removeCardFromHand(1, p2CardID)
	m.Discard[0] = append(m.Discard[0], p1CardID)
	m.Discard[1] = append(m.Discard[1], p2CardID)

	// Repõe as mãos
	m.refillHands()

	// Cria logs da rodada
	logs := m.createRoundLogs(p1Card, p2Card, p1Bonus, p2Bonus, p1DamageDealt, p2DamageDealt)

	// Envia resultado da rodada
	m.broadcastRoundResult(p1Card, p2Card, p1Bonus, p2Bonus, p1DamageDealt, p2DamageDealt, logs)

	// Limpa as jogadas
	m.Waiting = make(map[string]string)
	m.Round++

	// Verifica fim do jogo
	if m.EndIfGameOver() {
		return
	}

	// Próxima rodada
	m.State = StateAwaitingPlays
	m.Deadline = time.Now().Add(time.Duration(RoundPlayTimeout) * time.Millisecond)

	// Envia estado atualizado
	m.BroadcastState()

	// Agenda timeout para auto-play apenas se pelo menos um jogador tiver autoplay ativo
	if m.P1.AutoPlay || m.P2.AutoPlay {
		go m.scheduleAutoPlay()
	}
}

// removeCardFromHand remove uma carta da mão do jogador
func (m *Match) removeCardFromHand(playerIndex int, cardID string) {
	for i, handCardID := range m.Hands[playerIndex] {
		if handCardID == cardID {
			m.Hands[playerIndex] = append(m.Hands[playerIndex][:i], m.Hands[playerIndex][i+1:]...)
			break
		}
	}
}

// refillHands repõe as mãos até o tamanho máximo
func (m *Match) refillHands() {
	for playerIndex := 0; playerIndex < 2; playerIndex++ {
		for len(m.Hands[playerIndex]) < HandSize {
			newCard := m.CardDB.GetRandomCard()
			if newCard != "" {
				m.Hands[playerIndex] = append(m.Hands[playerIndex], newCard)
			}
		}
	}
}

// createRoundLogs cria os logs da rodada
func (m *Match) createRoundLogs(p1Card, p2Card Card, p1Bonus, p2Bonus, p1Dmg, p2Dmg int) []string {
	logs := []string{}

	p1BonusText := ""
	if p1Bonus > 0 {
		p1BonusText = fmt.Sprintf(" (+%d bônus elemental)", p1Bonus)
	}

	logs = append(logs, fmt.Sprintf("Você jogou %s (ATK %d%s). Oponente jogou %s (DEF %d).",
		p1Card.Name, p1Card.ATK, p1BonusText, p2Card.Name, p2Card.DEF))

	if p1Dmg > 0 {
		logs = append(logs, fmt.Sprintf("Você causou %d de dano!", p1Dmg))
	}
	if p2Dmg > 0 {
		logs = append(logs, fmt.Sprintf("Você recebeu %d de dano!", p2Dmg))
	}

	return logs
}

// broadcastRoundResult envia o resultado da rodada para ambos jogadores
func (m *Match) broadcastRoundResult(p1Card, p2Card Card, p1Bonus, p2Bonus, p1Dmg, p2Dmg int, logs []string) {
	// Para P1
	p1Msg := protocol.ServerMsg{
		T: protocol.ROUND_RESULT,
		You: &protocol.PlayerView{
			HP:           m.HP[0],
			CardID:       p1Card.ID,
			ElementBonus: p1Bonus,
			DmgDealt:     p1Dmg,
			DmgTaken:     p2Dmg,
		},
		Opponent: &protocol.PlayerView{
			HP:           m.HP[1],
			CardID:       p2Card.ID,
			ElementBonus: p2Bonus,
		},
		Logs: logs,
	}

	// Para P2 (perspectiva invertida)
	p2Logs := []string{}
	for _, log := range logs {
		// Inverte "Você" e "Oponente" nos logs para P2
		if log == logs[0] { // primeiro log
			p2Logs = append(p2Logs, fmt.Sprintf("Você jogou %s (ATK %d%s). Oponente jogou %s (DEF %d).",
				p2Card.Name, p2Card.ATK,
				func() string {
					if p2Bonus > 0 {
						return fmt.Sprintf(" (+%d bônus elemental)", p2Bonus)
					}
					return ""
				}(),
				p1Card.Name, p1Card.DEF))
		} else {
			// Inverte dano causado/recebido
			if p1Dmg > 0 && log == fmt.Sprintf("Você causou %d de dano!", p1Dmg) {
				p2Logs = append(p2Logs, fmt.Sprintf("Você recebeu %d de dano!", p1Dmg))
			} else if p2Dmg > 0 && log == fmt.Sprintf("Você recebeu %d de dano!", p2Dmg) {
				p2Logs = append(p2Logs, fmt.Sprintf("Você causou %d de dano!", p2Dmg))
			}
		}
	}

	p2Msg := protocol.ServerMsg{
		T: protocol.ROUND_RESULT,
		You: &protocol.PlayerView{
			HP:           m.HP[1],
			CardID:       p2Card.ID,
			ElementBonus: p2Bonus,
			DmgDealt:     p2Dmg,
			DmgTaken:     p1Dmg,
		},
		Opponent: &protocol.PlayerView{
			HP:           m.HP[0],
			CardID:       p1Card.ID,
			ElementBonus: p1Bonus,
		},
		Logs: p2Logs,
	}

	m.P1.SendMsg(p1Msg)
	m.P2.SendMsg(p2Msg)
}

// BroadcastState envia o estado atual para ambos jogadores
func (m *Match) BroadcastState() {
	// Calcula deadline apenas se pelo menos um jogador tiver autoplay ativo
	var deadlineMs int64 = 0
	if m.P1.AutoPlay || m.P2.AutoPlay {
		deadlineMs = time.Until(m.Deadline).Milliseconds()
		if deadlineMs < 0 {
			deadlineMs = 0
		}
	}

	// Para P1
	p1Msg := protocol.ServerMsg{
		T: protocol.STATE,
		You: &protocol.PlayerView{
			HP:   m.HP[0],
			Hand: m.Hands[0],
		},
		Opponent: &protocol.PlayerView{
			HP:       m.HP[1],
			HandSize: len(m.Hands[1]),
		},
		Round:      m.Round,
		DeadlineMs: deadlineMs,
	}

	// Para P2
	p2Msg := protocol.ServerMsg{
		T: protocol.STATE,
		You: &protocol.PlayerView{
			HP:   m.HP[1],
			Hand: m.Hands[1],
		},
		Opponent: &protocol.PlayerView{
			HP:       m.HP[0],
			HandSize: len(m.Hands[0]),
		},
		Round:      m.Round,
		DeadlineMs: deadlineMs,
	}

	sendMsgToPlayer(m.P1, p1Msg)
	sendMsgToPlayer(m.P2, p2Msg)
}

// EndIfGameOver verifica se o jogo terminou e envia MATCH_END
func (m *Match) EndIfGameOver() bool {
	if m.HP[0] <= 0 || m.HP[1] <= 0 {
		m.State = StateEnded

		var p1Result, p2Result string

		if m.HP[0] <= 0 && m.HP[1] <= 0 {
			// Empate
			p1Result = protocol.DRAW
			p2Result = protocol.DRAW
		} else if m.HP[0] <= 0 {
			// P1 perdeu
			p1Result = protocol.LOSE
			p2Result = protocol.WIN
		} else {
			// P2 perdeu
			p1Result = protocol.WIN
			p2Result = protocol.LOSE
		}

		// Envia resultado final
		m.P1.SendMsg(protocol.ServerMsg{T: protocol.MATCH_END, Result: p1Result})
		m.P2.SendMsg(protocol.ServerMsg{T: protocol.MATCH_END, Result: p2Result})

		log.Printf("[MATCH %s] Partida finalizada. P1(%s): %s, P2(%s): %s",
			m.ID, m.P1.ID, p1Result, m.P2.ID, p2Result)

		// Sinaliza que a partida terminou
		select {
		case m.done <- true:
		default:
		}

		return true
	}
	return false
}

// scheduleAutoPlay agenda o auto-play se necessário
func (m *Match) scheduleAutoPlay() {
	time.Sleep(time.Duration(RoundPlayTimeout) * time.Millisecond)
	m.AutoplayIfNeeded()
}

// AutoplayIfNeeded executa auto-play para jogadores que não jogaram e têm autoplay ativo
func (m *Match) AutoplayIfNeeded() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.State != StateAwaitingPlays {
		return
	}

	// Verifica quais jogadores não jogaram e têm autoplay ativo
	playersToAutoplay := []string{}

	if _, played := m.Waiting[m.P1.ID]; !played && m.P1.AutoPlay {
		playersToAutoplay = append(playersToAutoplay, m.P1.ID)
	}

	if _, played := m.Waiting[m.P2.ID]; !played && m.P2.AutoPlay {
		playersToAutoplay = append(playersToAutoplay, m.P2.ID)
	}

	// Executa auto-play
	for _, playerID := range playersToAutoplay {
		playerIndex := m.GetPlayerIndex(playerID)
		if len(m.Hands[playerIndex]) > 0 {
			// Escolhe carta aleatória
			randomIndex := rand.Intn(len(m.Hands[playerIndex]))
			randomCard := m.Hands[playerIndex][randomIndex]
			m.Waiting[playerID] = randomCard

			log.Printf("[MATCH %s] Auto-play para %s: %s", m.ID, playerID, randomCard)
		}
	}

	// Se ambos jogaram (incluindo auto-play), resolve a rodada
	if len(m.Waiting) == 2 {
		go m.resolveRound()
	}
}

// Done retorna o canal que sinaliza quando a partida termina
func (m *Match) Done() <-chan bool {
	return m.done
}

// ElementalBonus calcula o bônus elemental
func ElementalBonus(a, b Element) int {
	if (a == FIRE && b == PLANT) ||
		(a == PLANT && b == WATER) ||
		(a == WATER && b == FIRE) {
		return ElementalATKBonus
	}
	return 0
}

// max retorna o maior entre dois inteiros
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}


func sendMsgToPlayer(player *protocol.PlayerConn, msg protocol.ServerMsg) {
	if player.IsRemote {
		// --- LÓGICA PARA JOGADOR REMOTO ---
		msgBytes, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[MATCH] Erro ao codificar msg para jogador remoto %s: %v", player.ID, err)
			return
		}

		forwardURL := player.RemoteServerURL + "/api/forward"

		// O corpo da requisição precisa dizer ao servidor proxy para qual jogador a mensagem se destina
		payload := map[string]string{
			"targetPlayerId": player.ID,
			"message":        string(msgBytes),
		}
		payloadBytes, _ := json.Marshal(payload)

		// Envia a mensagem para o servidor proxy
		resp, err := player.HttpClient.Post(forwardURL, "application/json", strings.NewReader(string(payloadBytes)))
		if err != nil {
			log.Printf("[MATCH] Erro ao encaminhar msg para jogador remoto %s: %v", player.ID, err)
		} else if resp != nil {
			resp.Body.Close()
		}
	} else if player.Encoder != nil {
		// --- LÓGICA PARA JOGADOR LOCAL ---
		player.SendMsg(msg)
	} else {
        log.Printf("[MATCH] Aviso: Tentou enviar mensagem para jogador local sem encoder: %s", player.ID)
    }
}