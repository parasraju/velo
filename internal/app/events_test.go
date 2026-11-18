package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"velo/internal/events"
	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
)

func sinkApp(t *testing.T, dir string, mp *mock.Provider, sink events.Emitter, stdin string) *App {
	t.Helper()
	reg := provider.NewRegistry()
	if err := reg.Register("mock", func(o provider.Options) (provider.Provider, error) {
		return mp, nil
	}); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	var stdout, stderr bytes.Buffer
	a, err := New(Options{
		Version:   "test",
		Stdin:     strings.NewReader(stdin),
		Stdout:    &stdout,
		Stderr:    &stderr,
		Workspace: dir,
		Registry:  reg,
		LookupEnv: testLookup(nil),
		Sink:      sink,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return a
}

func TestOneShotPublishesRunEvents(t *testing.T) {
	dir := sampleWorkspace(t)
	toolConfig(t, dir)

	rec := &events.Recorder{}
	mp := mock.New("mock", "mock-model",
		mock.Step{
			ToolCalls: []protocol.ToolCall{{
				ID:        "call_1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"main.go"}`),
			}},
			FinishReason: protocol.FinishToolCalls,
			Usage:        &protocol.Usage{InputTokens: 20, OutputTokens: 4},
		},
		mock.Step{Text: "The project is a Go program in main.go.", FinishReason: protocol.FinishStop, Usage: &protocol.Usage{InputTokens: 30, OutputTokens: 10}},
	)
	a := sinkApp(t, dir, mp, rec, "")

	if err := a.Execute([]string{"Explain this project"}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	kinds := rec.Kinds()
	counts := map[events.Kind]int{}
	for _, k := range kinds {
		counts[k]++
	}
	for _, want := range []events.Kind{
		events.RunStarted, events.TurnStarted, events.ToolStarted,
		events.ToolFinished, events.UsageUpdated, events.RunFinished,
	} {
		if counts[want] == 0 {
			t.Errorf("missing %q event; kinds=%v", want, kinds)
		}
	}
	if kinds[0] != events.RunStarted {
		t.Errorf("first event = %q, want RunStarted", kinds[0])
	}
	if kinds[len(kinds)-1] != events.RunFinished {
		t.Errorf("last event = %q, want RunFinished", kinds[len(kinds)-1])
	}

	// All events carry the session id and a timestamp.
	for _, e := range rec.Snapshot() {
		if e.SessionID == "" {
			t.Errorf("%q event missing session id", e.Kind)
		}
		if e.Time.IsZero() {
			t.Errorf("%q event missing timestamp", e.Kind)
		}
	}
}

func TestOneShotTextReachesBothSinkAndStdout(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := testConfig()
	cfg.Agent.Stream = true
	cfg.Tools.Enabled = strings.Split(testTools, ",")
	writeConfig(t, dir, cfg)

	rec := &events.Recorder{}
	mp := mock.New("mock", "mock-model",
		mock.Step{Text: "answer text", FinishReason: protocol.FinishStop},
	)
	reg := provider.NewRegistry()
	if err := reg.Register("mock", func(o provider.Options) (provider.Provider, error) {
		return mp, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var stdout bytes.Buffer
	a, err := New(Options{
		Version: "test", Stdout: &stdout, Stderr: &bytes.Buffer{},
		Workspace: dir, Registry: reg, LookupEnv: testLookup(nil), Sink: rec,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Execute([]string{"say something"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout.String(), "answer text") {
		t.Errorf("stdout missing text: %q", stdout.String())
	}
	var streamed strings.Builder
	for _, e := range rec.Snapshot() {
		if e.Kind == events.TextDelta {
			streamed.WriteString(e.Delta)
		}
	}
	if streamed.String() != "answer text" {
		t.Errorf("sink text = %q, want %q", streamed.String(), "answer text")
	}
}
