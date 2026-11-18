package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"velo/internal/repository"
	"velo/internal/security"
)

func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func newBuiltinHarness(t *testing.T) (*Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, dir, "a.txt", "alpha\nbeta\ngamma\n")
	writeTestFile(t, dir, "notes.md", "# notes\n")
	writeTestFile(t, dir, "m1.txt", "needle one\n")
	writeTestFile(t, dir, "m2.txt", "needle two\n")
	writeTestFile(t, dir, "sub/b.txt", "first search hit\nno match here\nsecond search hit\n")
	writeTestFile(t, dir, "sub/deep/c.txt", "deep\n")

	repo, err := repository.Open(repository.Options{Workspace: dir, NoGit: true})
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	policy, err := security.New(dir, security.WithWrite(true))
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, BuiltinOptions{Repo: repo, Policy: policy}); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}
	d, err := NewDispatcher(DispatcherOptions{
		Registry: reg,
		Approver: func(context.Context, Definition, Call) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d, dir
}

func dispatchBuiltin(t *testing.T, d *Dispatcher, name, args string) (Result, error) {
	t.Helper()
	return d.Dispatch(context.Background(), Call{ID: "call-1", Name: name, Arguments: json.RawMessage(args)})
}

func TestRegisterBuiltins(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	defs := d.Definitions()
	names := make([]string, len(defs))
	for i, def := range defs {
		names[i] = def.Name
		switch def.Name {
		case "read_file", "list_files", "search_files", "project_structure", "git_status", "git_diff", "git_log", "git_branch":
			if def.Category != CategoryRead || !def.ReadOnly {
				t.Fatalf("builtin %q must be read-only", def.Name)
			}
		case "write_file", "apply_patch":
			if def.Category != CategoryWrite || def.ReadOnly {
				t.Fatalf("builtin %q must be a write tool", def.Name)
			}
		case "run_command":
			if def.Category != CategoryExec || def.ReadOnly {
				t.Fatalf("builtin %q must be an exec tool", def.Name)
			}
		default:
			t.Fatalf("unexpected builtin %q", def.Name)
		}
	}
	want := []string{
		"apply_patch", "git_branch", "git_diff", "git_log", "git_status",
		"list_files", "project_structure", "read_file", "run_command",
		"search_files", "write_file",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

func TestRegisterBuiltinsRequiresDependencies(t *testing.T) {
	if err := RegisterBuiltins(nil, BuiltinOptions{}); err == nil {
		t.Fatal("expected error for nil registry")
	}
	r := NewRegistry()
	if err := RegisterBuiltins(r, BuiltinOptions{}); err == nil {
		t.Fatal("expected error for nil repository")
	}
}

func TestBuiltinReadFile(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "read_file", `{"path":"a.txt"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.HasPrefix(res.Content, "a.txt\n") {
		t.Fatalf("unexpected header: %q", res.Content)
	}
	if !strings.Contains(res.Content, "alpha") || !strings.Contains(res.Content, "gamma") {
		t.Fatalf("missing content: %q", res.Content)
	}
	if res.Metadata["path"] != "a.txt" {
		t.Fatalf("unexpected metadata: %+v", res.Metadata)
	}
	if res.Metadata["truncated"] != false {
		t.Fatalf("unexpected truncation: %+v", res.Metadata)
	}
}

func TestBuiltinReadFileLineRange(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "read_file", `{"path":"a.txt","start_line":2,"end_line":3}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.Contains(res.Content, "2 | beta") || !strings.Contains(res.Content, "3 | gamma") {
		t.Fatalf("unexpected range output: %q", res.Content)
	}
	if strings.Contains(res.Content, "1 | alpha") {
		t.Fatalf("range included excluded line: %q", res.Content)
	}
	res, err = dispatchBuiltin(t, d, "read_file", `{"path":"a.txt","start_line":3}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.Contains(res.Content, "3 | gamma") {
		t.Fatalf("unexpected open range output: %q", res.Content)
	}
}

func TestBuiltinReadFileInvalidRanges(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	for _, args := range []string{
		`{"path":"a.txt","start_line":0}`,
		`{"path":"a.txt","start_line":3,"end_line":2}`,
		`{"path":"a.txt","end_line":-1}`,
		`{"path":"a.txt","max_bytes":0}`,
	} {
		_, err := dispatchBuiltin(t, d, "read_file", args)
		if !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("expected ErrInvalidArguments for %s, got %v", args, err)
		}
	}
}

func TestBuiltinReadFileTruncation(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "read_file", `{"path":"a.txt","max_bytes":5}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.Contains(res.Content, "[truncated: limit 5 bytes]") {
		t.Fatalf("missing truncation notice: %q", res.Content)
	}
	if res.Metadata["truncated"] != true {
		t.Fatalf("unexpected metadata: %+v", res.Metadata)
	}
}

func TestBuiltinReadFileBadArguments(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	for _, args := range []string{
		`{}`,
		`{"path":""}`,
		`{"path":"a.txt","bogus":1}`,
		`{"path":"a.txt"} {"path":"b.txt"}`,
	} {
		_, err := dispatchBuiltin(t, d, "read_file", args)
		if !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("expected ErrInvalidArguments for %s, got %v", args, err)
		}
	}
}

func TestDecodeArgsRejectsNonObject(t *testing.T) {
	var out struct {
		Path string `json:"path"`
	}
	for _, raw := range []string{`null`, `[]`, `"a.txt"`, `123`, `true`} {
		if err := decodeArgs(json.RawMessage(raw), &out); !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("expected ErrInvalidArguments for %s, got %v", raw, err)
		}
	}
	if err := decodeArgs(json.RawMessage(`{"path":"a.txt"}`), &out); err != nil {
		t.Fatalf("valid object rejected: %v", err)
	}
	if err := decodeArgs(json.RawMessage(`{"path":"a.txt","bogus":1}`), &out); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected unknown field rejection, got %v", err)
	}
	if err := decodeArgs(json.RawMessage(`{"path":"a.txt"} {}`), &out); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected trailing data rejection, got %v", err)
	}
}

func TestBuiltinReadFileSecretDenied(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	writeTestFile(t, dir, ".env", "TOP_SECRET=1\n")
	res, err := dispatchBuiltin(t, d, "read_file", `{"path":".env"}`)
	if !errors.Is(err, repository.ErrSecret) {
		t.Fatalf("expected repository.ErrSecret, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if strings.Contains(res.Content, "TOP_SECRET") {
		t.Fatalf("secret content leaked: %q", res.Content)
	}
}

func TestBuiltinReadFileBinaryRejected(t *testing.T) {
	d, dir := newBuiltinHarness(t)
	writeTestFile(t, dir, "blob.bin", "abc\x00def")
	res, err := dispatchBuiltin(t, d, "read_file", `{"path":"blob.bin"}`)
	if !errors.Is(err, repository.ErrBinary) {
		t.Fatalf("expected repository.ErrBinary, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
}

func TestBuiltinListFilesSorted(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "list_files", `{"path":"."}`)
	if err != nil {
		t.Fatalf("list_files: %v", err)
	}
	lines := strings.Split(res.Content, "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		name, _, _ := strings.Cut(line, "\t")
		names = append(names, name)
	}
	want := []string{"a.txt", "m1.txt", "m2.txt", "notes.md"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("expected sorted entries %v, got %v", want, names)
	}
}

func TestBuiltinPathEscapeDenied(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	for _, name := range []string{"read_file", "list_files", "project_structure"} {
		res, err := dispatchBuiltin(t, d, name, `{"path":"../outside.txt"}`)
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: expected ErrDenied, got %v", name, err)
		}
		if !res.IsError {
			t.Fatalf("%s: result should be an error", name)
		}
	}
	_, err := dispatchBuiltin(t, d, "search_files", `{"pattern":"x"}`)
	if err != nil {
		t.Fatalf("search_files on workspace root should be allowed: %v", err)
	}
}

func TestBuiltinListFiles(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "list_files", `{"path":"."}`)
	if err != nil {
		t.Fatalf("list_files: %v", err)
	}
	if !strings.Contains(res.Content, "a.txt") || !strings.Contains(res.Content, "notes.md") {
		t.Fatalf("missing files: %q", res.Content)
	}
	if strings.Contains(res.Content, "sub/b.txt") {
		t.Fatalf("non-recursive listing recurred: %q", res.Content)
	}

	res, err = dispatchBuiltin(t, d, "list_files", `{"path":".","recursive":true}`)
	if err != nil {
		t.Fatalf("list_files recursive: %v", err)
	}
	if !strings.Contains(res.Content, "sub/b.txt") || !strings.Contains(res.Content, "sub/deep/c.txt") {
		t.Fatalf("recursive listing missing files: %q", res.Content)
	}
	if !strings.Contains(res.Content, "\t") || !strings.Contains(res.Content, " bytes") {
		t.Fatalf("unexpected entry format: %q", res.Content)
	}
}

func TestBuiltinListFilesInclude(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "list_files", `{"path":".","recursive":true,"include":"notes.md"}`)
	if err != nil {
		t.Fatalf("list_files: %v", err)
	}
	if !strings.Contains(res.Content, "notes.md") {
		t.Fatalf("include filter removed the target: %q", res.Content)
	}
	if strings.Contains(res.Content, "a.txt") {
		t.Fatalf("include filter did not apply: %q", res.Content)
	}
}

func TestBuiltinListFilesValidation(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	if _, err := dispatchBuiltin(t, d, "list_files", `{"max_depth":65}`); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
	if _, err := dispatchBuiltin(t, d, "list_files", `{"bogus":true}`); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
}

func TestBuiltinSearchFiles(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "search_files", `{"pattern":"search hit","regex":false,"case_sensitive":true,"include":"*.txt"}`)
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	if !strings.Contains(res.Content, "sub/b.txt:") || !strings.Contains(res.Content, "search hit") {
		t.Fatalf("unexpected search output: %q", res.Content)
	}
	if res.Metadata["truncated"] != false {
		t.Fatalf("unexpected metadata: %+v", res.Metadata)
	}
}

func TestBuiltinSearchFilesContext(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "search_files", `{"pattern":"first search hit","context_lines":1}`)
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	if !strings.Contains(res.Content, "\n  ") {
		t.Fatalf("expected context lines: %q", res.Content)
	}
}

func TestBuiltinSearchFilesTruncated(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "search_files", `{"pattern":"needle","max_results":1}`)
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	if res.Metadata["truncated"] != true {
		t.Fatalf("expected truncated metadata: %+v", res.Metadata)
	}
	if !strings.Contains(res.Content, "[truncated: more matches]") {
		t.Fatalf("missing truncation marker: %q", res.Content)
	}
}

func TestBuiltinSearchFilesEmptyAndValidation(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "search_files", `{"pattern":"zzzznotpresent"}`)
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	if res.Content != "no matches" {
		t.Fatalf("unexpected empty result: %q", res.Content)
	}
	for _, args := range []string{
		`{}`,
		`{"pattern":""}`,
		`{"pattern":"x","context_lines":4}`,
		`{"pattern":"x","max_results":0}`,
		`{"pattern":"x","max_results":1001}`,
		`{"pattern":"x","bogus":1}`,
	} {
		if _, err := dispatchBuiltin(t, d, "search_files", args); !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("expected ErrInvalidArguments for %s, got %v", args, err)
		}
	}
}

func TestBuiltinProjectStructure(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	res, err := dispatchBuiltin(t, d, "project_structure", `{"path":"."}`)
	if err != nil {
		t.Fatalf("project_structure: %v", err)
	}
	if !strings.HasPrefix(res.Content, ".") {
		t.Fatalf("missing header: %q", res.Content)
	}
	if !strings.Contains(res.Content, "a.txt") || !strings.Contains(res.Content, "sub/") {
		t.Fatalf("missing tree entries: %q", res.Content)
	}
	if !strings.Contains(res.Content, "├── ") && !strings.Contains(res.Content, "└── ") {
		t.Fatalf("missing tree connectors: %q", res.Content)
	}
	if _, ok := res.Metadata["files"]; !ok {
		t.Fatalf("missing files metadata: %+v", res.Metadata)
	}
	if _, ok := res.Metadata["bytes"]; !ok {
		t.Fatalf("missing bytes metadata: %+v", res.Metadata)
	}
}

func TestBuiltinProjectStructureValidation(t *testing.T) {
	d, _ := newBuiltinHarness(t)
	for _, args := range []string{
		`{"max_depth":0}`,
		`{"max_depth":11}`,
		`{"max_entries":0}`,
		`{"max_entries":5001}`,
		`{"bogus":1}`,
	} {
		if _, err := dispatchBuiltin(t, d, "project_structure", args); !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("expected ErrInvalidArguments for %s, got %v", args, err)
		}
	}
}
