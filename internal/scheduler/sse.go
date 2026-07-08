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

	ch := make(chan string, 32)

	b.mu.Lock()
	if b.subs[runID] == nil {
		b.subs[runID] = make(map[chan string]struct{})
	}
	b.subs[runID][ch] = struct{}{}
	b.mu.Unlock()

	return ch
}

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

	channels := make([]chan string, 0, len(b.subs[runID]))
	for ch := range b.subs[runID] {
		channels = append(channels, ch)
	}
	b.mu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- data:
		default:
		}
	}
}
