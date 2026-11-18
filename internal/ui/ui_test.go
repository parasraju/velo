package ui

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"velo/internal/events"
	"velo/internal/protocol"
)

type fakeController struct {
	mu         sync.Mutex
	info       Info
	eventsCh   chan events.Event
	runs       []string
	decisions  []Decision
	cancelled  int
	shutdownN  int
	resumed    []string
	newSess    int
	sessionRow []SessionRow
	transcript []protocol.Message
	runDelay   time.Duration
}

func newFakeController() *fakeController {
	return &fakeController{
		info: Info{
			Version:   "test",
			Workspace: `/home/u/proj`,
			Model:     "mock-model",
			SessionID: "sess-0001",
		},
		eventsCh: make(chan events.Event, 64),
	}
}

func (f *fakeController) Info() Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}

func (f *fakeController) Events() <-chan events.Event { return f.eventsCh }

func (f *fakeController) Run(ctx context.Context, objective string) error {
	f.mu.Lock()
	f.runs = append(f.runs, objective)
	delay := f.runDelay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakeController) Decide(d Decision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decisions = append(f.decisions, d)
}

func (f *fakeController) ListSessions() ([]SessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionRow, nil
}

func (f *fakeController) Resume(id string) (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, id)
	f.info.SessionID = id
	return f.info, nil
}

func (f *fakeController) Transcript() ([]protocol.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transcript, nil
}

func (f *fakeController) NewSession() (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newSess++
	f.info.SessionID = "sess-new"
	return f.info, nil
}

func (f *fakeController) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled++
}

func (f *fakeController) Shutdown() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdownN++
}

func (f *fakeController) snapshotRuns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.runs...)
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func updated(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return mm, cmd
}

func sendKey(t *testing.T, m Model, km tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	return updated(t, m, km)
}

func enter(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	return sendKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

func newTestModel(t *testing.T) (*fakeController, Model) {
	t.Helper()
	fake := newFakeController()
	m := newModel(Options{Controller: fake, Theme: "notty", BaseCtx: context.Background()})
	m, _ = updated(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	return fake, m
}

func batch(evs ...events.Event) tea.Msg { return eventBatchMsg{events: evs} }

func TestModelRendersStreamedAnswer(t *testing.T) {
	_, m := newTestModel(t)

	m, _ = updated(t, m, batch(
		events.Event{Kind: events.RunStarted, Objective: "hi"},
		events.Event{Kind: events.TurnStarted, Turn: 1},
		events.Event{Kind: events.TextDelta, Delta: "hello "},
		events.Event{Kind: events.TextDelta, Delta: "there"},
		events.Event{Kind: events.UsageUpdated, Usage: &protocol.Usage{InputTokens: 10, OutputTokens: 4}},
		events.Event{Kind: events.RunFinished, Run: &events.RunSummary{Status: "completed", Turns: 1}},
	))

	view := stripANSI(m.View())
	if !strings.Contains(view, "there") {
		t.Errorf("view missing streamed text:\n%s", view)
	}
	if !strings.Contains(view, "idle") {
		t.Errorf("view should show idle after run finished:\n%s", view)
	}
	if !strings.Contains(view, "10↑") {
		t.Errorf("view missing usage:\n%s", view)
	}
	if m.running {
		t.Error("model still marked running after RunFinished")
	}
}

func TestModelToolBlocksExpandCollapse(t *testing.T) {
	_, m := newTestModel(t)

	m, _ = updated(t, m, batch(
		events.Event{Kind: events.ToolStarted, Tool: &events.Tool{ID: "c1", Name: "read_file", Category: "read", Arguments: `{"path":"main.go"}`, Status: "running"}},
		events.Event{Kind: events.ToolFinished, Tool: &events.Tool{ID: "c1", Name: "read_file", Category: "read", Arguments: `{"path":"main.go"}`, Status: "succeeded", Output: "package main", DurationMS: 12}},
	))

	view := stripANSI(m.View())
	if !strings.Contains(view, "read_file") || !strings.Contains(view, "main.go") {
		t.Errorf("tool line missing name/path:\n%s", view)
	}
	if strings.Contains(view, "package main") {
		t.Error("collapsed tool block should hide output")
	}

	m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	view = stripANSI(m.View())
	if !strings.Contains(view, "package main") {
		t.Errorf("expanded tool block should show output:\n%s", view)
	}
}

func TestModelSubmitRunsObjective(t *testing.T) {
	fake, m := newTestModel(t)
	m.input.SetValue("fix the flaky test")

	m, cmd := enter(t, m)
	if cmd == nil {
		t.Fatal("submit should return a command (spinner tick)")
	}
	if !m.running {
		t.Error("model should be running after submit")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "fix the flaky test") {
		t.Errorf("user message missing:\n%s", view)
	}

	// Wait for the fake controller's Run to record the objective.
	deadline := time.After(2 * time.Second)
	for {
		if len(fake.snapshotRuns()) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("controller.Run never called")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if got := fake.snapshotRuns()[0]; got != "fix the flaky test" {
		t.Errorf("objective = %q", got)
	}

	m, _ = updated(t, m, batch(events.Event{Kind: events.RunFinished, Run: &events.RunSummary{Status: "completed"}}))
	if m.running {
		t.Error("run should be finished")
	}
}

func TestModelApprovalFlow(t *testing.T) {
	fake, m := newTestModel(t)

	m, _ = updated(t, m, batch(events.Event{Kind: events.ApprovalRequested, Tool: &events.Tool{
		ID: "c9", Name: "write_file", Category: "write",
		Arguments: `{"path":"notes.txt","content":"hello"}`, Status: "requested", Destructive: true,
	}}))

	if m.pending == nil {
		t.Fatal("approval should be pending")
	}
	view := stripANSI(m.View())
	for _, want := range []string{"approval required", "write_file", "DESTRUCTIVE", "notes.txt", "[y]", "[n]", "[t]"} {
		if !strings.Contains(view, want) {
			t.Errorf("modal missing %q:\n%s", want, view)
		}
	}

	m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if m.pending != nil {
		t.Error("pending should clear after answer")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.decisions) != 1 || !fake.decisions[0].Approve || fake.decisions[0].ID != "c9" {
		t.Fatalf("decisions = %+v", fake.decisions)
	}
}

func TestModelApprovalDenyAndTrust(t *testing.T) {
	fake, m := newTestModel(t)
	ask := events.Event{Kind: events.ApprovalRequested, Tool: &events.Tool{ID: "a", Name: "run_command", Status: "requested"}}

	m, _ = updated(t, m, batch(ask))
	m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m, _ = updated(t, m, batch(ask))
	m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.decisions) != 2 {
		t.Fatalf("decisions = %+v", fake.decisions)
	}
	if fake.decisions[0].Approve {
		t.Error("n should deny")
	}
	if !fake.decisions[1].Approve || !fake.decisions[1].Trust {
		t.Errorf("t should approve+trust: %+v", fake.decisions[1])
	}
}

func TestModelEscCancelsRun(t *testing.T) {
	fake, m := newTestModel(t)
	m.input.SetValue("long task")
	m, _ = enter(t, m)
	m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.cancelled == 0 {
		t.Error("esc should cancel the running task")
	}
}

func TestModelCommands(t *testing.T) {
	t.Run("quit", func(t *testing.T) {
		fake, m := newTestModel(t)
		m.input.SetValue(":quit")
		m, cmd := enter(t, m)
		if cmd == nil {
			t.Fatal("expected quit command")
		}
		if msg := cmd(); msg == nil {
			t.Fatal("expected a message from the quit command")
		} else if _, ok := msg.(tea.QuitMsg); !ok {
			t.Fatalf("cmd msg = %v (%T), want tea.QuitMsg", msg, msg)
		}
		if fake.cancelled == 0 {
			// :quit cancels any in-flight run first; calling with none is fine.
			_ = fake
		}
		_ = m
	})

	t.Run("help", func(t *testing.T) {
		_, m := newTestModel(t)
		m.input.SetValue(":help")
		m, _ = enter(t, m)
		if !m.showHelp {
			t.Fatal("help should open")
		}
		view := stripANSI(m.View())
		if !strings.Contains(view, "commands") || !strings.Contains(view, ":sessions") {
			t.Errorf("help view incomplete:\n%s", view)
		}
		m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
		if m.showHelp {
			t.Error("any key should close help")
		}
	})

	t.Run("unknown", func(t *testing.T) {
		_, m := newTestModel(t)
		m.input.SetValue(":frobnicate")
		m, _ = enter(t, m)
		if !strings.Contains(m.notice, "unknown command") {
			t.Errorf("notice = %q", m.notice)
		}
	})
}

func TestModelSessionPickerAndResume(t *testing.T) {
	fake, m := newTestModel(t)
	fake.mu.Lock()
	fake.sessionRow = []SessionRow{
		{ID: "sess-2", Status: "completed", Turns: 3, Messages: 8, Updated: "2026-11-18 10:00", Objective: "refactor parser"},
		{ID: "sess-1", Status: "failed", Turns: 1, Messages: 2, Updated: "2026-11-17 09:00", Objective: "fix tests"},
	}
	fake.transcript = []protocol.Message{
		{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("refactor parser")}},
		{Role: protocol.RoleAssistant, Content: []protocol.Content{
			protocol.ToolCallContent(protocol.ToolCall{ID: "t1", Name: "list_files", Arguments: []byte(`{"path":"."}`)}),
		}},
		{Role: protocol.RoleTool, ToolCallID: "t1", Name: "list_files", Content: []protocol.Content{protocol.TextContent("parser.go")}},
		{Role: protocol.RoleAssistant, Content: []protocol.Content{protocol.TextContent("done refactoring")}},
	}
	fake.mu.Unlock()

	m.input.SetValue(":sessions")
	m, cmd := enter(t, m)
	if !m.showPicker {
		t.Fatal("picker should open")
	}
	msg := cmd()
	sm, ok := msg.(sessionsMsg)
	if !ok || len(sm.rows) != 2 {
		t.Fatalf("sessions msg = %#v", msg)
	}
	m, _ = updated(t, m, sm)
	view := stripANSI(m.View())
	if !strings.Contains(view, "refactor parser") || !strings.Contains(view, "sess-2") {
		t.Errorf("picker missing rows:\n%s", view)
	}

	m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.pickerIdx != 1 {
		t.Errorf("pickerIdx = %d", m.pickerIdx)
	}
	m, _ = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m, _ = enter(t, m)
	if m.showPicker {
		t.Error("picker should close after resume")
	}
	fake.mu.Lock()
	resumed := append([]string(nil), fake.resumed...)
	fake.mu.Unlock()
	if len(resumed) != 1 || resumed[0] != "sess-2" {
		t.Fatalf("resumed = %v", resumed)
	}
	if m.info.SessionID != "sess-2" {
		t.Errorf("session id = %q", m.info.SessionID)
	}
	view = stripANSI(m.View())
	if !strings.Contains(view, "refactor parser") {
		t.Errorf("history missing user message:\n%s", view)
	}
	if !strings.Contains(view, "done refactoring") {
		t.Errorf("history missing assistant message:\n%s", view)
	}
	if !strings.Contains(view, "list_files") {
		t.Errorf("history missing tool block:\n%s", view)
	}
}

func TestModelNewSessionClears(t *testing.T) {
	_, m := newTestModel(t)
	m, _ = updated(t, m, batch(
		events.Event{Kind: events.RunStarted},
		events.Event{Kind: events.TextDelta, Delta: "old answer"},
	))
	m.input.SetValue(":new")
	m, _ = enter(t, m)
	if m.info.SessionID != "sess-new" {
		t.Errorf("session = %q", m.info.SessionID)
	}
	if len(m.blocks) != 1 || m.blocks[0].kind != blockSystem {
		t.Errorf("blocks = %+v, want only the new-session notice", m.blocks)
	}
}

func TestModelPollForwardsChannelEvents(t *testing.T) {
	fake, m := newTestModel(t)

	fake.eventsCh <- events.Event{Kind: events.TextDelta, Delta: "from channel"}
	cmd := m.poll()
	if cmd == nil {
		t.Fatal("poll command missing")
	}
	msg := cmd()
	b, ok := msg.(eventBatchMsg)
	if !ok || len(b.events) != 1 || b.events[0].Delta != "from channel" {
		t.Fatalf("batch = %#v", msg)
	}

	// Closed stream terminates the poll with no message.
	close(fake.eventsCh)
	if got := m.poll()(); got != nil {
		t.Fatalf("poll after close = %#v, want nil", got)
	}
}

func TestModelStatusBarShowsContext(t *testing.T) {
	_, m := newTestModel(t)
	view := stripANSI(m.View())
	for _, want := range []string{"mock-model", "sess-0001", "idle"} {
		if !strings.Contains(view, want) {
			t.Errorf("status/transcript missing %q:\n%s", want, view)
		}
	}
}
