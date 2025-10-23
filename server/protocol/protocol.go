package protocol

import (
	"bufio"
	"encoding/json"
	"net"
)

// Mensagens do Cliente para o Servidor
type ClientMsg struct {
	T      string `json:"t"`
	CardID string `json:"cardId,omitempty"`
	Text   string `json:"text,omitempty"`
	TS     int64  `json:"ts,omitempty"`
}

// Mensagens do Servidor para o Cliente
type ServerMsg struct {
	T string `json:"t"`
	// Campos específicos por tipo
	MatchID    string      `json:"matchId,omitempty"`
	OpponentID string      `json:"opponentId,omitempty"`
	You        *PlayerView `json:"you,omitempty"`
	Opponent   *PlayerView `json:"opponent,omitempty"`
	Round      int         `json:"round,omitempty"`
	DeadlineMs int64       `json:"deadlineMs,omitempty"`
	Cards      []string    `json:"cards,omitempty"`
	Stock      int         `json:"stock,omitempty"`
	Code       string      `json:"code,omitempty"`
	Msg        string      `json:"msg,omitempty"`
	TS         int64       `json:"ts,omitempty"`
	RTTMs      int64       `json:"rttMs,omitempty"`
	Result     string      `json:"result,omitempty"`
	Logs       []string    `json:"logs,omitempty"`
	// Campos para chat
	SenderID string `json:"senderId,omitempty"`
	Text     string `json:"text,omitempty"`
}

// ClientAction é uma estrutura para encapsular a mensagem do cliente e quem a enviou
type ClientAction struct {
	Player *PlayerConn
	Msg    *ClientMsg
}

// PlayerView representa a visão de um jogador no estado da partida
type PlayerView struct {
	HP           int      `json:"hp"`
	Hand         []string `json:"hand,omitempty"`
	HandSize     int      `json:"handSize,omitempty"`
	CardID       string   `json:"cardId,omitempty"`
	ElementBonus int      `json:"elementBonus,omitempty"`
	DmgDealt     int      `json:"dmgDealt,omitempty"`
	DmgTaken     int      `json:"dmgTaken,omitempty"`
}

// TokenState representa o estado global que é passado junto com o token.
type TokenState struct {
	PackStock int `json:"packStock"`
}

// Constantes de tipos de mensagens
const (
	// Cliente -> Servidor
	FIND_MATCH = "FIND_MATCH"
	PLAY       = "PLAY"
	CHAT       = "CHAT"
	PING       = "PING"
	OPEN_PACK  = "OPEN_PACK"
	LEAVE      = "LEAVE"
	AUTOPLAY   = "AUTOPLAY"
	NOAUTOPLAY = "NOAUTOPLAY"
	REMATCH    = "REMATCH"

	// Servidor -> Cliente
	MATCH_FOUND      = "MATCH_FOUND"
	STATE            = "STATE"
	ROUND_RESULT     = "ROUND_RESULT"
	PACK_OPENED      = "PACK_OPENED"
	ERROR            = "ERROR"
	PONG             = "PONG"
	MATCH_END        = "MATCH_END"
	CHAT_MESSAGE     = "CHAT_MESSAGE"
	REMATCH_REQUEST  = "REMATCH_REQUEST"
	REMATCH_ACCEPTED = "REMATCH_ACCEPTED"
)

// Códigos de erro
const (
	INVALID_MESSAGE = "INVALID_MESSAGE"
	INVALID_CARD    = "INVALID_CARD"
	NOT_YOUR_TURN   = "NOT_YOUR_TURN"
	TIMEOUT_PLAY    = "TIMEOUT_PLAY"
	MATCH_NOT_FOUND = "MATCH_NOT_FOUND"
	OUT_OF_STOCK    = "OUT_OF_STOCK"
	INTERNAL        = "INTERNAL"
)

// Resultados de partida
const (
	WIN  = "WIN"
	LOSE = "LOSE"
	DRAW = "DRAW"
)

// PlayerConn representa um jogador conectado com encoder/decoder JSON
type PlayerConn struct {
	ID           string
	Conn         net.Conn
	Encoder      *json.Encoder
	Decoder      *json.Decoder
	Scanner      *bufio.Scanner
	LastPing     int64
	AutoPlay     bool
	WantsRematch bool
	LastOpponent string
}

// NewPlayerConn cria uma nova conexão de jogador
func NewPlayerConn(id string, conn net.Conn) *PlayerConn {
	return &PlayerConn{
		ID:      id,
		Conn:    conn,
		Encoder: json.NewEncoder(conn),
		Decoder: json.NewDecoder(conn),
		Scanner: bufio.NewScanner(conn),
	}
}

// SendMsg envia uma mensagem para o jogador
func (pc *PlayerConn) SendMsg(msg ServerMsg) error {
	return pc.Encoder.Encode(msg)
}

// ReadMsg lê uma mensagem do jogador
func (pc *PlayerConn) ReadMsg() (*ClientMsg, error) {
	if !pc.Scanner.Scan() {
		if err := pc.Scanner.Err(); err != nil {
			return nil, err
		}
		return nil, nil // EOF
	}

	var msg ClientMsg
	if err := json.Unmarshal(pc.Scanner.Bytes(), &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

// Close fecha a conexão
func (pc *PlayerConn) Close() error {
	return pc.Conn.Close()
}
