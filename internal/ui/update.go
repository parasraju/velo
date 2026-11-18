package ui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"velo/internal/events"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.input.SetWidth(m.width - 4)
		m.relayout()
		m.vp.GotoBottom()
		return m, nil

	case eventBatchMsg:
		for _, e := range msg.events {
			m = m.handleEvent(e)
		}
		return m, m.poll()

	case spinner.TickMsg:
		if m.running {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
		return m, nil

	case sessionsMsg:
		if msg.err != nil {
			m.notice = "sessions: " + msg.err.Error()
			return m, nil
		}
		m.picker = msg.rows
		if m.pickerIdx >= len(m.picker) {
			m.pickerIdx = 0
		}
		return m, nil

	case runDoneMsg:
		// The terminal RunFinished event drives rendering; this is a safety net
		// for runs that fail before emitting it.
		if msg.err != nil && m.running {
			m.appendSystem("run error: " + msg.err.Error())
		}
		m.finishRun()
		return m, nil

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Unhandled messages (cursor blink, paste, …) belong to the input widget.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) handleEvent(e events.Event) Model {
	m.notice = ""
	switch e.Kind {
	case events.RunStarted:
		// Reset per-run counters; transcript persists within a session.
		m.turn = 0
	case events.TurnStarted:
		m.finalizeAssistant()
		m.turn = e.Turn
	case events.TextDelta:
		m.appendDelta(e.Delta)
	case events.ToolStarted:
		m.finalizeAssistant()
		if e.Tool != nil {
			m.upsertTool(e.Tool, false)
		}
	case events.ToolFinished:
		if e.Tool != nil {
			m.upsertTool(e.Tool, true)
		}
	case events.ApprovalRequested:
		if e.Tool != nil {
			cp := *e.Tool
			m.pending = &cp
		}
	case events.ApprovalResolved:
		m.pending = nil
		m.relayout()
	case events.UsageUpdated:
		if e.Usage != nil {
			u := *e.Usage
			m.usage = &u
		}
	case events.ContextCompacted:
		m.appendSystem("context compacted to fit the input budget")
	case events.StateChanged:
		// State transitions inform the status bar only; nothing to append.
	case events.Error:
		m.appendSystem("error: " + e.Message)
	case events.RunFinished:
		m.finishRun()
		if e.Run != nil && e.Run.Status != "completed" && e.Run.Status != "cancelled" {
			m.appendSystem("status: " + e.Run.Status)
		}
	}
	m.relayout()
	return m
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pending != nil {
		return m.handleApprovalKey(msg)
	}
	if m.showPicker {
		return m.handlePickerKey(msg)
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		m.ctrl.Cancel()
		return m, tea.Quit
	case "ctrl+o":
		m.toggleExpand()
		return m, nil
	case "esc":
		if m.running {
			m.ctrl.Cancel()
			m.appendSystem("cancelling run…")
			return m, nil
		}
	}

	if m.input.Value() == "" {
		if next, cmd, ok := m.scrollKey(msg); ok {
			return next, cmd
		}
	}

	if msg.String() == "enter" {
		if m.running {
			return m, nil
		}
		return m.submit()
	}
	if msg.String() == "ctrl+j" && !m.running {
		m.input.InsertRune('\n')
		m.relayout()
		return m, nil
	}
	if msg.String() == "?" && m.input.Value() == "" {
		m.showHelp = true
		return m, nil
	}

	if m.running {
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.relayout()
	return m, cmd
}

func (m Model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	id := ""
	if m.pending != nil {
		id = m.pending.ID
	}
	switch msg.String() {
	case "y", "Y":
		m.ctrl.Decide(Decision{ID: id, Approve: true})
		m.pending = nil
		return m, nil
	case "t", "T":
		m.ctrl.Decide(Decision{ID: id, Approve: true, Trust: true})
		m.pending = nil
		return m, nil
	case "n", "N", "esc", "ctrl+c":
		m.ctrl.Decide(Decision{ID: id, Approve: false})
		m.pending = nil
		return m, nil
	}
	return m, nil
}

func (m Model) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.showPicker = false
		return m, nil
	case "up", "k":
		if m.pickerIdx > 0 {
			m.pickerIdx--
		}
		return m, nil
	case "down", "j":
		if m.pickerIdx < len(m.picker)-1 {
			m.pickerIdx++
		}
		return m, nil
	case "enter":
		if m.pickerIdx >= 0 && m.pickerIdx < len(m.picker) {
			id := m.picker[m.pickerIdx].ID
			m.showPicker = false
			info, err := m.ctrl.Resume(id)
			if err != nil {
				m.notice = "resume: " + err.Error()
				return m, nil
			}
			m.info = info
			m.reloadTranscript()
		}
		return m, nil
	}
	return m, nil
}

func (m Model) scrollKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "pgup", "ctrl+u":
		m.vp.PageUp()
		return m, nil, true
	case "pgdown", "ctrl+d":
		m.vp.PageDown()
		return m, nil, true
	case "ctrl+b":
		m.vp.HalfViewUp()
		return m, nil, true
	case "ctrl+f":
		m.vp.HalfViewDown()
		return m, nil, true
	case "home":
		m.vp.GotoTop()
		return m, nil, true
	case "end":
		m.vp.GotoBottom()
		return m, nil, true
	}
	return m, nil, false
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	m.input.Reset()
	if strings.HasPrefix(text, ":") {
		return m.command(text)
	}
	return m.startRun(text)
}

func (m Model) command(line string) (tea.Model, tea.Cmd) {
	switch line {
	case ":quit", ":exit", ":q":
		m.ctrl.Cancel()
		return m, tea.Quit
	case ":sessions", ":s":
		return m.openPicker()
	case ":new":
		if m.running {
			m.notice = "cannot start a new session during a run"
			return m, nil
		}
		info, err := m.ctrl.NewSession()
		if err != nil {
			m.notice = "new session: " + err.Error()
			return m, nil
		}
		m.info = info
		m.blocks = nil
		m.usage = nil
		m.turn = 0
		m.relayout()
		m.appendSystem("started new session " + shortID(info.SessionID))
		return m, nil
	case ":help", ":h":
		m.showHelp = true
		return m, nil
	default:
		m.notice = "unknown command: " + line
		return m, nil
	}
}

func (m Model) openPicker() (tea.Model, tea.Cmd) {
	m.showPicker = true
	m.pickerIdx = 0
	ctrl := m.ctrl
	return m, func() tea.Msg {
		rows, err := ctrl.ListSessions()
		return sessionsMsg{rows: rows, err: err}
	}
}

func (m Model) startRun(objective string) (tea.Model, tea.Cmd) {
	m.notice = ""
	m.finalizeAssistant()
	m.blocks = append(m.blocks, newUserBlock(objective))
	m.running = true
	m.turn = 0
	m.relayout()
	m.scrollToBottom(true)

	ctx, cancel := context.WithCancel(m.ctx())
	m.runCancel = cancel
	ctrl, prog := m.ctrl, m.program
	go func() {
		err := ctrl.Run(ctx, objective)
		prog.send(runDoneMsg{err: err})
	}()
	return m, m.spin.Tick
}

// ctx returns a non-nil base context so runs survive even when the caller did
// not supply one.
func (m Model) ctx() context.Context {
	if m.baseCtx != nil {
		return m.baseCtx
	}
	return context.Background()
}

func (m *Model) finishRun() {
	if !m.running {
		return
	}
	m.running = false
	if m.runCancel != nil {
		m.runCancel()
		m.runCancel = nil
	}
	m.finalizeAssistant()
	m.relayout()
}

func (m *Model) appendSystem(text string) {
	m.finalizeAssistant()
	m.blocks = append(m.blocks, newSystemBlock(text))
	m.relayout()
	m.scrollToBottom(false)
}

func (m *Model) toggleExpand() {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool {
			m.blocks[i].expanded = !m.blocks[i].expanded
			m.relayout()
			return
		}
	}
}

// finalizeAssistant renders the trailing streaming assistant block into cached
// markdown so the scrollback stays stable while later events arrive.
func (m *Model) finalizeAssistant() {
	if n := len(m.blocks); n > 0 {
		b := &m.blocks[n-1]
		if b.kind == blockAssistant && b.streaming {
			b.streaming = false
			b.rendered = renderMarkdown(m.theme.Name, b.text, m.contentWidth())
		}
	}
}

func (m *Model) appendDelta(text string) {
	if text == "" {
		return
	}
	if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockAssistant && m.blocks[n-1].streaming {
		m.blocks[n-1].text += text
		return
	}
	m.blocks = append(m.blocks, block{kind: blockAssistant, text: text, streaming: true})
}

func (m *Model) upsertTool(t *events.Tool, finished bool) {
	cp := *t
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool && m.blocks[i].tool != nil && m.blocks[i].tool.ID == t.ID {
			m.blocks[i].tool = &cp
			m.relayout()
			return
		}
	}
	m.blocks = append(m.blocks, newToolBlock(&cp))
	m.relayout()
	if finished {
		m.scrollToBottom(false)
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
