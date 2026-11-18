package repository

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeRepoFile(t *testing.T, root, rel, content string) {
	t.Helper()
	writeRepoBytes(t, root, rel, []byte(content))
}

func writeRepoBytes(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func makeRepoDir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
}

func openRepo(t *testing.T, root string) *Repository {
	t.Helper()
	repo, err := Open(Options{Workspace: root, NoGit: true})
	if err != nil {
		t.Fatalf("Open(%q): %v", root, err)
	}
	return repo
}

func entryPaths(entries []Entry) []string {
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	return paths
}

func nodePaths(nodes []Node) []string {
	paths := make([]string, len(nodes))
	for i, node := range nodes {
		paths[i] = node.Path
	}
	return paths
}

func TestOpenValidatesWorkspace(t *testing.T) {
	dir := t.TempDir()
	repo, err := Open(Options{Workspace: dir, NoGit: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if repo.Root == "" || !filepath.IsAbs(repo.Root) {
		t.Fatalf("Root must be absolute, got %q", repo.Root)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if filepath.Clean(repo.Root) != filepath.Clean(resolved) {
		t.Fatalf("Root = %q, want %q", repo.Root, resolved)
	}

	for _, workspace := range []string{"", "   ", dir + "\x00"} {
		if _, err := Open(Options{Workspace: workspace}); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Open(%q) = %v, want ErrInvalidPath", workspace, err)
		}
	}
	file := filepath.Join(dir, "file.txt")
	writeRepoFile(t, dir, "file.txt", "x")
	if _, err := Open(Options{Workspace: file}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Open(file) = %v, want ErrInvalidPath", err)
	}
	if _, err := Open(Options{Workspace: filepath.Join(dir, "missing")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open(missing) = %v, want ErrNotFound", err)
	}
}

func TestOpenCopiesIgnorePatterns(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "keep.txt", "keep")
	writeRepoFile(t, dir, "drop.bak", "drop")
	patterns := []string{"*.bak"}
	repo, err := Open(Options{Workspace: dir, NoGit: true, Ignore: patterns})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	patterns[0] = "*.txt"
	entries, err := repo.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if got := entryPaths(entries); !reflect.DeepEqual(got, []string{"keep.txt"}) {
		t.Fatalf("entries = %v, want [keep.txt]", got)
	}
}

func TestListNonRecursiveRecursiveInclude(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "alpha\n")
	writeRepoFile(t, dir, "notes.md", "# notes\n")
	writeRepoFile(t, dir, "sub/b.txt", "beta\n")
	writeRepoFile(t, dir, "sub/deep/c.txt", "gamma\n")
	repo := openRepo(t, dir)

	entries, total, err := repo.List(ListOptions{Path: "."})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(entryPaths(entries), []string{"a.txt", "notes.md"}) {
		t.Fatalf("non-recursive entries = %v", entryPaths(entries))
	}
	if total != 2 {
		t.Fatalf("non-recursive total = %d, want 2", total)
	}

	entries, total, err = repo.List(ListOptions{Path: ".", Recursive: true})
	if err != nil {
		t.Fatalf("List recursive: %v", err)
	}
	want := []string{"a.txt", "notes.md", "sub/b.txt", "sub/deep/c.txt"}
	if !reflect.DeepEqual(entryPaths(entries), want) {
		t.Fatalf("recursive entries = %v, want %v", entryPaths(entries), want)
	}
	if total != 4 {
		t.Fatalf("recursive total = %d, want 4", total)
	}

	entries, total, err = repo.List(ListOptions{Path: ".", Recursive: true, Include: "notes.md"})
	if err != nil {
		t.Fatalf("List include: %v", err)
	}
	if !reflect.DeepEqual(entryPaths(entries), []string{"notes.md"}) || total != 1 {
		t.Fatalf("include entries = %v total = %d", entryPaths(entries), total)
	}

	entries, _, err = repo.List(ListOptions{Path: ".", Recursive: true, Include: "*.txt"})
	if err != nil {
		t.Fatalf("List glob include: %v", err)
	}
	want = []string{"a.txt", "sub/b.txt", "sub/deep/c.txt"}
	if !reflect.DeepEqual(entryPaths(entries), want) {
		t.Fatalf("glob entries = %v, want %v", entryPaths(entries), want)
	}

	entries, total, err = repo.List(ListOptions{Path: "sub"})
	if err != nil {
		t.Fatalf("List sub: %v", err)
	}
	if !reflect.DeepEqual(entryPaths(entries), []string{"sub/b.txt"}) || total != 1 {
		t.Fatalf("sub entries = %v total = %d", entryPaths(entries), total)
	}

	entries, total, err = repo.List(ListOptions{Path: ".", Recursive: true, MaxEntries: 1})
	if err != nil {
		t.Fatalf("List max entries: %v", err)
	}
	if len(entries) != 1 || total != 4 {
		t.Fatalf("max entries len = %d total = %d", len(entries), total)
	}

	entries, total, err = repo.List(ListOptions{Path: ".", Recursive: true, MaxDepth: 2})
	if err != nil {
		t.Fatalf("List max depth: %v", err)
	}
	want = []string{"a.txt", "notes.md", "sub/b.txt"}
	if !reflect.DeepEqual(entryPaths(entries), want) || total != 3 {
		t.Fatalf("max depth entries = %v total = %d", entryPaths(entries), total)
	}

	if _, _, err := repo.List(ListOptions{Path: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("List(missing) = %v, want ErrNotFound", err)
	}
	if _, _, err := repo.List(ListOptions{Path: "a.txt"}); !errors.Is(err, ErrNotFile) {
		t.Fatalf("List(file) = %v, want ErrNotFile", err)
	}
}

func TestReadFileTruncationAndRejections(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "hello world")
	writeRepoFile(t, dir, "empty.txt", "")
	writeRepoFile(t, dir, ".env", "TOKEN=secret")
	writeRepoFile(t, dir, "certs/server.pem", "key")
	writeRepoBytes(t, dir, "bin.dat", []byte{0x01, 0x00, 0x02})
	writeRepoFile(t, dir, "sub/b.txt", "beta")
	repo := openRepo(t, dir)

	file, err := repo.ReadFile("a.txt", 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(file.Content) != "hello world" || file.Size != 11 || file.Truncated || file.Binary {
		t.Fatalf("unexpected file: %+v content=%q", file, file.Content)
	}
	if file.Path != "a.txt" {
		t.Fatalf("path = %q", file.Path)
	}

	file, err = repo.ReadFile("a.txt", 5)
	if err != nil {
		t.Fatalf("ReadFile truncated: %v", err)
	}
	if string(file.Content) != "hello" || file.Size != 11 || !file.Truncated {
		t.Fatalf("unexpected truncated file: %+v content=%q", file, file.Content)
	}

	file, err = repo.ReadFile("empty.txt", 0)
	if err != nil {
		t.Fatalf("ReadFile empty: %v", err)
	}
	if len(file.Content) != 0 || file.Size != 0 || file.Truncated {
		t.Fatalf("unexpected empty file: %+v", file)
	}

	file, err = repo.ReadFile("sub\\b.txt", 0)
	if err != nil || string(file.Content) != "beta" {
		t.Fatalf("ReadFile backslash path = %+v, %v", file, err)
	}

	if _, err := repo.ReadFile("missing.txt", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadFile(missing) = %v, want ErrNotFound", err)
	}
	if _, err := repo.ReadFile("sub", 0); !errors.Is(err, ErrNotFile) {
		t.Fatalf("ReadFile(dir) = %v, want ErrNotFile", err)
	}
	if _, err := repo.ReadFile(".env", 0); !errors.Is(err, ErrSecret) {
		t.Fatalf("ReadFile(.env) = %v, want ErrSecret", err)
	}
	if _, err := repo.ReadFile("certs/server.pem", 0); !errors.Is(err, ErrSecret) {
		t.Fatalf("ReadFile(pem) = %v, want ErrSecret", err)
	}
	file, err = repo.ReadFile("bin.dat", 0)
	if !errors.Is(err, ErrBinary) {
		t.Fatalf("ReadFile(binary) = %v, want ErrBinary", err)
	}
	if !file.Binary {
		t.Fatalf("binary file flag not set: %+v", file)
	}
	writeRepoBytes(t, dir, "bin2.dat", []byte{'a', 'b', 0, 'c'})
	if _, err := repo.ReadFile("bin2.dat", 2); !errors.Is(err, ErrBinary) {
		t.Fatalf("ReadFile(binary beyond limit) = %v, want ErrBinary", err)
	}

	big := strings.Repeat("x", int(defaultMaxFileBytes)+64)
	writeRepoFile(t, dir, "big.txt", big)
	file, err = repo.ReadFile("big.txt", 0)
	if err != nil {
		t.Fatalf("ReadFile big: %v", err)
	}
	if int64(len(file.Content)) != defaultMaxFileBytes || !file.Truncated || file.Size != int64(len(big)) {
		t.Fatalf("big file len=%d size=%d truncated=%v", len(file.Content), file.Size, file.Truncated)
	}
	huge := strings.Repeat("y", int(maxMaxFileBytes)+1024)
	writeRepoFile(t, dir, "huge.txt", huge)
	file, err = repo.ReadFile("huge.txt", maxMaxFileBytes+1024)
	if err != nil {
		t.Fatalf("ReadFile huge: %v", err)
	}
	if int64(len(file.Content)) != maxMaxFileBytes || !file.Truncated {
		t.Fatalf("huge file cap len=%d truncated=%v", len(file.Content), file.Truncated)
	}
}

func TestReadFilePathEscapes(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "a")
	writeRepoFile(t, outside, "outside.txt", "outside")
	repo := openRepo(t, dir)

	for _, rel := range []string{"../outside.txt", filepath.Join(outside, "outside.txt"), "..\\outside.txt"} {
		if _, err := repo.ReadFile(rel, 0); !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("ReadFile(%q) = %v, want ErrOutsideRoot", rel, err)
		}
	}
	for _, rel := range []string{"", "a\x00b"} {
		if _, err := repo.ReadFile(rel, 0); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("ReadFile(%q) = %v, want ErrInvalidPath", rel, err)
		}
	}
}

func TestSearchLiteralRegexCaseContextInclude(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "Hello world\nsecond HELLO line\nxx needle here\n")
	writeRepoFile(t, dir, "sub/b.txt", "first\nneedle two\nlast\n")
	writeRepoBytes(t, dir, "bin.dat", []byte("needle\x00binary"))
	writeRepoFile(t, dir, ".env", "needle=secret")
	writeRepoFile(t, dir, "big.txt", strings.Repeat("a", 2000)+"\nneedle\n")
	writeRepoFile(t, dir, ".git/config", "needle\n")
	writeRepoFile(t, dir, "skip.log", "needle log\n")
	writeRepoFile(t, dir, ".gitignore", "*.log\n")
	repo := openRepo(t, dir)

	matches, truncated, err := repo.Search(SearchOptions{Pattern: "hello"})
	if err != nil {
		t.Fatalf("Search literal: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(matches) != 2 || matches[0].Path != "a.txt" || matches[0].Line != 1 || matches[0].Column != 1 {
		t.Fatalf("literal matches = %+v", matches)
	}
	if matches[1].Line != 2 || matches[1].Text != "second HELLO line" {
		t.Fatalf("second match = %+v", matches[1])
	}

	matches, _, err = repo.Search(SearchOptions{Pattern: "Hello", CaseSensitive: true})
	if err != nil {
		t.Fatalf("Search case sensitive: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 1 {
		t.Fatalf("case sensitive matches = %+v", matches)
	}

	matches, _, err = repo.Search(SearchOptions{Pattern: `needle\s+\w+`, Regex: true})
	if err != nil {
		t.Fatalf("Search regex: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("regex matches = %+v", matches)
	}
	if matches[0].Path != "a.txt" || matches[0].Text != "xx needle here" || matches[0].Column != 4 {
		t.Fatalf("regex first match = %+v", matches[0])
	}
	if matches[1].Path != "sub/b.txt" || matches[1].Line != 2 {
		t.Fatalf("regex second match = %+v", matches[1])
	}

	matches, _, err = repo.Search(SearchOptions{Pattern: "needle", Include: "*.txt"})
	if err != nil {
		t.Fatalf("Search include: %v", err)
	}
	if len(matches) != 3 || matches[0].Path != "a.txt" || matches[1].Path != "big.txt" || matches[2].Path != "sub/b.txt" {
		t.Fatalf("include matches = %+v", matches)
	}

	matches, _, err = repo.Search(SearchOptions{Pattern: "needle", Include: "sub/*.txt", ContextLines: 1})
	if err != nil {
		t.Fatalf("Search context: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("context matches = %+v", matches)
	}
	if !reflect.DeepEqual(matches[0].Context, []string{"first", "last"}) {
		t.Fatalf("context = %v", matches[0].Context)
	}

	matches, truncated, err = repo.Search(SearchOptions{Pattern: "needle", Include: "big.txt", MaxFileBytes: 100})
	if err != nil {
		t.Fatalf("Search oversized: %v", err)
	}
	if len(matches) != 0 || truncated {
		t.Fatalf("oversized matches = %+v truncated=%v", matches, truncated)
	}

	matches, _, err = repo.Search(SearchOptions{Pattern: "needle", Include: ".env"})
	if err != nil {
		t.Fatalf("Search secret: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("secret matches = %+v", matches)
	}

	matches, _, err = repo.Search(SearchOptions{Pattern: "needle", Include: "*.dat"})
	if err != nil {
		t.Fatalf("Search binary: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("binary matches = %+v", matches)
	}

	matches, _, err = repo.Search(SearchOptions{Pattern: "needle", MaxResults: 2})
	if err != nil {
		t.Fatalf("Search max results: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("max results matches = %+v", matches)
	}

	matches, truncated, err = repo.Search(SearchOptions{Pattern: "needle", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search truncation: %v", err)
	}
	if len(matches) != 1 || !truncated {
		t.Fatalf("truncation matches = %+v truncated=%v", matches, truncated)
	}

	if _, _, err := repo.Search(SearchOptions{Pattern: "[", Regex: true}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid regex = %v, want ErrInvalidPath", err)
	}
	if _, _, err := repo.Search(SearchOptions{Pattern: ""}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("empty pattern = %v, want ErrInvalidPath", err)
	}
}

func TestSearchSkipsIgnoredAndDefaults(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "visible.txt", "target\n")
	writeRepoFile(t, dir, "skip.log", "target\n")
	writeRepoFile(t, dir, ".git/config", "target\n")
	writeRepoFile(t, dir, ".velo/state", "target\n")
	writeRepoFile(t, dir, "node_modules/pkg/index.js", "target\n")
	writeRepoFile(t, dir, ".gitignore", "*.log\n")
	repo := openRepo(t, dir)

	matches, _, err := repo.Search(SearchOptions{Pattern: "target"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !reflect.DeepEqual(matchPaths(matches), []string{"visible.txt"}) {
		t.Fatalf("matches = %+v", matches)
	}
}

func matchPaths(matches []Match) []string {
	paths := make([]string, len(matches))
	for i, match := range matches {
		paths[i] = match.Path
	}
	return paths
}

func TestStructureDepthsAggregationTruncation(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "aaaa")
	writeRepoFile(t, dir, "sub/b.txt", "bb")
	writeRepoFile(t, dir, "sub/deep/c.txt", "ccc")
	makeRepoDir(t, dir, "empty")
	makeRepoDir(t, dir, ".git")
	writeRepoFile(t, dir, ".git/HEAD", "ref")
	repo := openRepo(t, dir)

	nodes, truncated, err := repo.Structure(StructureOptions{Path: "."})
	if err != nil {
		t.Fatalf("Structure: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	wantPaths := []string{"a.txt", "empty", "sub", "sub/b.txt", "sub/deep", "sub/deep/c.txt"}
	if !reflect.DeepEqual(nodePaths(nodes), wantPaths) {
		t.Fatalf("nodes = %v, want %v", nodePaths(nodes), wantPaths)
	}
	wantDepth := []int{1, 1, 1, 2, 2, 3}
	for i, node := range nodes {
		if node.Depth != wantDepth[i] {
			t.Fatalf("node %q depth = %d, want %d", node.Path, node.Depth, wantDepth[i])
		}
	}
	index := map[string]Node{}
	for _, node := range nodes {
		index[node.Path] = node
	}
	if !index["empty"].IsDir || index["empty"].Files != 0 || index["empty"].Bytes != 0 {
		t.Fatalf("empty node = %+v", index["empty"])
	}
	if !index["sub"].IsDir || index["sub"].Files != 2 || index["sub"].Bytes != 5 {
		t.Fatalf("sub node = %+v", index["sub"])
	}
	if !index["sub/deep"].IsDir || index["sub/deep"].Files != 1 || index["sub/deep"].Bytes != 3 {
		t.Fatalf("deep node = %+v", index["sub/deep"])
	}
	if index["a.txt"].IsDir || index["a.txt"].Files != 0 || index["a.txt"].Bytes != 4 {
		t.Fatalf("file node = %+v", index["a.txt"])
	}
	if index["a.txt"].Name != "a.txt" || index["sub/deep"].Name != "deep" {
		t.Fatalf("names = %q %q", index["a.txt"].Name, index["sub/deep"].Name)
	}

	nodes, truncated, err = repo.Structure(StructureOptions{Path: ".", MaxDepth: 1})
	if err != nil {
		t.Fatalf("Structure depth 1: %v", err)
	}
	if !reflect.DeepEqual(nodePaths(nodes), []string{"a.txt", "empty", "sub"}) || !truncated {
		t.Fatalf("depth 1 nodes = %v truncated=%v", nodePaths(nodes), truncated)
	}

	nodes, truncated, err = repo.Structure(StructureOptions{Path: ".", MaxDepth: 2})
	if err != nil {
		t.Fatalf("Structure depth 2: %v", err)
	}
	wantPaths = []string{"a.txt", "empty", "sub", "sub/b.txt", "sub/deep"}
	if !reflect.DeepEqual(nodePaths(nodes), wantPaths) || !truncated {
		t.Fatalf("depth 2 nodes = %v truncated=%v", nodePaths(nodes), truncated)
	}

	nodes, truncated, err = repo.Structure(StructureOptions{Path: ".", MaxDepth: 3})
	if err != nil {
		t.Fatalf("Structure depth 3: %v", err)
	}
	if len(nodes) != 6 || truncated {
		t.Fatalf("depth 3 nodes = %v truncated=%v", nodePaths(nodes), truncated)
	}

	nodes, truncated, err = repo.Structure(StructureOptions{Path: ".", MaxEntries: 2})
	if err != nil {
		t.Fatalf("Structure max entries: %v", err)
	}
	if !reflect.DeepEqual(nodePaths(nodes), []string{"a.txt", "empty"}) || !truncated {
		t.Fatalf("max entries nodes = %v truncated=%v", nodePaths(nodes), truncated)
	}

	nodes, truncated, err = repo.Structure(StructureOptions{Path: "sub"})
	if err != nil {
		t.Fatalf("Structure sub: %v", err)
	}
	wantPaths = []string{"sub/b.txt", "sub/deep", "sub/deep/c.txt"}
	if !reflect.DeepEqual(nodePaths(nodes), wantPaths) || truncated {
		t.Fatalf("sub nodes = %v truncated=%v", nodePaths(nodes), truncated)
	}
	if nodes[0].Depth != 1 || nodes[1].Depth != 1 || nodes[2].Depth != 2 {
		t.Fatalf("sub depths = %d %d %d", nodes[0].Depth, nodes[1].Depth, nodes[2].Depth)
	}

	if _, _, err := repo.Structure(StructureOptions{Path: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Structure(missing) = %v, want ErrNotFound", err)
	}
	if _, _, err := repo.Structure(StructureOptions{Path: "a.txt"}); !errors.Is(err, ErrNotFile) {
		t.Fatalf("Structure(file) = %v, want ErrNotFile", err)
	}
}

func TestIgnoreRules(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "keep.txt", "keep")
	writeRepoFile(t, dir, "keep.log", "keep log")
	writeRepoFile(t, dir, "skip.log", "skip log")
	writeRepoFile(t, dir, "custom.bak", "bak")
	writeRepoFile(t, dir, "tmp/cache.dat", "cache")
	writeRepoFile(t, dir, ".git/config", "git")
	writeRepoFile(t, dir, "node_modules/pkg/index.js", "js")
	writeRepoFile(t, dir, "vendor/lib.go", "go")
	writeRepoFile(t, dir, ".velo/state.json", "{}")
	writeRepoFile(t, dir, "dist/out.js", "js")
	writeRepoFile(t, dir, "build/obj.o", "obj")
	writeRepoFile(t, dir, "__pycache__/m.pyc", "pyc")
	writeRepoFile(t, dir, ".gitignore", "*.log\n!keep.log\ntmp/\n")

	repo, err := Open(Options{Workspace: dir, NoGit: true, Ignore: []string{"*.bak"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entries, err := repo.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	want := []string{".gitignore", "keep.log", "keep.txt"}
	if !reflect.DeepEqual(entryPaths(entries), want) {
		t.Fatalf("entries = %v, want %v", entryPaths(entries), want)
	}
	files, err := repo.Files()
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if !reflect.DeepEqual(entryPaths(files), want) {
		t.Fatalf("files = %v, want %v", entryPaths(files), want)
	}
}

func TestEntriesFilesFindFlags(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "alpha")
	writeRepoFile(t, dir, "notes.md", "# notes")
	writeRepoFile(t, dir, "sub/b.txt", "beta")
	writeRepoFile(t, dir, ".env", "SECRET=1")
	writeRepoFile(t, dir, "certs/server.pem", "pem")
	writeRepoBytes(t, dir, "bin.dat", []byte{'x', 0x00, 'y'})
	repo := openRepo(t, dir)

	entries, err := repo.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	flags := map[string]Entry{}
	for _, entry := range entries {
		flags[entry.Path] = entry
	}
	if !flags[".env"].Secret || !flags["certs/server.pem"].Secret {
		t.Fatalf("secret flags missing: %+v", flags)
	}
	if !flags["bin.dat"].Binary {
		t.Fatalf("binary flag missing: %+v", flags["bin.dat"])
	}

	files, err := repo.Files()
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := []string{"a.txt", "notes.md", "sub/b.txt"}
	if !reflect.DeepEqual(entryPaths(files), want) {
		t.Fatalf("files = %v, want %v", entryPaths(files), want)
	}

	for name, expected := range map[string][]string{
		"b.txt":     {"sub/b.txt"},
		"sub/b.txt": {"sub/b.txt"},
		"a.txt":     {"a.txt"},
		"notes.md":  {"notes.md"},
	} {
		found, err := repo.Find(name)
		if err != nil {
			t.Fatalf("Find(%q): %v", name, err)
		}
		if !reflect.DeepEqual(entryPaths(found), expected) {
			t.Fatalf("Find(%q) = %v, want %v", name, entryPaths(found), expected)
		}
	}
	if found, err := repo.Find("nope.txt"); err != nil || len(found) != 0 {
		t.Fatalf("Find(nope) = %v, %v", found, err)
	}
	if _, err := repo.Find(""); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Find(empty) = %v, want ErrInvalidPath", err)
	}
}

func TestSymlinkEntriesSkipped(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeRepoFile(t, dir, "visible.txt", "visible")
	writeRepoFile(t, outside, "outside.txt", "outside")
	makeRepoDir(t, outside, "outside-dir")
	writeRepoFile(t, outside, "outside-dir/nested.txt", "nested")

	outsideFile := filepath.Join(outside, "outside.txt")
	outsideDir := filepath.Join(outside, "outside-dir")
	if err := os.Symlink(outsideFile, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(dir, "linkdir")); err != nil {
		t.Skipf("directory symlink creation unavailable: %v", err)
	}
	writeRepoFile(t, dir, "inside.txt", "inside")
	if err := os.Symlink(filepath.Join(dir, "inside.txt"), filepath.Join(dir, "inside-link.txt")); err != nil {
		t.Skipf("file symlink creation unavailable: %v", err)
	}

	repo := openRepo(t, dir)
	entries, err := repo.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if !reflect.DeepEqual(entryPaths(entries), []string{"inside.txt", "visible.txt"}) {
		t.Fatalf("entries = %v", entryPaths(entries))
	}

	listed, _, err := repo.List(ListOptions{Path: ".", Recursive: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(entryPaths(listed), []string{"inside.txt", "visible.txt"}) {
		t.Fatalf("listed = %v", entryPaths(listed))
	}

	nodes, _, err := repo.Structure(StructureOptions{Path: "."})
	if err != nil {
		t.Fatalf("Structure: %v", err)
	}
	if !reflect.DeepEqual(nodePaths(nodes), []string{"inside.txt", "visible.txt"}) {
		t.Fatalf("nodes = %v", nodePaths(nodes))
	}

	if _, err := repo.ReadFile("link.txt", 0); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("ReadFile(link.txt) = %v, want ErrOutsideRoot", err)
	}
	if _, err := repo.ReadFile("inside-link.txt", 0); !errors.Is(err, ErrNotFile) {
		t.Fatalf("ReadFile(inside-link.txt) = %v, want ErrNotFile", err)
	}
}

func TestPathEscapeRejection(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "a")
	writeRepoFile(t, outside, "outside.txt", "outside")
	repo := openRepo(t, dir)

	for _, rel := range []string{"..", "../outside.txt", filepath.Join(outside, "outside.txt"), "sub/../../x"} {
		if _, _, err := repo.List(ListOptions{Path: rel}); !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("List(%q) = %v, want ErrOutsideRoot", rel, err)
		}
		if _, _, err := repo.Structure(StructureOptions{Path: rel}); !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("Structure(%q) = %v, want ErrOutsideRoot", rel, err)
		}
	}
	for _, rel := range []string{"a\x00b", "../a\x00b"} {
		if _, _, err := repo.List(ListOptions{Path: rel}); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("List(%q) = %v, want ErrInvalidPath", rel, err)
		}
		if _, _, err := repo.Structure(StructureOptions{Path: rel}); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Structure(%q) = %v, want ErrInvalidPath", rel, err)
		}
		if _, err := repo.ReadFile(rel, 0); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("ReadFile(%q) = %v, want ErrInvalidPath", rel, err)
		}
	}

	entries, total, err := repo.List(ListOptions{})
	if err != nil {
		t.Fatalf("List default path: %v", err)
	}
	if !reflect.DeepEqual(entryPaths(entries), []string{"a.txt"}) || total != 1 {
		t.Fatalf("default list = %v total=%d", entryPaths(entries), total)
	}
	nodes, _, err := repo.Structure(StructureOptions{})
	if err != nil {
		t.Fatalf("Structure default path: %v", err)
	}
	if !reflect.DeepEqual(nodePaths(nodes), []string{"a.txt"}) {
		t.Fatalf("default structure = %v", nodePaths(nodes))
	}
}
