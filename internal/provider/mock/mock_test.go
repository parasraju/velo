package mock_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
	"unicode/utf8"

	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
)

func request() protocol.Request {
	return protocol.Request{Model: "test-model"}
}

func collect(t *testing.T, s provider.Stream) *protocol.Response {
	t.Helper()
	b := protocol.NewStreamBuilder()
	for {
		ev, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv() error: %v", err)
		}
		if err := b.Apply(ev); err != nil {
			t.Fatalf("Apply(%+v) error: %v", ev, err)
		}
	}
	resp, err := b.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	return resp
}

func TestCompleteStepsAndRemaining(t *testing.T) {
	steps := []mock.Step{
		{Text: "first", Usage: &protocol.Usage{InputTokens: 3, OutputTokens: 2}},
		{ToolCalls: []protocol.ToolCall{{
			ID:        "call_1",
			Name:      "lookup",
			Arguments: json.RawMessage(`{"q":"x"}`),
		}}},
	}
	p := mock.New("test", "model", steps...)
	if p.Name() != "test" || p.Model() != "model" {
		t.Fatalf("Name/Model = %q/%q, want test/model", p.Name(), p.Model())
	}
	if got := p.Remaining(); got != 2 {
		t.Fatalf("Remaining() = %d, want 2", got)
	}

	resp1, err := p.Complete(context.Background(), request())
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got := resp1.Message.Text(); got != "first" {
		t.Fatalf("first response text = %q, want %q", got, "first")
	}
	if resp1.FinishReason != protocol.FinishStop {
		t.Fatalf("first finish reason = %q, want %q", resp1.FinishReason, protocol.FinishStop)
	}
	if resp1.Usage == nil || resp1.Usage.TotalTokens != 5 {
		t.Fatalf("first usage = %+v, want total 5", resp1.Usage)
	}
	if got := p.Remaining(); got != 1 {
		t.Fatalf("Remaining() = %d, want 1", got)
	}

	resp2, err := p.Complete(context.Background(), request())
	if err != nil {
		t.Fatalf("second Complete() error: %v", err)
	}
	if got := resp2.Message.Text(); got != "" {
		t.Fatalf("second response text = %q, want empty", got)
	}
	calls := resp2.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "lookup" {
		t.Fatalf("second tool calls = %+v, want one call_1/lookup", calls)
	}
	if string(calls[0].Arguments) != `{"q":"x"}` {
		t.Fatalf("second tool arguments = %s", calls[0].Arguments)
	}
	if resp2.FinishReason != protocol.FinishToolCalls {
		t.Fatalf("second finish reason = %q, want %q", resp2.FinishReason, protocol.FinishToolCalls)
	}
	if got := p.Remaining(); got != 0 {
		t.Fatalf("Remaining() = %d, want 0", got)
	}

	_, err = p.Complete(context.Background(), request())
	if err == nil {
		t.Fatal("Complete() after exhaustion error = nil, want error")
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) || pe.Kind != protocol.ErrProtocol {
		t.Fatalf("exhaustion error = %v, want ErrProtocol ProviderError", err)
	}
	if pe.Message != "mock provider script exhausted" {
		t.Fatalf("exhaustion message = %q", pe.Message)
	}
}

func TestCompleteStepErr(t *testing.T) {
	sentinel := errors.New("boom")
	p := mock.New("", "", mock.Step{Err: sentinel})
	_, err := p.Complete(context.Background(), request())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Complete() error = %v, want sentinel", err)
	}

	p2 := mock.New("", "", mock.Step{Err: sentinel})
	_, err = p2.Stream(context.Background(), request())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Stream() error = %v, want sentinel", err)
	}
}

func TestCompleteClonesScript(t *testing.T) {
	args := json.RawMessage(`{"a":1}`)
	steps := []mock.Step{{
		Text:      "original",
		ToolCalls: []protocol.ToolCall{{ID: "call_1", Name: "lookup", Arguments: args}},
		Usage:     &protocol.Usage{InputTokens: 1, OutputTokens: 1},
	}}
	p := mock.New("", "", steps...)

	steps[0].Text = "mutated"
	steps[0].ToolCalls[0].ID = "mutated_id"
	steps[0].ToolCalls[0].Arguments[0] = 'X'
	steps[0].Usage.InputTokens = 99

	resp, err := p.Complete(context.Background(), request())
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got := resp.Message.Text(); got != "original" {
		t.Fatalf("text = %q, want original", got)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v, want call_1", calls)
	}
	if string(calls[0].Arguments) != `{"a":1}` {
		t.Fatalf("arguments = %s, want original", calls[0].Arguments)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 1 {
		t.Fatalf("usage = %+v, want input 1", resp.Usage)
	}
}

func TestStreamAggregation(t *testing.T) {
	step := mock.Step{
		Text: "Hello, 世界",
		ToolCalls: []protocol.ToolCall{{
			ID:        "call_1",
			Name:      "lookup",
			Arguments: json.RawMessage(`{"q":"x"}`),
		}},
		Usage:     &protocol.Usage{InputTokens: 4, OutputTokens: 6},
		ChunkSize: 3,
	}
	p := mock.New("", "", step)
	s, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	resp := collect(t, s)

	if got := resp.Message.Text(); got != "Hello, 世界" {
		t.Fatalf("text = %q, want %q", got, "Hello, 世界")
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v, want one call_1/lookup", calls)
	}
	if string(calls[0].Arguments) != `{"q":"x"}` {
		t.Fatalf("arguments = %s", calls[0].Arguments)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v, want total 10", resp.Usage)
	}
	if resp.FinishReason != protocol.FinishToolCalls {
		t.Fatalf("finish reason = %q, want %q", resp.FinishReason, protocol.FinishToolCalls)
	}

	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() after done = %v, want io.EOF", err)
	}
	if got := p.Remaining(); got != 0 {
		t.Fatalf("Remaining() = %d, want 0", got)
	}
}

func TestStreamUTF8Chunking(t *testing.T) {
	text := "héllo—世界🌍"
	p := mock.New("", "", mock.Step{Text: text, ChunkSize: 1})
	s, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	b := protocol.NewStreamBuilder()
	deltas := 0
	for {
		ev, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv() error: %v", err)
		}
		if ev.Type == protocol.StreamTextDelta {
			deltas++
			if !utf8.ValidString(ev.Text) {
				t.Fatalf("text delta %q is not valid UTF-8", ev.Text)
			}
		}
		if err := b.Apply(ev); err != nil {
			t.Fatalf("Apply() error: %v", err)
		}
	}

	resp, err := b.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if got := resp.Message.Text(); got != text {
		t.Fatalf("aggregated text = %q, want %q", got, text)
	}
	if want := utf8.RuneCountInString(text); deltas != want {
		t.Fatalf("text deltas = %d, want %d", deltas, want)
	}
}

func TestCancellationComplete(t *testing.T) {
	p := mock.New("", "", mock.Step{Delay: 200 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Complete(ctx, request())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Complete() error = nil, want cancellation error")
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Complete() error = %T, want *protocol.ProviderError", err)
	}
	if pe.Kind != protocol.ErrTimeout && pe.Kind != protocol.ErrCancelled {
		t.Fatalf("kind = %q, want timeout or cancelled", pe.Kind)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("Complete() took %v, want prompt cancellation", elapsed)
	}
}

func TestCancellationStream(t *testing.T) {
	p := mock.New("", "", mock.Step{Delay: 200 * time.Millisecond, Text: "late"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	s, err := p.Stream(ctx, request())
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	start := time.Now()
	_, err = s.Recv()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Recv() error = nil, want cancellation error")
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Recv() error = %T, want *protocol.ProviderError", err)
	}
	if pe.Kind != protocol.ErrTimeout && pe.Kind != protocol.ErrCancelled {
		t.Fatalf("kind = %q, want timeout or cancelled", pe.Kind)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("Recv() took %v, want prompt cancellation", elapsed)
	}
}

func TestCloseProvider(t *testing.T) {
	p := mock.New("", "", mock.Step{Text: "hello"})
	if err := p.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if _, err := p.Complete(context.Background(), request()); err == nil {
		t.Fatal("Complete() after Close error = nil, want error")
	}
	if _, err := p.Stream(context.Background(), request()); err == nil {
		t.Fatal("Stream() after Close error = nil, want error")
	}
}

func TestCloseStream(t *testing.T) {
	p := mock.New("", "", mock.Step{Text: "hello", Delay: 2 * time.Second})
	s, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	start := time.Now()
	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() after Close = %v, want io.EOF", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Recv() after Close took %v, want prompt io.EOF", elapsed)
	}
}
