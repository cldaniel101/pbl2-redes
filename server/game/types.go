package game

// Element representa os tipos elementais do jogo
type Element string

const (
	FIRE  Element = "FIRE"
	WATER Element = "WATER"
	PLANT Element = "PLANT"
)

// Card representa uma carta do jogo
type Card struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Element Element `json:"element"`
	ATK     int     `json:"atk"`
	DEF     int     `json:"def"`
}

// Hand representa a mão de um jogador (IDs das cartas)
type Hand []string

// Constantes do jogo configuráveis
const (
	HPStart           = 20
	HandSize          = 5
	ElementalATKBonus = 3
	RoundPlayTimeout  = 12_000 // ms
	MatchIdleTimeout  = 60_000 // ms
	KeepAliveInterval = 5_000  // ms para PING
	KeepAliveTimeout  = 30_000 // ms sem tráfego
	ReconnectWindow   = 10_000 // ms para reconexão rápida
)

// Constantes do sistema de consenso (Two-Phase Commit)
const (
	ConsensusACKTimeout      = 5_000  // ms - timeout para receber ACKs de todos os servidores
	ConsensusCheckTimeout    = 3_000  // ms - timeout para verificação cross-server
	ConsensusCommitTimeout   = 5_000  // ms - timeout para commit em outros servidores
	ConsensusPollInterval    = 100    // ms - intervalo de verificação de ACKs
	ConsensusMaxRetries      = 3      // número máximo de tentativas de operação
	ConsensusOperationExpiry = 30_000 // ms - tempo até operação pendente expirar
)

// PlayerStatus representa o estado de um jogador
type PlayerStatus string

const (
	StatusIdle         PlayerStatus = "IDLE"
	StatusQueued       PlayerStatus = "QUEUED"
	StatusMatching     PlayerStatus = "MATCHING"
	StatusInMatch      PlayerStatus = "IN_MATCH"
	StatusDisconnected PlayerStatus = "DISCONNECTED"
)

// MatchState representa o estado interno de uma partida
type MatchState string

const (
	StateAwaitingPlays MatchState = "AWAITING_PLAYS"
	StateResolving     MatchState = "RESOLVING"
	StateBroadcasting  MatchState = "BROADCASTING"
	StateNextRound     MatchState = "NEXT_ROUND"
	StateEnded         MatchState = "ENDED"
)

// RoundResult representa o resultado de uma rodada
type RoundResult struct {
	P1Card        Card
	P2Card        Card
	P1Bonus       int
	P2Bonus       int
	P1DamageDealt int
	P1DamageTaken int
	P2DamageDealt int
	P2DamageTaken int
	P1HPAfter     int
	P2HPAfter     int
	Logs          []string
}

// PackConfig representa a configuração dos pacotes
type PackConfig struct {
	CardsPerPack int
	Stock        int
	RNGSeed      int64
}
