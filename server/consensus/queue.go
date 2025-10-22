package consensus

import (
	"container/heap"
	"fmt"
	"sync"
)

// OperationQueue é uma fila de prioridade ordenada por timestamp vetorial
// Usa um heap para manter as operações ordenadas
type OperationQueue struct {
	mu    sync.RWMutex
	items *operationHeap
}

// NewOperationQueue cria uma nova fila de operações
func NewOperationQueue() *OperationQueue {
	oq := &OperationQueue{
		items: &operationHeap{},
	}
	heap.Init(oq.items)
	return oq
}

// Add adiciona uma operação à fila
func (oq *OperationQueue) Add(op *Operation) {
	oq.mu.Lock()
	defer oq.mu.Unlock()

	heap.Push(oq.items, op)
}

// Remove remove e retorna a operação com menor timestamp
func (oq *OperationQueue) Remove() *Operation {
	oq.mu.Lock()
	defer oq.mu.Unlock()

	if oq.items.Len() == 0 {
		return nil
	}

	return heap.Pop(oq.items).(*Operation)
}

// GetTop retorna (sem remover) a operação com menor timestamp
func (oq *OperationQueue) GetTop() *Operation {
	oq.mu.RLock()
	defer oq.mu.RUnlock()

	if oq.items.Len() == 0 {
		return nil
	}

	return (*oq.items)[0]
}

// IsEmpty verifica se a fila está vazia
func (oq *OperationQueue) IsEmpty() bool {
	oq.mu.RLock()
	defer oq.mu.RUnlock()

	return oq.items.Len() == 0
}

// Size retorna o número de operações na fila
func (oq *OperationQueue) Size() int {
	oq.mu.RLock()
	defer oq.mu.RUnlock()

	return oq.items.Len()
}

// Contains verifica se uma operação com determinado ID está na fila
func (oq *OperationQueue) Contains(operationID string) bool {
	oq.mu.RLock()
	defer oq.mu.RUnlock()

	for _, op := range *oq.items {
		if op.ID == operationID {
			return true
		}
	}

	return false
}

// RemoveByID remove uma operação específica pelo ID
func (oq *OperationQueue) RemoveByID(operationID string) bool {
	oq.mu.Lock()
	defer oq.mu.Unlock()

	for i, op := range *oq.items {
		if op.ID == operationID {
			heap.Remove(oq.items, i)
			return true
		}
	}

	return false
}

// GetAll retorna todas as operações na fila (sem remover)
func (oq *OperationQueue) GetAll() []*Operation {
	oq.mu.RLock()
	defer oq.mu.RUnlock()

	result := make([]*Operation, oq.items.Len())
	copy(result, *oq.items)
	return result
}

// Clear remove todas as operações da fila
func (oq *OperationQueue) Clear() {
	oq.mu.Lock()
	defer oq.mu.Unlock()

	oq.items = &operationHeap{}
	heap.Init(oq.items)
}

// ToString retorna uma representação em string da fila
func (oq *OperationQueue) ToString() string {
	oq.mu.RLock()
	defer oq.mu.RUnlock()

	if oq.items.Len() == 0 {
		return "Queue{empty}"
	}

	result := "Queue{\n"
	for i, op := range *oq.items {
		result += fmt.Sprintf("  [%d] %s\n", i, op.ToString())
	}
	result += "}"

	return result
}

// operationHeap implementa heap.Interface para ordenar operações por timestamp vetorial
type operationHeap []*Operation

func (h operationHeap) Len() int {
	return len(h)
}

func (h operationHeap) Less(i, j int) bool {
	// Compara os timestamps vetoriais
	cmp := h[i].CompareTimestamp(h[j])

	// Se i aconteceu antes de j, i tem prioridade
	if cmp < 0 {
		return true
	}

	// Se j aconteceu antes de i, j tem prioridade
	if cmp > 0 {
		return false
	}

	// Se são concorrentes (cmp == 0), já há desempate por ServerID no CompareTimestamp
	// Mas como precaução, verificamos aqui também
	return h[i].ServerID < h[j].ServerID
}

func (h operationHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *operationHeap) Push(x interface{}) {
	*h = append(*h, x.(*Operation))
}

func (h *operationHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
