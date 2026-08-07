package sse

import "sync"

// clientBuffer is the per-client channel depth. A client that falls this far
// behind is dropped on the next broadcast rather than stalling the fan-out.
const clientBuffer = 16

// hub is the in-memory per-client fan-out. One LISTEN connection feeds it; it
// broadcasts each Event to every subscribed client with a non-blocking send,
// so a slow or vanished client can never wedge the others (or the listener).
type hub struct {
	mu      sync.Mutex
	clients map[chan Event]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[chan Event]struct{})}
}

// subscribe registers a new client and returns its buffered channel.
func (h *hub) subscribe() chan Event {
	ch := make(chan Event, clientBuffer)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// unsubscribe removes a client. Idempotent.
func (h *hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// broadcast delivers ev to every client, dropping any whose buffer is full.
func (h *hub) broadcast(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
			// Slow client: drop this event for it. On reconnect the client
			// resumes from its Last-Event-ID, so no message is lost durably.
		}
	}
}
