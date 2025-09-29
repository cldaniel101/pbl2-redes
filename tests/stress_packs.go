package main

import (
	"fmt"
	"sync"
	"time"

	"pingpong/server/packs"
)

// MockPlayerConn implementa a interface PlayerConn para testes
type MockPlayerConn struct {
	id string
}

func (m *MockPlayerConn) GetID() string {
	return m.id
}

// TestResult armazena o resultado de um teste de abertura de pacote
type TestResult struct {
	PlayerID string
	Cards    []string
	Error    error
	Success  bool
}

func main() {
	fmt.Println("=== TESTE DE STRESS: ABERTURA DE PACOTES ===")
	fmt.Println()

	// Configuração do teste
	const (
		numClients   = 20
		initialStock = 10
		cardsPerPack = 3
	)

	// Tabela de raridade simples (IDs das cartas disponíveis)
	rarityTable := []string{
		"c_001", "c_002", "c_003", "c_004", "c_005",
		"c_006", "c_007", "c_008", "c_009",
	}

	// Cria PackStore com estoque inicial de 10
	packStore := packs.NewPackStore(initialStock, cardsPerPack, rarityTable, 12345) // seed fixo para reprodutibilidade

	fmt.Printf("📦 Estoque inicial: %d pacotes\n", packStore.GetStock())
	fmt.Printf("👥 Número de clientes simultâneos: %d\n", numClients)
	fmt.Printf("🃏 Cartas por pacote: %d\n", cardsPerPack)
	fmt.Println()

	// Canal para resultados
	results := make(chan TestResult, numClients)
	var wg sync.WaitGroup

	// Inicia timestamp para medir tempo
	startTime := time.Now()

	// Lança 20 goroutines "clientes" tentando abrir pacotes simultaneamente
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			player := &MockPlayerConn{
				id: fmt.Sprintf("player_%02d", clientID),
			}

			cards, err := packStore.OpenPack(player)

			results <- TestResult{
				PlayerID: player.GetID(),
				Cards:    cards,
				Error:    err,
				Success:  err == nil,
			}
		}(i + 1)
	}

	// Espera todos terminarem
	wg.Wait()
	close(results)

	duration := time.Since(startTime)

	// Coleta resultados
	var successes, failures int
	var allCards []string
	var successfulPlayers []string
	var failedPlayers []string
	var allResults []TestResult

	fmt.Println("=== RESULTADOS ===")
	for result := range results {
		allResults = append(allResults, result)
		if result.Success {
			successes++
			successfulPlayers = append(successfulPlayers, result.PlayerID)
			allCards = append(allCards, result.Cards...)
			fmt.Printf("✅ %s: %v\n", result.PlayerID, result.Cards)
		} else {
			failures++
			failedPlayers = append(failedPlayers, result.PlayerID)
			fmt.Printf("❌ %s: %v\n", result.PlayerID, result.Error)
		}
	}

	fmt.Println()
	fmt.Printf("⏱️  Tempo total: %v\n", duration)
	fmt.Printf("✅ Sucessos: %d\n", successes)
	fmt.Printf("❌ Falhas: %d\n", failures)
	fmt.Printf("📦 Estoque final: %d\n", packStore.GetStock())
	fmt.Println()

	// Validações
	fmt.Println("=== VALIDAÇÕES ===")

	// 1. Deve haver exatamente 10 sucessos e 10 falhas
	if successes == initialStock && failures == (numClients-initialStock) {
		fmt.Printf("✅ Controle de estoque correto: %d sucessos, %d falhas\n", successes, failures)
	} else {
		fmt.Printf("❌ Erro no controle de estoque: esperado %d sucessos e %d falhas, obteve %d sucessos e %d falhas\n",
			initialStock, numClients-initialStock, successes, failures)
	}

	// 2. Estoque final deve ser 0
	finalStock := packStore.GetStock()
	if finalStock == 0 {
		fmt.Println("✅ Estoque final correto: 0")
	} else {
		fmt.Printf("❌ Estoque final incorreto: esperado 0, obteve %d\n", finalStock)
	}

	// 3. Validar tamanho correto dos pacotes
	packSizeValid := true
	for _, result := range allResults {
		if result.Success && len(result.Cards) != cardsPerPack {
			packSizeValid = false
			fmt.Printf("❌ Pacote com tamanho incorreto para %s: esperado %d, obteve %d\n",
				result.PlayerID, cardsPerPack, len(result.Cards))
		}
	}
	if packSizeValid {
		fmt.Println("✅ Todos os pacotes têm o tamanho correto")
	}

	// 4. Verificar se não há cartas duplicadas no mesmo pacote
	noDuplicatesInPack := true

	for _, result := range allResults {
		if result.Success {
			seen := make(map[string]bool)
			for _, card := range result.Cards {
				if seen[card] {
					noDuplicatesInPack = false
					fmt.Printf("❌ Carta duplicada no pacote de %s: %s\n", result.PlayerID, card)
				}
				seen[card] = true
			}
		}
	}
	if noDuplicatesInPack {
		fmt.Println("✅ Nenhuma carta duplicada dentro dos pacotes")
	}

	// 5. Verificar log de auditoria
	auditLog := packStore.GetAuditLog()
	if len(auditLog) == successes {
		fmt.Printf("✅ Log de auditoria correto: %d entradas\n", len(auditLog))
	} else {
		fmt.Printf("❌ Log de auditoria incorreto: esperado %d entradas, obteve %d\n", successes, len(auditLog))
	}

	// 6. Todos os erros devem ser OUT_OF_STOCK
	allErrorsCorrect := true
	for _, result := range allResults {
		if !result.Success && result.Error != packs.ErrOutOfStock {
			allErrorsCorrect = false
			fmt.Printf("❌ Erro inesperado para %s: %v\n", result.PlayerID, result.Error)
		}
	}
	if allErrorsCorrect {
		fmt.Println("✅ Todos os erros são do tipo correto (OUT_OF_STOCK)")
	}

	fmt.Println()
	fmt.Println("=== RESUMO DO LOG DE AUDITORIA ===")
	for i, audit := range auditLog {
		fmt.Printf("[%d] %s -> %s: %v (em %s)\n",
			i+1, audit.PackID, audit.PlayerID, audit.Cards, audit.Timestamp.Format("15:04:05.000"))
	}

	fmt.Println()
	if successes == initialStock && failures == (numClients-initialStock) &&
		finalStock == 0 && packSizeValid && noDuplicatesInPack &&
		len(auditLog) == successes && allErrorsCorrect {
		fmt.Println("🎉 TESTE PASSOU: Todos os critérios foram atendidos!")
	} else {
		fmt.Println("❌ TESTE FALHOU: Alguns critérios não foram atendidos.")
	}
}
