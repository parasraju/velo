package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"velo/internal/repository"
	"velo/internal/security"
)

// TestMilestone2EditRunTestAndDiff exercises the milestone's vertical slice:
// modify a file, approve and run a test command, then inspect the real diff.
func TestMilestone2EditRunTestAndDiff(t *testing.T) {
	requireGit(t)
	goPath := requireGo(t)
	dir := t.TempDir()
	runGitTest(t, dir, "init")
	writeTestFile(t, dir, "go.mod", "module smoke\n\ngo 1.20\n")
	writeTestFile(t, dir, "calc.go", "package smoke\n\nfunc Add(a, b int) int { return a - b }\n")
	writeTestFile(t, dir, "calc_test.go", "package smoke\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatalf(\"Add(1,2) = %d\", Add(1, 2))\n\t}\n}\n")
	runGitTest(t, dir, "add", ".")
	runGitTest(t, dir, "commit", "-m", "broken add")

	repo, err := repository.Open(repository.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	policy, err := security.New(dir, security.WithWrite(true), security.WithExecute(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, BuiltinOptions{Repo: repo, Policy: policy, Workspace: dir}); err != nil {
		t.Fatalf("register builtins: %v", err)
	}
	approved := 0
	d, err := NewDispatcher(DispatcherOptions{
		Registry: reg,
		Policy:   policy,
		Approver: func(ctx context.Context, def Definition, call Call) error {
			if def.Name == "run_command" {
				approved++
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}

	// Edit the file using the hash returned by read_file.
	res, err := dispatchBuiltin(t, d, "read_file", `{"path":"calc.go"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	digest := ""
	for _, line := range strings.Split(res.Content, "\n") {
		if strings.HasPrefix(line, "sha256: ") {
			digest = strings.TrimPrefix(line, "sha256: ")
			break
		}
	}
	if digest == "" {
		t.Fatalf("read_file did not report a sha256: %q", res.Content)
	}
	if _, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":            "calc.go",
		"content":         "package smoke\n\nfunc Add(a, b int) int { return a + b }\n",
		"expected_sha256": digest,
	})); err != nil {
		t.Fatalf("write_file: %v", err)
	}

	// A command call must request approval before it executes.
	testRes, err := dispatchBuiltin(t, d, "run_command", argsJSON(t, map[string]any{
		"executable": goPath,
		"args":       []string{"test", "./..."},
	}))
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	if approved != 1 {
		t.Fatalf("expected exactly one command approval, got %d", approved)
	}
	if testRes.IsError {
		t.Fatalf("tests failed: %s", testRes.Content)
	}
	if !strings.Contains(testRes.Content, "exit code: 0") {
		t.Fatalf("unexpected test output: %q", testRes.Content)
	}

	// Inspect the real diff produced by git, not the edit summary.
	diffRes, err := dispatchBuiltin(t, d, "git_diff", `{"path":"calc.go"}`)
	if err != nil {
		t.Fatalf("git_diff: %v", err)
	}
	if !strings.Contains(diffRes.Content, "-func Add(a, b int) int { return a - b }") {
		t.Fatalf("diff is missing the removed line: %q", diffRes.Content)
	}
	if !strings.Contains(diffRes.Content, "+func Add(a, b int) int { return a + b }") {
		t.Fatalf("diff is missing the added line: %q", diffRes.Content)
	}

	// Confirm the command really ran in the workspace.
	cmd := exec.Command(goPath, "test", "./...")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("direct test run after edit: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "calc.go")); err != nil {
		t.Fatalf("calc.go disappeared: %v", err)
	}
}
