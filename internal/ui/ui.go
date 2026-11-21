package ui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"velo/internal/events"
)

// Run starts the full-screen TUI against the supplied controller. It blocks
// until the user quits or the base context is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.Controller == nil {
		return errors.New("ui: controller is required")
	}
	if opts.BaseCtx == nil {
		opts.BaseCtx = ctx
	}
	if opts.BaseCtx == nil {
		opts.BaseCtx = context.Background()
	}

	m := newModel(opts)

	// Alt screen keeps the caller's scrollback: the TUI draws on the alternate
	// buffer and the previous terminal contents are restored on exit.
	p := tea.NewProgram(m, tea.WithContext(opts.BaseCtx), tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.program.p = p

	err := watchContext(opts.BaseCtx, p, opts.Controller)

	opts.Controller.Cancel()
	opts.Controller.Shutdown()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// watchContext runs the program, additionally quitting when the base context
// is cancelled.
func watchContext(ctx context.Context, p *tea.Program, ctrl Controller) error {
	errCh := make(chan error, 1)
	go func() {
		_, err := p.Run()
		errCh <- err
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		ctrl.Cancel()
		p.Quit()
		return <-errCh
	}
}

// eventBatchMsg carries a drained batch of runtime events to the model. The
// poll command model avoids tea.Program.Send from foreign goroutines, which
// can deadlock once the program has stopped reading.
type eventBatchMsg struct {
	events []events.Event
}

// poll blocks until at least one event is available, then drains everything
// queued without blocking. It returns nil (no message) when the stream closes.
func (m Model) poll() tea.Cmd {
	sub := m.sub
	if sub == nil {
		return nil
	}
	return func() tea.Msg {
		first, ok := <-sub
		if !ok {
			return nil
		}
		batch := []events.Event{first}
		for {
			select {
			case e, ok := <-sub:
				if !ok {
					return eventBatchMsg{events: batch}
				}
				batch = append(batch, e)
			default:
				return eventBatchMsg{events: batch}
			}
		}
	}
}

// String aids debugging in tests.
func (msg eventBatchMsg) String() string {
	return fmt.Sprintf("eventBatch(%d)", len(msg.events))
}
