package s2s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"pingpong/server/consensus"
	"pingpong/server/protocol"
	"time"
)

// ForwardAction retransmite a ação de um jogador (ex: jogar uma carta) para o servidor do oponente.
func ForwardAction(opponentServer, matchID, playerID, cardID string) {
	// Garantir que todos os campos necessários estejam presentes
	payload := map[string]string{
		"playerId": playerID, // ID real do jogador que fez a jogada
		"cardId":   cardID,   // ID da carta jogada
	}
	jsonPayload, _ := json.Marshal(payload)

	// Construir URL do endpoint no servidor do oponente
	url := fmt.Sprintf("%s/matches/%s/action", opponentServer, matchID)

	// Log para debug
	log.Printf("[S2S] Enviando jogada do jogador %s com carta %s para %s", playerID, cardID, url)

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
	} else {
		log.Printf("[S2S] Jogada retransmitida com sucesso para %s", url)
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

// ========================================
// Funções do Sistema de Consenso
// ========================================

// ProposeOperation envia uma proposta de operação para outro servidor (Fase 1)
func ProposeOperation(remoteServer, matchID string, op *consensus.Operation) error {
	jsonPayload, err := json.Marshal(op)
	if err != nil {
		return fmt.Errorf("falha ao serializar operação: %v", err)
	}

	url := fmt.Sprintf("%s/api/matches/%s/propose", remoteServer, matchID)
	log.Printf("[S2S] Propondo operação %s para %s", op.ID, url)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("falha ao enviar proposta para %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erro ao propor operação: status %d - %s", resp.StatusCode, string(body))
	}

	log.Printf("[S2S] Operação %s proposta com sucesso para %s", op.ID, remoteServer)
	return nil
}

// SendACK envia um ACK de uma operação para outro servidor
func SendACK(remoteServer, matchID, operationID, senderServerID string) error {
	payload := map[string]string{
		"operationId": operationID,
		"serverId":    senderServerID,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("falha ao serializar ACK: %v", err)
	}

	url := fmt.Sprintf("%s/api/matches/%s/ack", remoteServer, matchID)
	log.Printf("[S2S] Enviando ACK da operação %s para %s", operationID, url)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("falha ao enviar ACK para %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erro ao enviar ACK: status %d - %s", resp.StatusCode, string(body))
	}

	log.Printf("[S2S] ACK da operação %s enviado com sucesso para %s", operationID, remoteServer)
	return nil
}

// CheckOperation solicita verificação de validade de uma operação (Fase 2)
func CheckOperation(remoteServer, matchID string, op *consensus.Operation) (bool, error) {
	jsonPayload, err := json.Marshal(op)
	if err != nil {
		return false, fmt.Errorf("falha ao serializar operação: %v", err)
	}

	url := fmt.Sprintf("%s/api/matches/%s/check", remoteServer, matchID)
	log.Printf("[S2S] Verificando operação %s em %s", op.ID, url)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return false, fmt.Errorf("falha ao verificar operação em %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf("[S2S] Operação %s é válida em %s", op.ID, remoteServer)
		return true, nil
	} else if resp.StatusCode == http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[S2S] Operação %s é inválida em %s: %s", op.ID, remoteServer, string(body))
		return false, nil
	} else {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("erro ao verificar operação: status %d - %s", resp.StatusCode, string(body))
	}
}

// CommitOperation solicita execução de uma operação após consenso (Fase 2)
func CommitOperation(remoteServer, matchID string, op *consensus.Operation) error {
	jsonPayload, err := json.Marshal(op)
	if err != nil {
		return fmt.Errorf("falha ao serializar operação: %v", err)
	}

	url := fmt.Sprintf("%s/api/matches/%s/commit", remoteServer, matchID)
	log.Printf("[S2S] Comitando operação %s em %s", op.ID, url)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("falha ao comitar operação em %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erro ao comitar operação: status %d - %s", resp.StatusCode, string(body))
	}

	log.Printf("[S2S] Operação %s comitada com sucesso em %s", op.ID, remoteServer)
	return nil
}

// RollbackOperation solicita rollback de uma operação (Fase 2)
func RollbackOperation(remoteServer, matchID string, op *consensus.Operation) error {
	jsonPayload, err := json.Marshal(op)
	if err != nil {
		return fmt.Errorf("falha ao serializar operação: %v", err)
	}

	url := fmt.Sprintf("%s/api/matches/%s/rollback", remoteServer, matchID)
	log.Printf("[S2S] Solicitando rollback da operação %s em %s", op.ID, url)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("falha ao solicitar rollback em %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erro ao solicitar rollback: status %d - %s", resp.StatusCode, string(body))
	}

	log.Printf("[S2S] Rollback da operação %s solicitado com sucesso em %s", op.ID, remoteServer)
	return nil
}
