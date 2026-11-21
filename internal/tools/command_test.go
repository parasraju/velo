package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"velo/internal/repository"
	"velo/internal/security"
)

func newCommandHarness(t *testing.T, opts ...security.Option) (*Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	all := append([]security.Option{security.WithWrite(true), security.WithExecute(true)}, opts...)
	policy, err := security.New(dir, all...)
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	repo, err := repository.Open(repository.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, BuiltinOptions{Repo: repo, Policy: policy, Workspace: dir}); err != nil {
		t.Fatalf("register builtins: %v", err)
	}
	d, err := NewDispatcher(DispatcherOptions{
		Registry: reg,
		// run_command is an approval tool; the caller can override this with
		// a denying approver when testing the approval path.
		Approver: func(context.Context, Definition, Call) error { return nil },
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	return d, dir
}

func requireGo(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go executable not available")
	}
	return path
}

func TestRunCommandSuccess(t *testing.T) {
	d, _ := newCommandHarness(t)
	path := requireGo(t)
	res, err := dispatchBuiltin(t, d, "run_command", argsJSON(t, map[string]any{
		"executable": path,
		"args":       []string{"env", "GOOS"},
	}))
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %q", res.Content)
	}
	if !strings.Contains(res.Content, "exit code: 0") {
		t.Fatalf("unexpected output: %q", res.Content)
	}
}

func TestRunCommandReportsNonZeroExit(t *testing.T) {
	d, _ := newCommandHarness(t)
	path := requireGo(t)
	res, err := dispatchBuiltin(t, d, "run_command", argsJSON(t, map[string]any{
		"executable": path,
		"args":       []string{"env", "-definitely-not-a-flag"},
	}))
	if err != nil {
		t.Fatalf("run_command returned a transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for non-zero exit, got %+v", res)
	}
	if res.Metadata["exit_code"] == 0 {
		t.Fatalf("expected non-zero exit code: %+v", res.Metadata)
	}
}

func TestRunCommandRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	policy, err := security.New(dir, security.WithExecute(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	repo, err := repository.Open(repository.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, BuiltinOptions{Repo: repo, Policy: policy, Workspace: dir}); err != nil {
		t.Fatalf("register builtins: %v", err)
	}
	d, err := NewDispatcher(DispatcherOptions{Registry: reg})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	_, err = dispatchBuiltin(t, d, "run_command", `{"executable":"go","args":["version"]}`)
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expected ErrApprovalRequired, got %v", err)
	}
}

func TestRunCommandDeniedWithoutExecuteCapability(t *testing.T) {
	dir := t.TempDir()
	policy, err := security.New(dir, security.WithWrite(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	repo, err := repository.Open(repository.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, BuiltinOptions{Repo: repo, Policy: policy, Workspace: dir}); err != nil {
		t.Fatalf("register builtins: %v", err)
	}
	approved := false
	d, err := NewDispatcher(DispatcherOptions{
		Registry: reg,
		Policy:   policy,
		Approver: func(context.Context, Definition, Call) error {
			approved = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	_, err = dispatchBuiltin(t, d, "run_command", `{"executable":"go","args":["version"]}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
	if approved {
		t.Fatal("approval must not be requested after a capability denial")
	}
}

func TestRunCommandShellRequiresOptIn(t *testing.T) {
	d, _ := newCommandHarness(t)
	_, err := dispatchBuiltin(t, d, "run_command", `{"executable":"bash","args":["-c","echo hi"]}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied for shell without opt-in, got %v", err)
	}
}

func TestRunCommandNetworkRequiresOptIn(t *testing.T) {
	d, _ := newCommandHarness(t)
	_, err := dispatchBuiltin(t, d, "run_command", `{"executable":"curl","args":["https://example.com"]}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied for network without opt-in, got %v", err)
	}
}

func TestRunCommandRejectsUnknownArgs(t *testing.T) {
	d, _ := newCommandHarness(t)
	_, err := dispatchBuiltin(t, d, "run_command", `{"executable":"go","unexpected":true}`)
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
}

func TestRunCommandRejectsNULInArgument(t *testing.T) {
	d, _ := newCommandHarness(t)
	_, err := dispatchBuiltin(t, d, "run_command", argsJSON(t, map[string]any{
		"executable": "go",
		"args":       []string{"version\x00"},
	}))
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
}

func TestRunCommandTimeout(t *testing.T) {
	dir := t.TempDir()
	policy, err := security.New(dir, security.WithExecute(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	tool := &runCommandTool{policy: policy, outputBytes: 1024}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = tool.Execute(ctx, Call{
		ID:        "1",
		Name:      "run_command",
		Arguments: json.RawMessage(`{"executable":"go","args":["version"]}`),
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
}

func TestRunCommandOutputLimit(t *testing.T) {
	path := requireGo(t)
	// Rebuild the tool with a tiny output cap so the truncation path is
	// deterministic regardless of the size of `go env`.
	policy, err := security.New(t.TempDir(), security.WithExecute(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	tool := &runCommandTool{policy: policy, outputBytes: 4}
	res, err := tool.Execute(context.Background(), Call{
		ID:        "1",
		Name:      "run_command",
		Arguments: json.RawMessage(`{"executable":` + quoteJSON(path) + `,"args":["env","GOOS"]}`),
	})
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	if res.Metadata["stdout_cut"] != true {
		t.Fatalf("expected stdout truncation, got %+v (%q)", res.Metadata, res.Content)
	}
	if !strings.Contains(res.Content, "truncated at 4 bytes") {
		t.Fatalf("missing truncation notice: %q", res.Content)
	}
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestCommandEnvScrubsSecrets(t *testing.T) {
	t.Setenv("VELO_TEST_SECRET", "hunter2")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("LOCALAPPDATA", `C:\Users\test\AppData\Local`)
	env := commandEnv(map[string]string{"GIT_TERMINAL_PROMPT": "0"})
	for _, entry := range env {
		if strings.HasPrefix(entry, "VELO_TEST_SECRET=") || strings.HasPrefix(entry, "OPENAI_API_KEY=") {
			t.Fatalf("secret leaked into child environment: %q", entry)
		}
	}
	assertEnv := func(want string) {
		t.Helper()
		for _, entry := range env {
			if entry == want {
				return
			}
		}
		t.Fatalf("expected %q in child environment", want)
	}
	assertEnv(`LOCALAPPDATA=C:\Users\test\AppData\Local`)
	assertEnv("GIT_TERMINAL_PROMPT=0")
}

func TestLimitWriter(t *testing.T) {
	w := newLimitWriter(5)
	n, err := w.Write([]byte("hello world"))
	if err != nil || n != 11 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if w.String() != "hello" || !w.Truncated() {
		t.Fatalf("unexpected writer state: %q truncated=%v", w.String(), w.Truncated())
	}
	if _, err := w.Write([]byte("more")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if w.String() != "hello" || !w.Truncated() {
		t.Fatalf("unexpected writer state after second write: %q", w.String())
	}
}

func compileMain(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+name+"\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	out := filepath.Join(dir, name+".exe")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, combined)
	}
	return out
}

func TestRunCommandWaitDelayUnblocksInheritedPipe(t *testing.T) {
	requireGo(t)
	previous := commandWaitDelay
	commandWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { commandWaitDelay = previous })

	holder := compileMain(t, "holder", `package main
import ("fmt"; "os"; "strconv"; "time")
func main() {
  if len(os.Args) > 1 {
    _ = os.WriteFile(os.Args[1], []byte(strconv.Itoa(os.Getpid())), 0644)
  }
  fmt.Println("held")
  time.Sleep(30 * time.Second)
}
`)
	parent := compileMain(t, "parent", `package main
import ("os"; "os/exec")
func main() {
  cmd := exec.Command(os.Args[1], os.Args[2:]...)
  cmd.Stdout = os.Stdout
  cmd.Stderr = os.Stderr
  _ = cmd.Start()
}
`)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	})

	d, _ := newCommandHarness(t)
	started := time.Now()
	_, err := d.Dispatch(context.Background(), Call{
		ID:   "wait-delay",
		Name: "run_command",
		Arguments: json.RawMessage(argsJSON(t, map[string]any{
			"executable": parent,
			"args":       []string{holder, pidFile},
		})),
	})
	elapsed := time.Since(started)
	if elapsed > 5*time.Second {
		t.Fatalf("run_command stayed blocked for %s (err %v)", elapsed, err)
	}
}
