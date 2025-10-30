package main

import (
	"bufio"
	"encoding/json"
	"strconv"
	"net"
	"sync"
	"testing"
	"time"
)

// ESTE TESTE ASSUME QUE OS 3 SERVIDORES JÁ ESTÃO A RODAR NAS SUAS MÁQUINAS REAIS
func TestStressRemoteCluster(t *testing.T) {

	// --- Configuração dos Servidores ---
	// !!! MUDE ISTO PARA OS SEUS IPS REAIS !!!
	serverIPs := []string{
		"192.168.1.10",
		"192.168.1.11",
		"192.168.1.12",
	}
	
	numServers := len(serverIPs)
	tcpPort := 9000 // A porta TCP que os seus servidores usam
	
	// A lógica de compilação, arranque e cleanup do servidor foi REMOVIDA.

	// Aguardar para garantir que os servidores estão prontos (opcional)
	t.Log("A assumir que os servidores já estão a rodar remotamente...")
	time.Sleep(2 * time.Second) 

	// --- Início da lógica do cliente ---
	const initialStock = 1000 // Mude isto para o stock real no seu state/manager.go
	const numClients = initialStock + 200 
	
	results := make(chan string, numClients)
	var wg sync.WaitGroup

	t.Logf("A iniciar %d clientes para o teste de estresse contra %s...", numClients, serverIPs)

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			// Round-robin para distribuir clientes entre os servidores
			serverIdx := clientID % numServers
			targetIP := serverIPs[serverIdx] // IP real
			serverTCPAddr := net.JoinHostPort(targetIP, strconv.Itoa(tcpPort)) // Endereço TCP real

			conn, err := net.Dial("tcp", serverTCPAddr)
			if err != nil {
				t.Logf("Cliente %d falhou ao conectar-se a %s: %v", clientID, serverTCPAddr, err)
				results <- "CONNECTION_ERROR"
				return
			}
			defer conn.Close()

			// Enviar comando OPEN_PACK
			openPackMsg := `{"t": "OPEN_PACK"}`
			_, err = conn.Write([]byte(openPackMsg + "\n"))
			if err != nil {
				results <- "SEND_ERROR"
				return
			}

			// Ler a resposta
			reader := bufio.NewReader(conn)
			// Definir um deadline para a leitura
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			response, err := reader.ReadString('\n')
			if err != nil {
				results <- "READ_ERROR"
				return
			}

			// Analisar a resposta (Lógica idêntica ao original)
			var serverMsg struct {
				T    string `json:"t"`
				Code string `json:"code,omitempty"`
			}
			if err := json.Unmarshal([]byte(response), &serverMsg); err != nil {
				results <- "JSON_ERROR"
				return
			}

			if serverMsg.T == "PACK_OPENED" {
				results <- "PACK_OPENED"
			} else if serverMsg.T == "ERROR" && serverMsg.Code == "OUT_OF_STOCK" {
				results <- "OUT_OF_STOCK"
			} else {
				results <- "UNEXPECTED_RESPONSE"
			}
		}(i)
	}

	wg.Wait()
	close(results)

	// --- Validação dos resultados ---
	packOpenedCount := 0
	outOfStockCount := 0
	errorCount := 0

	for result := range results {
		switch result {
		case "PACK_OPENED":
			packOpenedCount++
		case "OUT_OF_STOCK":
			outOfStockCount++
		default:
			errorCount++
		}
	}

	t.Logf("Resultados: %d PACK_OPENED, %d OUT_OF_STOCK, %d erros", packOpenedCount, outOfStockCount, errorCount)

	// Validação final
	if packOpenedCount != initialStock {
		t.Errorf("Contagem de PACK_OPENED esperada: %d, obtida: %d", initialStock, packOpenedCount)
	}
	if outOfStockCount != (numClients - initialStock) {
		t.Errorf("Contagem de OUT_OF_STOCK esperada: %d, obtida: %d", numClients-initialStock, outOfStockCount)
	}
	if errorCount > 0 {
		t.Errorf("%d clientes encontraram erros durante o teste", errorCount)
	}
}
