package commands

import (
	"sync"

	"james/hem/pkg/protocol"
)

// EventBroker is a bounded, process-local hint fanout. It never owns durable
// state; consumers must refetch authoritative data after reconnect or overflow.
type EventBroker struct {
	mu   sync.RWMutex
	subs map[chan *protocol.Response]struct{}
}

func NewEventBroker() *EventBroker {
	return &EventBroker{subs: make(map[chan *protocol.Response]struct{})}
}

func (b *EventBroker) Subscribe() (<-chan *protocol.Response, func()) {
	ch := make(chan *protocol.Response, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			close(ch)
			b.mu.Unlock()
		})
	}
}

func (b *EventBroker) Publish(resp *protocol.Response) {
	if b == nil || resp == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- resp:
		default:
			// The next polling cycle is the resync path. Never block an
			// agent callback or a Unix/MI6 request on a slow viewer.
		}
	}
}
