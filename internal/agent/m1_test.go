package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
	"velo/internal/tools"
)

type scriptProvider struct {
	stream provider.Stream
	resp   *protocol.Response
	err    error
}

func (p *scriptProvider) Name() string  { return "script" }
func (p *scriptProvider) Model() string { return "script-model" }
func (p *scriptProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Streaming: p.stream != nil}
}
func (p *scriptProvider) Complete(context.Context, protocol.Request) (*protocol.Response, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.resp, nil
}
func (p *scriptProvider) Stream(context.Context, protocol.Request) (provider.Stream, error) {
	return p.stream, nil
}
func (p *scriptProvider) Close() error { return nil }

type scriptStream struct {
	events []protocol.StreamEvent
	i      int
	after  error
}

func (s *scriptStream) Recv() (protocol.StreamEvent, error) {
	if s.i >= len(s.events) {
		if s.after != nil {
			return protocol.StreamEvent{}, s.after
		}
		return protocol.StreamEvent{}, io.EOF
	}
	ev := s.events[s.i]
	s.i++
	return ev, nil
}

func (s *scriptStream) Close() error { return nil }

type holdStream struct {
	ctx     context.Context
	sent    bool
	entered chan struct{}
}

func (s *holdStream) Recv() (protocol.StreamEvent, error) {
	if !s.sent {
		s.sent = true
		return protocol.StreamEvent{Type: protocol.StreamTextDelta, Text: "partial"}, nil
	}
	select {
	case <-s.entered:
	default:
		close(s.entered)
	}
	<-s.ctx.Done()
	return protocol.StreamEvent{}, s.ctx.Err()
}

func (s *holdStream) Close() error { return nil }

type holdProvider struct {
	entered chan struct{}
}

func (p *holdProvider) Name() string  { return "hold" }
func (p *holdProvider) Model() string { return "hold-model" }
func (p *holdProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Streaming: true}
}
func (p *holdProvider) Complete(context.Context, protocol.Request) (*protocol.Response, error) {
	return nil, errors.New("complete not used")
}
func (p *holdProvider) Stream(ctx context.Context, _ protocol.Request) (provider.Stream, error) {
	return &holdStream{ctx: ctx, entered: p.entered}, nil
}
func (p *holdProvider) Close() error { return nil }

func assertBalanced(t *testing.T, msgs []protocol.Message) {
	t.Helper()
	pending := map[string]int{}
	for _, m := range msgs {
		for _, call := range m.ToolCalls() {
			pending[call.ID]++
		}
		if m.Role == protocol.RoleTool {
			pending[m.ToolCallID]--
		}
	}
	for id, n := range pending {
		if n != 0 {
			t.Fatalf("tool call %s balance = %d", id, n)
		}
	}
}

func TestStreamEOFIsNotCompleted(t *testing.T) {
	p := &scriptProvider{stream: &scriptStream{
		events: []protocol.StreamEvent{{Type: protocol.StreamTextDelta, Text: "partial"}},
	}}
	a, sess := newTestAgent(t, p, newRegistry(t), nil, func(opts *Options) {
		opts.Stream = true
	})
	result, err := a.Run(context.Background(), "stream")
	if !errors.Is(err, protocol.ErrStreamIncomplete) {
		t.Fatalf("error = %v, want ErrStreamIncomplete", err)
	}
	if result.Status == StateCompleted || sess.State().Status == string(StateCompleted) {
		t.Fatalf("partial stream completed: result=%s state=%s", result.Status, sess.State().Status)
	}
}

func TestStreamDoneCompletes(t *testing.T) {
	p := &scriptProvider{stream: &scriptStream{events: []protocol.StreamEvent{
		{Type: protocol.StreamTextDelta, Text: "hello"},
		{Type: protocol.StreamDone, FinishReason: protocol.FinishStop},
	}}}
	a, _ := newTestAgent(t, p, newRegistry(t), nil, func(opts *Options) {
		opts.Stream = true
	})
	result, err := a.Run(context.Background(), "stream")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != StateCompleted || result.Truncated || result.Content != "hello" {
		t.Fatalf("result = %+v", result)
	}
}

func TestStreamCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	a, sess := newTestAgent(t, &holdProvider{entered: entered}, newRegistry(t), nil, func(opts *Options) {
		opts.Stream = true
	})
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := a.Run(ctx, "cancel the stream")
		done <- outcome{result, err}
	}()
	select {
	case <-entered:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("blocked Recv did not start")
	}
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", got.err)
		}
		if got.result.Status != StateCancelled || sess.State().Status != string(StateCancelled) {
			t.Fatalf("status = %s state = %s", got.result.Status, sess.State().Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestFinishReasons(t *testing.T) {
	cases := []struct {
		name      string
		step      mock.Step
		want      State
		wantErr   error
		truncated bool
		content   string
	}{
		{
			name:    "stop",
			step:    mock.Step{Text: "done", FinishReason: protocol.FinishStop},
			want:    StateCompleted,
			content: "done",
		},
		{
			name:      "length",
			step:      mock.Step{Text: "part", FinishReason: protocol.FinishLength},
			want:      StateCompleted,
			truncated: true,
			content:   "part",
		},
		{
			name:    "length empty",
			step:    mock.Step{FinishReason: protocol.FinishLength},
			want:    StateFailed,
			wantErr: protocol.ErrTruncated,
		},
		{
			name:    "content_filter",
			step:    mock.Step{Text: "hidden", FinishReason: protocol.FinishContentFilter},
			want:    StateFailed,
			wantErr: protocol.ErrContentFiltered,
		},
		{
			name:    "error",
			step:    mock.Step{Text: "boom", FinishReason: protocol.FinishError},
			want:    StateFailed,
			wantErr: protocol.ErrFinishError,
		},
		{
			name:    "unknown",
			step:    mock.Step{Text: "?", FinishReason: protocol.FinishUnknown},
			want:    StateFailed,
			wantErr: protocol.ErrFinishUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mock.New("mock", "mock-model", tc.step)
			a, sess := newTestAgent(t, p, newRegistry(t), nil, nil)
			result, err := a.Run(context.Background(), "finish")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("run: %v", err)
			}
			if result.Status != tc.want || sess.State().Status != string(tc.want) {
				t.Fatalf("status = %s state = %s, want %s", result.Status, sess.State().Status, tc.want)
			}
			if result.Truncated != tc.truncated {
				t.Fatalf("truncated = %v, want %v", result.Truncated, tc.truncated)
			}
			if tc.content != "" && result.Content != tc.content {
				t.Fatalf("content = %q, want %q", result.Content, tc.content)
			}
			if tc.want == StateCompleted && tc.truncated == false && result.Status != StateCompleted {
				t.Fatal("unqualified completion was not completed")
			}
		})
	}

	t.Run("tool_calls", func(t *testing.T) {
		p := mock.New("mock", "mock-model",
			mock.Step{
				ToolCalls:    []protocol.ToolCall{toolCall("call_1", "echo", `{"text":"hi"}`)},
				FinishReason: protocol.FinishToolCalls,
			},
			mock.Step{Text: "done", FinishReason: protocol.FinishStop},
		)
		a, _ := newTestAgent(t, p, newRegistry(t, echoTool()), nil, nil)
		result, err := a.Run(context.Background(), "use the tool")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if result.Status != StateCompleted || result.Truncated || result.Content != "done" || result.ToolCalls != 1 {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("length with tool call", func(t *testing.T) {
		p := mock.New("mock", "mock-model", mock.Step{
			ToolCalls:    []protocol.ToolCall{toolCall("call_1", "echo", `{"text":"hi"}`)},
			FinishReason: protocol.FinishLength,
		})
		a, sess := newTestAgent(t, p, newRegistry(t, echoTool()), nil, nil)
		result, err := a.Run(context.Background(), "cut off")
		if !errors.Is(err, protocol.ErrTruncated) {
			t.Fatalf("error = %v", err)
		}
		if result.Status != StateFailed {
			t.Fatalf("status = %s", result.Status)
		}
		assertBalanced(t, sess.Messages())
	})
}

func TestCancelAfterToolCallPersist(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := []protocol.ToolCall{
		toolCall("c1", "echo", `{"text":"one"}`),
		toolCall("c2", "echo", `{"text":"two"}`),
	}
	p := &scriptProvider{resp: &protocol.Response{
		Message: protocol.Message{
			Role: protocol.RoleAssistant,
			Content: []protocol.Content{
				protocol.ToolCallContent(calls[0]),
				protocol.ToolCallContent(calls[1]),
			},
		},
		FinishReason: protocol.FinishToolCalls,
	}}
	// Cancel inside Complete, after the model response exists and before the
	// agent can execute the calls it is about to persist.
	p.err = nil
	base := p
	cancelling := &cancelOnComplete{scriptProvider: base, cancel: cancel}
	a, sess := newTestAgent(t, cancelling, newRegistry(t, echoTool()), nil, nil)
	result, err := a.Run(ctx, "call tools")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if result.Status != StateCancelled || sess.State().Status != string(StateCancelled) {
		t.Fatalf("status = %s state = %s", result.Status, sess.State().Status)
	}
	msgs := sess.Messages()
	assertBalanced(t, msgs)
	var results int
	for _, m := range msgs {
		if m.Role == protocol.RoleTool {
			results++
			if !strings.Contains(m.Text(), "cancelled") {
				t.Fatalf("tool result = %q", m.Text())
			}
		}
	}
	if results != 2 {
		t.Fatalf("tool results = %d, want 2", results)
	}

	store := sess.Store()
	id := sess.ID()
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := store.Open(id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	assertBalanced(t, reopened.Messages())
	if len(reopened.Messages()) != len(msgs) {
		t.Fatalf("reopened messages = %d, want %d", len(reopened.Messages()), len(msgs))
	}
}

type cancelOnComplete struct {
	*scriptProvider
	cancel context.CancelFunc
}

func (p *cancelOnComplete) Complete(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	p.cancel()
	return p.scriptProvider.Complete(ctx, req)
}

func TestResumeRepairsOrphanedToolCalls(t *testing.T) {
	sess := newSession(t)
	if err := sess.AppendMessage(protocol.Message{
		Role:    protocol.RoleUser,
		Content: []protocol.Content{protocol.TextContent("original")},
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := sess.AppendMessage(protocol.Message{
		Role: protocol.RoleAssistant,
		Content: []protocol.Content{
			protocol.ToolCallContent(toolCall("c1", "echo", `{"text":"one"}`)),
			protocol.ToolCallContent(toolCall("c2", "echo", `{"text":"two"}`)),
		},
	}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	p := mock.New("mock", "mock-model", mock.Step{Text: "resumed", FinishReason: protocol.FinishStop})
	dispatcher, err := tools.NewDispatcher(tools.DispatcherOptions{
		Registry:       newRegistry(t, echoTool()),
		DefaultTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	a, err := New(Options{Provider: p, Tools: dispatcher, Session: sess, Workspace: sess.Workspace()})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	result, err := a.Run(context.Background(), "continue")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != StateCompleted || result.Content != "resumed" {
		t.Fatalf("result = %+v", result)
	}
	msgs := sess.Messages()
	assertBalanced(t, msgs)
	if len(msgs) != 6 {
		t.Fatalf("messages = %d, want 6", len(msgs))
	}
	if msgs[2].Role != protocol.RoleTool || msgs[2].ToolCallID != "c1" || msgs[3].ToolCallID != "c2" {
		t.Fatalf("repaired results = %+v %+v", msgs[2], msgs[3])
	}
	if msgs[4].Role != protocol.RoleUser || msgs[4].Text() != "continue" {
		t.Fatalf("user message landed before repair: %+v", msgs[4])
	}
	var sawRecovery bool
	for _, ev := range sess.Events() {
		if ev.Kind == "state" && ev.Data["to"] == string(StateRecoveryRequired) {
			sawRecovery = true
		}
	}
	if !sawRecovery {
		t.Fatal("state did not pass through recovery_required")
	}

	store := sess.Store()
	id := sess.ID()
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := store.Open(id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	assertBalanced(t, reopened.Messages())
	if reopened.State().Status != string(StateCompleted) {
		t.Fatalf("reopened state = %s", reopened.State().Status)
	}
}

func TestTooManyToolCallsRepairsTranscript(t *testing.T) {
	p := mock.New("mock", "mock-model", mock.Step{
		ToolCalls: []protocol.ToolCall{
			toolCall("c1", "echo", `{"text":"one"}`),
			toolCall("c2", "echo", `{"text":"two"}`),
		},
		FinishReason: protocol.FinishToolCalls,
	})
	a, sess := newTestAgent(t, p, newRegistry(t, echoTool()), nil, func(opts *Options) {
		opts.MaxToolCallsPerTurn = 1
	})
	result, err := a.Run(context.Background(), "too many")
	if err == nil || !strings.Contains(err.Error(), "too many tool calls") {
		t.Fatalf("error = %v", err)
	}
	if result.Status != StateFailed {
		t.Fatalf("status = %s", result.Status)
	}
	assertBalanced(t, sess.Messages())
}
