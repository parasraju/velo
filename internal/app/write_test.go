package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
	"velo/internal/tools"
)

func scriptedWriteApp(t *testing.T, dir string, mp *mock.Provider, approver tools.Approver) (*App, *bytes.Buffer, *bytes.Buffer) {
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
		Stdin:     strings.NewReader(""),
		Stdout:    &stdout,
		Stderr:    &stderr,
		Workspace: dir,
		Registry:  reg,
		LookupEnv: testLookup(nil),
		Approver:  approver,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return a, &stdout, &stderr
}

func writeToolConfig(t *testing.T, dir string, mode string) {
	t.Helper()
	cfg := testConfig()
	cfg.Tools.Enabled = []string{"read_file", "write_file"}
	cfg.Agent.Stream = false
	cfg.Security.DefaultMode = mode
	writeConfig(t, dir, cfg)
}

func writeStep(path, content string) protocol.ToolCall {
	return protocol.ToolCall{
		ID:        "call_write",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"path":"` + path + `","content":"` + content + `"}`),
	}
}

func TestOneShotWriteWithApproval(t *testing.T) {
	dir := sampleWorkspace(t)
	writeToolConfig(t, dir, "ask")
	approved := 0
	approver := func(ctx context.Context, def tools.Definition, call tools.Call) error {
		approved++
		if def.Name != "write_file" {
			t.Fatalf("unexpected approval for %q", def.Name)
		}
		return nil
	}
	mp := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{writeStep("out.txt", "hello\\n")}, FinishReason: protocol.FinishToolCalls},
		mock.Step{Text: "wrote the file", FinishReason: protocol.FinishStop},
	)
	a, stdout, _ := scriptedWriteApp(t, dir, mp, approver)
	if err := a.Execute([]string{"create out.txt"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if approved != 1 {
		t.Fatalf("expected one approval, got %d", approved)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("unexpected content: %q", data)
	}
	if !strings.Contains(stdout.String(), "verification: unverified") {
		t.Fatalf("missing verification line: %q", stdout.String())
	}

	store := sessionStoreFor(t, dir)
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	sess, err := store.Open(list[0].ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var resultStatus string
	for _, event := range sess.Events() {
		if event.Kind == "tool.result" {
			resultStatus, _ = event.Data["status"].(string)
		}
	}
	if resultStatus != "succeeded" {
		t.Fatalf("expected succeeded tool result, got %q", resultStatus)
	}
}

func TestOneShotWriteDeniedByApprover(t *testing.T) {
	dir := sampleWorkspace(t)
	writeToolConfig(t, dir, "ask")
	approver := func(ctx context.Context, def tools.Definition, call tools.Call) error {
		return errors.New("operator said no")
	}
	mp := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{writeStep("out.txt", "hello")}, FinishReason: protocol.FinishToolCalls},
		mock.Step{Text: "the write was denied", FinishReason: protocol.FinishStop},
	)
	a, _, _ := scriptedWriteApp(t, dir, mp, approver)
	if err := a.Execute([]string{"create out.txt"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write created a file: %v", err)
	}
}

func TestNonInteractiveApprovalDenied(t *testing.T) {
	dir := sampleWorkspace(t)
	writeToolConfig(t, dir, "ask")
	mp := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{writeStep("out.txt", "hello")}, FinishReason: protocol.FinishToolCalls},
		mock.Step{Text: "denied", FinishReason: protocol.FinishStop},
	)
	a, _, _ := scriptedWriteApp(t, dir, mp, nil)
	if err := a.Execute([]string{"create out.txt"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("non-interactive approval created a file: %v", err)
	}
}

func TestAutoModeAllowsWrite(t *testing.T) {
	dir := sampleWorkspace(t)
	writeToolConfig(t, dir, "auto")
	mp := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{writeStep("auto.txt", "hello")}, FinishReason: protocol.FinishToolCalls},
		mock.Step{Text: "done", FinishReason: protocol.FinishStop},
	)
	a, _, _ := scriptedWriteApp(t, dir, mp, nil)
	if err := a.Execute([]string{"create auto.txt"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auto.txt")); err != nil {
		t.Fatalf("auto mode did not write: %v", err)
	}
}

func TestDenyModeDeniesWrite(t *testing.T) {
	dir := sampleWorkspace(t)
	writeToolConfig(t, dir, "deny")
	mp := mock.New("mock", "mock-model",
		mock.Step{ToolCalls: []protocol.ToolCall{writeStep("deny.txt", "hello")}, FinishReason: protocol.FinishToolCalls},
		mock.Step{Text: "done", FinishReason: protocol.FinishStop},
	)
	a, _, _ := scriptedWriteApp(t, dir, mp, nil)
	if err := a.Execute([]string{"create deny.txt"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deny.txt")); !os.IsNotExist(err) {
		t.Fatalf("deny mode created a file: %v", err)
	}
}

func TestToolsCommandShowsCapabilities(t *testing.T) {
	dir := t.TempDir()
	stdout, _, err := run(t, dir, nil, "tools")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, want := range []string{"write_file (write)", "read_file (read)", "run_command (exec)"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in tools output, got %q", want, stdout)
		}
	}
}
