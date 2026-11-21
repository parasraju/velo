package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"velo/internal/config"
	"velo/internal/protocol"
	"velo/internal/provider"
	"velo/internal/provider/mock"
	"velo/internal/session"
)

const testTools = "read_file,list_files,search_files,project_structure"

func TestAllowExecuteConfigReachesPolicy(t *testing.T) {
	dir := t.TempDir()
	a := &App{workspace: dir, cfg: config.Default()}
	a.cfg.Security.AllowExecute = false
	policy, _, _, err := a.buildAllTools()
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if policy.AllowsExecute() {
		t.Fatal("allow_execute false still permitted execution")
	}
	a.cfg.Security.AllowExecute = true
	policy, _, _, err = a.buildAllTools()
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if !policy.AllowsExecute() {
		t.Fatal("allow_execute true did not permit execution")
	}
}

func toolConfig(t *testing.T, dir string) {
	t.Helper()
	cfg := testConfig()
	cfg.Tools.Enabled = strings.Split(testTools, ",")
	cfg.Agent.Stream = false
	writeConfig(t, dir, cfg)
}

func scriptedApp(t *testing.T, dir string, mp *mock.Provider, stdin string) (*App, *bytes.Buffer, *bytes.Buffer) {
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
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return a, &stdout, &stderr
}

func sampleWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module sample\n\ngo 1.27.3\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(\"hello\") }\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return dir
}

func sessionStoreFor(t *testing.T, dir string) *session.Store {
	t.Helper()
	store, err := session.NewStore(filepath.Join(dir, ".velo", "sessions"))
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	return store
}

func TestOneShotRunsToolThenAnswers(t *testing.T) {
	dir := sampleWorkspace(t)
	toolConfig(t, dir)

	mp := mock.New("mock", "mock-model",
		mock.Step{
			ToolCalls: []protocol.ToolCall{{
				ID:        "call_1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"main.go"}`),
			}},
			FinishReason: protocol.FinishToolCalls,
		},
		mock.Step{Text: "The project is a Go program in main.go.", FinishReason: protocol.FinishStop},
	)
	a, stdout, _ := scriptedApp(t, dir, mp, "")

	if err := a.Execute([]string{"Explain this project"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"The project is a Go program in main.go.", "status: completed", "session: ", "verification: unverified"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got %q", want, out)
		}
	}
	if mp.Remaining() != 0 {
		t.Fatalf("expected provider script exhausted, remaining %d", mp.Remaining())
	}

	store := sessionStoreFor(t, dir)
	list, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one session, got %d", len(list))
	}
	sess, err := store.Open(list[0].ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	msgs := sess.Messages()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != protocol.RoleUser || msgs[0].Text() != "Explain this project" {
		t.Fatalf("unexpected user message: %+v", msgs[0])
	}
	if msgs[1].Role != protocol.RoleAssistant || len(msgs[1].ToolCalls()) != 1 {
		t.Fatalf("unexpected assistant tool call: %+v", msgs[1])
	}
	if msgs[2].Role != protocol.RoleTool || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("unexpected tool message: %+v", msgs[2])
	}
	if !strings.Contains(msgs[2].Text(), "package main") {
		t.Fatalf("tool result missing file content: %q", msgs[2].Text())
	}
	if msgs[3].Role != protocol.RoleAssistant || !strings.Contains(msgs[3].Text(), "main.go") {
		t.Fatalf("unexpected final message: %+v", msgs[3])
	}
	if state := sess.State(); state.Status != "completed" || state.Turns != 2 {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestREPLRunsTaskAndExits(t *testing.T) {
	dir := sampleWorkspace(t)
	toolConfig(t, dir)

	mp := mock.New("mock", "mock-model",
		mock.Step{Text: "A small Go project.", FinishReason: protocol.FinishStop},
	)
	a, stdout, _ := scriptedApp(t, dir, mp, "What is this?\nexit\n")

	if err := a.Execute(nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "velo> ") || !strings.Contains(out, "A small Go project.") {
		t.Fatalf("unexpected repl output: %q", out)
	}
	if !strings.Contains(out, "session: ") {
		t.Fatalf("repl did not report session: %q", out)
	}
}

func TestSessionsListAndInspect(t *testing.T) {
	dir := sampleWorkspace(t)
	toolConfig(t, dir)

	mp := mock.New("mock", "mock-model",
		mock.Step{Text: "First answer.", FinishReason: protocol.FinishStop},
	)
	a, stdout, _ := scriptedApp(t, dir, mp, "")
	if err := a.Execute([]string{"Explain this project"}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	store := sessionStoreFor(t, dir)
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	id := list[0].ID

	stdout.Reset()
	if err := a.Execute([]string{"sessions", "list"}); err != nil {
		t.Fatalf("sessions list: %v", err)
	}
	if !strings.Contains(stdout.String(), id) || !strings.Contains(stdout.String(), "completed") {
		t.Fatalf("unexpected sessions list: %q", stdout.String())
	}

	stdout.Reset()
	if err := a.Execute([]string{"sessions", "inspect", id}); err != nil {
		t.Fatalf("sessions inspect: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"id: " + id, "status: completed", "transcript: 2 message(s)", "file: "} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in inspect output, got %q", want, out)
		}
	}
}

func TestSessionsDeleteAndArchive(t *testing.T) {
	dir := sampleWorkspace(t)
	toolConfig(t, dir)
	store := sessionStoreFor(t, dir)
	if _, err := store.Create("drop-me", dir); err != nil {
		t.Fatalf("create drop-me: %v", err)
	}
	if _, err := store.Create("keep-me", dir); err != nil {
		t.Fatalf("create keep-me: %v", err)
	}

	mp := mock.New("mock", "mock-model", mock.Step{Text: "unused", FinishReason: protocol.FinishStop})
	a, stdout, _ := scriptedApp(t, dir, mp, "")
	if err := a.Execute([]string{"sessions", "delete", "drop-me"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.Contains(stdout.String(), "deleted session drop-me") {
		t.Fatalf("delete output: %q", stdout.String())
	}
	if _, err := store.Open("drop-me"); err == nil {
		t.Fatal("deleted session still opens")
	}

	stdout.Reset()
	if err := a.Execute([]string{"sessions", "archive", "keep-me"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	list, err := store.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("list after archive: %v %+v", err, list)
	}
	if _, err := os.Stat(filepath.Join(dir, ".velo", "sessions", "keep-me.jsonl.archived")); err != nil {
		t.Fatalf("archived file: %v", err)
	}
	if err := a.Execute([]string{"resume", "drop-me", "Continue"}); err == nil {
		t.Fatal("resume of a deleted session succeeded")
	}
	if err := a.Execute([]string{"resume", "keep-me", "Continue"}); err == nil {
		t.Fatal("resume of an archived session succeeded")
	}
}

func TestResumePreservesSession(t *testing.T) {
	dir := sampleWorkspace(t)
	toolConfig(t, dir)

	first := mock.New("mock", "mock-model",
		mock.Step{Text: "First answer.", FinishReason: protocol.FinishStop},
	)
	a, _, _ := scriptedApp(t, dir, first, "")
	if err := a.Execute([]string{"Explain this project"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	store := sessionStoreFor(t, dir)
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	id := list[0].ID

	second := mock.New("mock", "mock-model",
		mock.Step{Text: "Second answer.", FinishReason: protocol.FinishStop},
	)
	resumed, stdout, _ := scriptedApp(t, dir, second, "")
	if err := resumed.Execute([]string{"resume", id, "Continue"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "resuming session "+id) || !strings.Contains(out, "Second answer.") {
		t.Fatalf("unexpected resume output: %q", out)
	}

	sess, err := store.Open(id)
	if err != nil {
		t.Fatalf("open resumed: %v", err)
	}
	msgs := sess.Messages()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages after resume, got %d", len(msgs))
	}
	if msgs[2].Role != protocol.RoleUser || msgs[2].Text() != "Continue" {
		t.Fatalf("resume objective not persisted: %+v", msgs[2])
	}
	if state := sess.State(); state.Status != "completed" || state.Turns != 1 {
		t.Fatalf("unexpected resumed state: %+v", state)
	}
}

func TestTaskWithoutConfigurationFailsBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	a, err := New(Options{
		Version:   "test",
		Stdin:     strings.NewReader(""),
		Stdout:    &stdout,
		Stderr:    &stderr,
		Workspace: dir,
		LookupEnv: testLookup(nil),
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if err := a.Execute([]string{"hello"}); err == nil {
		t.Fatal("expected an error for unconfigured default provider")
	} else if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected configuration error, got %v", err)
	}
}

func TestDefaultConfigIncludesProjectStructure(t *testing.T) {
	found := false
	for _, name := range config.Default().Tools.Enabled {
		if name == "project_structure" {
			found = true
		}
	}
	if !found {
		t.Fatal("default tools must include project_structure")
	}
}
