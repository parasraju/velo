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

	case providersMsg:
		if msg.err != nil {
			m.notice = "providers: " + msg.err.Error()
			return m, nil
		}
		m.providers = msg.rows
		if m.providerIdx >= len(m.providers) {
			m.providerIdx = 0
		}
		return m, nil

	case modelsMsg:
		if msg.err != nil {
			m.notice = "models: " + msg.err.Error()
			m.showModels = false
			return m, nil
		}
		m.models = msg.rows
		if len(m.models) == 0 {
			m.showModels = false
			m.notice = "select a provider before choosing a model"
			return m, nil
		}
		if m.modelIdx >= len(m.models) {
			m.modelIdx = 0
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
		if m.showCommands {
			if idx, ok := clickRow(msg, 3, len(commandPalette)); ok {
				m.commandIdx = idx
				return m.runPalette(commandPalette[idx].id)
			}
		}
		if m.showProviders {
			if idx, ok := clickRow(msg, 3, len(m.providers)); ok {
				m.providerIdx = idx
				return m.chooseProvider()
			}
		}
		if m.showModels {
			if idx, ok := clickRow(msg, 3, len(m.models)); ok {
				m.modelIdx = idx
				return m.chooseModel()
			}
		}
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
		if e.Run != nil && e.Run.Truncated {
			m.appendSystem("output truncated at the length limit")
		}
		if e.Run != nil && e.Run.Status != "completed" && e.Run.Status != "cancelled" {
			m.appendSystem("status: " + e.Run.Status)
		}
	}
	m.relayout()
	return m
}

type paletteItem struct {
	id, label, help string
}

var commandPalette = []paletteItem{
	{"providers", "providers", "choose a provider"},
	{"models", "models", "choose a model for the selected provider"},
	{"sessions", "sessions", "browse and resume sessions"},
	{"new", "new", "start a new session"},
	{"status", "status", "show runtime status"},
	{"config", "config", "show configuration"},
	{"tools", "tools", "list tools"},
	{"connect", "connect", "enter an API key"},
	{"help", "help", "show help"},
	{"quit", "quit", "exit velo"},
}

func clickRow(msg tea.MouseMsg, first, n int) (int, bool) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft || n <= 0 {
		return 0, false
	}
	idx := msg.Y - first
	if idx < 0 || idx >= n {
		return 0, false
	}
	return idx, true
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pending != nil {
		return m.handleApprovalKey(msg)
	}
	if m.showCommands {
		return m.handleCommandKey(msg)
	}
	if m.showProviders {
		return m.handleProviderKey(msg)
	}
	if m.showModels {
		return m.handleModelKey(msg)
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
		if m.awaitKey {
			m.awaitKey = false
			m.input.Reset()
			m.notice = "connect cancelled"
			return m, nil
		}
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
			m.notice = "a run is in progress; the draft stays in the input"
			return m, nil
		}
		return m.submit()
	}
	if msg.String() == "ctrl+j" {
		m.input.InsertRune('\n')
		m.relayout()
		return m, nil
	}
	if msg.String() == "?" && m.input.Value() == "" && !m.running {
		m.showHelp = true
		return m, nil
	}
	if msg.String() == "/" && strings.TrimSpace(m.input.Value()) == "" && !m.running && !m.awaitKey {
		return m.openCommands()
	}

	// Keystrokes during a run stay in the draft instead of being discarded.
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
	default:
		m.notice = "press y to approve, n to deny, or t to trust"
		return m, nil
	}
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
	if m.awaitKey {
		m.awaitKey = false
		if strings.HasPrefix(text, "/") {
			m.notice = "connect cancelled"
			return m, nil
		}
		info, msg, err := m.ctrl.ConnectKey(text)
		if err != nil {
			m.notice = "connect: " + err.Error()
			return m, nil
		}
		m.info = info
		m.notice = msg
		m.appendSystem(msg)
		return m, nil
	}
	if text == "/" {
		return m.openCommands()
	}
	if strings.HasPrefix(text, "/") {
		return m.command(text)
	}
	return m.startRun(text)
}

func (m Model) openCommands() (tea.Model, tea.Cmd) {
	m.showCommands = true
	m.showProviders = false
	m.showModels = false
	m.showPicker = false
	m.showHelp = false
	m.commandIdx = 0
	return m, nil
}

func (m Model) handleCommandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.showCommands = false
		return m, nil
	case "up", "k":
		if m.commandIdx > 0 {
			m.commandIdx--
		}
		return m, nil
	case "down", "j":
		if m.commandIdx < len(commandPalette)-1 {
			m.commandIdx++
		}
		return m, nil
	case "enter":
		if m.commandIdx >= 0 && m.commandIdx < len(commandPalette) {
			return m.runPalette(commandPalette[m.commandIdx].id)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) runPalette(id string) (tea.Model, tea.Cmd) {
	m.showCommands = false
	switch id {
	case "quit":
		m.ctrl.Cancel()
		return m, tea.Quit
	case "sessions":
		return m.openPicker()
	case "new":
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
	case "help":
		m.showHelp = true
		return m, nil
	case "providers":
		return m.openProviders()
	case "models":
		return m.openModels()
	case "connect":
		m.awaitKey = true
		m.notice = "enter the API key and press enter. It stays in this process and is not saved. Esc cancels."
		return m, nil
	case "status", "config", "tools":
		text, err := m.ctrl.Report(id)
		if err != nil {
			m.notice = id + ": " + err.Error()
			return m, nil
		}
		m.appendSystem(strings.TrimRight(text, "\n"))
		return m, nil
	default:
		m.notice = "unknown command: /" + id
		return m, nil
	}
}

func (m Model) command(line string) (tea.Model, tea.Cmd) {
	switch line {
	case "/quit", "/exit", "/q":
		return m.runPalette("quit")
	case "/sessions", "/s":
		return m.runPalette("sessions")
	case "/new":
		return m.runPalette("new")
	case "/help", "/h":
		return m.runPalette("help")
	case "/providers", "/provider":
		return m.runPalette("providers")
	case "/models", "/model":
		return m.runPalette("models")
	case "/connect":
		return m.runPalette("connect")
	case "/status", "/config", "/tools":
		return m.runPalette(strings.TrimPrefix(line, "/"))
	default:
		if strings.HasPrefix(line, "/provider ") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "/provider "))
			fields := strings.Fields(name)
			if len(fields) == 0 {
				return m.openProviders()
			}
			return m.afterProvider(fields[0], true)
		}
		if strings.HasPrefix(line, "/model ") {
			model := strings.TrimSpace(strings.TrimPrefix(line, "/model "))
			info, msg, err := m.ctrl.UseModel(model)
			if err != nil {
				m.notice = err.Error()
				return m, nil
			}
			m.info = info
			m.notice = msg
			m.appendSystem(msg)
			return m, nil
		}
		m.notice = "unknown command: " + line
		return m, nil
	}
}

func (m Model) openProviders() (tea.Model, tea.Cmd) {
	m.showProviders = true
	m.showPicker = false
	m.providerIdx = 0
	ctrl := m.ctrl
	return m, func() tea.Msg {
		rows, err := ctrl.ListProviders()
		return providersMsg{rows: rows, err: err}
	}
}

func (m Model) handleProviderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.showProviders = false
		return m, nil
	case "up", "k":
		if m.providerIdx > 0 {
			m.providerIdx--
		}
		return m, nil
	case "down", "j":
		if m.providerIdx < len(m.providers)-1 {
			m.providerIdx++
		}
		return m, nil
	case "enter":
		return m.chooseProvider()
	}
	return m, nil
}

func (m Model) chooseProvider() (tea.Model, tea.Cmd) {
	if m.providerIdx < 0 || m.providerIdx >= len(m.providers) {
		return m, nil
	}
	row := m.providers[m.providerIdx]
	m.showProviders = false
	return m.afterProvider(row.Name, row.NeedsKey)
}

func (m Model) afterProvider(name string, needsKey bool) (tea.Model, tea.Cmd) {
	info, msg, err := m.ctrl.UseProvider(name)
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}
	m.info = info
	if needsKey && !info.Ready {
		m.awaitKey = true
		m.notice = "API key for " + name + ". Enter it and press enter. It is not saved. Esc cancels."
		return m, nil
	}
	m.notice = msg
	m.appendSystem(msg)
	return m, nil
}

func (m Model) openModels() (tea.Model, tea.Cmd) {
	m.showModels = true
	m.showCommands = false
	m.showProviders = false
	m.modelIdx = 0
	ctrl := m.ctrl
	return m, func() tea.Msg {
		rows, err := ctrl.ListModels()
		return modelsMsg{rows: rows, err: err}
	}
}

func (m Model) handleModelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.showModels = false
		return m, nil
	case "up", "k":
		if m.modelIdx > 0 {
			m.modelIdx--
		}
		return m, nil
	case "down", "j":
		if m.modelIdx < len(m.models)-1 {
			m.modelIdx++
		}
		return m, nil
	case "enter":
		return m.chooseModel()
	}
	return m, nil
}

func (m Model) chooseModel() (tea.Model, tea.Cmd) {
	if m.modelIdx < 0 || m.modelIdx >= len(m.models) {
		return m, nil
	}
	name := m.models[m.modelIdx].Name
	m.showModels = false
	info, msg, err := m.ctrl.UseModel(name)
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}
	m.info = info
	if !info.Ready && info.Provider != "" && info.Provider != "mock" {
		m.awaitKey = true
		m.notice = "API key for " + info.Provider + ". Enter it and press enter. It is not saved. Esc cancels."
		return m, nil
	}
	m.notice = msg
	m.appendSystem(msg)
	return m, nil
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
	m.pushBlock(newUserBlock(objective))
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
	m.pushBlock(newSystemBlock(text))
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
	m.pushBlock(block{kind: blockAssistant, text: text, streaming: true})
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
	m.pushBlock(newToolBlock(&cp))
	m.relayout()
	if finished {
		m.scrollToBottom(false)
	}
}

const maxTranscriptBlocks = 400

// pushBlock appends a transcript block and drops the oldest entries once the
// scrollback bound is reached.
func (m *Model) pushBlock(b block) {
	m.blocks = append(m.blocks, b)
	if extra := len(m.blocks) - maxTranscriptBlocks; extra > 0 {
		m.blocks = append([]block(nil), m.blocks[extra:]...)
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
