package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStressCluster(t *testing.T) {
	// Compilar o binário do servidor para o diretório de teste
	buildCmd := exec.Command("go", "build", "-o", "server_test_bin", ".")
	buildCmd.Dir = "../server"
	buildOutput, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Falha ao compilar o servidor: %v\n%s", err, string(buildOutput))
	}
	defer os.Remove("server_test_bin") // Limpa o binário após o teste

	// Configuração dos servidores
	numServers := 3
	initialTCPPort := 9000
	initialAPIPort := 8000
	var serverCmds []*exec.Cmd
	var allServersAPIs []string

	for i := 0; i < numServers; i++ {
		allServersAPIs = append(allServersAPIs, fmt.Sprintf("http://localhost:%d", initialAPIPort+i))
	}
	allServersStr := strings.Join(allServersAPIs, ",")

	// Iniciar servidores
	for i := 0; i < numServers; i++ {
		tcpAddr := ":" + strconv.Itoa(initialTCPPort+i)
		apiAddr := ":" + strconv.Itoa(initialAPIPort+i)

		cmd := exec.Command("./server_test_bin")
		// cmd.Dir = "../server" // Não é mais necessário, o binário está local
		cmd.Env = append(os.Environ(),
			"LISTEN_ADDR="+tcpAddr,
			"API_ADDR="+apiAddr,
			"ALL_SERVERS="+allServersStr,
			"HOSTNAME=localhost",
		)

		// Redirecionar a saída para o log de teste para depuração
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err := cmd.Start()
		if err != nil {
			t.Fatalf("Falha ao iniciar o servidor %d: %v", i, err)
		}
		serverCmds = append(serverCmds, cmd)
		t.Logf("Servidor %d iniciado com TCP em %s e API em %s", i, tcpAddr, apiAddr)
	}

	// Cleanup: garantir que todos os servidores sejam terminados
	t.Cleanup(func() {
		t.Log("A terminar os processos do servidor...")
		for _, cmd := range serverCmds {
			if cmd.Process != nil {
				if err := cmd.Process.Kill(); err != nil {
					t.Errorf("Falha ao terminar o processo do servidor %d: %v", cmd.Process.Pid, err)
				}
			}
		}
	})

	// Aguardar os servidores estarem prontos
	t.Log("A aguardar os servidores estarem prontos...")
	time.Sleep(5 * time.Second) // Simples espera, pode ser melhorada

	// --- Início da lógica do cliente ---
	initialStock := 1000
	numClients := initialStock + 200 // Simular mais clientes do que o estoque
	results := make(chan string, numClients)
	var wg sync.WaitGroup

	t.Logf("A iniciar %d clientes para o teste de estresse...", numClients)

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			// Round-robin para distribuir clientes entre os servidores
			serverIdx := clientID % numServers
			serverTCPAddr := fmt.Sprintf("localhost:%d", initialTCPPort+serverIdx)

			conn, err := net.Dial("tcp", serverTCPAddr)
			if err != nil {
				t.Errorf("Cliente %d falhou ao conectar-se a %s: %v", clientID, serverTCPAddr, err)
				results <- "CONNECTION_ERROR"
				return
			}
			defer conn.Close()

			// Enviar comando OPEN_PACK
			openPackMsg := `{"t": "OPEN_PACK"}`
			_, err = conn.Write([]byte(openPackMsg + "\n"))
			if err != nil {
				t.Errorf("Cliente %d falhou ao enviar mensagem: %v", clientID, err)
				results <- "SEND_ERROR"
				return
			}

			// Ler a resposta
			reader := bufio.NewReader(conn)
			response, err := reader.ReadString('\n')
			if err != nil {
				t.Errorf("Cliente %d falhou ao ler a resposta: %v", clientID, err)
				results <- "READ_ERROR"
				return
			}

			// Analisar a resposta para determinar o tipo
			var serverMsg struct {
				T    string `json:"t"`
				Code string `json:"code,omitempty"`
			}
			if err := json.Unmarshal([]byte(response), &serverMsg); err != nil {
				t.Errorf("Cliente %d falhou ao analisar JSON da resposta '%s': %v", clientID, response, err)
				results <- "JSON_ERROR"
				return
			}

			if serverMsg.T == "PACK_OPENED" {
				results <- "PACK_OPENED"
			} else if serverMsg.T == "ERROR" && serverMsg.Code == "OUT_OF_STOCK" {
				results <- "OUT_OF_STOCK"
			} else {
				t.Errorf("Cliente %d recebeu uma resposta inesperada: %s", clientID, response)
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

func TestClusterWithServerFailure(t *testing.T) {
	// --- Setup dos Servidores (similar ao teste anterior) ---
	buildCmd := exec.Command("go", "build", "-o", "server_test_bin", ".")
	buildCmd.Dir = "../server"
	buildOutput, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Falha ao compilar o servidor: %v\n%s", err, string(buildOutput))
	}
	defer os.Remove("server_test_bin")

	numServers := 3
	initialTCPPort := 9010 // Usar portas diferentes para evitar conflito
	initialAPIPort := 8010
	var serverCmds []*exec.Cmd
	var allServersAPIs []string
	for i := 0; i < numServers; i++ {
		allServersAPIs = append(allServersAPIs, fmt.Sprintf("http://localhost:%d", initialAPIPort+i))
	}
	allServersStr := strings.Join(allServersAPIs, ",")

	for i := 0; i < numServers; i++ {
		cmd := exec.Command("./server_test_bin")
		cmd.Env = append(os.Environ(),
			"LISTEN_ADDR=:"+strconv.Itoa(initialTCPPort+i),
			"API_ADDR=:"+strconv.Itoa(initialAPIPort+i),
			"ALL_SERVERS="+allServersStr,
			"HOSTNAME=localhost",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("Falha ao iniciar o servidor %d: %v", i, err)
		}
		serverCmds = append(serverCmds, cmd)
	}

	t.Cleanup(func() {
		for _, cmd := range serverCmds {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
	})

	t.Log("Aguardando servidores iniciarem...")
	time.Sleep(5 * time.Second)

	// --- Simulação de falha ---
	serverToKillIndex := 1
	t.Logf("--> A simular falha do servidor %d em 3 segundos...", serverToKillIndex)
	time.AfterFunc(3*time.Second, func() {
		t.Logf("!!! Matando o servidor %d !!!", serverToKillIndex)
		if err := serverCmds[serverToKillIndex].Process.Kill(); err != nil {
			t.Errorf("Falha ao matar o servidor %d: %v", serverToKillIndex, err)
		}
	})

	// --- Lógica dos Clientes (com tentativas contínuas) ---
	initialStock := 1000
	numClients := initialStock + 100
	results := make(chan string, numClients)
	var wg sync.WaitGroup

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			serverIdx := clientID % numServers
			serverTCPAddr := fmt.Sprintf("localhost:%d", initialTCPPort+serverIdx)

			conn, err := net.DialTimeout("tcp", serverTCPAddr, 1*time.Second)
			if err != nil {
				results <- "CONNECTION_ERROR"
				return
			}
			defer conn.Close()

			openPackMsg := `{"t": "OPEN_PACK"}` + "\n"
			if _, err := conn.Write([]byte(openPackMsg)); err != nil {
				results <- "SEND_ERROR"
				return
			}

			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			reader := bufio.NewReader(conn)
			response, err := reader.ReadString('\n')
			if err != nil {
				results <- "READ_ERROR"
				return
			}

			var serverMsg struct {
				T    string `json:"t"`
				Code string `json:"code,omitempty"`
			}
			json.Unmarshal([]byte(response), &serverMsg)

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

	// --- Validação dos Resultados ---
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

	t.Logf("Resultados com falha: %d PACK_OPENED, %d OUT_OF_STOCK, %d erros", packOpenedCount, outOfStockCount, errorCount)

	// Validação: o número de pacotes abertos com sucesso deve ser menor que o estoque inicial,
	// pois alguns clientes não conseguirão se conectar ou concluir a operação.
	if packOpenedCount >= initialStock {
		t.Errorf("Esperado menos de %d pacotes abertos, mas foram %d", initialStock, packOpenedCount)
	}
	if packOpenedCount == 0 {
		t.Error("Nenhum pacote foi aberto, o que indica que o cluster pode ter parado completamente.")
	}
	// O número total de respostas bem-sucedidas (abertas + sem estoque) mais os erros deve ser igual ao número de clientes.
	totalResponses := packOpenedCount + outOfStockCount + errorCount
	if totalResponses != numClients {
		t.Errorf("Número total de respostas (%d) não corresponde ao número de clientes (%d)", totalResponses, numClients)
	}

	t.Log("O teste de resiliência concluiu que o cluster continuou a operar após a falha de um nó.")
}
