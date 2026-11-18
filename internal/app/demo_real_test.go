package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
	"velo/internal/session"
)

func TestPhase2RealRepoDemo(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	tmp := t.TempDir()
	cfg := testConfig()
	cfg.Tools.Enabled = []string{"read_file", "list_files", "search_files", "project_structure"}
	cfg.Agent.Stream = false
	cfg.Sessions.Directory = filepath.Join(tmp, "sessions")
	cfgPath := writeConfig(t, tmp, cfg)

	newApp := func(mp *mock.Provider, stdin string) (*App, *bytes.Buffer) {
		reg := provider.NewRegistry()
		if err := reg.Register("mock", func(o provider.Options) (provider.Provider, error) {
			return mp, nil
		}); err != nil {
			t.Fatalf("register mock: %v", err)
		}
		var stdout, stderr bytes.Buffer
		a, err := New(Options{
			Version:    "demo",
			Stdin:      strings.NewReader(stdin),
			Stdout:     &stdout,
			Stderr:     &stderr,
			Workspace:  repoRoot,
			ConfigPath: cfgPath,
			Registry:   reg,
			LookupEnv:  testLookup(nil),
		})
		if err != nil {
			t.Fatalf("new app: %v", err)
		}
		return a, &stdout
	}

	first := mock.New("mock", "mock-model",
		mock.Step{
			ToolCalls: []protocol.ToolCall{{
				ID:        "call_struct",
				Name:      "project_structure",
				Arguments: json.RawMessage(`{"path":".","max_depth":1}`),
			}},
			FinishReason: protocol.FinishToolCalls,
		},
		mock.Step{
			ToolCalls: []protocol.ToolCall{{
				ID:        "call_gomod",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"go.mod"}`),
			}},
			FinishReason: protocol.FinishToolCalls,
		},
		mock.Step{
			Text:         "VELO is a Go module named velo (go.mod: module velo, go 1.27.3). The repository contains cmd/velo and internal packages including agent, app, config, protocol, provider, repository, security, session, and tools.",
			FinishReason: protocol.FinishStop,
		},
	)
	a, stdout := newApp(first, "")
	if err := a.Execute([]string{"explain this project"}); err != nil {
		t.Fatalf("one-shot: %v", err)
	}
	out := stdout.String()
	t.Logf("ONE-SHOT OUTPUT:\n%s", out)
	for _, want := range []string{"VELO is a Go module named velo", "status: completed", "turns: 3 | tool calls: 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}

	store, err := session.NewStore(filepath.Join(tmp, "sessions"))
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v sessions=%d", err, len(list))
	}
	id := list[0].ID
	sess, err := store.Open(id)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	msgs := sess.Messages()
	if len(msgs) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(msgs))
	}
	if !strings.Contains(msgs[2].Text(), "internal") || !strings.Contains(msgs[2].Text(), "cmd") {
		t.Fatalf("structure tool result not grounded in repo: %q", msgs[2].Text())
	}
	if !strings.Contains(msgs[4].Text(), "module velo") {
		t.Fatalf("go.mod tool result not grounded: %q", msgs[4].Text())
	}

	second := mock.New("mock", "mock-model",
		mock.Step{Text: "Resumed session: VELO is a Go module named velo with agent, app, tools, and provider packages.", FinishReason: protocol.FinishStop},
	)
	b, resumedOut := newApp(second, "")
	if err := b.Execute([]string{"resume", id, "what is this project?"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	t.Logf("RESUME OUTPUT:\n%s", resumedOut.String())
	if !strings.Contains(resumedOut.String(), "resuming session "+id) {
		t.Fatalf("missing resume banner: %q", resumedOut.String())
	}
	if !strings.Contains(resumedOut.String(), "Resumed session: VELO is a Go module") {
		t.Fatalf("missing resumed answer: %q", resumedOut.String())
	}

	reopened, err := store.Open(id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(reopened.Messages()); got != 8 {
		t.Fatalf("expected 8 messages after resume, got %d", got)
	}
	if state := reopened.State(); state.Status != "completed" {
		t.Fatalf("unexpected state after resume: %+v", state)
	}
}
