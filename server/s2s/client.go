package s2s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"pingpong/server/protocol"
	"strings"
	"time")

// ForwardAction retransmite a ação de um jogador (ex: jogar uma carta) para o servidor do oponente.
func ForwardAction(opponentServer, matchID, playerID, cardID string) {
	// Garantir que todos os campos necessários estejam presentes
	payload := map[string]string{
		"playerId": playerID, // ID real do jogador (NÃO usar RemoteAddr)
		"cardId":   cardID,   // ID da carta a ser jogada
	}
	jsonPayload, _ := json.Marshal(payload)

	// Construir URL usando o nome do serviço ao invés do IP
	url := fmt.Sprintf("http://server-%s:8000/matches/%s/action", 
		strings.TrimPrefix(opponentServer, "http://server-"),
		matchID)

	// Configurar cliente HTTP com timeout
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Enviar request
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		log.Printf("[S2S] Falha ao retransmitir a ação para %s: %v", url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Ler o corpo do erro para mais detalhes
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[S2S] Erro ao retransmitir ação: status code %d - %s", resp.StatusCode, string(body))
	}
}

// ForwardMessage forwards a server message to a remote player via their server.
func ForwardMessage(remoteServer, playerID string, msg protocol.ServerMsg) {
	jsonPayload, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[S2S] Failed to marshal message for forwarding: %v", err)
		return
	}

	url := fmt.Sprintf("%s/api/forward/message", remoteServer)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		log.Printf("[S2S] Failed to create request to forward message: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Player-ID", playerID)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[S2S] Failed to forward message to %s: %v", url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[S2S] Error forwarding message: status code %d", resp.StatusCode)
	}
}
