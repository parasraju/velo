package ui

import (
	"context"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"velo/internal/events"
	"velo/internal/protocol"
)

// Options configures a TUI session.
type Options struct {
	Controller Controller
	Theme      string // dark | light | notty
	// BaseCtx is the parent context for runs. Cancellation of it aborts the
	// active run and closes the program.
	BaseCtx context.Context
}

type runDoneMsg struct{ err error }
type sessionsMsg struct {
	rows []SessionRow
	err  error
}

// Model is the Bubble Tea model for the VELO chat interface.
type Model struct {
	ctrl  Controller
	theme Theme
	info  Info

	width  int
	height int
	ready  bool

	blocks []block
	vp     viewport.Model
	input  textarea.Model
	spin   spinner.Model

	running bool
	turn    int
	usage   *protocol.Usage

	pending    *events.Tool // approval awaiting a decision
	picker     []SessionRow
	pickerIdx  int
	showPicker bool
	showHelp   bool
	notice     string // transient status/error line under the input

	runCancel context.CancelFunc
	baseCtx   context.Context
	program   *programRef
	sub       <-chan events.Event
}

// programRef lets run goroutines deliver completion messages to the live
// program without copying the *tea.Program through value receivers.
type programRef struct {
	p *tea.Program
}

func (r *programRef) send(msg tea.Msg) {
	if r != nil && r.p != nil {
		r.p.Send(msg)
	}
}

func newModel(opts Options) Model {
	th := NewTheme(opts.Theme)

	ta := textarea.New()
	ta.Prompt = "› "
	ta.Placeholder = "Describe a task, or : for commands  (:help)"
	st := textarea.Style{Prompt: th.Prompt, Placeholder: th.Dim, Text: th.Base}
	ta.FocusedStyle = st
	ta.BlurredStyle = st
	ta.ShowLineNumbers = false
	ta.MaxHeight = 6
	ta.SetWidth(60)
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	sp.Style = th.Accent

	vp := viewport.New(80, 24)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3

	return Model{
		ctrl:    opts.Controller,
		theme:   th,
		info:    opts.Controller.Info(),
		vp:      vp,
		input:   ta,
		spin:    sp,
		baseCtx: opts.BaseCtx,
		sub:     opts.Controller.Events(),
		program: &programRef{},
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.poll())
}
