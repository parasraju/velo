package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"velo/internal/repository"
	"velo/internal/security"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not available")
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=VELO Test",
		"GIT_AUTHOR_EMAIL=velo@example.invalid",
		"GIT_COMMITTER_NAME=VELO Test",
		"GIT_COMMITTER_EMAIL=velo@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func newGitHarness(t *testing.T) (*Dispatcher, string) {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runGitTest(t, dir, "init")
	writeTestFile(t, dir, "a.txt", "alpha\n")
	runGitTest(t, dir, "add", "a.txt")
	runGitTest(t, dir, "commit", "-m", "initial commit")

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
	d, err := NewDispatcher(DispatcherOptions{Registry: reg})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	return d, dir
}

func TestGitStatusSeesModifiedFile(t *testing.T) {
	d, dir := newGitHarness(t)
	writeTestFile(t, dir, "a.txt", "changed\n")
	res, err := dispatchBuiltin(t, d, "git_status", `{}`)
	if err != nil {
		t.Fatalf("git_status: %v", err)
	}
	if !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("expected a.txt in status: %q", res.Content)
	}
}

func TestGitDiffShowsWorkingTreeChange(t *testing.T) {
	d, dir := newGitHarness(t)
	writeTestFile(t, dir, "a.txt", "beta\n")
	res, err := dispatchBuiltin(t, d, "git_diff", `{}`)
	if err != nil {
		t.Fatalf("git_diff: %v", err)
	}
	if !strings.Contains(res.Content, "-alpha") || !strings.Contains(res.Content, "+beta") {
		t.Fatalf("unexpected diff: %q", res.Content)
	}
}

func TestGitDiffNoChanges(t *testing.T) {
	d, _ := newGitHarness(t)
	res, err := dispatchBuiltin(t, d, "git_diff", `{}`)
	if err != nil {
		t.Fatalf("git_diff: %v", err)
	}
	if res.Content != "(no changes)" {
		t.Fatalf("expected no changes marker, got %q", res.Content)
	}
}

func TestGitLogOneline(t *testing.T) {
	d, _ := newGitHarness(t)
	res, err := dispatchBuiltin(t, d, "git_log", `{"limit":5,"format":"oneline"}`)
	if err != nil {
		t.Fatalf("git_log: %v", err)
	}
	if !strings.Contains(res.Content, "initial commit") {
		t.Fatalf("expected commit message: %q", res.Content)
	}
}

func TestGitBranchListsBranches(t *testing.T) {
	d, _ := newGitHarness(t)
	res, err := dispatchBuiltin(t, d, "git_branch", `{}`)
	if err != nil {
		t.Fatalf("git_branch: %v", err)
	}
	if strings.TrimSpace(res.Content) == "" {
		t.Fatalf("expected at least one branch, got %q", res.Content)
	}
}

func TestGitStatusPathOutsideWorkspaceDenied(t *testing.T) {
	d, _ := newGitHarness(t)
	_, err := dispatchBuiltin(t, d, "git_status", `{"path":"../outside"}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestGitLogRejectsInvalidLimit(t *testing.T) {
	d, _ := newGitHarness(t)
	_, err := dispatchBuiltin(t, d, "git_log", `{"limit":0}`)
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
}

func TestGitToolsFailOutsideRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
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
	d, err := NewDispatcher(DispatcherOptions{Registry: reg})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	if _, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "git_status", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("expected error outside a git repository")
	}
}

func TestGitStatusDoesNotFollowSymlinkOutsideWorkspace(t *testing.T) {
	d, dir := newGitHarness(t)
	outside := t.TempDir()
	writeTestFile(t, outside, "secret.txt", "secret\n")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := dispatchBuiltin(t, d, "git_status", `{"path":"link/secret.txt"}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func addSecretFile(t *testing.T, dir, content string) {
	t.Helper()
	writeTestFile(t, dir, ".env", content)
	// Force-add so a global ignore rule cannot silently change the fixture.
	runGitTest(t, dir, "add", "-f", ".env")
	runGitTest(t, dir, "commit", "-m", "add env")
}

func TestGitDiffExcludesSecretContents(t *testing.T) {
	d, dir := newGitHarness(t)
	addSecretFile(t, dir, "SECRET=old\n")
	writeTestFile(t, dir, ".env", "SECRET=new-value\n")
	writeTestFile(t, dir, "a.txt", "changed\n")

	res, err := dispatchBuiltin(t, d, "git_diff", `{}`)
	if err != nil {
		t.Fatalf("git_diff: %v", err)
	}
	if strings.Contains(res.Content, "SECRET") || strings.Contains(res.Content, ".env") {
		t.Fatalf("secret file leaked into diff: %q", res.Content)
	}
	if !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("non-secret change was dropped: %q", res.Content)
	}
}

func TestGitDiffExplicitSecretPathDenied(t *testing.T) {
	d, dir := newGitHarness(t)
	addSecretFile(t, dir, "SECRET=old\n")
	writeTestFile(t, dir, ".env", "SECRET=new-value\n")

	_, err := dispatchBuiltin(t, d, "git_diff", `{"path":".env"}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestGitStatusRedactsSecretPaths(t *testing.T) {
	d, dir := newGitHarness(t)
	addSecretFile(t, dir, "SECRET=old\n")
	writeTestFile(t, dir, ".env", "SECRET=new-value\n")

	res, err := dispatchBuiltin(t, d, "git_status", `{}`)
	if err != nil {
		t.Fatalf("git_status: %v", err)
	}
	if strings.Contains(res.Content, ".env") || strings.Contains(res.Content, "SECRET") {
		t.Fatalf("secret path leaked into status: %q", res.Content)
	}
	if !strings.Contains(res.Content, "[secret path redacted]") {
		t.Fatalf("expected redaction marker: %q", res.Content)
	}
}

func TestGitToolsDeniedWhenExecuteDisabled(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGitTest(t, dir, "init")
	writeTestFile(t, dir, "a.txt", "alpha\n")
	runGitTest(t, dir, "add", "a.txt")
	runGitTest(t, dir, "commit", "-m", "initial commit")

	repo, err := repository.Open(repository.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	policy, err := security.New(dir, security.WithWrite(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, BuiltinOptions{Repo: repo, Policy: policy, Workspace: dir}); err != nil {
		t.Fatalf("register builtins: %v", err)
	}
	d, err := NewDispatcher(DispatcherOptions{Registry: reg, Policy: policy})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	for _, name := range []string{"git_status", "git_diff", "git_log", "git_branch"} {
		_, err := dispatchBuiltin(t, d, name, `{}`)
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("%s with execute disabled: got %v, want ErrDenied", name, err)
		}
	}
}

func TestGitDiffCapsLargeOutput(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGitTest(t, dir, "init")
	original := strings.Repeat("a", gitMaxOutputBytes+64<<10)
	writeTestFile(t, dir, "big.txt", original+"\n")
	runGitTest(t, dir, "add", "big.txt")
	runGitTest(t, dir, "commit", "-m", "big")
	writeTestFile(t, dir, "big.txt", strings.Repeat("b", gitMaxOutputBytes+64<<10)+"\n")

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
	d, err := NewDispatcher(DispatcherOptions{
		Registry:       reg,
		Policy:         policy,
		MaxResultBytes: gitMaxOutputBytes + 4096,
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	res, err := dispatchBuiltin(t, d, "git_diff", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("git_diff: %v", err)
	}
	if !strings.Contains(res.Content, "[stdout truncated at") {
		t.Fatalf("expected truncation marker, content length %d", len(res.Content))
	}
	if len(res.Content) > gitMaxOutputBytes+256 {
		t.Fatalf("diff length %d exceeds cap %d", len(res.Content), gitMaxOutputBytes)
	}
	if res.Metadata["stdout_cut"] != true {
		t.Fatalf("stdout_cut = %v, want true", res.Metadata["stdout_cut"])
	}
}
