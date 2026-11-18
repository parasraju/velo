package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
	"velo/internal/session"
	"velo/internal/tools"
)

type testTool struct {
	def tools.Definition
	run func(ctx context.Context, call tools.Call) (tools.Result, error)
}

func (t testTool) Definition() tools.Definition {
	return t.def
}

func (t testTool) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	return t.run(ctx, call)
}

func echoTool() tools.Tool {
	return testTool{
		def: tools.Definition{
			Name:        "echo",
			Description: "Echo the provided text",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			Category:    tools.CategoryRead,
			ReadOnly:    true,
		},
		run: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return tools.Result{CallID: call.ID, Content: "invalid arguments", IsError: true}, err
			}
			return tools.Result{CallID: call.ID, Content: "echo:" + args.Text}, nil
		},
	}
}

func writeEchoTool() tools.Tool {
	return testTool{
		def: tools.Definition{
			Name:        "write_echo",
			Description: "Echo text as a mutating tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			Category:    tools.CategoryWrite,
		},
		run: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return tools.Result{CallID: call.ID, Content: "invalid arguments", IsError: true}, err
			}
			return tools.Result{CallID: call.ID, Content: "wrote:" + args.Text}, nil
		},
	}
}

func failingTool() tools.Tool {
	return testTool{
		def: tools.Definition{
			Name:        "fail_tool",
			Description: "Always fails",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Category:    tools.CategoryRead,
			ReadOnly:    true,
		},
		run: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			return tools.Result{CallID: call.ID, Content: "tool failure", IsError: true}, errors.New("tool failure")
		},
	}
}

func newTestAgent(t *testing.T, p provider.Provider, registry *tools.Registry, approver tools.Approver, mutate func(*Options)) (*Agent, *session.Session) {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	sess, err := store.Create("session-1", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	dispatcher, err := tools.NewDispatcher(tools.DispatcherOptions{
		Registry:       registry,
		Approver:       approver,
		DefaultTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	opts := Options{
		Provider:  p,
		Tools:     dispatcher,
		Session:   sess,
		Workspace: sess.Workspace(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	a, err := New(opts)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	return a, sess
}

func newRegistry(t *testing.T, registryTools ...tools.Tool) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	for _, tool := range registryTools {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register tool: %v", err)
		}
	}
	return registry
}

func toolCall(id, name, arguments string) protocol.ToolCall {
	return protocol.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}
}

func eventsOfKind(sess *session.Session, kind string) []session.Event {
	var out []session.Event
	for _, event := range sess.Events() {
		if event.Kind == kind {
			out = append(out, event)
		}
	}
	return out
}

func TestRunPlainCompletion(t *testing.T) {
	p := mock.New("mock", "mock-model", mock.Step{
		Text:  "hello",
		Usage: &protocol.Usage{InputTokens: 3, OutputTokens: 2},
	})
	a, sess := newTestAgent(t, p, newRegistry(t), nil, nil)

	result, err := a.Run(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != StateCompleted {
		t.Errorf("status = %q", result.Status)
	}
	if result.Content != "hello" {
		t.Errorf("content = %q", result.Content)
	}
	if result.Turns != 1 || result.ToolCalls != 0 {
		t.Errorf("turns=%d toolcalls=%d", result.Turns, result.ToolCalls)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", result.Usage)
	}
	if result.SessionID != sess.ID() {
		t.Errorf("session id = %q", result.SessionID)
	}

	messages := sess.Messages()
	if len(messages) != 2 {
		t.Fatalf("messages = %d", len(messages))
	}
	if messages[0].Role != protocol.RoleUser || messages[0].Text() != "say hello" {
		t.Errorf("user message = %+v", messages[0])
	}
	if messages[1].Role != protocol.RoleAssistant || messages[1].Text() != "hello" {
		t.Errorf("assistant message = %+v", messages[1])
	}
	state := sess.State()
	if state.Status != string(StateCompleted) || state.Turns != 1 || state.Objective != "say hello" {
		t.Errorf("state = %+v", state)
	}
	if len(eventsOfKind(sess, "verification")) != 1 {
		t.Error("verification event missing")
	}
	if len(eventsOfKind(sess, "model.response")) != 1 {
		t.Error("model response event missing")
	}
}

func TestRunToolCallThenCompletion(t *testing.T) {
	p := mock.New("mock", "mock-model",
		mock.Step{
			Text:      "working",
			ToolCalls: []protocol.ToolCall{toolCall("call_1", "echo", `{"text":"hi"}`)},
		},
		mock.Step{Text: "done"},
	)
	a, sess := newTestAgent(t, p, newRegistry(t, echoTool()), nil, nil)

	result, err := a.Run(context.Background(), "use the tool")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != StateCompleted || result.Content != "done" {
		t.Fatalf("result = %+v", result)
	}
	if result.Turns != 2 || result.ToolCalls != 1 {
		t.Errorf("turns=%d toolcalls=%d", result.Turns, result.ToolCalls)
	}

	messages := sess.Messages()
	if len(messages) != 4 {
		t.Fatalf("messages = %d", len(messages))
	}
	if messages[0].Role != protocol.RoleUser {
		t.Errorf("message 0 role = %q", messages[0].Role)
	}
	if messages[1].Role != protocol.RoleAssistant || len(messages[1].ToolCalls()) != 1 {
		t.Errorf("message 1 = %+v", messages[1])
	}
	if messages[2].Role != protocol.RoleTool || messages[2].ToolCallID != "call_1" || messages[2].Name != "echo" {
		t.Errorf("message 2 = %+v", messages[2])
	}
	if messages[2].Text() != "echo:hi" {
		t.Errorf("tool content = %q", messages[2].Text())
	}
	if messages[3].Role != protocol.RoleAssistant || messages[3].Text() != "done" {
		t.Errorf("message 3 = %+v", messages[3])
	}

	starts := eventsOfKind(sess, "tool.start")
	results := eventsOfKind(sess, "tool.result")
	if len(starts) != 1 || len(results) != 1 {
		t.Fatalf("tool events: starts=%d results=%d", len(starts), len(results))
	}
	if starts[0].Data["call_id"] != "call_1" || starts[0].Data["name"] != "echo" {
		t.Errorf("tool start = %+v", starts[0].Data)
	}
	if results[0].Data["call_id"] != "call_1" {
		t.Errorf("tool result = %+v", results[0].Data)
	}
	if _, ok := results[0].Data["duration_ms"]; !ok {
		t.Error("tool result missing duration")
	}
}

func TestRunToolErrorRecovery(t *testing.T) {
	p := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{toolCall("call_1", "fail_tool", `{}`)}},
		mock.Step{Text: "recovered"},
	)
	a, sess := newTestAgent(t, p, newRegistry(t, failingTool()), nil, nil)

	result, err := a.Run(context.Background(), "try the failing tool")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != StateCompleted || result.Content != "recovered" {
		t.Fatalf("result = %+v", result)
	}
	messages := sess.Messages()
	if len(messages) != 4 {
		t.Fatalf("messages = %d", len(messages))
	}
	toolMessage := messages[2]
	if toolMessage.Role != protocol.RoleTool || toolMessage.ToolCallID != "call_1" {
		t.Errorf("tool message = %+v", toolMessage)
	}
	if !strings.Contains(toolMessage.Text(), "tool failure") {
		t.Errorf("tool content = %q", toolMessage.Text())
	}
	results := eventsOfKind(sess, "tool.result")
	if len(results) != 1 || results[0].Data["error_category"] != "execution_error" {
		t.Errorf("tool results = %+v", results)
	}
}

func TestRunToolDenied(t *testing.T) {
	approver := func(ctx context.Context, def tools.Definition, call tools.Call) error {
		return errors.New("denied by policy")
	}
	p := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{toolCall("call_1", "write_echo", `{"text":"hi"}`)}},
		mock.Step{Text: "denied"},
	)
	a, sess := newTestAgent(t, p, newRegistry(t, writeEchoTool()), approver, nil)

	result, err := a.Run(context.Background(), "attempt denied call")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != StateCompleted {
		t.Fatalf("result = %+v", result)
	}
	messages := sess.Messages()
	if len(messages) != 4 {
		t.Fatalf("messages = %d", len(messages))
	}
	if !strings.Contains(messages[2].Text(), "denied by policy") {
		t.Errorf("tool content = %q", messages[2].Text())
	}
	results := eventsOfKind(sess, "tool.result")
	if len(results) != 1 || results[0].Data["error_category"] != "permission_denied" {
		t.Errorf("tool results = %+v", results)
	}
}

func TestRunMaxTurns(t *testing.T) {
	p := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{toolCall("call_1", "echo", `{"text":"hi"}`)}},
	)
	a, sess := newTestAgent(t, p, newRegistry(t, echoTool()), nil, func(opts *Options) {
		opts.MaxTurns = 1
	})

	result, err := a.Run(context.Background(), "loop")
	if err == nil || !strings.Contains(err.Error(), "maximum turns") {
		t.Fatalf("error = %v", err)
	}
	if result.Status != StateFailed {
		t.Errorf("status = %q", result.Status)
	}
	if sess.State().Status != string(StateFailed) {
		t.Errorf("session state = %q", sess.State().Status)
	}
}

func TestRunProviderError(t *testing.T) {
	providerErr := protocol.NewProviderError(protocol.ErrServer, "mock", "server exploded")
	p := mock.New("mock", "mock-model", mock.Step{Err: providerErr})
	a, sess := newTestAgent(t, p, newRegistry(t), nil, nil)

	result, err := a.Run(context.Background(), "fail")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) || pe.Kind != protocol.ErrServer {
		t.Errorf("error = %v", err)
	}
	if result.Status != StateFailed || sess.State().Status != string(StateFailed) {
		t.Errorf("status = %q state = %q", result.Status, sess.State().Status)
	}
}

func TestRunCancellation(t *testing.T) {
	p := mock.New("mock", "mock-model", mock.Step{Err: protocol.ErrorFromContext("mock", context.Canceled)})
	a, sess := newTestAgent(t, p, newRegistry(t), nil, nil)

	result, err := a.Run(context.Background(), "cancel me")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if result.Status != StateCancelled || sess.State().Status != string(StateCancelled) {
		t.Errorf("status = %q state = %q", result.Status, sess.State().Status)
	}
}

func TestRunEmptyResponse(t *testing.T) {
	p := mock.New("mock", "mock-model", mock.Step{})
	a, sess := newTestAgent(t, p, newRegistry(t), nil, nil)

	result, err := a.Run(context.Background(), "empty")
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("error = %v", err)
	}
	if result.Status != StateFailed || sess.State().Status != string(StateFailed) {
		t.Errorf("status = %q state = %q", result.Status, sess.State().Status)
	}
}

func TestRunStreaming(t *testing.T) {
	p := mock.New("mock", "mock-model",
		mock.Step{
			Text:      "hel",
			ChunkSize: 1,
			ToolCalls: []protocol.ToolCall{toolCall("call_1", "echo", `{"text":"hi"}`)},
			Usage:     &protocol.Usage{InputTokens: 2, OutputTokens: 1},
		},
		mock.Step{
			Text:      "lo",
			ChunkSize: 1,
			Usage:     &protocol.Usage{InputTokens: 1, OutputTokens: 1},
		},
	)
	var output bytes.Buffer
	a, sess := newTestAgent(t, p, newRegistry(t, echoTool()), nil, func(opts *Options) {
		opts.Stream = true
		opts.Output = &output
	})

	result, err := a.Run(context.Background(), "stream")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != StateCompleted || result.Content != "lo" {
		t.Fatalf("result = %+v", result)
	}
	if result.Turns != 2 || result.ToolCalls != 1 {
		t.Errorf("turns=%d toolcalls=%d", result.Turns, result.ToolCalls)
	}
	if output.String() != "hello" {
		t.Errorf("output = %q", output.String())
	}
	if result.Usage == nil || result.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", result.Usage)
	}
	messages := sess.Messages()
	if len(messages) != 4 {
		t.Fatalf("messages = %d", len(messages))
	}
	if messages[2].Role != protocol.RoleTool || messages[2].ToolCallID != "call_1" || messages[2].Text() != "echo:hi" {
		t.Errorf("tool message = %+v", messages[2])
	}
}

func TestMachineTransitions(t *testing.T) {
	machine := NewMachine()
	if machine.State() != StateCreated {
		t.Fatalf("initial state = %q", machine.State())
	}
	if err := machine.Transition(StateCompleted); err == nil {
		t.Error("created -> completed accepted")
	}
	if CanTransition(StateCreated, StateCompleted) {
		t.Error("CanTransition accepted created -> completed")
	}
	if CanTransition(StateCompleted, StateInterrupted) {
		t.Error("terminal state can be interrupted")
	}
	if !CanTransition(StateInterrupted, StateRecoveryRequired) {
		t.Error("interrupted -> recovery_required rejected")
	}
	if !CanTransition(StateProcessingResults, StateContextBuilding) {
		t.Error("processing_results -> context_building rejected")
	}

	sequence := []State{
		StateContextBuilding,
		StateRequestingModel,
		StateProcessingResults,
		StateVerifying,
		StateCompleted,
	}
	for _, state := range sequence {
		if err := machine.Transition(state); err != nil {
			t.Fatalf("transition to %q: %v", state, err)
		}
	}
	if err := machine.Transition(StateFailed); err == nil {
		t.Error("completed -> failed accepted")
	}
	if !StateCompleted.Terminal() || !StateFailed.Terminal() || !StateCancelled.Terminal() {
		t.Error("terminal classification is wrong")
	}
	if StateInterrupted.Terminal() || StateRecoveryRequired.Terminal() {
		t.Error("recoverable state classified as terminal")
	}
	if State("bogus").Valid() {
		t.Error("invalid state accepted")
	}
}

func TestNewValidatesOptions(t *testing.T) {
	sess := newSession(t)
	registry := newRegistry(t, echoTool())
	dispatcher, err := tools.NewDispatcher(tools.DispatcherOptions{Registry: registry})
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	p := mock.New("mock", "mock-model", mock.Step{Text: "hello"})

	if _, err := New(Options{Tools: dispatcher, Session: sess}); err == nil {
		t.Error("missing provider accepted")
	}
	if _, err := New(Options{Provider: p, Session: sess}); err == nil {
		t.Error("missing dispatcher accepted")
	}
	if _, err := New(Options{Provider: p, Tools: dispatcher}); err == nil {
		t.Error("missing session accepted")
	}
	a, err := New(Options{Provider: p, Tools: dispatcher, Session: sess})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	if a.maxTurns != defaultMaxTurns || a.maxToolCallsPerTurn != defaultMaxToolCallsPerTurn {
		t.Errorf("defaults = %d %d", a.maxTurns, a.maxToolCallsPerTurn)
	}
	if a.requestTimeout != defaultRequestTimeout || a.totalTimeout != defaultTotalTimeout {
		t.Errorf("timeouts = %s %s", a.requestTimeout, a.totalTimeout)
	}

	emptyObjective, err := a.Run(context.Background(), "  ")
	if err == nil {
		t.Fatal("empty objective accepted")
	}
	if emptyObjective.Status != StateFailed {
		t.Errorf("status = %q", emptyObjective.Status)
	}
}

func newSession(t *testing.T) *session.Session {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	sess, err := store.Create("session-1", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}
