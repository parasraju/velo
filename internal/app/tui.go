package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"velo/internal/agent"
	"velo/internal/contextmgr"
	"velo/internal/events"
	"velo/internal/protocol"
	"velo/internal/session"
	"velo/internal/tools"
	"velo/internal/ui"
)

// runTUI drives the full-screen terminal interface for the interactive
// session. It replaces the plain REPL only when both stdin and stdout are
// terminals and ui.enabled is set.
func (a *App) runTUI(ctx context.Context) error {
	if err := a.checkProviderConfigured(); err != nil {
		return err
	}
	c, err := newTUIController(a)
	if err != nil {
		return err
	}
	return ui.Run(ctx, ui.Options{
		Controller: c,
		Theme:      a.cfg.UI.Theme,
		BaseCtx:    ctx,
	})
}

// useTUI reports whether the interactive launch should open the TUI.
func (a *App) useTUI() bool {
	if !a.cfg.UI.Enabled || a.plain {
		return false
	}
	return a.interactiveStdin() && isTerminalWriter(a.stdout)
}

func isTerminalWriter(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// approvalDecision is the UI's answer to one approval request.
type approvalDecision struct {
	approve bool
	trust   bool
}

// tuiController adapts runtimeEnv to the ui.Controller interface. The relay
// carries run events to the UI; approvals travel back through a channel
// Approver installed on the App before the runtime is built.
type tuiController struct {
	app   *App
	rt    *runtimeEnv
	relay *events.Relay
	sub   <-chan events.Event

	mu       sync.Mutex
	sess     *session.Session
	pending  map[string]chan approvalDecision
	runStop  context.CancelFunc
	inFlight bool
	dead     bool
}

func newTUIController(a *App) (*tuiController, error) {
	relay := events.NewRelay()
	c := &tuiController{
		app:     a,
		relay:   relay,
		pending: make(map[string]chan approvalDecision),
	}

	// Route run events and approval prompts through the UI seams. The App is
	// dedicated to this interactive session, so installing the relay and the
	// channel approver before building the runtime is safe.
	a.sink = relay
	a.opts.Approver = c.approve

	rt, err := a.newRuntime()
	if err != nil {
		a.sink = nil
		a.opts.Approver = nil
		return nil, err
	}
	c.rt = rt

	sess, err := rt.createSession()
	if err != nil {
		a.sink = nil
		a.opts.Approver = nil
		return nil, err
	}
	c.sess = sess
	c.sub, _ = relay.Subscribe(1024)
	return c, nil
}

func (c *tuiController) Info() ui.Info {
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	id := ""
	if sess != nil {
		id = sess.ID()
	}
	return ui.Info{
		Version:   c.app.version,
		Workspace: c.app.workspace,
		Model:     c.app.cfg.Provider.Model,
		SessionID: id,
	}
}

func (c *tuiController) Events() <-chan events.Event { return c.sub }

// Run executes one objective. It is called from the UI's run goroutine and
// returns when the agent stops; rendering keys off the event stream.
func (c *tuiController) Run(ctx context.Context, objective string) error {
	c.mu.Lock()
	if c.inFlight {
		c.mu.Unlock()
		return errors.New("a run is already in progress")
	}
	if c.dead {
		c.mu.Unlock()
		return errors.New("controller is shut down")
	}
	sess := c.sess
	c.mu.Unlock()
	if sess == nil {
		return errors.New("no active session")
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.runStop = cancel
	c.inFlight = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.inFlight = false
		c.runStop = nil
		c.mu.Unlock()
		cancel()
	}()

	_, err := c.runAgent(runCtx, sess, objective)
	return err
}

func (c *tuiController) runAgent(ctx context.Context, sess *session.Session, objective string) (agent.Result, error) {
	a := c.app
	rt := c.rt
	runner, err := agent.New(agent.Options{
		Provider:            rt.prov,
		Tools:               rt.dispatcher,
		Session:             sess,
		Workspace:           a.workspace,
		MaxTurns:            a.cfg.Agent.MaxTurns,
		MaxToolCallsPerTurn: a.cfg.Agent.MaxToolCallsPerTurn,
		MaxOutputTokens:     a.cfg.Agent.MaxOutputTokens,
		RequestTimeout:      a.cfg.Agent.RequestTimeout.Std(),
		TotalTimeout:        a.cfg.Agent.TotalTimeout.Std(),
		Stream:              a.cfg.Agent.Stream,
		Sink:                c.relay,
		SystemPrompt:        a.systemPrompt(rt.registry),
		ContextBudget: contextmgr.Budget{
			MaxInputTokens:      a.cfg.Context.MaxInputTokens,
			ReserveOutputTokens: a.cfg.Context.ReserveOutputTokens,
			SafetyMargin:        a.cfg.Context.SafetyMargin,
		},
		CompactThreshold: a.cfg.Context.CompactThreshold,
		CompactTarget:    a.cfg.Context.CompactTarget,
	})
	if err != nil {
		return agent.Result{}, err
	}
	return runner.Run(ctx, objective)
}

// Decide resolves a pending approval requested by the UI.
func (c *tuiController) Decide(d ui.Decision) {
	c.mu.Lock()
	ch := c.pending[d.ID]
	delete(c.pending, d.ID)
	c.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- approvalDecision{approve: d.Approve, trust: d.Trust}
}

func (c *tuiController) ListSessions() ([]ui.SessionRow, error) {
	list, err := c.rt.store.List()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	c.mu.Lock()
	current := ""
	if c.sess != nil {
		current = c.sess.ID()
	}
	c.mu.Unlock()
	rows := make([]ui.SessionRow, 0, len(list))
	for _, s := range list {
		objective := ""
		if sess, err := c.rt.store.Open(s.ID); err == nil {
			objective = oneLine(sess.State().Objective, 80)
		}
		rows = append(rows, ui.SessionRow{
			ID:        s.ID,
			Status:    s.Status,
			Turns:     s.Turns,
			Messages:  s.Messages,
			Updated:   s.UpdatedAt.Format("2006-01-02 15:04"),
			Objective: objective,
			Current:   s.ID == current,
		})
	}
	return rows, nil
}

func (c *tuiController) Resume(id string) (ui.Info, error) {
	c.mu.Lock()
	running := c.inFlight
	c.mu.Unlock()
	if running {
		return ui.Info{}, errors.New("cancel the current run before switching sessions")
	}
	sess, err := c.rt.openSession(id)
	if err != nil {
		return ui.Info{}, err
	}
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
	return c.Info(), nil
}

func (c *tuiController) Transcript() ([]protocol.Message, error) {
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	if sess == nil {
		return nil, errors.New("no active session")
	}
	return sess.Messages(), nil
}

func (c *tuiController) NewSession() (ui.Info, error) {
	c.mu.Lock()
	running := c.inFlight
	c.mu.Unlock()
	if running {
		return ui.Info{}, errors.New("cancel the current run before starting a new session")
	}
	sess, err := c.rt.createSession()
	if err != nil {
		return ui.Info{}, err
	}
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
	return c.Info(), nil
}

func (c *tuiController) Cancel() {
	c.mu.Lock()
	stop := c.runStop
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (c *tuiController) Shutdown() {
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return
	}
	c.dead = true
	// Release any approval the dispatcher is still awaiting: closing the
	// channel reads as a denial on the receiving side.
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.mu.Unlock()
	c.Cancel()
	c.relay.Close()
}

// approve implements the UI-side security policy: it mirrors the modes of
// App.approveTool but routes prompts through the transcript modal.
func (c *tuiController) approve(ctx context.Context, def tools.Definition, call tools.Call) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(c.app.cfg.Security.DefaultMode))
	if mode == "" {
		mode = "ask"
	}
	destructive := isDestructiveCall(def, call)
	switch mode {
	case "deny":
		return errors.New("denied by security.default_mode=deny")
	case "auto":
		if !destructive {
			return nil
		}
	}
	if c.app.isTrusted(def, call) {
		return nil
	}
	return c.promptUI(ctx, def, call, destructive)
}

func (c *tuiController) promptUI(ctx context.Context, def tools.Definition, call tools.Call, destructive bool) error {
	dec := make(chan approvalDecision, 1)
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return errors.New("denied: interface is closing")
	}
	c.pending[call.ID] = dec
	c.mu.Unlock()

	tool := &events.Tool{
		ID:          call.ID,
		Name:        string(def.Name),
		Category:    string(def.Category),
		Arguments:   events.Cap(string(call.Arguments), events.MaxArgumentPreview),
		Status:      "requested",
		Destructive: destructive,
	}
	c.relay.Emit(stampEvent(toolEvent(events.ApprovalRequested, tool)))
	defer func() {
		c.mu.Lock()
		delete(c.pending, call.ID)
		c.mu.Unlock()
	}()

	resolve := func(status, errStr string) {
		done := *tool
		done.Status = status
		done.Error = errStr
		c.relay.Emit(stampEvent(toolEvent(events.ApprovalResolved, &done)))
	}

	// dec is buffered, so a decision that arrives while we emit the request
	// simply waits in the channel; a single blocking wait is enough.
	select {
	case d, ok := <-dec:
		// ok == false means Shutdown released the request: treat as denial.
		if !ok || !d.approve {
			resolve("denied", "denied by user")
			return errors.New("denied by user")
		}
		if d.trust {
			c.app.trust(def, call)
			resolve("trusted", "")
			return nil
		}
		resolve("approved", "")
		return nil
	case <-ctx.Done():
		resolve("denied", "denied: run cancelled")
		return fmt.Errorf("denied: %w", ctx.Err())
	}
}

func toolEvent(kind events.Kind, t *events.Tool) events.Event {
	return events.Event{Kind: kind, Tool: t}
}

func stampEvent(e events.Event) events.Event {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	return e
}
