package mock

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"velo/internal/protocol"
	"velo/internal/provider"
)

type Step struct {
	Text         string
	ToolCalls    []protocol.ToolCall
	FinishReason protocol.FinishReason
	Usage        *protocol.Usage
	Err          error
	Delay        time.Duration
	ChunkSize    int
}

type Provider struct {
	mu     sync.Mutex
	name   string
	model  string
	steps  []Step
	index  int
	closed bool
	loop   bool
}

func New(name, model string, steps ...Step) *Provider {
	if name == "" {
		name = "mock"
	}
	if model == "" {
		model = "mock-model"
	}
	cloned := make([]Step, len(steps))
	for i, s := range steps {
		cloned[i] = cloneStep(s)
	}
	return &Provider{name: name, model: model, steps: cloned}
}

// NewDemo returns a looping, always-responding provider running a canned
// script that exercises streaming, tool calls, and an approval prompt. It is
// what `provider.name: mock` yields in the real CLI so the TUI can be used
// (and demonstrated) without any API key.
func NewDemo(model string) *Provider {
	if model == "" {
		model = "mock-demo"
	}
	p := New("mock", model, demoSteps()...)
	p.loop = true
	return p
}

func demoSteps() []Step {
	return []Step{
		{
			Text:      "Scanning the workspace first. ",
			ChunkSize: 4,
			ToolCalls: []protocol.ToolCall{{
				ID: "demo_1", Name: "list_files", Arguments: json.RawMessage(`{"path":"."}`),
			}},
			Usage: &protocol.Usage{InputTokens: 180, OutputTokens: 32},
		},
		{
			Text:      "Now reading go.mod. ",
			ChunkSize: 4,
			ToolCalls: []protocol.ToolCall{{
				ID: "demo_2", Name: "read_file", Arguments: json.RawMessage(`{"path":"go.mod"}`),
			}},
			Usage: &protocol.Usage{InputTokens: 420, OutputTokens: 58},
		},
		{
			Text: "Let me leave a note about this run — approve or deny the prompt to see both paths. ",
			ToolCalls: []protocol.ToolCall{{
				ID:        "demo_3",
				Name:      "write_file",
				Arguments: json.RawMessage(`{"path":".velo/demo-note.txt","content":"hello from the velo demo model\n"}`),
			}},
			Usage: &protocol.Usage{InputTokens: 900, OutputTokens: 88},
		},
		{
			Text:         "## Demo complete\n\nThis is the **offline demo model** (`provider.name: mock`). It replays the same script for every task, with streamed deltas, tool blocks, and one approval prompt.\n\n- `ctrl+o` expands the last tool block\n- `:sessions` opens the session picker\n- `:help` lists every binding\n\nSet a real provider key and rerun `velo` to put the actual agent to work.",
			ChunkSize:    6,
			FinishReason: protocol.FinishStop,
			Usage:        &protocol.Usage{InputTokens: 1400, OutputTokens: 210},
		},
	}
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) Model() string {
	return p.model
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Streaming:         true,
		Tools:             true,
		Usage:             true,
		ParallelToolCalls: false,
		MaxInputTokens:    0,
		MaxOutputTokens:   0,
	}
}

func (p *Provider) Complete(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	step, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.wait(ctx, step.Delay); err != nil {
		return nil, err
	}
	if step.Err != nil {
		return nil, step.Err
	}

	msg := protocol.Message{Role: protocol.RoleAssistant}
	if step.Text != "" {
		msg.Content = append(msg.Content, protocol.TextContent(step.Text))
	}
	for _, tc := range step.ToolCalls {
		msg.Content = append(msg.Content, protocol.ToolCallContent(cloneToolCall(tc)))
	}

	resp := &protocol.Response{
		Message:      msg,
		FinishReason: inferFinishReason(step),
	}
	if step.Usage != nil {
		u := *step.Usage
		u.Normalize()
		resp.Usage = &u
	}
	if err := resp.Validate(); err != nil {
		return nil, err
	}
	return resp, nil
}

func (p *Provider) Stream(ctx context.Context, req protocol.Request) (provider.Stream, error) {
	step, err := p.next()
	if err != nil {
		return nil, err
	}
	if step.Err != nil {
		return nil, step.Err
	}

	return &stream{
		ctx:          ctx,
		name:         p.name,
		events:       buildEvents(step),
		delay:        step.Delay,
		delayPending: true,
		closeCh:      make(chan struct{}),
	}, nil
}

func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func (p *Provider) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.index >= len(p.steps) {
		return 0
	}
	return len(p.steps) - p.index
}

func (p *Provider) next() (Step, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return Step{}, protocol.NewProviderError(protocol.ErrProtocol, p.name, "mock provider is closed")
	}
	if p.index >= len(p.steps) {
		if p.loop && len(p.steps) > 0 {
			p.index = 0
		} else {
			return Step{}, protocol.NewProviderError(protocol.ErrProtocol, p.name, "mock provider script exhausted")
		}
	}
	step := p.steps[p.index]
	p.index++
	return step, nil
}

func (p *Provider) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return protocol.ErrorFromContext(p.name, err)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return protocol.ErrorFromContext(p.name, ctx.Err())
	case <-timer.C:
		return nil
	}
}

type stream struct {
	mu           sync.Mutex
	ctx          context.Context
	name         string
	events       []protocol.StreamEvent
	index        int
	closed       bool
	delay        time.Duration
	delayPending bool
	closeCh      chan struct{}
	closeOnce    sync.Once
}

func (s *stream) Recv() (protocol.StreamEvent, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocol.StreamEvent{}, io.EOF
	}
	if s.index >= len(s.events) {
		s.mu.Unlock()
		return protocol.StreamEvent{}, io.EOF
	}

	if s.delayPending {
		s.delayPending = false
		d := s.delay
		s.mu.Unlock()
		if err := s.waitDelay(d); err != nil {
			return protocol.StreamEvent{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return protocol.StreamEvent{}, io.EOF
		}
	}

	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return protocol.StreamEvent{}, protocol.ErrorFromContext(s.name, err)
	}

	event := s.events[s.index]
	s.index++
	s.mu.Unlock()
	return event, nil
}

func (s *stream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	return nil
}

func (s *stream) waitDelay(d time.Duration) error {
	if err := s.ctx.Err(); err != nil {
		return protocol.ErrorFromContext(s.name, err)
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return protocol.ErrorFromContext(s.name, s.ctx.Err())
	case <-s.closeCh:
		return io.EOF
	case <-timer.C:
		return nil
	}
}

func cloneStep(s Step) Step {
	c := s
	if s.ToolCalls != nil {
		c.ToolCalls = make([]protocol.ToolCall, len(s.ToolCalls))
		for i, tc := range s.ToolCalls {
			c.ToolCalls[i] = cloneToolCall(tc)
		}
	}
	if s.Usage != nil {
		u := *s.Usage
		c.Usage = &u
	}
	return c
}

func cloneToolCall(tc protocol.ToolCall) protocol.ToolCall {
	if tc.Arguments != nil {
		tc.Arguments = append(json.RawMessage(nil), tc.Arguments...)
	}
	return tc
}

func inferFinishReason(s Step) protocol.FinishReason {
	if s.FinishReason != "" {
		return s.FinishReason
	}
	if len(s.ToolCalls) > 0 {
		return protocol.FinishToolCalls
	}
	return protocol.FinishStop
}

func buildEvents(s Step) []protocol.StreamEvent {
	events := make([]protocol.StreamEvent, 0, len(s.ToolCalls)+2)
	if s.Text != "" {
		size := s.ChunkSize
		if size <= 0 {
			size = 8
		}
		runes := []rune(s.Text)
		for i := 0; i < len(runes); i += size {
			end := i + size
			if end > len(runes) {
				end = len(runes)
			}
			events = append(events, protocol.StreamEvent{
				Type: protocol.StreamTextDelta,
				Text: string(runes[i:end]),
			})
		}
	}
	for i, tc := range s.ToolCalls {
		events = append(events, protocol.StreamEvent{
			Type: protocol.StreamToolCallDelta,
			ToolCall: &protocol.ToolCallDelta{
				Index:          i,
				ID:             tc.ID,
				Name:           tc.Name,
				ArgumentsDelta: string(tc.Arguments),
			},
		})
	}
	if s.Usage != nil {
		u := *s.Usage
		u.Normalize()
		events = append(events, protocol.StreamEvent{
			Type:  protocol.StreamUsage,
			Usage: &u,
		})
	}
	events = append(events, protocol.StreamEvent{
		Type:         protocol.StreamDone,
		FinishReason: inferFinishReason(s),
	})
	return events
}
