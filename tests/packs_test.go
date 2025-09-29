package main

import (
	"sync"
	"testing"

	"pingpong/server/packs"
)

// TestPlayerConn implementa a interface PlayerConn para testes
type TestPlayerConn struct {
	id string
}

func (m *TestPlayerConn) GetID() string {
	return m.id
}

func TestPackStoreConcurrency(t *testing.T) {
	const (
		numClients   = 20
		initialStock = 10
		cardsPerPack = 3
	)

	// Tabela de raridade
	rarityTable := []string{
		"c_001", "c_002", "c_003", "c_004", "c_005",
		"c_006", "c_007", "c_008", "c_009",
	}

	// Cria PackStore
	packStore := packs.NewPackStore(initialStock, cardsPerPack, rarityTable, 12345)

	// Canal para resultados
	type result struct {
		playerID string
		cards    []string
		err      error
	}
	results := make(chan result, numClients)
	var wg sync.WaitGroup

	// Lança goroutines concorrentes
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			player := &TestPlayerConn{id: "player_" + string(rune('A'+clientID))}
			cards, err := packStore.OpenPack(player)
			results <- result{
				playerID: player.GetID(),
				cards:    cards,
				err:      err,
			}
		}(i)
	}

	wg.Wait()
	close(results)

	// Analisa resultados
	var successes, failures int
	var allCards []string

	for res := range results {
		if res.err == nil {
			successes++
			allCards = append(allCards, res.cards...)

			// Verifica se o pacote tem o tamanho correto
			if len(res.cards) != cardsPerPack {
				t.Errorf("Pacote de %s tem tamanho incorreto: esperado %d, obteve %d",
					res.playerID, cardsPerPack, len(res.cards))
			}

			// Verifica se não há duplicatas no mesmo pacote
			seen := make(map[string]bool)
			for _, card := range res.cards {
				if seen[card] {
					t.Errorf("Carta duplicada no pacote de %s: %s", res.playerID, card)
				}
				seen[card] = true
			}
		} else {
			failures++
			if res.err != packs.ErrOutOfStock {
				t.Errorf("Erro inesperado para %s: %v", res.playerID, res.err)
			}
		}
	}

	// Validações principais
	if successes != initialStock {
		t.Errorf("Número de sucessos incorreto: esperado %d, obteve %d", initialStock, successes)
	}

	if failures != (numClients - initialStock) {
		t.Errorf("Número de falhas incorreto: esperado %d, obteve %d", numClients-initialStock, failures)
	}

	if packStore.GetStock() != 0 {
		t.Errorf("Estoque final incorreto: esperado 0, obteve %d", packStore.GetStock())
	}

	// Verifica log de auditoria
	auditLog := packStore.GetAuditLog()
	if len(auditLog) != successes {
		t.Errorf("Log de auditoria incorreto: esperado %d entradas, obteve %d", successes, len(auditLog))
	}

	t.Logf("Teste concluído com sucesso: %d sucessos, %d falhas, estoque final: %d",
		successes, failures, packStore.GetStock())
}

func TestPackStoreBasicFunctionality(t *testing.T) {
	rarityTable := []string{"c_001", "c_002", "c_003"}
	packStore := packs.NewPackStore(3, 2, rarityTable, 42)

	// Testa estoque inicial
	if stock := packStore.GetStock(); stock != 3 {
		t.Errorf("Estoque inicial incorreto: esperado 3, obteve %d", stock)
	}

	player := &TestPlayerConn{id: "test_player"}

	// Abre primeiro pacote
	cards1, err := packStore.OpenPack(player)
	if err != nil {
		t.Fatalf("Erro inesperado ao abrir primeiro pacote: %v", err)
	}
	if len(cards1) != 2 {
		t.Errorf("Primeiro pacote tem tamanho incorreto: esperado 2, obteve %d", len(cards1))
	}
	if packStore.GetStock() != 2 {
		t.Errorf("Estoque após primeiro pacote: esperado 2, obteve %d", packStore.GetStock())
	}

	// Abre segundo pacote
	cards2, err := packStore.OpenPack(player)
	if err != nil {
		t.Fatalf("Erro inesperado ao abrir segundo pacote: %v", err)
	}
	if len(cards2) != 2 {
		t.Errorf("Segundo pacote tem tamanho incorreto: esperado 2, obteve %d", len(cards2))
	}

	// Abre terceiro pacote
	_, err = packStore.OpenPack(player)
	if err != nil {
		t.Fatalf("Erro inesperado ao abrir terceiro pacote: %v", err)
	}
	if packStore.GetStock() != 0 {
		t.Errorf("Estoque após terceiro pacote: esperado 0, obteve %d", packStore.GetStock())
	}

	// Tenta abrir quarto pacote (deve falhar)
	_, err = packStore.OpenPack(player)
	if err != packs.ErrOutOfStock {
		t.Errorf("Esperado erro OUT_OF_STOCK, obteve: %v", err)
	}
}

func BenchmarkPackStoreConcurrency(b *testing.B) {
	rarityTable := []string{
		"c_001", "c_002", "c_003", "c_004", "c_005",
		"c_006", "c_007", "c_008", "c_009",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		packStore := packs.NewPackStore(100, 3, rarityTable, int64(i))
		var wg sync.WaitGroup

		for j := 0; j < 50; j++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				player := &TestPlayerConn{id: "bench_player_" + string(rune('A'+id))}
				packStore.OpenPack(player)
			}(j)
		}
		wg.Wait()
	}
}
