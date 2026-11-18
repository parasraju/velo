package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"velo/internal/config"
	"velo/internal/events"
	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
	"velo/internal/ui"
)

func tuiMockApp(t *testing.T, dir string, cfg config.Config, steps ...mock.Step) *App {
	t.Helper()
	writeConfig(t, dir, cfg)
	mp := mock.New("mock", "mock-model", steps...)
	reg := provider.NewRegistry()
	if err := reg.Register("mock", func(o provider.Options) (provider.Provider, error) {
		return mp, nil
	}); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	a, err := New(Options{
		Version:   "test",
		Stdin:     strings.NewReader(""),
		Stdout:    io.Discard, // the TUI path writes nothing to stdout
		Stderr:    io.Discard,
		Workspace: dir,
		Registry:  reg,
		LookupEnv: testLookup(nil),
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	// The controller is exercised without going through cobra, so load the
	// configuration the way PersistentPreRunE would.
	if err := a.loadConfig(&cobra.Command{}); err != nil {
		t.Fatalf("load config: %v", err)
	}
	return a
}

func tuiConfig(dir string, enabled []string, mode string) config.Config {
	cfg := testConfig()
	cfg.Tools.Enabled = enabled
	cfg.Agent.Stream = false
	cfg.Security.DefaultMode = mode
	cfg.Sessions.Directory = filepath.Join(dir, ".velo", "sessions")
	return cfg
}

func waitFor(t *testing.T, ch <-chan events.Event, want events.Kind) events.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("event stream closed before %q", want)
			}
			if e.Kind == want {
				return e
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %q", want)
		}
	}
}

func TestTUIControllerReadonlyRun(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := tuiConfig(dir, strings.Split(testTools, ","), "ask")
	a := tuiMockApp(t, dir, cfg,
		mock.Step{Text: "all good", FinishReason: protocol.FinishStop},
	)

	c, err := newTUIController(a)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	defer c.Shutdown()

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), "summarize") }()

	waitFor(t, c.Events(), events.RunStarted)
	waitFor(t, c.Events(), events.TextDelta)
	fin := waitFor(t, c.Events(), events.RunFinished)
	if fin.Run == nil || fin.Run.Status != string("completed") {
		t.Fatalf("finish = %+v", fin)
	}
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestTUIControllerApprovalRoundTrip(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := tuiConfig(dir, []string{"write_file"}, "ask")
	a := tuiMockApp(t, dir, cfg,
		mock.Step{
			ToolCalls: []protocol.ToolCall{{
				ID:        "w1",
				Name:      "write_file",
				Arguments: json.RawMessage(`{"path":"out.txt","content":"from tui"}`),
			}},
			FinishReason: protocol.FinishToolCalls,
		},
		mock.Step{Text: "written", FinishReason: protocol.FinishStop},
	)

	c, err := newTUIController(a)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	defer c.Shutdown()

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), "write a file") }()

	req := waitFor(t, c.Events(), events.ApprovalRequested)
	if req.Tool == nil || req.Tool.Name != "write_file" || !strings.Contains(req.Tool.Arguments, "out.txt") {
		t.Fatalf("approval request = %+v", req)
	}
	c.Decide(ui.Decision{ID: req.Tool.ID, Approve: true})

	res := waitFor(t, c.Events(), events.ApprovalResolved)
	if res.Tool == nil || res.Tool.Status != "approved" {
		t.Fatalf("approval resolved = %+v", res.Tool)
	}
	tf := waitFor(t, c.Events(), events.ToolFinished)
	if tf.Tool == nil || tf.Tool.Status != "succeeded" {
		t.Fatalf("tool finished = %+v", tf.Tool)
	}
	waitFor(t, c.Events(), events.RunFinished)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil || string(data) != "from tui" {
		t.Fatalf("file not written: %q %v", data, err)
	}
}

func TestTUIControllerApprovalDenied(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := tuiConfig(dir, []string{"write_file"}, "ask")
	a := tuiMockApp(t, dir, cfg,
		mock.Step{
			ToolCalls: []protocol.ToolCall{{
				ID:        "w1",
				Name:      "write_file",
				Arguments: json.RawMessage(`{"path":"nope.txt","content":"x"}`),
			}},
			FinishReason: protocol.FinishToolCalls,
		},
		mock.Step{Text: "denied, understood", FinishReason: protocol.FinishStop},
	)

	c, err := newTUIController(a)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	defer c.Shutdown()

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), "try to write") }()

	req := waitFor(t, c.Events(), events.ApprovalRequested)
	c.Decide(ui.Decision{ID: req.Tool.ID, Approve: false})
	res := waitFor(t, c.Events(), events.ApprovalResolved)
	if res.Tool.Status != "denied" {
		t.Fatalf("status = %q", res.Tool.Status)
	}
	waitFor(t, c.Events(), events.RunFinished)
	<-done

	if _, err := os.Stat(filepath.Join(dir, "nope.txt")); err == nil {
		t.Fatal("denied write must not create the file")
	}
}

func TestTUIControllerCancel(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := tuiConfig(dir, strings.Split(testTools, ","), "ask")
	// First step blocks long enough for the test to cancel.
	a := tuiMockApp(t, dir, cfg,
		mock.Step{Text: "hangs", Delay: 10 * time.Second},
	)
	c, err := newTUIController(a)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	defer c.Shutdown()

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), "never finish") }()
	waitFor(t, c.Events(), events.RunStarted)

	c.Cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the run")
	}
	// A terminal event must still be published so the UI leaves the running state.
	waitFor(t, c.Events(), events.RunFinished)
}

func TestTUIControllerSessionsAndTranscript(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := tuiConfig(dir, strings.Split(testTools, ","), "ask")
	a := tuiMockApp(t, dir, cfg,
		mock.Step{Text: "first answer", FinishReason: protocol.FinishStop},
	)
	c, err := newTUIController(a)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	defer c.Shutdown()

	if err := c.Run(context.Background(), "hello there"); err != nil {
		t.Fatalf("run: %v", err)
	}

	rows, err := c.ListSessions()
	if err != nil || len(rows) == 0 {
		t.Fatalf("list sessions: %v rows=%d", err, len(rows))
	}
	if !rows[0].Current || rows[0].Objective != "hello there" {
		t.Errorf("expected current row with objective, got %+v", rows[0])
	}

	msgs, err := c.Transcript()
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	if len(msgs) < 2 || msgs[0].Role != protocol.RoleUser || msgs[0].Text() != "hello there" {
		t.Fatalf("transcript = %+v", msgs)
	}

	// New session must be a different, empty session.
	info, err := c.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if info.SessionID == rows[0].ID {
		t.Fatal("new session reused the id")
	}
	msgs2, err := c.Transcript()
	if err != nil || len(msgs2) != 0 {
		t.Fatalf("fresh transcript = %d (%v)", len(msgs2), err)
	}

	// Resume the first session and confirm the transcript returns.
	if _, err := c.Resume(rows[0].ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	msgs3, err := c.Transcript()
	if err != nil || len(msgs3) == 0 {
		t.Fatalf("resumed transcript = %d (%v)", len(msgs3), err)
	}
}
