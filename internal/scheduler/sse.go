// Package scheduler — SSE broker.
//
// Server-Sent Events (SSE) is a one-directional HTTP push protocol:
// the server keeps an HTTP response open and writes "data: ...\n\n"
// lines whenever something changes. The browser reconnects automatically
// if the connection drops.
//
// SSE vs WebSockets for this use case:
//   - SSE is simpler (plain HTTP, no upgrade handshake, no framing)
//   - SSE is one-directional — perfect for "server pushes status to browser"
//   - WebSockets would be needed if the browser needed to send data back
//     (e.g. sending a cancel command) — we'll add that later
//
// The broker pattern:
//   - Each run has a set of subscribers (one per open browser tab watching it)
//   - Each subscriber is a buffered channel of strings (raw SSE data lines)
//   - When a run's state changes, the server calls broker.Publish(runID, data)
//   - The broker fans the data out to all channels for that run
//   - Each SSE handler goroutine reads from its channel and writes to http.ResponseWriter
package scheduler

import "sync"

// SSEBroker manages subscriptions for Server-Sent Event streams.
// Thread-safe — multiple HTTP handler goroutines call it concurrently.
type SSEBroker struct {
	mu sync.Mutex
	// subs maps runID → set of subscriber channels.
	// Using map[chan string]struct{} as a set — same idiomatic pattern
	// as a HashSet<Channel> in C#.
	subs map[string]map[chan string]struct{}
}

// newSSEBroker creates an empty broker.
func newSSEBroker() *SSEBroker {
	return &SSEBroker{
		subs: make(map[string]map[chan string]struct{}),
	}
}

// Subscribe registers a new listener for a run's events.
// Returns a channel the caller should read from in a loop.
// The channel is buffered so a slow browser tab doesn't block Publish.
func (b *SSEBroker) Subscribe(runID string) chan string {
	// Buffer of 32 means we can queue 32 unread events before dropping.
	// In practice a browser tab reads events in microseconds, so this
	// is very conservative.
	ch := make(chan string, 32)

	b.mu.Lock()
	if b.subs[runID] == nil {
		b.subs[runID] = make(map[chan string]struct{})
	}
	b.subs[runID][ch] = struct{}{}
	b.mu.Unlock()

	return ch
}

// Unsubscribe removes a listener and closes its channel.
// Always call this (via defer) when an SSE handler returns,
// otherwise the channel leaks in the map forever.
func (b *SSEBroker) Unsubscribe(runID string, ch chan string) {
	b.mu.Lock()
	delete(b.subs[runID], ch)
	if len(b.subs[runID]) == 0 {
		delete(b.subs, runID)
	}
	b.mu.Unlock()
	close(ch)
}

// Publish sends data to all current subscribers for a run.
// Non-blocking: if a subscriber's buffer is full, the event is dropped
// for that subscriber only. They'll get the next one.
func (b *SSEBroker) Publish(runID, data string) {
	b.mu.Lock()
	// Copy the channel set so we can release the lock before sending.
	// Sending to channels while holding a mutex risks deadlock if a
	// subscriber is trying to Unsubscribe at the same time.
	channels := make([]chan string, 0, len(b.subs[runID]))
	for ch := range b.subs[runID] {
		channels = append(channels, ch)
	}
	b.mu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- data:
		default: // subscriber is slow — drop this event, they'll get the next
		}
	}
}
