package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"velo/internal/protocol"
	"velo/internal/provider"
)

func testProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p, err := New(provider.Options{
		Model:      "test-model",
		BaseURL:    server.URL,
		APIKey:     "test-secret",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	return p
}

func request() protocol.Request {
	return protocol.Request{
		Model: "test-model",
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("hi")}},
		},
		Tools: []protocol.ToolDefinition{
			{
				Name:        "echo",
				Description: "Echo text",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
			},
		},
	}
}

func TestCompleteParsesResponse(t *testing.T) {
	var sawAuth bool
	var sawToolSchema bool
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer test-secret" {
			sawAuth = true
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["stream"] != false {
			t.Errorf("stream = %v", body["stream"])
		}
		if tools, ok := body["tools"].([]any); ok && len(tools) == 1 {
			fn := tools[0].(map[string]any)["function"].(map[string]any)
			_, hasArguments := fn["arguments"]
			_, hasParameters := fn["parameters"]
			sawToolSchema = !hasArguments && hasParameters && fn["name"] == "echo"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":2}}}`)
	})
	resp, err := p.Complete(context.Background(), request())
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !sawAuth {
		t.Error("authorization header not sent")
	}
	if !sawToolSchema {
		t.Error("tool schema is not encoded correctly")
	}
	if got := resp.Message.Text(); got != "hello" {
		t.Errorf("text = %q", got)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "echo" || string(calls[0].Arguments) != `{"text":"hi"}` {
		t.Errorf("tool calls = %+v", calls)
	}
	if resp.FinishReason != protocol.FinishToolCalls {
		t.Errorf("finish reason = %q", resp.FinishReason)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 13 || resp.Usage.CachedInputTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestStreamParsesDeltas(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["stream"] != true {
			t.Errorf("stream = %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		lines := []string{
			`data: {"choices":[{"delta":{"role":"assistant","content":null}}]}`,
			"",
			`data: {"choices":[{"delta":{"content":"hel"}}]}`,
			"",
			`data: {"choices":[{"delta":{"content":"lo"}}]}`,
			"",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"text\":"}}]}}]}`,
			"",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hi\"}"}}]}}]}`,
			"",
			`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
			"",
			`data: {"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`,
			"",
			"data: [DONE]",
			"",
		}
		for _, line := range lines {
			fmt.Fprint(w, line+"\n")
			if line == "" && flusher != nil {
				flusher.Flush()
			}
		}
	})

	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()

	builder := protocol.NewStreamBuilder()
	var textDeltas, toolDeltas, usageEvents, doneEvents int
	var last protocol.StreamEvent
	for {
		event, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		switch event.Type {
		case protocol.StreamTextDelta:
			textDeltas++
		case protocol.StreamToolCallDelta:
			toolDeltas++
		case protocol.StreamUsage:
			usageEvents++
		case protocol.StreamDone:
			doneEvents++
		}
		last = event
		if err := builder.Apply(event); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	if textDeltas != 2 || toolDeltas != 2 || usageEvents != 1 || doneEvents != 1 {
		t.Fatalf("events: text=%d tool=%d usage=%d done=%d", textDeltas, toolDeltas, usageEvents, doneEvents)
	}
	if last.Type != protocol.StreamDone || last.FinishReason != protocol.FinishToolCalls {
		t.Errorf("last event = %+v", last)
	}
	if _, err := st.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("recv after done = %v", err)
	}
	resp, err := builder.Response()
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	if got := resp.Message.Text(); got != "hello" {
		t.Errorf("text = %q", got)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || string(calls[0].Arguments) != `{"text":"hi"}` {
		t.Errorf("calls = %+v", calls)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 11 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.FinishReason != protocol.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
}

func TestStreamDefaultsFinishReason(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()
	var done protocol.StreamEvent
	for {
		event, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if event.Type == protocol.StreamDone {
			done = event
		}
	}
	if done.Type != protocol.StreamDone || done.FinishReason != protocol.FinishStop {
		t.Fatalf("done = %+v", done)
	}
}

func TestStreamToolCallsWithoutFinishReason(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"echo\",\"arguments\":\"{}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()
	for {
		event, err := st.Recv()
		if errors.Is(err, io.EOF) {
			t.Fatal("done event not received")
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if event.Type == protocol.StreamDone {
			if event.FinishReason != protocol.FinishToolCalls {
				t.Fatalf("finish = %q", event.FinishReason)
			}
			return
		}
	}
}

func TestStreamCloseReturnsEOF(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := st.Recv(); err != nil {
		t.Fatalf("recv: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := st.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("recv after close = %v", err)
	}
}

func TestCompleteErrorNoRetry(t *testing.T) {
	var count atomic.Int32
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad request","type":"invalid_request_error"}}`)
	})

	_, err := p.Complete(context.Background(), request())
	if err == nil {
		t.Fatal("expected error")
	}
	if got := count.Load(); got != 1 {
		t.Errorf("requests = %d", got)
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) || pe.Kind != protocol.ErrInvalidRequest {
		t.Errorf("error = %v", err)
	}
	if strings.Contains(err.Error(), "test-secret") {
		t.Error("error leaked api key")
	}
}

func TestCompleteRetriesServerError(t *testing.T) {
	var count atomic.Int32
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"temporary","type":"server_error"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})
	start := time.Now()
	resp, err := p.Complete(context.Background(), request())
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Text() != "recovered" {
		t.Errorf("text = %q", resp.Message.Text())
	}
	if got := count.Load(); got != 2 {
		t.Errorf("requests = %d", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("retry took too long: %s", elapsed)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d, ok := parseRetryAfter("2"); !ok || d != 2*time.Second {
		t.Errorf("seconds: %v %v", d, ok)
	}
	if d, ok := parseRetryAfter("-5"); !ok || d != 0 {
		t.Errorf("negative: %v %v", d, ok)
	}
	if d, ok := parseRetryAfter(""); ok || d != 0 {
		t.Errorf("empty: %v %v", d, ok)
	}
	if _, ok := parseRetryAfter("not-a-date"); ok {
		t.Error("invalid value accepted")
	}
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	d, ok := parseRetryAfter(future)
	if !ok || d <= 0 || d > 4*time.Second {
		t.Errorf("http date: %v %v", d, ok)
	}
}

func TestStreamMalformedJSON(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\": [broken\n\n")
	})
	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()
	_, err = st.Recv()
	if err == nil || !strings.Contains(err.Error(), "invalid stream json") {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamOversizedLine(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: "+strings.Repeat("a", maxSSELineBytes)+"\n\n")
	})
	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()
	_, err = st.Recv()
	if err == nil || !strings.Contains(err.Error(), "line exceeded") {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamOversizedBody(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		line := ":" + strings.Repeat("a", maxSSELineBytes-2) + "\n"
		for i := 0; i < 9; i++ {
			if _, err := io.WriteString(w, line); err != nil {
				return
			}
		}
	})
	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()
	_, err = st.Recv()
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamCancellation(t *testing.T) {
	blocked := make(chan struct{})
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-blocked:
		}
	})
	defer close(blocked)

	ctx, cancel := context.WithCancel(context.Background())
	st, err := p.Stream(ctx, request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()
	if _, err := st.Recv(); err != nil {
		t.Fatalf("recv: %v", err)
	}
	cancel()

	result := make(chan error, 1)
	go func() {
		_, err := st.Recv()
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		var pe *protocol.ProviderError
		if !errors.As(err, &pe) || pe.Kind != protocol.ErrCancelled {
			t.Errorf("error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recv did not stop after cancellation")
	}
}

func TestStreamPrematureEOFIsIncomplete(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	})
	st, err := p.Stream(context.Background(), request())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer st.Close()

	var sawText, sawDone bool
	var recvErr error
	for {
		event, err := st.Recv()
		if err != nil {
			recvErr = err
			break
		}
		switch event.Type {
		case protocol.StreamTextDelta:
			sawText = true
		case protocol.StreamDone:
			sawDone = true
		}
	}
	if !sawText {
		t.Fatal("content delta was not delivered")
	}
	if sawDone {
		t.Fatal("premature EOF synthesized a done event")
	}
	if !errors.Is(recvErr, protocol.ErrStreamIncomplete) {
		t.Fatalf("recv error = %v, want ErrStreamIncomplete", recvErr)
	}
}
