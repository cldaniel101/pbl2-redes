package consensus

import (
	"testing"
)

// TestVectorialClock testa o funcionamento do relógio vetorial
func TestVectorialClock(t *testing.T) {
	// Cria três relógios para três servidores
	serverIDs := []string{"server-1", "server-2", "server-3"}

	vc1 := NewVectorialClock("server-1", serverIDs)
	vc2 := NewVectorialClock("server-2", serverIDs)
	_ = NewVectorialClock("server-3", serverIDs)

	// Testa estado inicial
	if vc1.GetValue("server-1") != 0 {
		t.Errorf("Valor inicial deveria ser 0, obtido %d", vc1.GetValue("server-1"))
	}

	// Testa Tick
	vc1.Tick()
	if vc1.GetValue("server-1") != 1 {
		t.Errorf("Após Tick, valor deveria ser 1, obtido %d", vc1.GetValue("server-1"))
	}

	// Testa Update
	vc2.Tick()
	vc2.Tick()
	snapshot := vc2.GetSnapshot()

	vc1.Update(snapshot)

	// vc1 deve ter: server-1=2 (1 anterior + 1 do update), server-2=2
	if vc1.GetValue("server-1") != 2 {
		t.Errorf("Após Update, server-1 deveria ter 2, obtido %d", vc1.GetValue("server-1"))
	}
	if vc1.GetValue("server-2") != 2 {
		t.Errorf("Após Update, server-2 deveria ter 2, obtido %d", vc1.GetValue("server-2"))
	}

	t.Log("✓ VectorialClock: Tick e Update funcionando corretamente")
}

// TestVectorialClockCompare testa a comparação de relógios vetoriais
func TestVectorialClockCompare(t *testing.T) {
	serverIDs := []string{"server-1", "server-2"}

	vc1 := NewVectorialClock("server-1", serverIDs)
	vc2 := NewVectorialClock("server-2", serverIDs)

	// Caso 1: vc1 < vc2
	vc1.SetValue("server-1", 1)
	vc1.SetValue("server-2", 0)

	vc2.SetValue("server-1", 2)
	vc2.SetValue("server-2", 1)

	if vc1.Compare(vc2.GetSnapshot()) != -1 {
		t.Errorf("vc1 deveria ser anterior a vc2")
	}

	// Caso 2: vc1 > vc2
	vc1.SetValue("server-1", 3)
	vc1.SetValue("server-2", 2)

	if vc1.Compare(vc2.GetSnapshot()) != 1 {
		t.Errorf("vc1 deveria ser posterior a vc2")
	}

	// Caso 3: vc1 e vc2 concorrentes
	vc1.SetValue("server-1", 5)
	vc1.SetValue("server-2", 1)

	vc2.SetValue("server-1", 2)
	vc2.SetValue("server-2", 3)

	if vc1.Compare(vc2.GetSnapshot()) != 0 {
		t.Errorf("vc1 e vc2 deveriam ser concorrentes")
	}

	t.Log("✓ VectorialClock: Compare funcionando corretamente")
}

// TestOperationOrdering testa a ordenação de operações
func TestOperationOrdering(t *testing.T) {
	// Cria operações com diferentes timestamps
	ts1 := map[string]int{"server-1": 1, "server-2": 0}
	ts2 := map[string]int{"server-1": 2, "server-2": 1}
	ts3 := map[string]int{"server-1": 1, "server-2": 1} // Concorrente com op1

	op1 := NewOperation(OpTypePlay, "match-1", "server-1", "player-1", "card-1", ts1)
	op2 := NewOperation(OpTypePlay, "match-1", "server-2", "player-2", "card-2", ts2)
	op3 := NewOperation(OpTypePlay, "match-1", "server-2", "player-3", "card-3", ts3)

	// op1 < op2
	if op1.CompareTimestamp(op2) != -1 {
		t.Errorf("op1 deveria ser anterior a op2")
	}

	// op2 > op1
	if op2.CompareTimestamp(op1) != 1 {
		t.Errorf("op2 deveria ser posterior a op1")
	}

	// op1 e op3 são concorrentes, mas o desempate por ServerID deve funcionar
	cmp := op1.CompareTimestamp(op3)
	if cmp == 0 {
		// Se retornou 0, significa que os timestamps são idênticos
		// Nesse caso, o desempate por ServerID deve ter sido aplicado
		if op1.ServerID >= op3.ServerID {
			t.Errorf("Desempate por ServerID não está funcionando corretamente")
		}
	}

	t.Log("✓ Operation: Ordenação funcionando corretamente")
}

// TestOperationQueue testa a fila de operações
func TestOperationQueue(t *testing.T) {
	queue := NewOperationQueue()

	// Testa fila vazia
	if !queue.IsEmpty() {
		t.Errorf("Fila deveria estar vazia")
	}

	if queue.Size() != 0 {
		t.Errorf("Tamanho da fila deveria ser 0")
	}

	// Adiciona operações em ordem aleatória
	ts1 := map[string]int{"server-1": 3, "server-2": 1}
	ts2 := map[string]int{"server-1": 1, "server-2": 0}
	ts3 := map[string]int{"server-1": 2, "server-2": 1}

	op1 := NewOperation(OpTypePlay, "match-1", "server-1", "player-1", "card-1", ts1)
	op2 := NewOperation(OpTypePlay, "match-1", "server-2", "player-2", "card-2", ts2)
	op3 := NewOperation(OpTypePlay, "match-1", "server-3", "player-3", "card-3", ts3)

	queue.Add(op1)
	queue.Add(op2)
	queue.Add(op3)

	if queue.Size() != 3 {
		t.Errorf("Tamanho da fila deveria ser 3, obtido %d", queue.Size())
	}

	// A ordem de remoção deve ser: op2, op3, op1 (baseado nos timestamps)
	removedOp := queue.Remove()
	if removedOp.ID != op2.ID {
		t.Errorf("Primeira operação removida deveria ser op2")
	}

	removedOp = queue.Remove()
	if removedOp.ID != op3.ID {
		t.Errorf("Segunda operação removida deveria ser op3")
	}

	removedOp = queue.Remove()
	if removedOp.ID != op1.ID {
		t.Errorf("Terceira operação removida deveria ser op1")
	}

	if !queue.IsEmpty() {
		t.Errorf("Fila deveria estar vazia após remover todas as operações")
	}

	t.Log("✓ OperationQueue: Funcionando corretamente")
}

// TestOperationQueueGetTop testa o método GetTop (sem remover)
func TestOperationQueueGetTop(t *testing.T) {
	queue := NewOperationQueue()

	ts1 := map[string]int{"server-1": 2}
	ts2 := map[string]int{"server-1": 1}

	op1 := NewOperation(OpTypePlay, "match-1", "server-1", "player-1", "card-1", ts1)
	op2 := NewOperation(OpTypePlay, "match-1", "server-2", "player-2", "card-2", ts2)

	queue.Add(op1)
	queue.Add(op2)

	// GetTop não deve remover
	top := queue.GetTop()
	if top.ID != op2.ID {
		t.Errorf("GetTop deveria retornar op2")
	}

	// Tamanho deve continuar 2
	if queue.Size() != 2 {
		t.Errorf("GetTop não deveria remover elementos")
	}

	// GetTop deve retornar o mesmo elemento
	top2 := queue.GetTop()
	if top2.ID != op2.ID {
		t.Errorf("GetTop deveria retornar o mesmo elemento")
	}

	t.Log("✓ OperationQueue: GetTop funcionando corretamente")
}

// TestOperationQueueRemoveByID testa a remoção por ID
func TestOperationQueueRemoveByID(t *testing.T) {
	queue := NewOperationQueue()

	ts := map[string]int{"server-1": 1}
	op1 := NewOperation(OpTypePlay, "match-1", "server-1", "player-1", "card-1", ts)
	op2 := NewOperation(OpTypePlay, "match-1", "server-2", "player-2", "card-2", ts)
	op3 := NewOperation(OpTypePlay, "match-1", "server-3", "player-3", "card-3", ts)

	queue.Add(op1)
	queue.Add(op2)
	queue.Add(op3)

	// Remove op2
	if !queue.RemoveByID(op2.ID) {
		t.Errorf("RemoveByID deveria retornar true")
	}

	if queue.Size() != 2 {
		t.Errorf("Tamanho deveria ser 2 após remover op2")
	}

	// Tenta remover novamente (deve retornar false)
	if queue.RemoveByID(op2.ID) {
		t.Errorf("RemoveByID de elemento inexistente deveria retornar false")
	}

	t.Log("✓ OperationQueue: RemoveByID funcionando corretamente")
}

// TestOperationClone testa a clonagem de operações
func TestOperationClone(t *testing.T) {
	ts := map[string]int{"server-1": 1, "server-2": 2}
	op := NewOperation(OpTypePlay, "match-1", "server-1", "player-1", "card-1", ts)

	clone := op.Clone()

	// Verifica se os campos são iguais
	if clone.ID != op.ID || clone.Type != op.Type || clone.MatchID != op.MatchID {
		t.Errorf("Clone não preservou os campos básicos")
	}

	// Verifica se o timestamp foi copiado (não referenciado)
	clone.Timestamp["server-1"] = 999
	if op.Timestamp["server-1"] == 999 {
		t.Errorf("Clone não deveria afetar o original")
	}

	t.Log("✓ Operation: Clone funcionando corretamente")
}

// TestVectorialClockClone testa a clonagem de relógios vetoriais
func TestVectorialClockClone(t *testing.T) {
	serverIDs := []string{"server-1", "server-2"}
	vc := NewVectorialClock("server-1", serverIDs)

	vc.Tick()
	vc.Tick()

	clone := vc.Clone()

	// Verifica se os valores são iguais
	if clone.GetValue("server-1") != vc.GetValue("server-1") {
		t.Errorf("Clone não preservou os valores")
	}

	// Modifica o clone
	clone.Tick()

	// Verifica se o original não foi afetado
	if vc.GetValue("server-1") == clone.GetValue("server-1") {
		t.Errorf("Clone não deveria afetar o original")
	}

	t.Log("✓ VectorialClock: Clone funcionando corretamente")
}
