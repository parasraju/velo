package ui

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"velo/internal/events"
	"velo/internal/protocol"
)

// TestProgramEndToEnd drives the complete Bubble Tea loop — Init, the poll
// chain, key handling, and view rendering — through the headless test
// program, proving the UI seams work without a real terminal.
func TestProgramEndToEnd(t *testing.T) {
	fake := newFakeController()
	m := newModel(Options{Controller: fake, Theme: "notty", BaseCtx: context.Background()})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(110, 34))
	t.Cleanup(func() { _ = tm.Quit() })

	// teatest writes into one shared, mutex-guarded buffer; reading consumes
	// it, so accumulate everything polled so far into our own snapshot.
	var mu sync.Mutex
	var collected strings.Builder
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		if data, err := io.ReadAll(tm.Output()); err == nil {
			collected.Write(data)
		}
		return stripANSI(collected.String())
	}
	await := func(want ...string) {
		t.Helper()
		deadline := time.After(8 * time.Second)
		for {
			text := snapshot()
			missing := want[:0:0]
			for _, w := range want {
				if !strings.Contains(text, w) {
					missing = append(missing, w)
				}
			}
			if len(missing) == 0 {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("output never contained %v (missing %v):\n%s", want, missing, text)
			default:
				time.Sleep(20 * time.Millisecond)
			}
		}
	}

	await("Describe a task") // welcome screen rendered at least once

	tm.Type("explain the bug")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	fake.eventsCh <- events.Event{Kind: events.RunStarted, Objective: "explain the bug"}
	fake.eventsCh <- events.Event{Kind: events.TurnStarted, Turn: 1}
	fake.eventsCh <- events.Event{Kind: events.TextDelta, Delta: "the bug is real"}
	fake.eventsCh <- events.Event{Kind: events.ToolStarted, Tool: &events.Tool{
		ID: "t1", Name: "read_file", Category: "read", Arguments: `{"path":"bug.go"}`, Status: "running",
	}}
	fake.eventsCh <- events.Event{Kind: events.ToolFinished, Tool: &events.Tool{
		ID: "t1", Name: "read_file", Category: "read", Arguments: `{"path":"bug.go"}`, Status: "succeeded", DurationMS: 3,
	}}
	fake.eventsCh <- events.Event{Kind: events.UsageUpdated, Usage: &protocol.Usage{InputTokens: 5, OutputTokens: 7}}
	fake.eventsCh <- events.Event{Kind: events.RunFinished, Run: &events.RunSummary{Status: "completed", Turns: 1}}

	await("explain the bug", "the bug is real", "read_file", "idle")

	// The objective reached the backend controller.
	deadline := time.After(3 * time.Second)
	for len(fake.snapshotRuns()) == 0 {
		select {
		case <-deadline:
			t.Fatal("controller.Run was never invoked")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if got := fake.snapshotRuns()[0]; got != "explain the bug" {
		t.Errorf("objective = %q", got)
	}

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for i := 1; i < len(commandPalette); i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.WaitFinished(t, teatest.WithFinalTimeout(10*time.Second))
	await("explain the bug", "the bug is real")
}
