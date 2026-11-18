package security

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCapabilityOptions(t *testing.T) {
	dir := t.TempDir()
	p, err := New(dir, WithWrite(true), WithExecute(true), WithShell(false), WithNetwork(false))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	if !p.AllowsWrite() || !p.AllowsExecute() {
		t.Fatal("expected write and execute to be allowed")
	}
	if p.AllowsShell() || p.AllowsNetwork() {
		t.Fatal("expected shell and network to be denied")
	}

	zero, err := New(dir)
	if err != nil {
		t.Fatalf("new zero policy: %v", err)
	}
	if zero.AllowsWrite() || zero.AllowsExecute() || zero.AllowsNetwork() || zero.AllowsShell() {
		t.Fatal("default policy must deny all optional capabilities")
	}
}

func TestCheckCommandDeniedWhenExecutionDisabled(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	if _, err := p.CheckCommand(CommandSpec{Executable: "go", Args: []string{"test"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestCheckCommandResolvesWorkspaceDirectory(t *testing.T) {
	dir := t.TempDir()
	p, err := New(dir, WithExecute(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	got, err := p.CheckCommand(CommandSpec{Executable: "go", Args: []string{"version"}, Dir: "."})
	if err != nil {
		t.Fatalf("check command: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(dir) {
		t.Fatalf("expected %q, got %q", dir, got)
	}
	if _, err := p.CheckCommand(CommandSpec{Executable: "go", Dir: "../outside"}); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("expected ErrOutsideWorkspace, got %v", err)
	}
}

func TestCheckCommandRequiresShellOptIn(t *testing.T) {
	dir := t.TempDir()
	p, err := New(dir, WithExecute(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	if _, err := p.CheckCommand(CommandSpec{Executable: "bash", Args: []string{"-c", "echo hi"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected shell denial, got %v", err)
	}
	shell, err := New(dir, WithExecute(true), WithShell(true))
	if err != nil {
		t.Fatalf("new shell policy: %v", err)
	}
	if _, err := shell.CheckCommand(CommandSpec{Executable: "bash", Args: []string{"-c", "echo hi"}}); err != nil {
		t.Fatalf("expected shell to be allowed: %v", err)
	}
}

func TestCheckCommandNetworkPolicy(t *testing.T) {
	dir := t.TempDir()
	p, err := New(dir, WithExecute(true))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	if _, err := p.CheckCommand(CommandSpec{Executable: "curl", Args: []string{"https://example.com"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected network denial, got %v", err)
	}
	network, err := New(dir, WithExecute(true), WithNetwork(true))
	if err != nil {
		t.Fatalf("new network policy: %v", err)
	}
	if _, err := network.CheckCommand(CommandSpec{Executable: "curl", Args: []string{"https://example.com"}}); err != nil {
		t.Fatalf("expected network command to be allowed: %v", err)
	}
}

func TestIsDestructive(t *testing.T) {
	cases := []struct {
		exe  string
		args []string
		want bool
	}{
		{"rm", []string{"-rf", "x"}, true},
		{"git", []string{"reset", "--hard"}, true},
		{"git", []string{"status"}, false},
		{"go", []string{"test", "./..."}, false},
	}
	for _, tc := range cases {
		if got := IsDestructive(tc.exe, tc.args); got != tc.want {
			t.Fatalf("IsDestructive(%q, %v) = %v, want %v", tc.exe, tc.args, got, tc.want)
		}
	}
}

func TestIsNetworkCommand(t *testing.T) {
	cases := []struct {
		exe  string
		args []string
		want bool
	}{
		{"curl", []string{"https://example.com"}, true},
		{"git", []string{"fetch"}, true},
		{"git", []string{"-C", ".", "fetch"}, true},
		{"git", []string{"status"}, false},
		{"go", []string{"test"}, false},
		{"go", []string{"get", "example.com/x"}, true},
	}
	for _, tc := range cases {
		if got := IsNetworkCommand(tc.exe, tc.args); got != tc.want {
			t.Fatalf("IsNetworkCommand(%q, %v) = %v, want %v", tc.exe, tc.args, got, tc.want)
		}
	}
}
