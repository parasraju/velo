package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func ptrFloat(v float64) *float64 { return &v }

func validAssistantMessage() Message {
	return Message{
		Role: RoleAssistant,
		Content: []Content{
			TextContent("hi"),
			ToolCallContent(ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}),
		},
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	orig := Message{
		Role: RoleAssistant,
		Content: []Content{
			TextContent("hello"),
			ToolCallContent(ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}),
		},
		ProviderMetadata: json.RawMessage(`{"trace":"abc","n":1}`),
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !bytes.Equal(got.ProviderMetadata, orig.ProviderMetadata) {
		t.Fatalf("provider metadata not preserved: got %s want %s", got.ProviderMetadata, orig.ProviderMetadata)
	}
	if got.Text() != "hello" {
		t.Fatalf("text: got %q want %q", got.Text(), "hello")
	}
	calls := got.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: got %d want 1", len(calls))
	}
	if !bytes.Equal(calls[0].Arguments, orig.Content[1].ToolCall.Arguments) {
		t.Fatalf("arguments not preserved: got %s want %s", calls[0].Arguments, orig.Content[1].ToolCall.Arguments)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round tripped message invalid: %v", err)
	}
}

func TestRequestJSONRoundTrip(t *testing.T) {
	orig := Request{
		Model:    "test-model",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextContent("hi")}}},
		Tools: []ToolDefinition{{
			Name:        "lookup",
			Description: "look things up",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		MaxOutputTokens: 128,
		Temperature:     ptrFloat(0.7),
		Stream:          true,
		ProviderOptions: json.RawMessage(`{"vendor":{"flag":true}}`),
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Temperature == nil || *got.Temperature != 0.7 {
		t.Fatalf("temperature not preserved: %v", got.Temperature)
	}
	if !bytes.Equal(got.ProviderOptions, orig.ProviderOptions) {
		t.Fatalf("provider options not preserved: got %s want %s", got.ProviderOptions, orig.ProviderOptions)
	}
	if !bytes.Equal(got.Tools[0].InputSchema, orig.Tools[0].InputSchema) {
		t.Fatalf("input schema not preserved")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round tripped request invalid: %v", err)
	}
}

func TestValidation(t *testing.T) {
	validResp := Response{
		Message:      validAssistantMessage(),
		FinishReason: FinishToolCalls,
		Usage:        &Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
	}

	wrongRole := validResp
	wrongRole.Message.Role = RoleUser

	badArgs := validResp
	badArgs.Message.Content = []Content{
		ToolCallContent(ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{`)}),
	}

	missingID := validResp
	missingID.Message.Content = []Content{
		ToolCallContent(ToolCall{ID: "", Name: "lookup", Arguments: json.RawMessage(`{}`)}),
	}

	duplicateIDs := validResp
	duplicateIDs.Message.Content = []Content{
		ToolCallContent(ToolCall{ID: "call_1", Name: "a", Arguments: json.RawMessage(`{}`)}),
		ToolCallContent(ToolCall{ID: "call_1", Name: "b", Arguments: json.RawMessage(`{}`)}),
	}

	badFinish := validResp
	badFinish.FinishReason = FinishReason("bogus")

	negativeUsage := validResp
	negativeUsage.Usage = &Usage{InputTokens: -1}

	emptyModel := Request{
		Model:    "",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextContent("hi")}}},
	}

	cases := []struct {
		name string
		err  error
	}{
		{"valid response", validResp.Validate()},
		{"wrong role", wrongRole.Validate()},
		{"bad json arguments", badArgs.Validate()},
		{"missing tool call id", missingID.Validate()},
		{"duplicate call ids", duplicateIDs.Validate()},
		{"invalid finish reason", badFinish.Validate()},
		{"negative usage", negativeUsage.Validate()},
		{"empty model request", emptyModel.Validate()},
	}

	for _, tc := range cases {
		wantErr := tc.name != "valid response"
		if wantErr && tc.err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !wantErr && tc.err != nil {
			t.Errorf("%s: expected nil, got %v", tc.name, tc.err)
		}
	}
}

func TestStreamBuilder(t *testing.T) {
	b := NewStreamBuilder()
	events := []StreamEvent{
		{Type: StreamTextDelta, Text: "Hel"},
		{Type: StreamTextDelta, Text: "lo"},
		{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: 1, ID: "call_2", Name: "second"}},
		{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: 0, ID: "call_1", Name: "first"}},
		{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: 1, ArgumentsDelta: `{"b":`}},
		{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: 0, ArgumentsDelta: `{"a":`}},
		{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: 1, ArgumentsDelta: `2}`}},
		{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: 0, ArgumentsDelta: `1}`}},
		{Type: StreamUsage, Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
		{Type: StreamDone, FinishReason: FinishToolCalls},
	}
	for i, ev := range events {
		if err := b.Apply(ev); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}

	resp, err := b.Response()
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	if got := resp.Message.Text(); got != "Hello" {
		t.Fatalf("text: got %q want %q", got, "Hello")
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("calls: got %d want 2", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "first" {
		t.Fatalf("first call: got %+v", calls[0])
	}
	if !bytes.Equal(calls[0].Arguments, json.RawMessage(`{"a":1}`)) {
		t.Fatalf("first args: got %s", calls[0].Arguments)
	}
	if calls[1].ID != "call_2" || calls[1].Name != "second" {
		t.Fatalf("second call: got %+v", calls[1])
	}
	if !bytes.Equal(calls[1].Arguments, json.RawMessage(`{"b":2}`)) {
		t.Fatalf("second args: got %s", calls[1].Arguments)
	}
	if resp.FinishReason != FinishToolCalls {
		t.Fatalf("finish: got %q want %q", resp.FinishReason, FinishToolCalls)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage not normalized: %+v", resp.Usage)
	}
	if err := resp.Validate(); err != nil {
		t.Fatalf("response invalid: %v", err)
	}

	again, err := b.Response()
	if err != nil {
		t.Fatalf("second response: %v", err)
	}
	if again.Message.Text() != "Hello" || len(again.Message.ToolCalls()) != 2 {
		t.Fatalf("second response not rebuilt correctly")
	}

	empty := NewStreamBuilder()
	if _, err = empty.Response(); !errors.Is(err, ErrStreamIncomplete) {
		t.Fatalf("empty response: %v, want ErrStreamIncomplete", err)
	}

	partial := NewStreamBuilder()
	if err := partial.Apply(StreamEvent{Type: StreamTextDelta, Text: "partial"}); err != nil {
		t.Fatalf("apply partial: %v", err)
	}
	if _, err = partial.Response(); !errors.Is(err, ErrStreamIncomplete) {
		t.Fatalf("partial response: %v, want ErrStreamIncomplete", err)
	}

	inferred := NewStreamBuilder()
	if err := inferred.Apply(StreamEvent{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{
		Index: 0, ID: "call_1", Name: "lookup", ArgumentsDelta: `{}`,
	}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := inferred.Apply(StreamEvent{Type: StreamDone}); err != nil {
		t.Fatalf("apply done: %v", err)
	}
	resp, err = inferred.Response()
	if err != nil {
		t.Fatalf("inferred response: %v", err)
	}
	if resp.FinishReason != FinishToolCalls {
		t.Fatalf("inferred finish: got %q want %q", resp.FinishReason, FinishToolCalls)
	}

	defaulted := NewStreamBuilder()
	if err := defaulted.Apply(StreamEvent{Type: StreamDone}); err != nil {
		t.Fatalf("apply done: %v", err)
	}
	resp, err = defaulted.Response()
	if err != nil {
		t.Fatalf("defaulted response: %v", err)
	}
	if resp.FinishReason != FinishStop {
		t.Fatalf("defaulted finish: got %q want %q", resp.FinishReason, FinishStop)
	}

	if err := b.Apply(StreamEvent{Type: StreamTextDelta, Text: "late"}); err == nil {
		t.Fatal("expected error applying event after done")
	}
}

func TestStreamBuilderErrorEvent(t *testing.T) {
	b := NewStreamBuilder()
	err := b.Apply(StreamEvent{Type: StreamError, Error: NewProviderError(ErrServer, "test", "boom")})
	if err == nil {
		t.Fatal("expected apply error")
	}
	if _, respErr := b.Response(); respErr == nil {
		t.Fatal("expected response error")
	}

	empty := NewStreamBuilder()
	if err := empty.Apply(StreamEvent{Type: StreamError}); err == nil {
		t.Fatal("expected apply error for nil error event")
	}
	if _, respErr := empty.Response(); respErr == nil {
		t.Fatal("expected response error after nil error event")
	}
}

func TestErrorFromStatus(t *testing.T) {
	cases := []struct {
		status    int
		kind      ErrorKind
		retryable bool
	}{
		{401, ErrAuthentication, false},
		{403, ErrPermission, false},
		{404, ErrNotFound, false},
		{408, ErrTimeout, true},
		{429, ErrRateLimit, true},
		{500, ErrServer, true},
		{400, ErrInvalidRequest, false},
	}
	for _, tc := range cases {
		err := ErrorFromStatus("test", tc.status, "message", 3*time.Second)
		if err.Kind != tc.kind {
			t.Errorf("status %d: kind got %q want %q", tc.status, err.Kind, tc.kind)
		}
		if err.Retryable != tc.retryable {
			t.Errorf("status %d: retryable got %v want %v", tc.status, err.Retryable, tc.retryable)
		}
		if err.RetryAfter != 3*time.Second {
			t.Errorf("status %d: retry after got %v", tc.status, err.RetryAfter)
		}
		if IsRetryable(err) != tc.retryable {
			t.Errorf("status %d: IsRetryable got %v want %v", tc.status, IsRetryable(err), tc.retryable)
		}
	}
}

func TestErrorFromContextAndUnwrap(t *testing.T) {
	cancelled := ErrorFromContext("test", context.Canceled)
	if cancelled.Kind != ErrCancelled || cancelled.Retryable {
		t.Fatalf("cancelled: %+v", cancelled)
	}
	if !errors.Is(cancelled, context.Canceled) {
		t.Fatal("cancelled error does not unwrap to context.Canceled")
	}
	if cancelled.Unwrap() != context.Canceled {
		t.Fatal("cancelled Unwrap mismatch")
	}

	deadline := ErrorFromContext("test", context.DeadlineExceeded)
	if deadline.Kind != ErrTimeout || !deadline.Retryable {
		t.Fatalf("deadline: %+v", deadline)
	}
	if !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatal("deadline error does not unwrap to context.DeadlineExceeded")
	}

	networkCause := errors.New("connection reset")
	network := ErrorFromContext("test", networkCause)
	if network.Kind != ErrNetwork || !network.Retryable {
		t.Fatalf("network: %+v", network)
	}
	if !errors.Is(network, networkCause) {
		t.Fatal("network error does not unwrap to cause")
	}

	wrapped := fmt.Errorf("outer: %w", deadline)
	if !IsRetryable(wrapped) {
		t.Fatal("IsRetryable failed through wrapping")
	}
	if IsRetryable(errors.New("plain")) {
		t.Fatal("plain error should not be retryable")
	}

	msg := network.Error()
	for _, want := range []string{string(ErrNetwork), "test", "connection reset"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Fatalf("error string %q missing %q", msg, want)
		}
	}
}

func TestStreamEventValidation(t *testing.T) {
	cases := []struct {
		name    string
		event   StreamEvent
		wantErr bool
	}{
		{"valid text", StreamEvent{Type: StreamTextDelta, Text: "x"}, false},
		{"empty text", StreamEvent{Type: StreamTextDelta}, true},
		{"tool delta missing call", StreamEvent{Type: StreamToolCallDelta}, true},
		{"tool delta negative index", StreamEvent{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: -1}}, true},
		{"valid tool delta", StreamEvent{Type: StreamToolCallDelta, ToolCall: &ToolCallDelta{Index: 0}}, false},
		{"usage missing usage", StreamEvent{Type: StreamUsage}, true},
		{"valid usage", StreamEvent{Type: StreamUsage, Usage: &Usage{InputTokens: 1}}, false},
		{"error missing error", StreamEvent{Type: StreamError}, true},
		{"valid error", StreamEvent{Type: StreamError, Error: NewProviderError(ErrTimeout, "test", "slow")}, false},
		{"valid done", StreamEvent{Type: StreamDone}, false},
		{"bad finish", StreamEvent{Type: StreamDone, FinishReason: FinishReason("bogus")}, true},
		{"bad type", StreamEvent{Type: StreamEventType("bogus")}, true},
	}
	for _, tc := range cases {
		err := tc.event.Validate()
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: expected nil, got %v", tc.name, err)
		}
	}
}
