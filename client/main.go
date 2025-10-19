package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Estruturas de mensagens (simplificadas para o cliente)
type ClientMsg struct {
	T      string `json:"t"`
	CardID string `json:"cardId,omitempty"`
	Text   string `json:"text,omitempty"`
	TS     int64  `json:"ts,omitempty"`
}

type ServerMsg struct {
	T          string      `json:"t"`
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

type PlayerView struct {
	HP           int      `json:"hp"`
	Hand         []string `json:"hand,omitempty"`
	HandSize     int      `json:"handSize,omitempty"`
	CardID       string   `json:"cardId,omitempty"`
	ElementBonus int      `json:"elementBonus,omitempty"`
	DmgDealt     int      `json:"dmgDealt,omitempty"`
	DmgTaken     int      `json:"dmgTaken,omitempty"`
}

// Estrutura para informações das cartas (simulada do servidor)
type Card struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Element string `json:"element"`
	ATK     int    `json:"atk"`
	DEF     int    `json:"def"`
}

// Base de dados de cartas local (simulada - em um jogo real viria do servidor)
var cardDB = map[string]Card{
	"c_001": {ID: "c_001", Name: "Fire Dragon", Element: "FIRE", ATK: 8, DEF: 5},
	"c_002": {ID: "c_002", Name: "Ice Mage", Element: "WATER", ATK: 6, DEF: 6},
	"c_003": {ID: "c_003", Name: "Vine Beast", Element: "PLANT", ATK: 7, DEF: 4},
	"c_004": {ID: "c_004", Name: "Flame Warrior", Element: "FIRE", ATK: 6, DEF: 7},
	"c_005": {ID: "c_005", Name: "Water Serpent", Element: "WATER", ATK: 9, DEF: 3},
	"c_006": {ID: "c_006", Name: "Forest Guardian", Element: "PLANT", ATK: 5, DEF: 8},
	"c_007": {ID: "c_007", Name: "Inferno Titan", Element: "FIRE", ATK: 10, DEF: 2},
	"c_008": {ID: "c_008", Name: "Frost Giant", Element: "WATER", ATK: 7, DEF: 7},
	"c_009": {ID: "c_009", Name: "Nature Spirit", Element: "PLANT", ATK: 4, DEF: 9},
}

func main() {
	addr := getEnv("SERVER_ADDR", "localhost:9000")
	for {
		log.Printf("[CLIENT] dialing %s ...", addr)
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			log.Printf("[CLIENT] dial error: %v (retrying in 1s)", err)
			time.Sleep(time.Second)
			continue
		}
		handleConn(conn)
	}
}

var (
	showPing    bool
	pingMutex   sync.RWMutex
	inMatch     bool
	currentHand []string
	// CORREÇÃO: A variável 'gameState' foi removida porque não era utilizada.
)

func handleConn(conn net.Conn) {
	defer conn.Close()
	peer := conn.RemoteAddr().String()
	log.Printf("[CLIENT] Conectado ao servidor %s", peer)

	encoder := json.NewEncoder(conn)
	scanner := bufio.NewScanner(conn)

	// Goroutine para receber mensagens do servidor
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			var msg ServerMsg
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				log.Printf("[CLIENT] Erro ao decodificar JSON: %v", err)
				continue
			}
			handleServerMessage(&msg)
		}
		if err := scanner.Err(); err != nil {
			log.Printf("[CLIENT] Erro de leitura: %v", err)
		}
		log.Printf("[CLIENT] Servidor fechou a conexão")
	}()

	// Envia FIND_MATCH automaticamente
	sendMessage(encoder, ClientMsg{T: "FIND_MATCH"})
	fmt.Println("🔍 Procurando partida...")

	// Goroutine para enviar PINGs periódicos
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			timestamp := time.Now().UnixMilli()
			sendMessage(encoder, ClientMsg{T: "PING", TS: timestamp})
		}
	}()

	// Goroutine para ler entrada do usuário
	go func() {
		inputScanner := bufio.NewScanner(os.Stdin)
		fmt.Println("\n=== ATTRIBUTE WAR CLIENT ===")
		fmt.Println("Comandos disponíveis:")
		fmt.Println("  /play <idx> - Jogar carta pelo índice (1-5)")
		fmt.Println("  /hand       - Mostrar sua mão atual")
		fmt.Println("  /ping       - Liga/desliga exibição de RTT")
		fmt.Println("  /pack       - Abrir pacote de cartas")
		fmt.Println("  /autoplay   - Ativar autoplay (cartas automáticas após 12s)")
		fmt.Println("  /noautoplay - Desativar autoplay (tempo ilimitado)")
		fmt.Println("  /rematch    - Solicitar nova partida com último oponente")
		fmt.Println("  /help       - Mostrar ajuda")
		fmt.Println("  /quit       - Sair do jogo")
		fmt.Println("  [1-5]       - Atalho para jogar carta")
		fmt.Println("  <mensagem>  - Enviar chat")
		fmt.Println()

		for inputScanner.Scan() {
			text := strings.TrimSpace(inputScanner.Text())
			if text == "" {
				continue
			}

			if strings.HasPrefix(text, "/") {
				handleCommand(text, encoder)
			} else if inMatch && len(text) == 1 && text >= "1" && text <= "5" {
				cardIndex, _ := strconv.Atoi(text)
				playCardByIndex(cardIndex, encoder)
			} else {
				sendMessage(encoder, ClientMsg{T: "CHAT", Text: text})
			}
		}
	}()

	select {}
}

func sendMessage(encoder *json.Encoder, msg ClientMsg) {
	if err := encoder.Encode(msg); err != nil {
		log.Printf("[CLIENT] Erro ao enviar mensagem: %v", err)
	}
}

func playCardByIndex(cardIndex int, encoder *json.Encoder) {
	if !inMatch {
		fmt.Println("❌ Você não está em uma partida!")
		return
	}

	if cardIndex < 1 || cardIndex > len(currentHand) {
		fmt.Printf("❌ Índice inválido! Use 1-%d\n", len(currentHand))
		return
	}

	cardID := currentHand[cardIndex-1]
	card, exists := cardDB[cardID]
	if !exists {
		fmt.Printf("🎴 Jogando carta %d: %s\n", cardIndex, cardID)
	} else {
		fmt.Printf("🎴 Jogando carta %d: %s (%s %d/%d)\n",
			cardIndex, card.Name, card.Element, card.ATK, card.DEF)
	}

	sendMessage(encoder, ClientMsg{T: "PLAY", CardID: cardID})
}

func showHand() {
	if !inMatch || len(currentHand) == 0 {
		fmt.Println("❌ Você não está em uma partida ou não tem cartas na mão!")
		return
	}

	fmt.Println("\n🃏 === SUA MÃO ===")
	for i, cardID := range currentHand {
		card, exists := cardDB[cardID]
		if exists {
			fmt.Printf("  [%d] %s - %s (ATK: %d / DEF: %d)\n",
				i+1, card.Name, card.Element, card.ATK, card.DEF)
		} else {
			fmt.Printf("  [%d] %s (dados não disponíveis)\n", i+1, cardID)
		}
	}
	fmt.Println()
}

func handleServerMessage(msg *ServerMsg) {
	switch msg.T {
	case "MATCH_FOUND":
		fmt.Printf("🎮 Partida encontrada! Oponente: %s\n", msg.OpponentID)
		inMatch = true

	case "STATE":
		// CORREÇÃO: A atribuição a 'gameState' foi removida.
		currentHand = msg.You.Hand
		fmt.Printf("\n=== RODADA %d ===\n", msg.Round)
		fmt.Printf("💚 Seu HP: %d | ❤️ HP do Oponente: %d\n", msg.You.HP, msg.Opponent.HP)
		fmt.Printf("🃏 Sua mão (%d cartas):\n", len(msg.You.Hand))
		for i, cardID := range msg.You.Hand {
			card, exists := cardDB[cardID]
			if exists {
				fmt.Printf("  [%d] %s - %s (ATK: %d / DEF: %d)\n",
					i+1, card.Name, card.Element, card.ATK, card.DEF)
			} else {
				fmt.Printf("  [%d] %s\n", i+1, cardID)
			}
		}
		if msg.DeadlineMs > 0 {
			fmt.Printf("⏰ Tempo para jogar: %.1f segundos (autoplay ativo)\n", float64(msg.DeadlineMs)/1000)
		} else {
			fmt.Println("⏰ Tempo ilimitado para jogar (autoplay desativado)")
		}
		fmt.Println("Digite o número da carta (1-5) ou use /play <número>:")

	case "ROUND_RESULT":
		fmt.Println("\n=== RESULTADO DA RODADA ===")
		yourCard, yourExists := cardDB[msg.You.CardID]
		if yourExists {
			fmt.Printf("🎴 Você jogou: %s (ATK %d", yourCard.Name, yourCard.ATK)
			if msg.You.ElementBonus > 0 {
				fmt.Printf("+%d", msg.You.ElementBonus)
			}
			fmt.Print(")")
		} else {
			fmt.Printf("🎴 Você jogou: %s", msg.You.CardID)
		}
		oppCard, oppExists := cardDB[msg.Opponent.CardID]
		if oppExists {
			fmt.Printf("\n🎴 Oponente jogou: %s (DEF %d)", oppCard.Name, oppCard.DEF)
		} else {
			fmt.Printf("\n🎴 Oponente jogou: %s", msg.Opponent.CardID)
		}
		fmt.Printf("\n⚔️ Dano causado: %d | 🛡️ Dano recebido: %d\n", msg.You.DmgDealt, msg.You.DmgTaken)
		fmt.Printf("💚 Seu HP: %d | ❤️ HP do Oponente: %d\n", msg.You.HP, msg.Opponent.HP)
		if len(msg.Logs) > 0 {
			fmt.Println("📜 Logs:")
			for _, log := range msg.Logs {
				fmt.Printf("  %s\n", log)
			}
		}

	case "MATCH_END":
		fmt.Printf("\n🏁 PARTIDA FINALIZADA! Resultado: %s\n", msg.Result)
		switch msg.Result {
		case "WIN":
			fmt.Println("🎉 VITÓRIA! Parabéns!")
		case "LOSE":
			fmt.Println("😞 Derrota... Tente novamente!")
		case "DRAW":
			fmt.Println("🤝 Empate!")
		}
		inMatch = false
		currentHand = nil

	case "PACK_OPENED":
		fmt.Printf("📦 Pacote aberto! Cartas recebidas: %v\n", msg.Cards)
		fmt.Printf("📊 Estoque restante: %d pacotes\n", msg.Stock)

	case "ERROR":
		// CORREÇÃO: Adicionado um caso específico para "QUEUED" para formatar a mensagem corretamente.
		switch msg.Code {
		case "QUEUED":
			fmt.Printf("✅ %s\n", msg.Msg)
		case "AUTOPLAY_ENABLED":
			fmt.Printf("✅ %s\n", msg.Msg)
		case "AUTOPLAY_DISABLED":
			fmt.Printf("✅ %s\n", msg.Msg)
		case "REMATCH_REQUESTED":
			fmt.Printf("🔄 %s\n", msg.Msg)
		case "NO_LAST_OPPONENT", "OPPONENT_NOT_ONLINE", "ALREADY_IN_MATCH":
			fmt.Printf("❌ %s\n", msg.Msg)
		default:
			fmt.Printf("❌ Erro [%s]: %s\n", msg.Code, msg.Msg)
		}

	case "PONG":
		pingMutex.RLock()
		if showPing {
			fmt.Printf("🏓 RTT: %d ms\n", msg.RTTMs)
		}
		pingMutex.RUnlock()

	case "CHAT_MESSAGE":
		fmt.Printf("💬 %s: %s\n", msg.SenderID, msg.Text)

	case "REMATCH_REQUEST":
		fmt.Printf("\n🔄 === SOLICITAÇÃO DE REMATCH ===\n")
		fmt.Printf("📢 %s\n", msg.Msg)
		fmt.Printf("Digite /rematch para aceitar ou ignore para recusar.\n")

	case "REMATCH_ACCEPTED":
		fmt.Printf("\n🎉 === REMATCH ACEITO ===\n")
		fmt.Printf("✅ %s\n", msg.Msg)
		fmt.Printf("🎮 Nova partida com %s!\n", msg.OpponentID)
		inMatch = true
	}
}

func handleCommand(command string, encoder *json.Encoder) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return
	}

	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/play":
		if len(parts) < 2 {
			fmt.Println("❌ Uso: /play <índice> (exemplo: /play 1)")
			return
		}
		cardIndex, err := strconv.Atoi(parts[1])
		if err != nil {
			fmt.Println("❌ Índice deve ser um número entre 1-5")
			return
		}
		playCardByIndex(cardIndex, encoder)

	case "/hand":
		showHand()

	case "/ping":
		pingMutex.Lock()
		showPing = !showPing
		pingMutex.Unlock()
		if showPing {
			fmt.Println("🏓 Exibição de RTT ativada")
		} else {
			fmt.Println("🏓 Exibição de RTT desativada")
		}

	case "/pack":
		sendMessage(encoder, ClientMsg{T: "OPEN_PACK"})
		fmt.Println("📦 Tentando abrir pacote...")

	case "/autoplay":
		sendMessage(encoder, ClientMsg{T: "AUTOPLAY"})
		fmt.Println("⏰ Ativando autoplay...")

	case "/noautoplay":
		sendMessage(encoder, ClientMsg{T: "NOAUTOPLAY"})
		fmt.Println("⏰ Desativando autoplay...")

	case "/rematch":
		sendMessage(encoder, ClientMsg{T: "REMATCH"})
		fmt.Println("🔄 Solicitando rematch...")

	case "/help":
		fmt.Println("\n=== AJUDA ===")
		fmt.Println("  /play <idx> - Jogar carta pelo índice (1-5)")
		fmt.Println("  /hand       - Mostrar sua mão atual")
		fmt.Println("  /ping       - Liga/desliga exibição de RTT")
		fmt.Println("  /pack       - Abrir pacote de cartas")
		fmt.Println("  /autoplay   - Ativar autoplay (cartas automáticas após 12s)")
		fmt.Println("  /noautoplay - Desativar autoplay (tempo ilimitado)")
		fmt.Println("  /rematch    - Solicitar nova partida com último oponente")
		fmt.Println("  /help       - Mostrar esta ajuda")
		fmt.Println("  /quit       - Sair do jogo")
		fmt.Println("  [1-5]       - Atalho para jogar carta")
		fmt.Println("  <mensagem>  - Enviar chat")
		fmt.Println()

	case "/quit":
		sendMessage(encoder, ClientMsg{T: "LEAVE"})
		fmt.Println("👋 Saindo do jogo...")
		os.Exit(0)

	default:
		fmt.Printf("❓ Comando desconhecido: %s. Digite /help para ajuda.\n", cmd)
	}
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}