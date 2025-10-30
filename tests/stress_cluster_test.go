package main

import (
	"bufio"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// ServerInfo armazena o IP e a Porta de um servidor do cluster
type ServerInfo struct {
	IP   string
	Port int
}

// ESTE TESTE ASSUME QUE OS 3 SERVIDORES JÁ ESTÃO A RODAR NAS SUAS MÁQUINAS REAIS
func TestStressMatchmaking(t *testing.T) {

	// --- 1. Configuração dos Servidores ---
	// !!! MUDE OS IPS PARA OS IPS REAIS DAS SUAS MÁQUINAS !!!
	servers := []ServerInfo{
		{IP: "172.16.103.11", Port: 9000}, // Servidor 1
		{IP: "172.16.103.12", Port: 9001}, // Servidor 2
		{IP: "172.16.103.13", Port: 9002}, // Servidor 3
	}
	// ---------------------------------------------------

	numServers := len(servers)

	// --- 2. Configuração do Teste ---
	// Vamos usar um número par de clientes para formar pares
	const numClients = 100 // 50 pares = 50 partidas
	
	// Aumenta a rampa para dar tempo ao matchmaking de processar
	const clientRampUpDelay = 100 * time.Millisecond 
	
	// Timeout total para um cliente esperar por uma partida
	const matchmakingTimeout = 20 * time.Second
	// ---------------------------------------------------

	results := make(chan string, numClients)
	var wg sync.WaitGroup

	t.Logf("A iniciar %d clientes (rampa de %v) para stress de MATCHMAKING...", 
		numClients, clientRampUpDelay)

	// --- 3. Execução dos Clientes ---
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			// Round-robin para distribuir clientes entre os servidores
			server := servers[clientID%numServers]
			serverTCPAddr := net.JoinHostPort(server.IP, strconv.Itoa(server.Port))

			// Conecta ao servidor
			conn, err := net.DialTimeout("tcp", serverTCPAddr, 5*time.Second)
			if err != nil {
				t.Logf("[Cliente %d] Falhou ao conectar-se a %s: %v", clientID, serverTCPAddr, err)
				results <- "CONNECTION_ERROR"
				return
			}
			defer conn.Close()

			// Envia comando FIND_MATCH
			findMatchMsg := `{"t": "FIND_MATCH"}`
			_, err = conn.Write([]byte(findMatchMsg + "\n"))
			if err != nil {
				t.Logf("[Cliente %d] Falhou ao enviar FIND_MATCH para %s: %v", clientID, serverTCPAddr, err)
				results <- "SEND_ERROR"
				return
			}

			// Define um deadline LONGO. O cliente vai esperar até 20s por uma partida.
			conn.SetReadDeadline(time.Now().Add(matchmakingTimeout))
			reader := bufio.NewReader(conn)

			// Loop de leitura: O cliente espera por "QUEUED" e depois por "MATCH_FOUND"
			for {
				response, err := reader.ReadString('\n')
				if err != nil {
					// Isto é um TIMEOUT - o cliente esperou 20s e não encontrou partida
					t.Logf("[Cliente %d] Timeout à espera de partida em %s: %v", clientID, serverTCPAddr, err)
					results <- "MATCH_TIMEOUT"
					return
				}

				// Analisar a resposta
				var serverMsg struct {
					T    string `json:"t"`
					Code string `json:"code,omitempty"`
				}
				json.Unmarshal([]byte(response), &serverMsg)

				// "QUEUED" é uma resposta esperada, continuamos à espera
				if serverMsg.T == "ERROR" && serverMsg.Code == "QUEUED" {
					t.Logf("[Cliente %d] Na fila em %s. A aguardar partida...", clientID, serverTCPAddr)
					continue
				}

				// "MATCH_FOUND" é o nosso SUCESSO!
				if serverMsg.T == "MATCH_FOUND" {
					t.Logf("[Cliente %d] SUCESSO: Partida encontrada em %s!", clientID, serverTCPAddr)
					results <- "MATCH_SUCCESS"
					return // O teste para este cliente terminou
				}
				
				// Qualquer outra mensagem é inesperada
				t.Logf("[Cliente %d] Resposta inesperada de %s: %s", clientID, serverTCPAddr, response)
			}

		}(i)
		
		// Pausa entre o arranque de cada cliente (rampa)
		time.Sleep(clientRampUpDelay)
	}

	// --- 4. Validação dos Resultados ---
	t.Log("Todos os clientes foram iniciados. A aguardar o matchmaking...")
	wg.Wait()
	close(results)
	t.Log("Todos os clientes terminaram (encontraram partida ou expiraram).")

	matchSuccessCount := 0
	matchTimeoutCount := 0
	errorCount := 0

	for result := range results {
		switch result {
		case "MATCH_SUCCESS":
			matchSuccessCount++
		case "MATCH_TIMEOUT":
			matchTimeoutCount++
		default:
			errorCount++
		}
	}

	t.Logf("--- RESULTADO FINAL ---")
	t.Logf("Clientes que encontraram partida: %d", matchSuccessCount)
	t.Logf("Clientes que expiraram (timeout): %d", matchTimeoutCount)
	t.Logf("Erros de Conexão/Envio:         %d", errorCount)
	t.Logf("-----------------------")


	// Validação final:
	// O seu token auto-refresca-se, 
	// por isso o "estoque" é infinito.
	// Todos os clientes devem encontrar uma partida.
	if matchSuccessCount != numClients {
		t.Errorf("Contagem de SUCESSO esperada: %d, obtida: %d", numClients, matchSuccessCount)
	}
	if matchTimeoutCount > 0 {
		t.Errorf("%d clientes sofreram TIMEOUT à espera de uma partida.", matchTimeoutCount)
	}
	if errorCount > 0 {
		t.Errorf("%d clientes encontraram erros de conexão/envio.", errorCount)
	}
}