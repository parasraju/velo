package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
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
	repo, err := repository.Open(repository.Options{Workspace: dir, NoGit: true})
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
	repo, err := repository.Open(repository.Options{Workspace: dir, NoGit: true})
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
	repo, err := repository.Open(repository.Options{Workspace: dir, NoGit: true})
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
