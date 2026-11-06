// Package events fans server-side change notifications out to WebSocket clients.
package events

import (
	"encoding/json"
	"sync"
	"time"
)

// Hub broadcasts JSON messages {"type": ..., "data": ...} to subscribers.
type Hub struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	pending map[string]bool
	timer   *time.Timer
}

func NewHub() *Hub {
	return &Hub{subs: map[chan []byte]struct{}{}, pending: map[string]bool{}}
}

// Subscribe returns a channel of messages and a cancel func.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *Hub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default: // slow client: drop; the UI resyncs on reconnect
		}
	}
}

// Send broadcasts immediately.
func (h *Hub) Send(kind string, data any) {
	b, err := json.Marshal(map[string]any{"type": kind, "data": data})
	if err == nil {
		h.broadcast(b)
	}
}

// Message encodes a message without broadcasting it.
func Message(kind string, data any) []byte {
	b, _ := json.Marshal(map[string]any{"type": kind, "data": data})
	return b
}

// Notify announces that something of the given kind changed. Bursts are
// coalesced so at most one message per kind goes out every 400 ms.
func (h *Hub) Notify(kind string) {
	h.mu.Lock()
	h.pending[kind] = true
	if h.timer == nil {
		h.timer = time.AfterFunc(400*time.Millisecond, h.flush)
	}
	h.mu.Unlock()
}

func (h *Hub) flush() {
	h.mu.Lock()
	kinds := make([]string, 0, len(h.pending))
	for k := range h.pending {
		kinds = append(kinds, k)
	}
	h.pending = map[string]bool{}
	h.timer = nil
	h.mu.Unlock()
	for _, k := range kinds {
		h.Send(k, nil)
	}
}
