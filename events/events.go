// Package events provides a lightweight in-process pub/sub event hub.
//
// It has no dependency on the rest of the daemon (stdlib only), so it can be
// imported by any package (models, app, ...) without creating import cycles.
package events

import "sync"

// EventUpgradeProgress is the event type published while an apt upgrade is
// in progress.
const EventUpgradeProgress = "upgrade_progress"

// subscriberBufferSize is the size of the per-subscriber channel buffer.
// Publish is non-blocking: if a subscriber's channel is full, the event is
// dropped for that subscriber.
const subscriberBufferSize = 32

// Event is a typed message broadcast to subscribers.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Subscriber receives events published on the Hub it subscribed to.
type Subscriber struct {
	Ch chan Event
}

// Hub is a simple pub/sub broadcaster. The zero value is not usable, use
// NewHub to create one.
type Hub struct {
	mu     sync.RWMutex
	subs   map[*Subscriber]struct{}
	closed bool
}

// NewHub creates a new, ready to use Hub.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[*Subscriber]struct{}),
	}
}

// Subscribe registers a new Subscriber and returns it. The returned
// Subscriber's Ch will receive every Event subsequently published on the
// Hub, unless its buffer is full in which case events are dropped.
func (h *Hub) Subscribe() *Subscriber {
	s := &Subscriber{Ch: make(chan Event, subscriberBufferSize)}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		// Hub already closed: return a subscriber with an already-closed
		// channel so callers reading from it get a clean, immediate close.
		close(s.Ch)
		return s
	}

	h.subs[s] = struct{}{}
	return s
}

// Unsubscribe removes s from the Hub and closes its channel. It is
// idempotent: calling it more than once (or with an unknown/nil
// Subscriber) is a no-op.
func (h *Hub) Unsubscribe(s *Subscriber) {
	if s == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	close(s.Ch)
}

// Publish broadcasts an Event of the given type and data to all current
// subscribers. It never blocks: subscribers whose channel buffer is full
// simply miss the event. Publish on a closed Hub is a no-op. data should be
// a complete snapshot (not a delta).
func (h *Hub) Publish(evType string, data any) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.closed {
		return
	}

	ev := Event{Type: evType, Data: data}
	for s := range h.subs {
		select {
		case s.Ch <- ev:
		default:
			// Slow consumer: drop the event rather than block.
		}
	}
}

// Close marks the Hub as closed and closes every subscriber's channel.
// Subsequent Publish calls become no-ops. Close is idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	h.closed = true

	for s := range h.subs {
		close(s.Ch)
		delete(h.subs, s)
	}
}

// Default is the package-level Hub used by the top-level convenience
// functions below.
var Default = NewHub()

// Subscribe registers a new Subscriber on the Default hub.
func Subscribe() *Subscriber {
	return Default.Subscribe()
}

// Unsubscribe removes s from the Default hub.
func Unsubscribe(s *Subscriber) {
	Default.Unsubscribe(s)
}

// Publish broadcasts an event on the Default hub.
func Publish(evType string, data any) {
	Default.Publish(evType, data)
}

// Close closes the Default hub.
func Close() {
	Default.Close()
}
