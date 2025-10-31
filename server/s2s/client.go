package s2s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"pingpong/server/protocol"
	"time"
)

// ForwardAction retransmite ação de um jogador para o servidor do oponente.
// Retorna um erro se a comunicação falhar.
func ForwardAction(opponentServer, matchID, playerID, cardID string) error {
	payload := map[string]string{
		"playerId": playerID,
		"cardId":   cardID,
	}
	jsonPayload, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/matches/%s/action", opponentServer, matchID)
	log.Printf("[S2S] Enviando jogada do jogador %s para %s", playerID, url)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		log.Printf("[S2S] Falha ao retransmitir a ação para %s: %v", url, err)
		return fmt.Errorf("não foi possível conectar ao servidor do oponente: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[S2S] Erro ao retransmitir ação: status code %d - %s", resp.StatusCode, string(body))
		return fmt.Errorf("o servidor do oponente retornou um erro (status %d)", resp.StatusCode)
	}

	log.Printf("[S2S] Jogada retransmitida com sucesso para %s", url)
	return nil
}

// ForwardMessage forwards a server message to a remote player via their server.
// Retorna um erro se a comunicação falhar.
func ForwardMessage(remoteServer, playerID string, msg protocol.ServerMsg) error {
	jsonPayload, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[S2S] Falha ao serializar mensagem para retransmissão: %v", err)
		return err
	}

	url := fmt.Sprintf("%s/api/forward/message", remoteServer)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		log.Printf("[S2S] Falha ao criar requisição para retransmitir mensagem: %v", err)
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Player-ID", playerID)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[S2S] Falha ao retransmitir mensagem para %s: %v", url, err)
		return fmt.Errorf("não foi possível conectar ao servidor do oponente: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[S2S] Erro ao retransmitir mensagem: status code %d", resp.StatusCode)
		return fmt.Errorf("o servidor do oponente retornou um erro (status %d)", resp.StatusCode)
	}

	return nil
}
