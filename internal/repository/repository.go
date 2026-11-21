package repository

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultMaxFileBytes int64 = 1 << 20
	maxMaxFileBytes     int64 = 10 << 20

	defaultSearchMaxResults = 200
	maxSearchResults        = 1000
	maxContextLines         = 3

	defaultStructureDepth   = 6
	maxStructureDepth       = 10
	defaultStructureEntries = 2000
	maxStructureEntries     = 5000
)

var (
	ErrOutsideRoot = errors.New("repository: path is outside workspace")
	ErrNotFound    = errors.New("repository: file not found")
	ErrNotFile     = errors.New("repository: not a regular file")
	ErrSecret      = errors.New("repository: protected secret file")
	ErrBinary      = errors.New("repository: binary file")
	ErrTooLarge    = errors.New("repository: file too large")
	ErrInvalidPath = errors.New("repository: invalid path")
)

type Options struct {
	Workspace    string
	Ignore       []string
	MaxFileBytes int64
}

type Repository struct {
	Root string

	ignores      []string
	maxFileBytes int64
}

type Entry struct {
	Path   string
	Size   int64
	Binary bool
	Secret bool
}

type File struct {
	Path      string
	Content   []byte
	Size      int64
	Binary    bool
	Truncated bool
}

type ListOptions struct {
	Path       string
	Recursive  bool
	MaxDepth   int
	Include    string
	MaxEntries int
}

type SearchOptions struct {
	Pattern       string
	Regex         bool
	CaseSensitive bool
	Include       string
	MaxResults    int
	MaxFileBytes  int64
	ContextLines  int
}

type Match struct {
	Path    string
	Line    int
	Column  int
	Text    string
	Context []string
}

type StructureOptions struct {
	Path       string
	MaxDepth   int
	MaxEntries int
}

type Node struct {
	Path  string
	Name  string
	Depth int
	IsDir bool
	Files int
	Bytes int64
}

func Open(opts Options) (*Repository, error) {
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace == "" {
		return nil, fmt.Errorf("repository: workspace is required: %w", ErrInvalidPath)
	}
	if strings.IndexByte(workspace, 0) >= 0 {
		return nil, fmt.Errorf("repository: workspace contains NUL byte: %w", ErrInvalidPath)
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("repository: resolve workspace %q: %w", workspace, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("repository: workspace %q does not exist: %w", abs, ErrNotFound)
		}
		return nil, fmt.Errorf("repository: stat workspace %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository: workspace %q is not a directory: %w", abs, ErrInvalidPath)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("repository: evaluate workspace %q: %w", abs, err)
	}
	limit := opts.MaxFileBytes
	if limit <= 0 {
		limit = defaultMaxFileBytes
	}
	if limit > maxMaxFileBytes {
		limit = maxMaxFileBytes
	}
	ignores := append([]string(nil), opts.Ignore...)
	ignores = append(ignores, readGitignore(root)...)
	return &Repository{
		Root:         filepath.Clean(root),
		ignores:      ignores,
		maxFileBytes: limit,
	}, nil
}

func (r *Repository) Entries() ([]Entry, error) {
	entries := make([]Entry, 0)
	err := r.walkTree(r.Root, ".", func(rel, abs string, info os.FileInfo) error {
		entries = append(entries, r.entryFor(rel, abs, info))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortEntries(entries)
	return entries, nil
}

func (r *Repository) Files() ([]Entry, error) {
	entries, err := r.Entries()
	if err != nil {
		return nil, err
	}
	files := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Binary || entry.Secret {
			continue
		}
		files = append(files, entry)
	}
	return files, nil
}

func (r *Repository) Find(name string) ([]Entry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("repository: empty name: %w", ErrInvalidPath)
	}
	if strings.IndexByte(name, 0) >= 0 {
		return nil, fmt.Errorf("repository: name contains NUL byte: %w", ErrInvalidPath)
	}
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "./")
	entries, err := r.Entries()
	if err != nil {
		return nil, err
	}
	matches := make([]Entry, 0)
	for _, entry := range entries {
		if entry.Path == name || path.Base(entry.Path) == name || strings.HasSuffix(entry.Path, "/"+name) {
			matches = append(matches, entry)
		}
	}
	return matches, nil
}

func (r *Repository) normalize(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("repository: empty path: %w", ErrInvalidPath)
	}
	if strings.IndexByte(rel, 0) >= 0 {
		return "", fmt.Errorf("repository: path contains NUL byte: %w", ErrInvalidPath)
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	native := filepath.FromSlash(rel)
	if path.IsAbs(rel) || filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, rel)
	}
	clean := path.Clean(rel)
	if clean == "." {
		return ".", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, rel)
	}
	return clean, nil
}

func (r *Repository) normalizeDir(rel string) (string, error) {
	if rel == "" {
		return ".", nil
	}
	return r.normalize(rel)
}

func (r *Repository) absFor(clean string) (string, error) {
	abs := r.Root
	if clean != "." {
		abs = filepath.Join(r.Root, filepath.FromSlash(clean))
	}
	inside, err := within(r.Root, abs)
	if err != nil {
		return "", fmt.Errorf("repository: check containment for %q: %w", clean, err)
	}
	if !inside {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, clean)
	}
	return abs, nil
}

func (r *Repository) safeAbs(clean string) (string, error) {
	abs, err := r.absFor(clean)
	if err != nil {
		return "", err
	}
	if clean == "." {
		return abs, nil
	}
	current := r.Root
	for _, part := range strings.Split(clean, "/") {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if err != nil {
			break
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(current)
		if err != nil {
			return "", fmt.Errorf("%w: %q is an unresolvable symlink", ErrOutsideRoot, clean)
		}
		inside, err := within(r.Root, target)
		if err != nil || !inside {
			return "", fmt.Errorf("%w: %q is a symlink escape", ErrOutsideRoot, clean)
		}
	}
	return abs, nil
}

func within(root, target string) (bool, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, nil
	}
	if rel == "." {
		return true, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false, nil
	}
	return true, nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
}
