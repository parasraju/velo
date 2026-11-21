package events

import "sync"

// Relay fans events out to any number of subscribers. A nil *Relay is usable
// as an Emitter (Emit is a no-op), which keeps producers free of nil checks
// on the happy path.
//
// Publish semantics: each subscriber owns a buffered channel. Emit never
// blocks. When a subscriber's buffer is full the new event is dropped
// (drop-newest) so a slow consumer cannot stall the agent loop. A subscriber
// that keeps up does not lose events. Send and close share a per-subscriber
// mutex, so Emit cannot send on a closed channel.
type Relay struct {
	mu     sync.Mutex
	subs   map[*relaySub]struct{}
	closed bool
	sinkMu sync.Mutex
	sinks  []Emitter
}

type relaySub struct {
	mu     sync.Mutex
	ch     chan Event
	closed bool
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
		ch: make(chan Event, buf),
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
			sub.close()
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
		sub.send(e)
	}
}

// send delivers e unless the subscriber is closed or its buffer is full.
// A full buffer drops e (drop-newest) and returns immediately.
func (s *relaySub) send(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- e:
	default:
	}
}

func (s *relaySub) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
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
		sub.close()
	}
}
