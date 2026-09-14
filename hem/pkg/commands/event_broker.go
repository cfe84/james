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
			// Replace one queued hint with an explicit bounded resync marker.
			// Never block an agent callback or a Unix/MI6 request.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- &protocol.Response{Status: protocol.StatusOK, Event: protocol.EventResyncRequired}:
			default:
			}
		}
	}
}
