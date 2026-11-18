package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSecurityFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return full
}

func newPolicy(t *testing.T, workspace string, opts ...Option) *Policy {
	t.Helper()
	p, err := New(workspace, opts...)
	if err != nil {
		t.Fatalf("New(%q): %v", workspace, err)
	}
	return p
}

func TestNewValidatesWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeSecurityFile(t, dir, "a.txt", "alpha")

	p := newPolicy(t, dir)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if p.Workspace() != filepath.Clean(resolved) {
		t.Fatalf("Workspace() = %q, want %q", p.Workspace(), filepath.Clean(resolved))
	}
	if !filepath.IsAbs(p.Workspace()) {
		t.Fatalf("workspace must be absolute: %q", p.Workspace())
	}

	for _, workspace := range []string{"", "   "} {
		if _, err := New(workspace); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("New(%q) = %v, want ErrInvalidPath", workspace, err)
		}
	}
	file := writeSecurityFile(t, dir, "plain.txt", "x")
	if _, err := New(file); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("New(file) = %v, want ErrInvalidPath", err)
	}
	if _, err := New(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("New(missing) should fail")
	}
}

func TestResolvePaths(t *testing.T) {
	dir := t.TempDir()
	writeSecurityFile(t, dir, "a.txt", "alpha")
	writeSecurityFile(t, dir, "sub/b.txt", "beta")
	p := newPolicy(t, dir)
	workspace := p.Workspace()

	cases := map[string]string{
		"a.txt":                           filepath.Join(workspace, "a.txt"),
		"./a.txt":                         filepath.Join(workspace, "a.txt"),
		"sub/../a.txt":                    filepath.Join(workspace, "a.txt"),
		"sub/b.txt":                       filepath.Join(workspace, "sub", "b.txt"),
		"sub":                             filepath.Join(workspace, "sub"),
		filepath.Join(workspace, "a.txt"): filepath.Join(workspace, "a.txt"),
	}
	for input, want := range cases {
		got, err := p.Resolve(input)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", input, err)
		}
		if filepath.Clean(got) != filepath.Clean(want) {
			t.Fatalf("Resolve(%q) = %q, want %q", input, got, want)
		}
	}

	got, err := p.Resolve(".")
	if err != nil || filepath.Clean(got) != workspace {
		t.Fatalf("Resolve(.) = %q, %v", got, err)
	}

	for _, input := range []string{
		"../x",
		"sub/../../x",
		filepath.Join(workspace, "..", "outside.txt"),
	} {
		if _, err := p.Resolve(input); !errors.Is(err, ErrOutsideWorkspace) {
			t.Fatalf("Resolve(%q) = %v, want ErrOutsideWorkspace", input, err)
		}
	}
	for _, input := range []string{"", "a\x00b"} {
		if _, err := p.Resolve(input); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Resolve(%q) = %v, want ErrInvalidPath", input, err)
		}
	}
}

func TestEnsureReadableAndWritable(t *testing.T) {
	dir := t.TempDir()
	writeSecurityFile(t, dir, "a.txt", "alpha")
	p := newPolicy(t, dir, WithWrite(true))

	readable, err := p.EnsureReadable("a.txt")
	if err != nil {
		t.Fatalf("EnsureReadable: %v", err)
	}
	if filepath.Clean(readable) != filepath.Join(p.Workspace(), "a.txt") {
		t.Fatalf("EnsureReadable = %q", readable)
	}

	for _, target := range []string{"new.txt", "newdir/new.txt"} {
		got, err := p.EnsureWritable(target)
		if err != nil {
			t.Fatalf("EnsureWritable(%q): %v", target, err)
		}
		want := filepath.Join(p.Workspace(), filepath.FromSlash(target))
		if filepath.Clean(got) != filepath.Clean(want) {
			t.Fatalf("EnsureWritable(%q) = %q, want %q", target, got, want)
		}
		if _, err := os.Stat(want); !os.IsNotExist(err) {
			t.Fatalf("EnsureWritable(%q) created the target", target)
		}
	}

	if _, err := p.EnsureWritable("../x"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("EnsureWritable(../x) = %v, want ErrOutsideWorkspace", err)
	}
	if _, err := p.EnsureWritable("a\x00b"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("EnsureWritable(NUL) = %v, want ErrInvalidPath", err)
	}
	if _, err := p.EnsureWritable("a.txt"); err != nil {
		t.Fatalf("EnsureWritable(existing): %v", err)
	}
}

func TestWriteDisabled(t *testing.T) {
	dir := t.TempDir()
	writeSecurityFile(t, dir, "a.txt", "alpha")
	p := newPolicy(t, dir, WithWrite(false))

	if _, err := p.EnsureReadable("a.txt"); err != nil {
		t.Fatalf("EnsureReadable with write disabled: %v", err)
	}
	if _, err := p.EnsureWritable("a.txt"); !errors.Is(err, ErrWriteDenied) {
		t.Fatalf("EnsureWritable = %v, want ErrWriteDenied", err)
	}
	if _, err := p.EnsureWritable("../x"); !errors.Is(err, ErrWriteDenied) {
		t.Fatalf("EnsureWritable(../x) = %v, want ErrWriteDenied", err)
	}
}

func TestAllowedRoots(t *testing.T) {
	dir := t.TempDir()
	writeSecurityFile(t, dir, "docs/d.txt", "doc")
	writeSecurityFile(t, dir, "src/s.txt", "src")
	p := newPolicy(t, dir, WithAllowedRoots("docs"))

	if _, err := p.EnsureReadable("docs/d.txt"); err != nil {
		t.Fatalf("EnsureReadable inside allowed root: %v", err)
	}
	if _, err := p.EnsureReadable("src/s.txt"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("EnsureReadable sibling = %v, want ErrOutsideWorkspace", err)
	}
	if _, err := p.EnsureReadable("docs/../src/s.txt"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("EnsureReadable traversal sibling = %v, want ErrOutsideWorkspace", err)
	}

	absolute := newPolicy(t, dir, WithAllowedRoots(filepath.Join(dir, "docs")))
	if _, err := absolute.EnsureReadable("docs/d.txt"); err != nil {
		t.Fatalf("absolute allowed root: %v", err)
	}

	both := newPolicy(t, dir, WithAllowedRoots("docs", "src"))
	if _, err := both.EnsureReadable("src/s.txt"); err != nil {
		t.Fatalf("second allowed root: %v", err)
	}

	whole := newPolicy(t, dir, WithAllowedRoots("."))
	if _, err := whole.EnsureReadable("src/s.txt"); err != nil {
		t.Fatalf("workspace allowed root: %v", err)
	}

	outside, err := New(dir, WithAllowedRoots(filepath.Join(dir, "..", "outside")))
	if !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("outside allowed root = %v (policy %v), want ErrOutsideWorkspace", err, outside)
	}
	if _, err := New(dir, WithAllowedRoots("")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("empty allowed root = %v, want ErrInvalidPath", err)
	}
}

func TestSymlinkEscapesWorkspace(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeSecurityFile(t, dir, "inside.txt", "inside")
	writeSecurityFile(t, dir, "sub/inside.txt", "inside")
	target := writeSecurityFile(t, outside, "secret.txt", "secret")
	outsideDir := filepath.Join(outside, "nested")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	writeSecurityFile(t, outside, "nested/deep.txt", "deep")

	link := filepath.Join(dir, "escape.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable on this system: %v", err)
	}
	dirLink := filepath.Join(dir, "escape-dir")
	if err := os.Symlink(outsideDir, dirLink); err != nil {
		t.Skipf("directory symlink creation unavailable on this system: %v", err)
	}
	insideLink := filepath.Join(dir, "inside-link.txt")
	if err := os.Symlink(filepath.Join(dir, "inside.txt"), insideLink); err != nil {
		t.Skipf("symlink creation unavailable on this system: %v", err)
	}

	p := newPolicy(t, dir)
	if _, err := p.EnsureReadable("escape.txt"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("EnsureReadable(escape file) = %v, want ErrSymlink", err)
	}
	if err := p.CheckSymlink("escape.txt"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("CheckSymlink(escape file) = %v, want ErrSymlink", err)
	}
	if _, err := p.EnsureReadable("escape-dir/deep.txt"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("EnsureReadable(escape dir) = %v, want ErrSymlink", err)
	}
	if err := p.CheckSymlink("escape-dir"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("CheckSymlink(escape dir) = %v, want ErrSymlink", err)
	}
	if _, err := p.EnsureReadable("inside-link.txt"); err != nil {
		t.Fatalf("EnsureReadable(inside symlink): %v", err)
	}
	if err := p.CheckSymlink("inside-link.txt"); err != nil {
		t.Fatalf("CheckSymlink(inside symlink): %v", err)
	}
	if err := p.CheckSymlink("missing.txt"); err != nil {
		t.Fatalf("CheckSymlink(missing): %v", err)
	}
}

func TestRelative(t *testing.T) {
	dir := t.TempDir()
	writeSecurityFile(t, dir, "a.txt", "alpha")
	writeSecurityFile(t, dir, "sub/b.txt", "beta")
	p := newPolicy(t, dir)

	cases := map[string]string{
		filepath.Join(p.Workspace(), "a.txt"):        "a.txt",
		filepath.Join(p.Workspace(), "sub", "b.txt"): "sub/b.txt",
		"a.txt":        "a.txt",
		"sub/../a.txt": "a.txt",
		"sub/b.txt":    "sub/b.txt",
		"sub":          "sub",
	}
	for input, want := range cases {
		got, err := p.Relative(input)
		if err != nil {
			t.Fatalf("Relative(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("Relative(%q) = %q, want %q", input, got, want)
		}
		if strings.Contains(got, "\\") {
			t.Fatalf("Relative(%q) = %q contains backslash", input, got)
		}
	}

	got, err := p.Relative(".")
	if err != nil || got != "." {
		t.Fatalf("Relative(.) = %q, %v", got, err)
	}
	got, err = p.Relative(p.Workspace())
	if err != nil || got != "." {
		t.Fatalf("Relative(workspace) = %q, %v", got, err)
	}

	for _, input := range []string{"../x", filepath.Join(p.Workspace(), "..", "x")} {
		if _, err := p.Relative(input); !errors.Is(err, ErrOutsideWorkspace) {
			t.Fatalf("Relative(%q) = %v, want ErrOutsideWorkspace", input, err)
		}
	}
	for _, input := range []string{"", "a\x00b"} {
		if _, err := p.Relative(input); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Relative(%q) = %v, want ErrInvalidPath", input, err)
		}
	}
}

func TestIsDenied(t *testing.T) {
	denied := []error{
		ErrDenied,
		ErrWriteDenied,
		ErrOutsideWorkspace,
		ErrSymlink,
		ErrInvalidPath,
		fmt.Errorf("wrapped: %w", ErrOutsideWorkspace),
		fmt.Errorf("wrapped: %w", ErrWriteDenied),
	}
	for _, err := range denied {
		if !IsDenied(err) {
			t.Fatalf("IsDenied(%v) = false", err)
		}
	}
	for _, err := range []error{nil, errors.New("other"), os.ErrNotExist} {
		if IsDenied(err) {
			t.Fatalf("IsDenied(%v) = true", err)
		}
	}
}
