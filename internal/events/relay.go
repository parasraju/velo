package events

import "sync"

// Relay fans events out to any number of subscribers. A nil *Relay is usable
// as an Emitter (Emit is a no-op), which keeps producers free of nil checks
// on the happy path.
//
// Publish semantics: each subscriber owns a buffered channel. Emit blocks per
// subscriber until the event is accepted or the subscriber is closed, so a
// slow or gone consumer can never block the run forever and a live consumer
// never silently loses events.
type Relay struct {
	mu     sync.Mutex
	subs   map[*relaySub]struct{}
	closed bool
	sinkMu sync.Mutex
	sinks  []Emitter
}

type relaySub struct {
	ch   chan Event
	done chan struct{}
	once sync.Once
}

func NewRelay() *Relay {
	return &Relay{subs: make(map[*relaySub]struct{})}
}

// Subscribe registers a consumer. The returned channel is closed when the
// subscription is cancelled via the returned function or Close is called on
// the relay. cancel is idempotent.
func (r *Relay) Subscribe(buf int) (<-chan Event, func()) {
	if r == nil {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	if buf <= 0 {
		buf = 256
	}
	sub := &relaySub{
		ch:   make(chan Event, buf),
		done: make(chan struct{}),
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		close(sub.ch)
		return sub.ch, func() {}
	}
	r.subs[sub] = struct{}{}
	r.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.subs, sub)
			r.mu.Unlock()
			sub.once.Do(func() {
				close(sub.done)
				close(sub.ch)
			})
		})
	}
	return sub.ch, cancel
}

// AddSink registers an external Emitter that receives every event for the
// lifetime of the relay (used for Options.Emitter embedder hooks). Sinks are
// called synchronously from Emit; they must not block indefinitely.
func (r *Relay) AddSink(e Emitter) {
	if r == nil || e == nil {
		return
	}
	r.sinkMu.Lock()
	r.sinks = append(r.sinks, e)
	r.sinkMu.Unlock()
}

func (r *Relay) Emit(e Event) {
	if r == nil {
		return
	}
	r.sinkMu.Lock()
	sinks := append([]Emitter(nil), r.sinks...)
	r.sinkMu.Unlock()
	for _, s := range sinks {
		s.Emit(e)
	}

	r.mu.Lock()
	subs := make([]*relaySub, 0, len(r.subs))
	for sub := range r.subs {
		subs = append(subs, sub)
	}
	r.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub.ch <- e:
		case <-sub.done:
		}
	}
}

// Close releases every subscriber.
func (r *Relay) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	subs := make([]*relaySub, 0, len(r.subs))
	for sub := range r.subs {
		subs = append(subs, sub)
	}
	r.subs = make(map[*relaySub]struct{})
	r.closed = true
	r.mu.Unlock()
	for _, sub := range subs {
		sub.once.Do(func() {
			close(sub.done)
			close(sub.ch)
		})
	}
}
