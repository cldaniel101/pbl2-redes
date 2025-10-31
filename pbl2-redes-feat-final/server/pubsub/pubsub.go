package pubsub

import (
	"sync"
)

// Message representa uma mensagem no sistema pub/sub
type Message struct {
	Topic   string
	Payload interface{}
}

// Subscriber é um canal de assinante
type Subscriber chan Message

// Broker gerencia tópicos e assinantes
type Broker struct {
	mu          sync.RWMutex
	topics      map[string]map[Subscriber]bool
	subscribers map[Subscriber]bool
}

// NewBroker cria um novo Broker
func NewBroker() *Broker {
	return &Broker{
		topics:      make(map[string]map[Subscriber]bool),
		subscribers: make(map[Subscriber]bool),
	}
}

// Subscribe adiciona um novo assinante a um tópico
func (b *Broker) Subscribe(topic string) Subscriber {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub := make(Subscriber, 1) // Buffered channel
	b.subscribers[sub] = true

	if b.topics[topic] == nil {
		b.topics[topic] = make(map[Subscriber]bool)
	}
	b.topics[topic][sub] = true

	return sub
}

// Unsubscribe remove um assinante de todos os tópicos
func (b *Broker) Unsubscribe(sub Subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Remove de todos os tópicos
	for topic := range b.topics {
		delete(b.topics[topic], sub)
	}

	// Remove da lista de assinantes e fecha o canal
	if _, ok := b.subscribers[sub]; ok {
		delete(b.subscribers, sub)
		close(sub)
	}
}

// Publish envia uma mensagem para todos os assinantes de um tópico
func (b *Broker) Publish(topic string, payload interface{}) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.topics[topic] == nil {
		return
	}

	msg := Message{Topic: topic, Payload: payload}
	for sub := range b.topics[topic] {
		// Envia de forma não bloqueante para evitar que um assinante lento
		// atrase todo o sistema.
		select {
		case sub <- msg:
		default:
			// O assinante não está pronto para receber.
			// Dependendo do caso de uso, pode-se logar, descartar ou
			// tentar reenviar mais tarde. Por enquanto, vamos descartar.
		}
	}
}
