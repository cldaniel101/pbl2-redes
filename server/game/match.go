package game

import (
	"fmt"
	"log"
	"math/rand"
	"pingpong/server/consensus"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"pingpong/server/s2s"
	"sync"
	"time"
)

// DistributedMatchInfo contém os detalhes necessários sobre uma partida distribuída
// para que a lógica do jogo encaminhe as mensagens, sem depender do pacote de estado.
type DistributedMatchInfo struct {
	MatchID     string
	HostServer  string
	GuestServer string
	HostPlayer  string
	GuestPlayer string
}

// StateInformer define o contrato do que o Match precisa saber
// sobre o estado mais amplo do servidor, para quebrar o ciclo de importação com o pacote de estado.
type StateInformer interface {
	GetDistributedMatchInfo(matchID string) (DistributedMatchInfo, bool)
	IsPlayerOnline(playerID string) bool
}

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
	broker   *pubsub.Broker
	informer StateInformer

	// Campos para o sistema de consenso distribuído
	OperationQueue *consensus.OperationQueue  // Fila de operações ordenada por timestamp vetorial
	VectorClock    *consensus.VectorialClock  // Relógio vetorial da partida
	PendingACKs    map[string]map[string]bool // operationID -> {serverID -> acked}
	ackMu          sync.RWMutex               // Mutex para PendingACKs
}

// NewMatch cria uma nova partida
func NewMatch(id string, p1, p2 *protocol.PlayerConn, cardDB *CardDB, broker *pubsub.Broker, informer StateInformer) *Match {
	match := &Match{
		ID:       id,
		P1:       p1,
		P2:       p2,
		HP:       [2]int{HPStart, HPStart},
		Hands:    [2]Hand{},
		Discard:  [2][]string{{}, {}},
		Round:    1,
		State:    StateAwaitingPlays,
		Waiting:  make(map[string]string),
		CardDB:   cardDB,
		done:     make(chan bool, 1),
		broker:   broker,
		informer: informer,

		// Inicializa estruturas de consenso
		OperationQueue: consensus.NewOperationQueue(),
		VectorClock:    nil, // Será inicializado pelo StateManager para partidas distribuídas
		PendingACKs:    make(map[string]map[string]bool),
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
	// Valida se o jogador está na partida (sem lock ainda)
	playerIndex := -1
	if m.P1.ID == playerID {
		playerIndex = 0
	} else if m.P2.ID == playerID {
		playerIndex = 1
	} else {
		return fmt.Errorf("jogador não está nesta partida")
	}

	// Se a entrada é um índice (1-5), converte para o ID da carta
	m.mu.Lock()
	if len(cardID) == 1 && cardID[0] >= '1' && cardID[0] <= '5' {
		cardIndex := int(cardID[0] - '1')
		if cardIndex >= 0 && cardIndex < len(m.Hands[playerIndex]) {
			cardID = m.Hands[playerIndex][cardIndex]
		} else {
			m.mu.Unlock()
			return fmt.Errorf("índice de carta inválido")
		}
	}

	// Valida se a carta existe no CardDB
	if !m.CardDB.ValidateCard(cardID) {
		m.mu.Unlock()
		return fmt.Errorf("carta inválida")
	}
	m.mu.Unlock()

	// Verifica se é uma partida distribuída
	if m.VectorClock != nil {
		// Usa Two-Phase Commit para partidas distribuídas
		return m.playCardWithConsensus(playerID, cardID)
	}

	// Partida local - usa lógica simples
	m.mu.Lock()
	defer m.mu.Unlock()

	// Registra a jogada
	m.Waiting[playerID] = cardID

	// Verifica se ambos jogaram
	if len(m.Waiting) == 2 {
		// Resolve a rodada
		go m.resolveRound()
	}

	return nil
}

// playCardWithConsensus implementa Two-Phase Commit para jogadas em partidas distribuídas
func (m *Match) playCardWithConsensus(playerID, cardID string) error {
	// Fase 1: Criar operação e propor
	op := m.CreatePlayOperation(playerID, cardID)
	if op == nil {
		return fmt.Errorf("falha ao criar operação (partida não distribuída)")
	}

	// Obtém informações da partida distribuída
	distMatch, isDistributed := m.informer.GetDistributedMatchInfo(m.ID)
	if !isDistributed {
		return fmt.Errorf("partida não é distribuída")
	}

	// Determina outros servidores para propor
	var otherServers []string

	// Identifica qual servidor somos baseado no jogador local
	isHostServer := m.informer.IsPlayerOnline(distMatch.HostPlayer)

	if isHostServer {
		// Somos o servidor host, propõe para o guest
		otherServers = []string{distMatch.GuestServer}
	} else {
		// Somos o servidor guest, propõe para o host
		otherServers = []string{distMatch.HostServer}
	}

	log.Printf("[MATCH %s] Iniciando consenso para operação %s com servidores: %v", m.ID, op.ID, otherServers)

	// Propõe operação (Fase 1)
	ackChan, errChan := m.proposeOperation(op, otherServers)

	// Aguarda resultado
	select {
	case <-ackChan:
		// Todos os ACKs recebidos, prossegue para Fase 2
		log.Printf("[MATCH %s] Todos os ACKs recebidos para operação %s", m.ID, op.ID)

		// Fase 2: Verificação cross-server
		allValid := true
		for _, serverAddr := range otherServers {
			valid, err := s2s.CheckOperation(serverAddr, m.ID, op)
			if err != nil || !valid {
				log.Printf("[MATCH %s] Operação %s inválida no servidor %s: %v", m.ID, op.ID, serverAddr, err)
				allValid = false
				break
			}
		}

		if allValid {
			// Executa localmente
			if err := m.ExecuteOperation(op); err != nil {
				log.Printf("[MATCH %s] Erro ao executar operação %s localmente: %v", m.ID, op.ID, err)
				return err
			}

			// Solicita commit nos outros servidores
			for _, serverAddr := range otherServers {
				go s2s.CommitOperation(serverAddr, m.ID, op)
			}

			log.Printf("[MATCH %s] Operação %s executada com sucesso via consenso", m.ID, op.ID)
			return nil
		} else {
			// Operação inválida, faz rollback
			m.RollbackOperation(op)

			// Solicita rollback nos outros servidores
			for _, serverAddr := range otherServers {
				go s2s.RollbackOperation(serverAddr, m.ID, op)
			}

			return fmt.Errorf("operação rejeitada pelo consenso (inválida em um ou mais servidores)")
		}

	case err := <-errChan:
		// Timeout ou erro aguardando ACKs
		log.Printf("[MATCH %s] Erro no consenso para operação %s: %v", m.ID, op.ID, err)
		m.RollbackOperation(op)

		// Solicita rollback nos outros servidores
		for _, serverAddr := range otherServers {
			go s2s.RollbackOperation(serverAddr, m.ID, op)
		}

		return fmt.Errorf("consenso falhou: %v", err)
	}
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
	playerIndex1 := m.GetPlayerIndex(m.P1.ID)
	playerIndex2 := m.GetPlayerIndex(m.P2.ID)

	m.removeCardFromHand(playerIndex1, p1CardID)
	m.removeCardFromHand(playerIndex2, p2CardID)
	m.Discard[playerIndex1] = append(m.Discard[playerIndex1], p1CardID)
	m.Discard[playerIndex2] = append(m.Discard[playerIndex2], p2CardID)

	// Repõe as mãos
	m.refillHands()

	// Cria logs da rodada
	logs := m.createRoundLogs(p1Card, p2Card, p1Bonus, p1DamageDealt, p2DamageDealt)

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
	// Proteção contra índice inválido
	if playerIndex < 0 || playerIndex >= len(m.Hands) {
		log.Printf("[MATCH %s] Erro ao remover carta: índice de jogador inválido %d", m.ID, playerIndex)
		return
	}

	// Procura a carta na mão e a remove
	for i, handCardID := range m.Hands[playerIndex] {
		if handCardID == cardID {
			// Remove a carta mantendo a ordem das demais
			m.Hands[playerIndex] = append(m.Hands[playerIndex][:i], m.Hands[playerIndex][i+1:]...)
			log.Printf("[MATCH %s] Carta %s removida da mão do jogador %d", m.ID, cardID, playerIndex)
			return
		}
	}

	log.Printf("[MATCH %s] Aviso: tentativa de remover carta %s que não está na mão do jogador %d", m.ID, cardID, playerIndex)
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
func (m *Match) createRoundLogs(p1Card, p2Card Card, p1Bonus, p1Dmg, p2Dmg int) []string {
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

	m.sendToPlayerSmart(m.P1.ID, p1Msg)
	m.sendToPlayerSmart(m.P2.ID, p2Msg)
}

// BroadcastState envia o estado atual para ambos jogadores
// BroadcastState envia o estado atual para ambos jogadores
func (m *Match) BroadcastState() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Calcula deadline apenas se pelo menos um jogador tiver autoplay ativo
	var deadlineMs int64 = 0
	if m.P1.AutoPlay || m.P2.AutoPlay {
		deadlineMs = time.Until(m.Deadline).Milliseconds()
		if deadlineMs < 0 {
			deadlineMs = 0
		}
	}

	log.Printf("[MATCH %s] Estado atual - P1 Hand (local=%v): %v, P2 Hand (local=%v): %v",
		m.ID, m.informer.IsPlayerOnline(m.P1.ID), m.Hands[0],
		m.informer.IsPlayerOnline(m.P2.ID), m.Hands[1])

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

	m.sendToPlayerSmart(m.P1.ID, p1Msg)
	m.sendToPlayerSmart(m.P2.ID, p2Msg)
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
		m.sendToPlayerSmart(m.P1.ID, protocol.ServerMsg{T: protocol.MATCH_END, Result: p1Result})
		m.sendToPlayerSmart(m.P2.ID, protocol.ServerMsg{T: protocol.MATCH_END, Result: p2Result})

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

// sendToPlayer envia uma mensagem para um jogador específico via pub/sub
func (m *Match) sendToPlayer(playerID string, msg protocol.ServerMsg) {
	if m.broker != nil {
		m.broker.Publish(fmt.Sprintf("player.%s", playerID), msg)
	}
}

// sendToPlayerSmart decide se envia uma mensagem localmente via broker ou
// retransmite para outro servidor.
func (m *Match) sendToPlayerSmart(playerID string, msg protocol.ServerMsg) {
	distMatch, isDistributed := m.informer.GetDistributedMatchInfo(m.ID)

	// Se não for uma partida distribuída, envia sempre localmente.
	if !isDistributed {
		m.sendToPlayer(playerID, msg)
		return
	}

	// Verifica se o jogador alvo é o anfitrião ou o convidado.
	isTargetHost := distMatch.HostPlayer == playerID
	isTargetGuest := distMatch.GuestPlayer == playerID

	// Se o jogador não pertence a esta partida distribuída, não faz nada.
	if !isTargetHost && !isTargetGuest {
		return
	}

	// Verifica se o jogador está neste servidor.
	isPlayerLocal := m.informer.IsPlayerOnline(playerID)

	if isPlayerLocal {
		m.sendToPlayer(playerID, msg)
	} else {
		// O jogador é remoto, determina para qual servidor retransmitir.
		var remoteServer string
		if isTargetHost {
			remoteServer = distMatch.HostServer
		} else {
			remoteServer = distMatch.GuestServer
		}
		log.Printf("[MATCH %s] A retransmitir mensagem do tipo %s para o jogador remoto %s no servidor %s", m.ID, msg.T, playerID, remoteServer)
		s2s.ForwardMessage(remoteServer, playerID, msg)
	}
}

// forwardPlayIfNeeded foi substituído pelo sistema de consenso Two-Phase Commit
// O método playCardWithConsensus() agora gerencia a sincronização entre servidores
// DEPRECADO: Este método não é mais usado
/*
func (m *Match) forwardPlayIfNeeded(playerID, cardID string) {
	// Obter informações da partida distribuída
	distMatch, isDistributed := m.informer.GetDistributedMatchInfo(m.ID)
	if !isDistributed {
		return // Não faz nada em partidas locais
	}

	// Verifica se o jogador que fez a jogada está neste servidor.
	// Apenas retransmitimos jogadas de jogadores locais.
	isPlayerLocal := m.informer.IsPlayerOnline(playerID)
	if !isPlayerLocal {
		log.Printf("[MATCH %s] Não retransmitindo jogada de %s pois não é local", m.ID, playerID)
		return
	}

	// Determina o servidor do oponente usando o nome de serviço correto
	var opponentServer string
	if distMatch.HostPlayer == playerID {
		opponentServer = distMatch.GuestServer
		fmt.Println("\n\n\n" + opponentServer + "\n\n\n")
	} else {
		opponentServer = distMatch.HostServer
	}

	log.Printf("[MATCH %s] Retransmitindo jogada do jogador local %s (carta %s) para o servidor do oponente em %s",
		m.ID, playerID, cardID, opponentServer)

	s2s.ForwardAction(opponentServer, m.ID, playerID, cardID)
}
*/

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

// ========================================
// Métodos para Sistema de Consenso
// ========================================

// InitializeConsensus inicializa o relógio vetorial e estruturas de consenso
// Deve ser chamado pelo StateManager ao criar partidas distribuídas
func (m *Match) InitializeConsensus(serverID string, allServerIDs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.VectorClock == nil {
		m.VectorClock = consensus.NewVectorialClock(serverID, allServerIDs)
		log.Printf("[MATCH %s] Sistema de consenso inicializado com relógio vetorial: %s",
			m.ID, m.VectorClock.ToString())
	}
}

// AddPendingACK registra que uma operação está aguardando ACKs
func (m *Match) AddPendingACK(operationID string, serverIDs []string) {
	m.ackMu.Lock()
	defer m.ackMu.Unlock()

	if m.PendingACKs[operationID] == nil {
		m.PendingACKs[operationID] = make(map[string]bool)
	}

	// Inicializa todos os servidores como não confirmados
	for _, serverID := range serverIDs {
		m.PendingACKs[operationID][serverID] = false
	}

	log.Printf("[MATCH %s] Operação %s aguardando ACKs de %v", m.ID, operationID, serverIDs)
}

// MarkACK marca que um servidor confirmou uma operação
// Retorna true se todos os ACKs foram recebidos
func (m *Match) MarkACK(operationID, serverID string) bool {
	m.ackMu.Lock()
	defer m.ackMu.Unlock()

	if acks, exists := m.PendingACKs[operationID]; exists {
		acks[serverID] = true

		// Verifica se todos os servidores confirmaram
		allAcked := true
		for _, acked := range acks {
			if !acked {
				allAcked = false
				break
			}
		}

		if allAcked {
			log.Printf("[MATCH %s] Operação %s recebeu todos os ACKs", m.ID, operationID)
			delete(m.PendingACKs, operationID)
			return true
		}
	}

	return false
}

// GetPendingACKCount retorna o número de operações aguardando ACKs
func (m *Match) GetPendingACKCount() int {
	m.ackMu.RLock()
	defer m.ackMu.RUnlock()

	return len(m.PendingACKs)
}

// CreatePlayOperation cria uma operação de jogada com timestamp vetorial
func (m *Match) CreatePlayOperation(playerID, cardID string) *consensus.Operation {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Se não há relógio vetorial, retorna nil (partida não distribuída)
	if m.VectorClock == nil {
		return nil
	}

	// Incrementa o relógio vetorial
	m.VectorClock.Tick()

	// Cria a operação
	op := consensus.NewOperation(
		consensus.OpTypePlay,
		m.ID,
		m.VectorClock.ToString(), // Usa o toString como ServerID temporariamente
		playerID,
		cardID,
		m.VectorClock.GetSnapshot(),
	)

	// Adiciona à fila de operações
	m.OperationQueue.Add(op)

	log.Printf("[MATCH %s] Operação de jogada criada: %s", m.ID, op.ToString())

	return op
}

// ProcessOperation processa uma operação da fila
func (m *Match) ProcessOperation() *consensus.Operation {
	// Pega a próxima operação (sem remover)
	op := m.OperationQueue.GetTop()
	if op == nil {
		return nil
	}

	// Aqui seria verificado se a operação pode ser executada
	// (se todos os ACKs foram recebidos, etc.)
	// Por enquanto, apenas retorna a operação

	return op
}

// RemoveOperation remove uma operação da fila após processamento
func (m *Match) RemoveOperation() *consensus.Operation {
	return m.OperationQueue.Remove()
}

// GetOperationQueueSize retorna o tamanho da fila de operações
func (m *Match) GetOperationQueueSize() int {
	return m.OperationQueue.Size()
}

// ========================================
// Métodos do Two-Phase Commit
// ========================================

// proposeOperation propõe uma operação para outros servidores (Fase 1)
// Retorna canais para aguardar ACKs e resultado da validação
func (m *Match) proposeOperation(op *consensus.Operation, otherServers []string) (ackChan chan bool, errChan chan error) {
	ackChan = make(chan bool, 1)
	errChan = make(chan error, 1)

	go func() {
		// Adiciona operação na fila local
		m.OperationQueue.Add(op)

		// Registra operação como pendente de ACKs
		m.AddPendingACK(op.ID, otherServers)

		log.Printf("[MATCH %s] Propondo operação %s para %v", m.ID, op.ID, otherServers)

		// Envia proposta para outros servidores
		for _, serverAddr := range otherServers {
			go s2s.ProposeOperation(serverAddr, m.ID, op)
		}

		// Aguarda ACKs com timeout
		allACKsReceived := m.waitForACKs(op.ID, 5*time.Second)

		if !allACKsReceived {
			errChan <- fmt.Errorf("timeout aguardando ACKs para operação %s", op.ID)
			return
		}

		ackChan <- true
	}()

	return ackChan, errChan
}

// waitForACKs aguarda confirmações de todos os servidores
// Retorna true se todos confirmaram, false se timeout
func (m *Match) waitForACKs(operationID string, timeout time.Duration) bool {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	deadline := time.Now().Add(timeout)

	for {
		select {
		case <-ticker.C:
			m.ackMu.RLock()
			acks, exists := m.PendingACKs[operationID]
			m.ackMu.RUnlock()

			if !exists {
				// Todos os ACKs foram recebidos (MarkACK já removeu)
				return true
			}

			// Verifica se ainda há ACKs pendentes
			allReceived := true
			for _, acked := range acks {
				if !acked {
					allReceived = false
					break
				}
			}

			if allReceived {
				return true
			}

			// Verifica timeout
			if time.Now().After(deadline) {
				log.Printf("[MATCH %s] Timeout aguardando ACKs para operação %s", m.ID, operationID)
				return false
			}
		}
	}
}

// CheckOperationValidity verifica se uma operação é válida no estado atual
// Retorna true se a operação pode ser executada, false caso contrário
func (m *Match) CheckOperationValidity(op *consensus.Operation) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch op.Type {
	case consensus.OpTypePlay:
		// Verifica se é uma operação de jogada
		playerIndex := -1
		if m.P1.ID == op.PlayerID {
			playerIndex = 0
		} else if m.P2.ID == op.PlayerID {
			playerIndex = 1
		} else {
			return false, fmt.Errorf("jogador %s não está nesta partida", op.PlayerID)
		}

		// Verifica se a partida está aceitando jogadas
		if m.State != StateAwaitingPlays {
			return false, fmt.Errorf("partida não está aguardando jogadas (estado: %v)", m.State)
		}

		// Verifica se o jogador já jogou nesta rodada
		if _, alreadyPlayed := m.Waiting[op.PlayerID]; alreadyPlayed {
			return false, fmt.Errorf("jogador %s já jogou nesta rodada", op.PlayerID)
		}

		// Verifica se a carta é válida
		if !m.CardDB.ValidateCard(op.CardID) {
			return false, fmt.Errorf("carta %s é inválida", op.CardID)
		}

		// Verifica se a carta está na mão do jogador
		cardFound := false
		for _, handCard := range m.Hands[playerIndex] {
			if handCard == op.CardID {
				cardFound = true
				break
			}
		}

		if !cardFound {
			return false, fmt.Errorf("carta %s não está na mão do jogador %s", op.CardID, op.PlayerID)
		}

		return true, nil

	case consensus.OpTypeResolve:
		// Verifica se ambos jogadores já jogaram
		if len(m.Waiting) != 2 {
			return false, fmt.Errorf("não há jogadas suficientes para resolver (apenas %d/2)", len(m.Waiting))
		}
		return true, nil

	case consensus.OpTypeEndMatch:
		// Verifica se o jogo terminou
		if m.HP[0] > 0 && m.HP[1] > 0 {
			return false, fmt.Errorf("partida ainda não terminou")
		}
		return true, nil

	default:
		return false, fmt.Errorf("tipo de operação desconhecido: %s", op.Type)
	}
}

// ExecuteOperation executa uma operação após consenso (Fase 2)
func (m *Match) ExecuteOperation(op *consensus.Operation) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	log.Printf("[MATCH %s] Executando operação %s (tipo: %s)", m.ID, op.ID, op.Type)

	// Atualiza o relógio vetorial com o timestamp da operação
	if m.VectorClock != nil {
		m.VectorClock.Update(op.Timestamp)
	}

	switch op.Type {
	case consensus.OpTypePlay:
		// Registra a jogada
		m.Waiting[op.PlayerID] = op.CardID
		log.Printf("[MATCH %s] Jogada registrada: %s jogou %s", m.ID, op.PlayerID, op.CardID)

		// Remove da fila
		m.OperationQueue.RemoveByID(op.ID)

		// Verifica se ambos jogaram
		if len(m.Waiting) == 2 {
			// Resolve a rodada
			go m.resolveRound()
		}

		return nil

	case consensus.OpTypeResolve:
		// Já implementado em resolveRound()
		return nil

	case consensus.OpTypeEndMatch:
		m.State = StateEnded
		return nil

	default:
		return fmt.Errorf("tipo de operação desconhecido: %s", op.Type)
	}
}

// RollbackOperation descarta uma operação inválida
func (m *Match) RollbackOperation(op *consensus.Operation) {
	log.Printf("[MATCH %s] Revertendo operação %s (tipo: %s)", m.ID, op.ID, op.Type)

	// Remove da fila de operações
	m.OperationQueue.RemoveByID(op.ID)

	// Remove dos ACKs pendentes
	m.ackMu.Lock()
	delete(m.PendingACKs, op.ID)
	m.ackMu.Unlock()

	// Notifica os jogadores se necessário
	switch op.Type {
	case consensus.OpTypePlay:
		// Notifica o jogador que a jogada foi rejeitada
		m.sendToPlayerSmart(op.PlayerID, protocol.ServerMsg{
			T:    protocol.ERROR,
			Code: protocol.INTERNAL,
			Msg:  "Jogada rejeitada pelo sistema de consenso",
		})
	}
}
