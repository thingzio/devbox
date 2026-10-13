// Package hub is an in-process publish/subscribe channel for change events.
package hub

import "sync"

// Kind is what an event reports.
type Kind uint8

const (
	// PRChanged says a PR's stored state or result changed.
	PRChanged Kind = iota
	// Polled says a repository poll was recorded, successful or not; Number is zero.
	Polled
)

// Event is one change notification.
type Event struct {
	Kind   Kind
	RepoID int64
	Number int
}

// Hub fans events out to subscribers without blocking publishers.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	closed bool
}

// New returns an open hub.
func New() *Hub { return &Hub{subs: map[chan Event]struct{}{}} }

// Subscribe returns an event channel and an idempotent unsubscribe function.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Publish delivers ev to every subscriber with buffer room. A full subscriber
// misses it but re-fetches its whole view on the events it does receive.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Close ends every subscription; later subscriptions are closed immediately.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// Subscribers is the number of active subscriptions.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
