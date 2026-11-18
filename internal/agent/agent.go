package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"velo/internal/contextmgr"
	"velo/internal/events"
	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/session"
	"velo/internal/tools"
)

const (
	defaultMaxTurns            = 40
	defaultMaxToolCallsPerTurn = 8
	defaultRequestTimeout      = 120 * time.Second
	defaultTotalTimeout        = 30 * time.Minute
)

type Options struct {
	Provider            provider.Provider
	Tools               *tools.Dispatcher
	Session             *session.Session
	Workspace           string
	MaxTurns            int
	MaxToolCallsPerTurn int
	MaxOutputTokens     int
	RequestTimeout      time.Duration
	TotalTimeout        time.Duration
	Stream              bool
	Output              io.Writer
	SystemPrompt        string
	ContextBudget       contextmgr.Budget
	// Sink receives live run events for user interfaces. It is optional; a nil
	// Sink disables streaming. Emission never blocks the run: the agent hands
	// each event to the sink synchronously and the sink is responsible for
	// buffering or dropping.
	Sink events.Emitter
	// CompactThreshold and CompactTarget control history compaction. History
	// is reduced only when it exceeds CompactThreshold and is trimmed to
	// CompactTarget in the same step, which keeps request prefixes stable
	// between turns. Zero values use the contextmgr defaults.
	CompactThreshold float64
	CompactTarget    float64
}

// VerificationStatus describes how much of the task outcome is backed by
// executed checks. A model assertion alone is never verification.
type VerificationStatus string

const (
	VerificationVerified   VerificationStatus = "verified"
	VerificationPartial    VerificationStatus = "partially_verified"
	VerificationUnverified VerificationStatus = "unverified"
	VerificationFailed     VerificationStatus = "failed"
)

type Result struct {
	SessionID          string
	Status             State
	Content            string
	Turns              int
	ToolCalls          int
	Usage              *protocol.Usage
	Verification       VerificationStatus
	VerificationDetail string
}

type Agent struct {
	provider            provider.Provider
	tools               *tools.Dispatcher
	session             *session.Session
	workspace           string
	maxTurns            int
	maxToolCallsPerTurn int
	maxOutputTokens     int
	requestTimeout      time.Duration
	totalTimeout        time.Duration
	stream              bool
	output              io.Writer
	systemPrompt        string
	contextBudget       contextmgr.Budget
	sink                events.Emitter
	compactor           *contextmgr.Compactor
	machine             *Machine
	objective           string
	turns               int
	toolCalls           int
	usage               protocol.Usage
	usageSet            bool
	finished            bool
	cause               error
	streamedText        bool
	content             string
	verification        VerificationStatus
	verificationDetail  string
}

func New(opts Options) (*Agent, error) {
	if opts.Provider == nil {
		return nil, fmt.Errorf("agent: provider is required")
	}
	if opts.Tools == nil {
		return nil, fmt.Errorf("agent: tools dispatcher is required")
	}
	if opts.Session == nil {
		return nil, fmt.Errorf("agent: session is required")
	}

	maxTurns := opts.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}
	if maxTurns < 0 {
		return nil, fmt.Errorf("agent: max turns must not be negative")
	}
	maxToolCalls := opts.MaxToolCallsPerTurn
	if maxToolCalls == 0 {
		maxToolCalls = defaultMaxToolCallsPerTurn
	}
	if maxToolCalls < 0 {
		return nil, fmt.Errorf("agent: max tool calls per turn must not be negative")
	}
	requestTimeout := opts.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	if requestTimeout < 0 {
		return nil, fmt.Errorf("agent: request timeout must not be negative")
	}
	totalTimeout := opts.TotalTimeout
	if totalTimeout == 0 {
		totalTimeout = defaultTotalTimeout
	}
	if totalTimeout < 0 {
		return nil, fmt.Errorf("agent: total timeout must not be negative")
	}
	if opts.MaxOutputTokens < 0 {
		return nil, fmt.Errorf("agent: max output tokens must not be negative")
	}

	budget := opts.ContextBudget
	if budget.MaxInputTokens > 0 && opts.SystemPrompt != "" {
		budget.MaxInputTokens -= contextmgr.EstimateStringTokens(opts.SystemPrompt)
		if budget.MaxInputTokens < 1 {
			budget.MaxInputTokens = 1
		}
	}
	compactor := contextmgr.NewCompactor(budget, opts.CompactTarget, opts.CompactThreshold)

	return &Agent{
		provider:            opts.Provider,
		tools:               opts.Tools,
		session:             opts.Session,
		workspace:           opts.Workspace,
		maxTurns:            maxTurns,
		maxToolCallsPerTurn: maxToolCalls,
		maxOutputTokens:     opts.MaxOutputTokens,
		requestTimeout:      requestTimeout,
		totalTimeout:        totalTimeout,
		stream:              opts.Stream,
		output:              opts.Output,
		systemPrompt:        opts.SystemPrompt,
		contextBudget:       budget,
		sink:                opts.Sink,
		compactor:           compactor,
		machine:             NewMachine(),
	}, nil
}

func (a *Agent) Run(ctx context.Context, objective string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(objective) == "" {
		return a.fail(fmt.Errorf("agent: objective is required"))
	}
	a.objective = objective
	a.compactor.Reset()
	a.publish(events.Event{Kind: events.RunStarted, Objective: objective})

	if a.totalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.totalTimeout)
		defer cancel()
	}

	if err := a.transitionState(StateContextBuilding); err != nil {
		return a.result(StateFailed), err
	}
	userMessage := protocol.Message{
		Role:    protocol.RoleUser,
		Content: []protocol.Content{protocol.TextContent(objective)},
	}
	if err := a.session.AppendMessage(userMessage); err != nil {
		return a.fail(fmt.Errorf("agent: append objective: %w", err))
	}

	for turn := 1; turn <= a.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return a.cancel(err)
		}
		a.turns = turn
		a.streamedText = false
		a.publish(events.Event{Kind: events.TurnStarted, Turn: turn})
		req, ctxStats := a.buildRequest()
		if ctxStats.Compacted || ctxStats.OverBudget {
			a.publish(events.Event{Kind: events.ContextCompacted, Turn: turn, Context: &events.ContextStats{
				Compacted:      ctxStats.Compacted,
				Trimmed:        ctxStats.Trimmed,
				OverBudget:     ctxStats.OverBudget,
				MessagesBefore: ctxStats.MessagesBefore,
				MessagesAfter:  ctxStats.MessagesAfter,
				Dropped:        ctxStats.Dropped,
				BytesBefore:    ctxStats.BytesBefore,
				BytesAfter:     ctxStats.BytesAfter,
				LimitTokens:    ctxStats.EstimatedLimit,
			}})
			if err := a.session.AppendEvent("context.trim", map[string]any{
				"compacted":       ctxStats.Compacted,
				"trimmed":         ctxStats.Trimmed,
				"over_budget":     ctxStats.OverBudget,
				"messages_before": ctxStats.MessagesBefore,
				"messages_after":  ctxStats.MessagesAfter,
				"bytes_before":    ctxStats.BytesBefore,
				"bytes_after":     ctxStats.BytesAfter,
				"limit_tokens":    ctxStats.EstimatedLimit,
			}); err != nil {
				return a.fail(fmt.Errorf("agent: append context event: %w", err))
			}
		}
		if err := a.transitionState(StateRequestingModel); err != nil {
			return a.result(StateFailed), err
		}
		resp, err := a.request(ctx, req)
		if err != nil {
			return a.handleRequestError(err)
		}
		a.addUsage(resp.Usage)
		if !a.streamedText {
			if text := resp.Message.Text(); text != "" {
				a.publish(events.Event{Kind: events.TextDelta, Turn: a.turns, Delta: events.Cap(text, events.MaxTextEvent)})
			}
		}
		if err := a.session.AppendMessage(resp.Message); err != nil {
			return a.fail(fmt.Errorf("agent: append model response: %w", err))
		}
		if err := a.appendResponseEvent(resp); err != nil {
			return a.fail(fmt.Errorf("agent: append model response event: %w", err))
		}
		if err := a.transitionState(StateProcessingResults); err != nil {
			return a.result(StateFailed), err
		}

		calls := resp.Message.ToolCalls()
		if len(calls) == 0 {
			text := resp.Message.Text()
			if strings.TrimSpace(text) == "" {
				return a.fail(fmt.Errorf("agent: empty response from model"))
			}
			a.content = text
			a.verification = VerificationUnverified
			a.verificationDetail = "no verification checks were executed"
			if err := a.transitionState(StateVerifying); err != nil {
				return a.result(StateFailed), err
			}
			if err := a.session.AppendEvent("verification", map[string]any{
				"status": string(a.verification),
				"detail": a.verificationDetail,
			}); err != nil {
				return a.fail(fmt.Errorf("agent: append verification event: %w", err))
			}
			if err := a.transitionState(StateCompleted); err != nil {
				return a.result(StateFailed), err
			}
			return a.result(StateCompleted), nil
		}

		if len(calls) > a.maxToolCallsPerTurn {
			return a.fail(fmt.Errorf("agent: too many tool calls in one turn: %d (max %d)", len(calls), a.maxToolCallsPerTurn))
		}
		for _, call := range calls {
			if err := ctx.Err(); err != nil {
				return a.cancel(err)
			}
			if err := a.executeCall(ctx, call); err != nil {
				return a.fail(err)
			}
		}
		if err := ctx.Err(); err != nil {
			return a.cancel(err)
		}

		if turn >= a.maxTurns {
			return a.fail(fmt.Errorf("agent: maximum turns exceeded (%d)", a.maxTurns))
		}
		if err := a.transitionState(StateContextBuilding); err != nil {
			return a.result(StateFailed), err
		}
	}

	return a.fail(fmt.Errorf("agent: maximum turns exceeded (%d)", a.maxTurns))
}

func (a *Agent) buildRequest() (protocol.Request, contextmgr.Stats) {
	messages, stats := a.buildMessages()
	return protocol.Request{
		Model:           a.provider.Model(),
		Messages:        messages,
		Tools:           a.toolDefinitions(),
		MaxOutputTokens: a.maxOutputTokens,
		Stream:          a.stream,
	}, stats
}

func (a *Agent) request(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	reqCtx := ctx
	if a.requestTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, a.requestTimeout)
		defer cancel()
	}
	if a.stream && a.provider.Capabilities().Streaming {
		return a.requestStream(reqCtx, req)
	}
	resp, err := a.provider.Complete(reqCtx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("agent: provider returned no response")
	}
	if err := resp.Validate(); err != nil {
		return nil, fmt.Errorf("agent: invalid provider response: %w", err)
	}
	return resp, nil
}

func (a *Agent) requestStream(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	st, err := a.provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	builder := protocol.NewStreamBuilder()
	for {
		event, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if event.Type == protocol.StreamTextDelta && event.Text != "" {
			a.streamedText = true
			a.publish(events.Event{Kind: events.TextDelta, Turn: a.turns, Delta: event.Text})
			if a.output != nil {
				_, _ = io.WriteString(a.output, event.Text)
			}
		}
		if err := builder.Apply(event); err != nil {
			return nil, err
		}
		if event.Type == protocol.StreamDone {
			break
		}
	}
	resp, err := builder.Response()
	if err != nil {
		return nil, err
	}
	if err := resp.Validate(); err != nil {
		return nil, fmt.Errorf("agent: invalid streamed response: %w", err)
	}
	return resp, nil
}

func (a *Agent) buildMessages() ([]protocol.Message, contextmgr.Stats) {
	history, stats := a.compactor.Compile(a.session.Messages())
	messages := make([]protocol.Message, 0, len(history)+1)
	if a.systemPrompt != "" {
		messages = append(messages, protocol.Message{
			Role:    protocol.RoleSystem,
			Content: []protocol.Content{protocol.TextContent(a.systemPrompt)},
		})
	}
	messages = append(messages, history...)
	return messages, stats
}

func (a *Agent) toolDefinitions() []protocol.ToolDefinition {
	definitions := a.tools.Definitions()
	if len(definitions) == 0 {
		return nil
	}
	out := make([]protocol.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, protocol.ToolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: definition.InputSchema,
		})
	}
	return out
}

func (a *Agent) appendResponseEvent(resp *protocol.Response) error {
	data := map[string]any{
		"turn":          a.turns,
		"finish_reason": string(resp.FinishReason),
		"usage":         resp.Usage,
	}
	return a.session.AppendEvent("model.response", data)
}

func (a *Agent) addUsage(usage *protocol.Usage) {
	if usage == nil {
		return
	}
	a.usage.InputTokens += usage.InputTokens
	a.usage.OutputTokens += usage.OutputTokens
	a.usage.TotalTokens += usage.TotalTokens
	a.usage.CachedInputTokens += usage.CachedInputTokens
	a.usage.Normalize()
	a.usageSet = true
	cumulative := a.usage
	a.publish(events.Event{Kind: events.UsageUpdated, Turn: a.turns, Usage: &cumulative})
}

func (a *Agent) executeCall(ctx context.Context, call protocol.ToolCall) error {
	if strings.TrimSpace(call.ID) == "" {
		return fmt.Errorf("agent: tool call id is required")
	}
	if strings.TrimSpace(call.Name) == "" {
		return fmt.Errorf("agent: tool call %q name is required", call.ID)
	}
	if err := a.transitionState(StateAwaitingToolApproval); err != nil {
		return err
	}
	category := ""
	if def, ok := a.tools.Definition(call.Name); ok {
		category = string(def.Category)
	}
	if err := a.session.AppendEvent("tool.start", map[string]any{
		"call_id":         call.ID,
		"name":            call.Name,
		"category":        category,
		"status":          "running",
		"arguments_bytes": len(call.Arguments),
	}); err != nil {
		return fmt.Errorf("agent: append tool start event: %w", err)
	}
	a.publish(events.Event{Kind: events.ToolStarted, Turn: a.turns, Tool: &events.Tool{
		ID:        call.ID,
		Name:      call.Name,
		Category:  category,
		Arguments: events.Cap(string(call.Arguments), events.MaxArgumentPreview),
		Status:    "running",
	}})
	if err := a.transitionState(StateExecutingTools); err != nil {
		return err
	}

	started := time.Now()
	result, dispatchErr := a.tools.Dispatch(ctx, tools.Call{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	})
	duration := time.Since(started).Milliseconds()

	status := "succeeded"
	if result.IsError || dispatchErr != nil {
		status = "failed"
	}
	if err := a.session.AppendEvent("tool.result", map[string]any{
		"call_id":        call.ID,
		"name":           call.Name,
		"status":         status,
		"error_category": errorCategory(dispatchErr),
		"is_error":       result.IsError || dispatchErr != nil,
		"duration_ms":    duration,
	}); err != nil {
		return fmt.Errorf("agent: append tool result event: %w", err)
	}
	a.publish(events.Event{Kind: events.ToolFinished, Turn: a.turns, Tool: &events.Tool{
		ID:         call.ID,
		Name:       call.Name,
		Category:   category,
		Arguments:  events.Cap(string(call.Arguments), events.MaxArgumentPreview),
		Status:     status,
		DurationMS: duration,
		Output:     events.Cap(result.Content, events.MaxOutputPreview),
		Error:      eventError(dispatchErr),
	}})
	if err := a.transitionState(StateProcessingResults); err != nil {
		return err
	}

	content := result.Content
	if content == "" && dispatchErr != nil {
		content = "error: " + dispatchErr.Error()
	}
	toolMessage := protocol.Message{
		Role:             protocol.RoleTool,
		ToolCallID:       call.ID,
		Name:             call.Name,
		ThoughtSignature: call.ThoughtSignature,
		Content:          []protocol.Content{protocol.TextContent(content)},
	}
	if err := a.session.AppendMessage(toolMessage); err != nil {
		return fmt.Errorf("agent: append tool result message: %w", err)
	}
	a.toolCalls++
	return nil
}

// eventError renders an error for a stream event without leaking full
// command output; the caller caps it.
func eventError(err error) string {
	if err == nil {
		return ""
	}
	return events.Cap(err.Error(), events.MaxErrorPreview)
}

func errorCategory(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, tools.ErrDenied), errors.Is(err, tools.ErrApprovalRequired):
		return "permission_denied"
	case errors.Is(err, tools.ErrNotFound):
		return "not_found"
	case errors.Is(err, tools.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, tools.ErrInvalidArguments):
		return "validation_error"
	case errors.Is(err, tools.ErrConflict):
		return "conflict"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "execution_error"
	}
}

func (a *Agent) transitionState(to State) error {
	from := a.machine.State()
	if err := a.machine.Transition(to); err != nil {
		return err
	}
	a.publish(events.Event{Kind: events.StateChanged, State: &events.StateChange{
		From: string(from),
		To:   string(to),
	}})
	if err := a.session.AppendEvent("state", map[string]any{
		"from": string(from),
		"to":   string(to),
	}); err != nil {
		a.machine.force(StateFailed)
		_ = a.saveState(StateFailed)
		return fmt.Errorf("agent: append state event: %w", err)
	}
	if err := a.saveState(to); err != nil {
		a.machine.force(StateFailed)
		_ = a.saveState(StateFailed)
		return err
	}
	return nil
}

func (a *Agent) saveState(state State) error {
	err := a.session.SaveState(session.State{
		Status:    string(state),
		Turns:     a.turns,
		Objective: a.objective,
	})
	if err != nil {
		return fmt.Errorf("agent: save state: %w", err)
	}
	return nil
}

func (a *Agent) handleRequestError(err error) (Result, error) {
	if errors.Is(err, context.Canceled) {
		return a.cancel(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return a.fail(fmt.Errorf("agent: request timed out: %w", err))
	}
	return a.fail(err)
}

func (a *Agent) cancel(cause error) (Result, error) {
	a.cause = cause
	state := a.machine.State()
	if !state.Terminal() {
		if err := a.transitionState(StateCancelled); err != nil {
			return a.result(StateCancelled), err
		}
	}
	return a.result(StateCancelled), cause
}

func (a *Agent) fail(cause error) (Result, error) {
	a.cause = cause
	if !a.machine.State().Terminal() {
		if err := a.transitionState(StateFailed); err != nil {
			return a.result(StateFailed), err
		}
	}
	return a.result(StateFailed), cause
}

// publish sends one event to the sink, stamping time and session id. Sinks
// must be quick; a nil sink drops the event.
func (a *Agent) publish(e events.Event) {
	if a.sink == nil {
		return
	}
	e.Time = time.Now().UTC()
	e.SessionID = a.session.ID()
	a.sink.Emit(e)
}

func (a *Agent) result(status State) Result {
	result := Result{
		SessionID:          a.session.ID(),
		Status:             status,
		Content:            a.content,
		Turns:              a.turns,
		ToolCalls:          a.toolCalls,
		Verification:       a.verification,
		VerificationDetail: a.verificationDetail,
	}
	if a.usageSet {
		usage := a.usage
		usage.Normalize()
		result.Usage = &usage
	}
	a.publishFinish(result)
	return result
}

// publishFinish emits the terminal Error (when applicable) and RunFinished
// events exactly once, whichever path ends the run.
func (a *Agent) publishFinish(res Result) {
	if a.finished {
		return
	}
	a.finished = true
	if res.Status != StateCompleted && a.cause != nil {
		a.publish(events.Event{Kind: events.Error, Message: a.cause.Error()})
	}
	summary := &events.RunSummary{
		Status:             string(res.Status),
		Content:            res.Content,
		Turns:              res.Turns,
		ToolCalls:          res.ToolCalls,
		Usage:              res.Usage,
		Verification:       string(res.Verification),
		VerificationDetail: res.VerificationDetail,
	}
	if a.cause != nil {
		summary.Error = a.cause.Error()
	}
	a.publish(events.Event{Kind: events.RunFinished, Run: summary})
}
