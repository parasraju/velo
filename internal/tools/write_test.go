package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func argsJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(data)
}

func TestWriteFileCreate(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":    "new/deep/file.txt",
		"content": "hello\n",
	}))
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(res.Content, "created new/deep/file.txt") {
		t.Fatalf("unexpected result: %q", res.Content)
	}
	if res.Metadata["created"] != true {
		t.Fatalf("unexpected metadata: %+v", res.Metadata)
	}
	data, err := os.ReadFile(filepath.Join(dir, "new", "deep", "file.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestWriteFileRefusesBlindOverwrite(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	_, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":    "a.txt",
		"content": "clobbered\n",
	}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("file was modified: %q", data)
	}
}

func TestWriteFileHashMismatch(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	_, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":            "a.txt",
		"content":         "clobbered\n",
		"expected_sha256": strings.Repeat("0", 64),
	}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("file was modified: %q", data)
	}
}

func TestWriteFileOverwriteWithHash(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	original := "alpha\nbeta\ngamma\n"
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	res, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":            "a.txt",
		"content":         "replaced\n",
		"expected_sha256": digestOf(original),
	}))
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(res.Content, "updated a.txt") {
		t.Fatalf("unexpected result: %q", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "replaced\n" {
		t.Fatalf("unexpected content: %q", data)
	}
	info, err := os.Stat(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("permissions not preserved: %o", perm)
		}
	}
}

func TestWriteFileRejectsExpectedHashForNewFile(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	_, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":            "fresh.txt",
		"content":         "x\n",
		"expected_sha256": strings.Repeat("a", 64),
	}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestWriteFilePathEscapeDenied(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	_, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":    "../escape.txt",
		"content": "x",
	}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestWriteFileInternalDirectoriesDenied(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	for _, path := range []string{".git/hooks/pre-commit", ".velo/config.yaml"} {
		_, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
			"path":    path,
			"content": "x",
		}))
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: expected ErrDenied, got %v", path, err)
		}
	}
}

func TestWriteFileSymlinkEscapeDenied(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	outside := t.TempDir()
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "target.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := dispatchBuiltin(t, d, "write_file", argsJSON(t, map[string]any{
		"path":    "link.txt",
		"content": "x",
	}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "target.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("write escaped the workspace: %v", statErr)
	}
}

func TestApplyPatchModify(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 alpha
-beta
+BETA
 gamma
`
	res, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": patch}))
	if err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if !strings.Contains(res.Content, "M a.txt (+1 -1)") {
		t.Fatalf("unexpected summary: %q", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestApplyPatchCreate(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	patch := `--- /dev/null
+++ b/sub/new.txt
@@ -0,0 +1,2 @@
+one
+two
`
	res, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": patch}))
	if err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if !strings.Contains(res.Content, "A sub/new.txt (+2 -0)") {
		t.Fatalf("unexpected summary: %q", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	if string(data) != "one\ntwo\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestApplyPatchConflictLeavesFileUntouched(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 alpha
-NOT_PRESENT
+changed
 gamma
`
	_, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": patch}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("file was modified: %q", data)
	}
}

func TestApplyPatchMultiFileAllOrNothing(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 alpha
-beta
+BETA
 gamma
--- a/m1.txt
+++ b/m1.txt
@@ -1,1 +1,1 @@
-needle one
+needle 1
`
	if _, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": patch})); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	m1, _ := os.ReadFile(filepath.Join(dir, "m1.txt"))
	if string(a) != "alpha\nBETA\ngamma\n" || string(m1) != "needle 1\n" {
		t.Fatalf("unexpected content: a=%q m1=%q", a, m1)
	}
}

func TestApplyPatchRejectsDeletion(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	patch := `--- a/a.txt
+++ /dev/null
@@ -1,3 +0,0 @@
-alpha
-beta
-gamma
`
	if _, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": patch})); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
}

func TestApplyPatchRejectsOutsideWorkspace(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	patch := `--- a/../escape.txt
+++ b/../escape.txt
@@ -1,1 +1,1 @@
-a
+b
`
	if _, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": patch})); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestApplyPatchPreservesMissingTrailingNewline(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	writeTestFile(t, dir, "n.txt", "a\nb")
	patch := `--- a/n.txt
+++ b/n.txt
@@ -1,2 +1,2 @@
 a
-b
+c
`
	if _, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": patch})); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "n.txt"))
	if string(data) != "a\nc" {
		t.Fatalf("trailing newline not preserved: %q", data)
	}
}

func TestApplyPatchNoHeaders(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	if _, err := dispatchBuiltin(t, d, "apply_patch", argsJSON(t, map[string]any{"patch": "not a patch"})); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
}
