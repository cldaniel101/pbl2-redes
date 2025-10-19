package s2s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"pingpong/server/protocol"
)

// ForwardPlay forwards a player's card play to the opponent's server.
func ForwardPlay(opponentServer, matchID, playerID, cardID string) {
	payload := map[string]string{
		"matchId":  matchID,
		"playerId": playerID,
		"cardId":   cardID,
	}
	jsonPayload, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/forward/play", opponentServer)
	_, err := http.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		log.Printf("[S2S] Failed to forward play to %s: %v", url, err)
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
