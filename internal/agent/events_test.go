package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"velo/internal/events"
	"velo/internal/protocol"
	"velo/internal/provider/mock"
)

func firstIndex(kinds []events.Kind, want events.Kind) int {
	for i, k := range kinds {
		if k == want {
			return i
		}
	}
	return -1
}

func TestEventStreamPlainCompletion(t *testing.T) {
	rec := &events.Recorder{}
	p := mock.New("mock", "mock-model", mock.Step{
		Text:  "hello world",
		Usage: &protocol.Usage{InputTokens: 4, OutputTokens: 6},
	})
	a, sess := newTestAgent(t, p, newRegistry(t), nil, func(o *Options) { o.Sink = rec })

	if _, err := a.Run(context.Background(), "say hello world"); err != nil {
		t.Fatalf("run: %v", err)
	}

	kinds := rec.Kinds()
	start, finish := firstIndex(kinds, events.RunStarted), firstIndex(kinds, events.RunFinished)
	if start == -1 || finish == -1 || start > finish {
		t.Fatalf("run bounds wrong: %v", kinds)
	}
	for _, want := range []events.Kind{events.TurnStarted, events.TextDelta, events.UsageUpdated, events.StateChanged} {
		if firstIndex(kinds, want) == -1 {
			t.Errorf("missing event %q in %v", want, kinds)
		}
	}
	// Ordering: RunStarted before TurnStarted before RunFinished.
	if !(firstIndex(kinds, events.RunStarted) < firstIndex(kinds, events.TurnStarted)) {
		t.Errorf("RunStarted should precede TurnStarted: %v", kinds)
	}

	var delta, done *events.Event
	snap := rec.Snapshot()
	for i := range snap {
		switch snap[i].Kind {
		case events.TextDelta:
			if delta == nil {
				delta = &snap[i]
			}
		case events.RunFinished:
			done = &snap[i]
		}
	}
	if delta == nil || delta.Delta != "hello world" {
		t.Errorf("text delta = %+v", delta)
	}
	if delta.SessionID != sess.ID() || delta.Turn != 1 {
		t.Errorf("delta stamps wrong: session=%q turn=%d", delta.SessionID, delta.Turn)
	}
	if delta.Time.IsZero() {
		t.Error("delta time not stamped")
	}
	if done.Run == nil || done.Run.Status != string(StateCompleted) {
		t.Errorf("run summary = %+v", done)
	}
}

func TestEventStreamToolCall(t *testing.T) {
	rec := &events.Recorder{}
	p := mock.New("mock", "mock-model",
		mock.Step{Text: "working", ToolCalls: []protocol.ToolCall{toolCall("call_1", "echo", `{"text":"hi"}`)}},
		mock.Step{Text: "done"},
	)
	a, _ := newTestAgent(t, p, newRegistry(t, echoTool()), nil, func(o *Options) { o.Sink = rec })

	if _, err := a.Run(context.Background(), "use the tool"); err != nil {
		t.Fatalf("run: %v", err)
	}

	var started, finished *events.Tool
	snap := rec.Snapshot()
	for i := range snap {
		switch snap[i].Kind {
		case events.ToolStarted:
			started = snap[i].Tool
		case events.ToolFinished:
			finished = snap[i].Tool
		}
	}
	if started == nil || started.Name != "echo" || started.Status != "running" {
		t.Fatalf("tool started = %+v", started)
	}
	if !strings.Contains(started.Arguments, "hi") {
		t.Errorf("tool started arguments = %q", started.Arguments)
	}
	if finished == nil || finished.Status != "succeeded" || finished.ID != "call_1" {
		t.Fatalf("tool finished = %+v", finished)
	}
	if finished.Output != "echo:hi" {
		t.Errorf("tool output = %q", finished.Output)
	}
	if firstIndex(rec.Kinds(), events.ToolStarted) > firstIndex(rec.Kinds(), events.ToolFinished) {
		t.Error("ToolStarted must precede ToolFinished")
	}
}

func TestEventStreamFailure(t *testing.T) {
	rec := &events.Recorder{}
	p := mock.New("mock", "mock-model", mock.Step{Err: errBoom})
	a, _ := newTestAgent(t, p, newRegistry(t), nil, func(o *Options) { o.Sink = rec })

	if _, err := a.Run(context.Background(), "boom"); err == nil {
		t.Fatal("expected error")
	}
	kinds := rec.Kinds()
	if firstIndex(kinds, events.Error) == -1 {
		t.Errorf("expected Error event, got %v", kinds)
	}
	finish := firstIndex(kinds, events.RunFinished)
	if finish == -1 {
		t.Fatalf("expected RunFinished event, got %v", kinds)
	}
	snap := rec.Snapshot()
	if snap[finish].Run == nil || snap[finish].Run.Status != string(StateFailed) {
		t.Errorf("finish run = %+v", snap[finish].Run)
	}
	if firstIndex(kinds, events.RunStarted) > finish {
		t.Error("RunStarted must precede RunFinished")
	}
}

func TestEventStreamStreamingDeltas(t *testing.T) {
	rec := &events.Recorder{}
	p := mock.New("mock", "mock-model", mock.Step{
		Text:      "streaming output here",
		ChunkSize: 4,
		Usage:     &protocol.Usage{InputTokens: 2, OutputTokens: 3},
	})
	a, _ := newTestAgent(t, p, newRegistry(t), nil, func(o *Options) {
		o.Sink = rec
		o.Stream = true
	})

	if _, err := a.Run(context.Background(), "stream"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var joined strings.Builder
	deltas := 0
	for _, e := range rec.Snapshot() {
		if e.Kind == events.TextDelta {
			deltas++
			joined.WriteString(e.Delta)
		}
	}
	if deltas < 2 {
		t.Errorf("expected multiple streamed deltas, got %d", deltas)
	}
	if joined.String() != "streaming output here" {
		t.Errorf("streamed text = %q", joined.String())
	}
}

var errBoom = errors.New("boom")
