package events

import (
	"sync"
	"testing"
	"time"
)

func TestCapRuneBoundary(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hell…"},
		{"héllo", 2, "h…"}, // 0xC3 0xA9 is one rune; byte 2 starts 'l'? "héllo": h(1) é(2) l l o; n=2 cuts after h
		{"ab日本", 4, "ab…"}, // n=4 splits 日 (bytes 2..4), so the rune is dropped whole
		{"", 3, ""},
		{"x", 0, "x"},
	}
	for _, c := range cases {
		if got := Cap(c.in, c.n); got != c.want {
			t.Errorf("Cap(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestRelaySubscribeAndCancel(t *testing.T) {
	r := NewRelay()
	ch, cancel := r.Subscribe(4)

	r.Emit(Event{Kind: TextDelta, Delta: "a"})
	select {
	case e := <-ch:
		if e.Delta != "a" {
			t.Fatalf("got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	cancel()
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after cancel")
	}
	// Emitting after cancel must not panic or block.
	done := make(chan struct{})
	go func() {
		r.Emit(Event{Kind: TextDelta, Delta: "b"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked with no subscribers")
	}
	cancel() // idempotent
}

func TestRelayNilSafe(t *testing.T) {
	var r *Relay
	r.Emit(Event{Kind: RunStarted})
	r.Close()
	ch, cancel := r.Subscribe(2)
	if _, ok := <-ch; ok {
		t.Fatal("nil relay channel should be closed")
	}
	cancel()
}

func TestRelayMultipleSubscribersAndClose(t *testing.T) {
	r := NewRelay()
	a, cancelA := r.Subscribe(2)
	defer cancelA()
	b, _ := r.Subscribe(2)

	r.AddSink(nil) // must be a no-op

	rec := &Recorder{}
	r.AddSink(rec)

	var wg sync.WaitGroup
	wg.Add(2)
	recv := func(ch <-chan Event, want string) {
		defer wg.Done()
		e := <-ch
		if e.Delta != want {
			t.Errorf("got %+v", e)
		}
	}
	go recv(a, "x")
	go recv(b, "x")
	r.Emit(Event{Kind: TextDelta, Delta: "x"})
	wg.Wait()

	if kinds := rec.Kinds(); len(kinds) != 1 || kinds[0] != TextDelta {
		t.Fatalf("sink kinds = %v", kinds)
	}

	r.Close()
	if _, ok := <-a; ok {
		t.Fatal("a should be closed")
	}
	if _, ok := <-b; ok {
		t.Fatal("b should be closed")
	}
	r.Emit(Event{Kind: TextDelta, Delta: "y"}) // after Close: sinks still fire, subs gone
}

func TestRelaySlowConsumerDoesNotBlock(t *testing.T) {
	r := NewRelay()
	_, cancel := r.Subscribe(1)
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			r.Emit(Event{Kind: TextDelta, Delta: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on a full subscriber")
	}
}

func TestRelayEmitCloseRace(t *testing.T) {
	for n := 0; n < 40; n++ {
		r := NewRelay()
		var chans []<-chan Event
		var cancels []func()
		for i := 0; i < 4; i++ {
			ch, cancel := r.Subscribe(1)
			chans = append(chans, ch)
			cancels = append(cancels, cancel)
		}
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			for i := 0; i < 80; i++ {
				r.Emit(Event{Kind: TextDelta, Delta: "x"})
			}
		}()
		go func() {
			defer wg.Done()
			for _, cancel := range cancels {
				cancel()
			}
		}()
		go func() {
			defer wg.Done()
			r.Close()
		}()
		wg.Wait()
		for _, ch := range chans {
			for range ch {
			}
		}
	}
}

func TestRecorder(t *testing.T) {
	r := &Recorder{}
	r.Emit(Event{Kind: RunStarted})
	r.Emit(Event{Kind: RunFinished})
	if got := r.Kinds(); len(got) != 2 || got[0] != RunStarted || got[1] != RunFinished {
		t.Fatalf("kinds = %v", got)
	}
	snap := r.Snapshot()
	snap[0].Kind = TextDelta
	if r.Snapshot()[0].Kind != RunStarted {
		t.Fatal("Snapshot must return copies")
	}
}
