// Package subscriptions demonstrates how to wire MCP resource
// subscriptions by hand against a generator-exposed resource URI
// template. protomcp deliberately does NOT codegen subscribe handlers:
// the notification source is a backend concern (pub/sub, CDC, polling,
// message bus, in-memory hub) and the SDK treats subscribe as a user
// concern. See the README in this directory for the full rationale.
//
// The package is self-contained: a trivial in-process Hub stands in for
// whatever backend notification source a real service would plug in. A
// Manager wraps the Hub with per-(session, URI) bookkeeping and session-
// close cleanup, and exposes Subscribe / Unsubscribe methods that are
// direct SDK ServerOptions.SubscribeHandler / UnsubscribeHandler shapes.
package subscriptions

import "sync"

// Hub is a trivial topic → subscribers multicast. Real services replace
// it with a CDC feed, pub/sub client, polling loop, or message-bus
// consumer. The rest of the example stays the same.
type Hub struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subscribers: map[string]map[chan struct{}]struct{}{}}
}

// Publish wakes every subscriber registered for topic. Slow subscribers
// drop the tick rather than block the publisher, the MCP contract is
// "a notification fires eventually on change", not "every mutation
// delivers". Tune the channel buffer in Subscribe if you need stronger
// guarantees.
func (h *Hub) Publish(topic string) {
	h.mu.Lock()
	subs := h.subscribers[topic]
	chans := make([]chan struct{}, 0, len(subs))
	for ch := range subs {
		chans = append(chans, ch)
	}
	h.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel that receives a tick on every Publish(topic)
// call, plus a cleanup func the caller MUST invoke when done. Calling
// the cleanup func more than once is a no-op.
func (h *Hub) Subscribe(topic string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 8)
	h.mu.Lock()
	if _, ok := h.subscribers[topic]; !ok {
		h.subscribers[topic] = map[chan struct{}]struct{}{}
	}
	h.subscribers[topic][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		set, ok := h.subscribers[topic]
		if !ok {
			return
		}
		delete(set, ch)
		if len(set) == 0 {
			delete(h.subscribers, topic)
		}
	}
}
