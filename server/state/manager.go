package state

import (
	"errors"
	"fmt"
	"log"
	"pingpong/server/game"
	"pingpong/server/protocol"
	"pingpong/server/pubsub"
	"strings"
	"sync"
	"time"
)

// DistributedMatch armazena informações sobre partidas que ocorrem entre diferentes servidores.
type DistributedMatch struct {
	MatchID     string
	HostServer  string
	GuestServer string
	HostPlayer  string
	GuestPlayer string
}

// StateManager gerencia todo o estado compartilhado do servidor (jogadores, partidas, filas)
// de forma segura para acesso concorrente.
type StateManager struct {
	mu                 sync.RWMutex
	CardDB             *game.CardDB
	PackSystem         *game.PackSystem
	PlayersOnline      map[string]*protocol.PlayerConn
	MatchmakingQueue   []*protocol.PlayerConn
	ActiveMatches      map[string]*game.Match
	DistributedMatches map[string]*DistributedMatch
}

// NewStateManager cria e inicializa um novo gerenciador de estado.
// Ele é responsável por carregar recursos iniciais como a base de dados de cartas.
func NewStateManager() *StateManager {
	cardDB := game.NewCardDB()
	if err := cardDB.LoadFromFile("cards.json"); err != nil {
		log.Fatalf("[STATE] Erro fatal ao carregar base de dados de cartas: %v", err)
	}

	// Configuração inicial do sistema de pacotes
	packConfig := game.PackConfig{CardsPerPack: 3, Stock: 100}
	packSystem := game.NewPackSystem(packConfig, cardDB)

	return &StateManager{
		CardDB:             cardDB,
		PackSystem:         packSystem,
		PlayersOnline:      make(map[string]*protocol.PlayerConn),
		MatchmakingQueue:   make([]*protocol.PlayerConn, 0),
		ActiveMatches:      make(map[string]*game.Match),
		DistributedMatches: make(map[string]*DistributedMatch),
	}
}

// AddPlayerOnline adiciona um novo jogador conectado ao mapa de jogadores online.
func (sm *StateManager) AddPlayerOnline(player *protocol.PlayerConn) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.PlayersOnline[player.ID] = player
}

// RemovePlayerOnline remove um jogador do mapa de jogadores online.
func (sm *StateManager) RemovePlayerOnline(playerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.PlayersOnline, playerID)
}

// FindPlayerMatch encontra a partida em que um determinado jogador está participando.
// Retorna nil se o jogador não estiver em nenhuma partida ativa.
func (sm *StateManager) FindPlayerMatch(playerID string) *game.Match {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, match := range sm.ActiveMatches {
		if match.P1.ID == playerID || match.P2.ID == playerID {
			return match
		}
	}
	return nil
}

// FindMatchByID encontra uma partida ativa pelo seu ID.
func (sm *StateManager) FindMatchByID(matchID string) *game.Match {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	match, ok := sm.ActiveMatches[matchID]
	if ok {
		return match
	}
	return nil
}

// CleanupPlayer remove um jogador de todas as estruturas de estado (online, fila, partida)
// e retorna o oponente caso o jogador estivesse em uma partida, para notificação.
func (sm *StateManager) CleanupPlayer(player *protocol.PlayerConn) *protocol.PlayerConn {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var opponent *protocol.PlayerConn

	// 1. Remove da lista de jogadores online
	delete(sm.PlayersOnline, player.ID)

	// 2. Limpa solicitações de rematch pendentes de outros jogadores para este jogador
	for _, p := range sm.PlayersOnline {
		if p.LastOpponent == player.ID {
			p.WantsRematch = false
		}
	}

	// 3. Remove da fila de matchmaking, se estiver nela
	for i, p := range sm.MatchmakingQueue {
		if p.ID == player.ID {
			sm.MatchmakingQueue = append(sm.MatchmakingQueue[:i], sm.MatchmakingQueue[i+1:]...)
			break
		}
	}

	// 4. Remove de partidas ativas
	var matchToRemove *game.Match
	for _, match := range sm.ActiveMatches {
		if match.P1.ID == player.ID {
			opponent = match.P2
			matchToRemove = match
			break
		}
		if match.P2.ID == player.ID {
			opponent = match.P1
			matchToRemove = match
			break
		}
	}

	if matchToRemove != nil {
		delete(sm.ActiveMatches, matchToRemove.ID)
		delete(sm.DistributedMatches, matchToRemove.ID) // Garante limpeza também no mapa distribuído
		log.Printf("[STATE] Jogador %s removido da partida %s.", player.ID, matchToRemove.ID)
	}

	return opponent
}

// CreateLocalMatch cria uma nova partida entre dois jogadores e a adiciona ao estado.
// O broker é injetado para desacoplar o estado da lógica de comunicação.
func (sm *StateManager) CreateLocalMatch(p1, p2 *protocol.PlayerConn, broker *pubsub.Broker) *game.Match {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	matchID := fmt.Sprintf("local_match_%d", time.Now().UnixNano())
	match := game.NewMatch(matchID, p1, p2, sm.CardDB, broker, sm)
	sm.ActiveMatches[matchID] = match

	log.Printf("[STATE] Partida local %s criada entre %s e %s.", matchID, p1.ID, p2.ID)
	return match
}

// AddPlayerToQueue adiciona um jogador à fila de matchmaking de forma segura.
func (sm *StateManager) AddPlayerToQueue(player *protocol.PlayerConn) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Evita adicionar o mesmo jogador duas vezes
	for _, p := range sm.MatchmakingQueue {
		if p.ID == player.ID {
			log.Printf("[STATE] Jogador %s já está na fila de matchmaking.", player.ID)
			return
		}
	}
	sm.MatchmakingQueue = append(sm.MatchmakingQueue, player)
	log.Printf("[STATE] Jogador %s entrou na fila de matchmaking.", player.ID)
}

// RemoveMatch remove uma partida do gerenciador de estado.
func (sm *StateManager) RemoveMatch(matchID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.ActiveMatches, matchID)
	delete(sm.DistributedMatches, matchID)
	log.Printf("[STATE] Partida %s removida do estado.", matchID)
}

func (sm *StateManager) GetFirstPlayerInQueue() *protocol.PlayerConn {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.MatchmakingQueue) > 0 {
		return sm.MatchmakingQueue[0]
	}
	return nil
}

// ConfirmAndCreateDistributedMatch é chamada pelo servidor convidado para finalizar
// a criação de uma partida. Ele verifica se o jogador convidado ainda está disponível,
// remove-o da fila e cria as estruturas de estado para a partida distribuída.
func (sm *StateManager) ConfirmAndCreateDistributedMatch(matchID, guestPlayerID, hostPlayerID, hostAddr, guestAddr string, broker *pubsub.Broker) (*protocol.PlayerConn, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 1. Verifica se o jogador convidado (que está neste servidor) ainda está na fila.
	if len(sm.MatchmakingQueue) == 0 || sm.MatchmakingQueue[0].ID != guestPlayerID {
		return nil, errors.New("jogador não está mais disponível na fila")
	}

	// 2. Jogador está disponível! Remove-o da fila.
	guestPlayer := sm.MatchmakingQueue[0]
	sm.MatchmakingQueue = sm.MatchmakingQueue[1:]

	// 3. Regista os detalhes da partida distribuída.
	// Extrai o host real do RemoteAddr para construir um URL HTTP válido.
	hostIP := strings.Split(hostAddr, ":")[0]
	distMatch := &DistributedMatch{
		MatchID:     matchID,
		HostServer:  "http://" + hostIP + ":8000", // Assumindo que a porta da API é 8000
		GuestServer: guestAddr,
		HostPlayer:  hostPlayerID,
		GuestPlayer: guestPlayerID,
	}
	sm.DistributedMatches[matchID] = distMatch

	// 4. Cria uma partida "proxy" localmente. (Esta parte pode ser omitida se não
	// for necessário um objeto Match no servidor convidado, mas pode ser útil).
	hostPlayerConn := protocol.NewPlayerConn(hostPlayerID, nil)
	match := game.NewMatch(matchID, hostPlayerConn, guestPlayer, sm.CardDB, broker, sm) // Broker é nil aqui
	sm.ActiveMatches[matchID] = match	

	log.Printf("[STATE] Partida distribuída %s confirmada para o jogador local %s.", matchID, guestPlayerID)

	return guestPlayer, nil
}

// GetMatchmakingQueueSnapshot retorna uma cópia da fila de matchmaking atual.
// Retornar uma cópia (snapshot) evita problemas de concorrência se o chamador
// iterar sobre a fila enquanto outra goroutine a modifica.
func (sm *StateManager) GetMatchmakingQueueSnapshot() []*protocol.PlayerConn {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Cria uma nova slice com o mesmo tamanho e capacidade.
	snapshot := make([]*protocol.PlayerConn, len(sm.MatchmakingQueue))
	// Copia os elementos da fila original para a nova slice.
	copy(snapshot, sm.MatchmakingQueue)

	return snapshot
}

// RemovePlayersFromQueue remove um ou mais jogadores da fila de matchmaking.
// É uma operação segura que re-slice a fila para manter a sua integridade.
func (sm *StateManager) RemovePlayersFromQueue(playersToRemove ...*protocol.PlayerConn) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Cria um mapa para pesquisa rápida dos IDs dos jogadores a serem removidos.
	toRemove := make(map[string]bool)
	for _, p := range playersToRemove {
		toRemove[p.ID] = true
	}

	// Cria uma nova slice (newQueue) contendo apenas os jogadores que NÃO devem ser removidos.
	newQueue := make([]*protocol.PlayerConn, 0, len(sm.MatchmakingQueue))
	for _, p := range sm.MatchmakingQueue {
		if !toRemove[p.ID] {
			newQueue = append(newQueue, p)
		}
	}

	// Substitui a fila antiga pela nova.
	sm.MatchmakingQueue = newQueue
	log.Printf("[STATE] Jogadores removidos da fila. Novo tamanho: %d", len(sm.MatchmakingQueue))
}

// CreateDistributedMatchAsHost é chamada pelo servidor anfitrião para criar as
// estruturas de estado de uma partida distribuída.
func (sm *StateManager) CreateDistributedMatchAsHost(matchID string, hostPlayer *protocol.PlayerConn, guestPlayerID, hostAddr, guestAddr string, broker *pubsub.Broker) (*game.Match, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 1. Verifica se o jogador anfitrião ainda está online.
	if _, ok := sm.PlayersOnline[hostPlayer.ID]; !ok {
		return nil, errors.New("jogador anfitrião desconectou-se antes da criação da partida")
	}

	// 2. Cria um jogador "placeholder" para o oponente remoto.
	// Ele não tem uma conexão TCP (conn=nil), servindo apenas para manter o ID.
	guestPlayerPlaceholder := protocol.NewPlayerConn(guestPlayerID, nil)

	// 3. Regista os detalhes da partida distribuída.
	distMatchInfo := &DistributedMatch{
		MatchID:     matchID,
		HostServer:  hostAddr,
		GuestServer: guestAddr,
		HostPlayer:  hostPlayer.ID,
		GuestPlayer: guestPlayerID,
	}
	sm.DistributedMatches[matchID] = distMatchInfo

	// 4. Cria o objeto da partida localmente, com o jogador local como P1
	// e o placeholder do jogador remoto como P2.
	match := game.NewMatch(matchID, hostPlayer, guestPlayerPlaceholder, sm.CardDB, broker, sm)
	sm.ActiveMatches[matchID] = match

	log.Printf("[STATE] Partida distribuída %s criada pelo anfitrião %s.", matchID, hostPlayer.ID)

	return match, nil
}

// GetDistributedMatchInfo implementa a interface game.StateInformer.
func (sm *StateManager) GetDistributedMatchInfo(matchID string) (game.DistributedMatchInfo, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	distMatch, ok := sm.DistributedMatches[matchID]
	if !ok {
		return game.DistributedMatchInfo{}, false
	}

	// Traduz de state.DistributedMatch para game.DistributedMatchInfo
	return game.DistributedMatchInfo{
		MatchID:     distMatch.MatchID,
		HostServer:  distMatch.HostServer,
		GuestServer: distMatch.GuestServer,
		HostPlayer:  distMatch.HostPlayer,
		GuestPlayer: distMatch.GuestPlayer,
	}, true
}

// IsPlayerOnline implementa a interface game.StateInformer.
func (sm *StateManager) IsPlayerOnline(playerID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	_, ok := sm.PlayersOnline[playerID]
	return ok
}
