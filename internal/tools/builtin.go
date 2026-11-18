package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"velo/internal/repository"
	"velo/internal/security"
)

const (
	defaultBuiltinMaxBytes = 1 << 20
	maxBuiltinMaxBytes     = 10 << 20
)

var (
	schemaReadFile = json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Workspace-relative file path."},
			"start_line": {"type": "integer", "description": "First line to return, 1-based and inclusive."},
			"end_line": {"type": "integer", "description": "Last line to return, 1-based and inclusive."},
			"max_bytes": {"type": "integer", "description": "Maximum number of bytes to read."}
		},
		"required": ["path"],
		"additionalProperties": false
	}`)

	schemaListFiles = json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Workspace-relative directory path."},
			"recursive": {"type": "boolean", "description": "Recurse into subdirectories."},
			"max_depth": {"type": "integer", "description": "Maximum recursion depth, 0 to 64."},
			"include": {"type": "string", "description": "Optional file name or glob filter."}
		},
		"additionalProperties": false
	}`)

	schemaSearchFiles = json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Text or regular expression to search for."},
			"regex": {"type": "boolean", "description": "Treat the pattern as a regular expression."},
			"case_sensitive": {"type": "boolean", "description": "Match case sensitively."},
			"include": {"type": "string", "description": "Optional file name or glob filter."},
			"max_results": {"type": "integer", "description": "Maximum number of matches, 1 to 1000."},
			"context_lines": {"type": "integer", "description": "Number of surrounding context lines, 0 to 3."}
		},
		"required": ["pattern"],
		"additionalProperties": false
	}`)

	schemaProjectStructure = json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Workspace-relative directory path."},
			"max_depth": {"type": "integer", "description": "Maximum tree depth, 1 to 10."},
			"max_entries": {"type": "integer", "description": "Maximum number of entries, 1 to 5000."}
		},
		"additionalProperties": false
	}`)
)

type BuiltinOptions struct {
	Repo     *repository.Repository
	Policy   *security.Policy
	MaxBytes int64
	// ReadMaxBytes is the default byte limit for one read_file call. It should
	// stay well below the context budget so a single read cannot force
	// immediate compaction. It defaults to MaxBytes when zero.
	ReadMaxBytes int64
	// Workspace is the Git working directory. It defaults to the policy
	// workspace when empty.
	Workspace string
}

func RegisterBuiltins(r *Registry, opts BuiltinOptions) error {
	if r == nil {
		return fmt.Errorf("tools: register builtins: %w: registry is required", ErrInvalidArguments)
	}
	if opts.Repo == nil {
		return fmt.Errorf("tools: register builtins: %w: repository is required", ErrInvalidArguments)
	}
	if opts.Policy == nil {
		return fmt.Errorf("tools: register builtins: %w: policy is required", ErrInvalidArguments)
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultBuiltinMaxBytes
	}
	if maxBytes > maxBuiltinMaxBytes {
		maxBytes = maxBuiltinMaxBytes
	}
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace == "" {
		workspace = opts.Policy.Workspace()
	}
	readMaxBytes := opts.ReadMaxBytes
	if readMaxBytes <= 0 || readMaxBytes > maxBytes {
		readMaxBytes = maxBytes
	}
	git := gitOptions{Repo: opts.Repo, Policy: opts.Policy, Workspace: workspace}
	builtins := []Tool{
		&readFileTool{repo: opts.Repo, policy: opts.Policy, maxBytes: readMaxBytes},
		&writeFileTool{repo: opts.Repo, policy: opts.Policy, maxBytes: maxBytes},
		&applyPatchTool{repo: opts.Repo, policy: opts.Policy, maxBytes: maxBytes},
		&listFilesTool{repo: opts.Repo, policy: opts.Policy},
		&searchFilesTool{repo: opts.Repo, policy: opts.Policy, maxBytes: maxBytes},
		&projectStructureTool{repo: opts.Repo, policy: opts.Policy},
		&runCommandTool{policy: opts.Policy},
		&gitStatusTool{opts: git},
		&gitDiffTool{opts: git},
		&gitLogTool{opts: git},
		&gitBranchTool{opts: git},
	}
	for _, t := range builtins {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	return nil
}

func decodeArgs(raw json.RawMessage, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if trimmed[0] != '{' {
		return fmt.Errorf("%w: arguments must be a json object", ErrInvalidArguments)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing data after arguments", ErrInvalidArguments)
	}
	return nil
}

func denyError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrDenied, err)
}

func normalizeRelative(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "."
	}
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")
	rel = path.Clean(rel)
	if rel == "" || rel == "." {
		return "."
	}
	return strings.TrimPrefix(rel, "./")
}

func resolveReadable(p *security.Policy, provided string) (string, error) {
	abs, err := p.EnsureReadable(provided)
	if err != nil {
		return "", denyError(err)
	}
	rel, err := p.Relative(abs)
	if err != nil {
		return "", denyError(err)
	}
	return normalizeRelative(rel), nil
}

type readFileArgs struct {
	Path      string `json:"path"`
	StartLine *int   `json:"start_line"`
	EndLine   *int   `json:"end_line"`
	MaxBytes  *int64 `json:"max_bytes"`
}

type readFileTool struct {
	repo     *repository.Repository
	policy   *security.Policy
	maxBytes int64
}

func (t *readFileTool) Definition() Definition {
	return Definition{
		Name:        "read_file",
		Description: "Read a file from the workspace, optionally restricted to a line range.",
		InputSchema: schemaReadFile,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *readFileTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args readFileArgs
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return Result{}, fmt.Errorf("%w: path is required", ErrInvalidArguments)
	}
	rel, err := resolveReadable(t.policy, args.Path)
	if err != nil {
		return Result{}, err
	}
	maxBytes := t.maxBytes
	if args.MaxBytes != nil {
		if *args.MaxBytes <= 0 {
			return Result{}, fmt.Errorf("%w: max_bytes must be positive", ErrInvalidArguments)
		}
		maxBytes = *args.MaxBytes
		if maxBytes > maxBuiltinMaxBytes {
			maxBytes = maxBuiltinMaxBytes
		}
	}
	start := 1
	end := 0
	ranged := args.StartLine != nil || args.EndLine != nil
	if args.StartLine != nil {
		if *args.StartLine < 1 {
			return Result{}, fmt.Errorf("%w: start_line must be at least 1", ErrInvalidArguments)
		}
		start = *args.StartLine
	}
	if args.EndLine != nil {
		if *args.EndLine < 0 {
			return Result{}, fmt.Errorf("%w: end_line must not be negative", ErrInvalidArguments)
		}
		if *args.EndLine != 0 {
			if *args.EndLine < start {
				return Result{}, fmt.Errorf("%w: end_line must be greater than or equal to start_line", ErrInvalidArguments)
			}
			end = *args.EndLine
		}
	}
	file, digest, err := t.repo.ReadFileHashed(rel, maxBytes)
	if err != nil {
		return Result{}, fmt.Errorf("read_file: %w", err)
	}
	lines := strings.Split(string(file.Content), "\n")
	if end == 0 || end > len(lines) {
		end = len(lines)
	}
	selected := 0
	if start <= end {
		selected = end - start + 1
	}
	var body strings.Builder
	if ranged {
		for i := start; i <= end; i++ {
			if i > start {
				body.WriteByte('\n')
			}
			body.WriteString(strconv.Itoa(i))
			body.WriteString(" | ")
			body.WriteString(lines[i-1])
		}
	} else {
		body.WriteString(string(file.Content))
	}
	truncated := file.Truncated
	out := rel + "\nsha256: " + digest + "\n" + body.String()
	if truncated {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "[truncated: limit " + strconv.FormatInt(maxBytes, 10) + " bytes]"
	}
	return Result{
		CallID:  call.ID,
		Content: out,
		Metadata: map[string]any{
			"path":      rel,
			"size":      file.Size,
			"lines":     selected,
			"sha256":    digest,
			"truncated": truncated,
		},
	}, nil
}

type listFilesArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	MaxDepth  *int   `json:"max_depth"`
	Include   string `json:"include"`
}

type listFilesTool struct {
	repo   *repository.Repository
	policy *security.Policy
}

func (t *listFilesTool) Definition() Definition {
	return Definition{
		Name:        "list_files",
		Description: "List files in a workspace directory.",
		InputSchema: schemaListFiles,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *listFilesTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args listFilesArgs
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		args.Path = "."
	}
	rel, err := resolveReadable(t.policy, args.Path)
	if err != nil {
		return Result{}, err
	}
	maxDepth := 0
	if args.MaxDepth != nil {
		if *args.MaxDepth < 0 || *args.MaxDepth > 64 {
			return Result{}, fmt.Errorf("%w: max_depth must be between 0 and 64", ErrInvalidArguments)
		}
		maxDepth = *args.MaxDepth
	}
	entries, total, err := t.repo.List(repository.ListOptions{
		Path:      rel,
		Recursive: args.Recursive,
		MaxDepth:  maxDepth,
		Include:   args.Include,
	})
	if err != nil {
		return Result{}, fmt.Errorf("list_files: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		line := filepath.ToSlash(entry.Path) + "\t" + strconv.FormatInt(entry.Size, 10) + " bytes"
		if entry.Secret {
			line += " [secret]"
		}
		if entry.Binary {
			line += " [binary]"
		}
		lines = append(lines, line)
	}
	content := "no files"
	if len(lines) > 0 {
		content = strings.Join(lines, "\n")
	}
	count := total
	if count <= 0 {
		count = len(entries)
	}
	return Result{
		CallID:   call.ID,
		Content:  content,
		Metadata: map[string]any{"count": count},
	}, nil
}

type searchFilesArgs struct {
	Pattern       string `json:"pattern"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"case_sensitive"`
	Include       string `json:"include"`
	MaxResults    *int   `json:"max_results"`
	ContextLines  *int   `json:"context_lines"`
}

type searchFilesTool struct {
	repo     *repository.Repository
	policy   *security.Policy
	maxBytes int64
}

func (t *searchFilesTool) Definition() Definition {
	return Definition{
		Name:        "search_files",
		Description: "Search workspace file contents for text or a regular expression.",
		InputSchema: schemaSearchFiles,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *searchFilesTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args searchFilesArgs
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	args.Pattern = strings.TrimSpace(args.Pattern)
	if args.Pattern == "" {
		return Result{}, fmt.Errorf("%w: pattern is required", ErrInvalidArguments)
	}
	maxResults := 200
	if args.MaxResults != nil {
		if *args.MaxResults < 1 || *args.MaxResults > 1000 {
			return Result{}, fmt.Errorf("%w: max_results must be between 1 and 1000", ErrInvalidArguments)
		}
		maxResults = *args.MaxResults
	}
	contextLines := 0
	if args.ContextLines != nil {
		if *args.ContextLines < 0 || *args.ContextLines > 3 {
			return Result{}, fmt.Errorf("%w: context_lines must be between 0 and 3", ErrInvalidArguments)
		}
		contextLines = *args.ContextLines
	}
	if _, err := t.policy.EnsureReadable(t.repo.Root); err != nil {
		return Result{}, denyError(err)
	}
	matches, truncated, err := t.repo.Search(repository.SearchOptions{
		Pattern:       args.Pattern,
		Regex:         args.Regex,
		CaseSensitive: args.CaseSensitive,
		Include:       args.Include,
		MaxResults:    maxResults,
		MaxFileBytes:  t.maxBytes,
		ContextLines:  contextLines,
	})
	if err != nil {
		return Result{}, fmt.Errorf("search_files: %w", err)
	}
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		lines = append(lines, filepath.ToSlash(match.Path)+":"+strconv.Itoa(match.Line)+":"+strconv.Itoa(match.Column)+": "+match.Text)
		for _, contextLine := range match.Context {
			lines = append(lines, "  "+contextLine)
		}
	}
	content := "no matches"
	if len(lines) > 0 {
		content = strings.Join(lines, "\n")
	}
	if truncated {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "[truncated: more matches]"
	}
	return Result{
		CallID:  call.ID,
		Content: content,
		Metadata: map[string]any{
			"matches":   len(matches),
			"truncated": truncated,
		},
	}, nil
}

type projectStructureArgs struct {
	Path       string `json:"path"`
	MaxDepth   *int   `json:"max_depth"`
	MaxEntries *int   `json:"max_entries"`
}

type projectStructureTool struct {
	repo   *repository.Repository
	policy *security.Policy
}

func (t *projectStructureTool) Definition() Definition {
	return Definition{
		Name:        "project_structure",
		Description: "Render a deterministic tree of the workspace directory structure.",
		InputSchema: schemaProjectStructure,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *projectStructureTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args projectStructureArgs
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		args.Path = "."
	}
	rel, err := resolveReadable(t.policy, args.Path)
	if err != nil {
		return Result{}, err
	}
	maxDepth := 6
	if args.MaxDepth != nil {
		if *args.MaxDepth < 1 || *args.MaxDepth > 10 {
			return Result{}, fmt.Errorf("%w: max_depth must be between 1 and 10", ErrInvalidArguments)
		}
		maxDepth = *args.MaxDepth
	}
	maxEntries := 2000
	if args.MaxEntries != nil {
		if *args.MaxEntries < 1 || *args.MaxEntries > 5000 {
			return Result{}, fmt.Errorf("%w: max_entries must be between 1 and 5000", ErrInvalidArguments)
		}
		maxEntries = *args.MaxEntries
	}
	nodes, truncated, err := t.repo.Structure(repository.StructureOptions{
		Path:       rel,
		MaxDepth:   maxDepth,
		MaxEntries: maxEntries,
	})
	if err != nil {
		return Result{}, fmt.Errorf("project_structure: %w", err)
	}
	content, files, bytesTotal := renderStructure(nodes, truncated)
	return Result{
		CallID:  call.ID,
		Content: content,
		Metadata: map[string]any{
			"files":     files,
			"bytes":     bytesTotal,
			"truncated": truncated,
		},
	}, nil
}

func renderStructure(nodes []repository.Node, truncated bool) (string, int, int64) {
	var b strings.Builder
	b.WriteString(".")
	stack := make([]bool, 0, 8)
	for i := range nodes {
		node := nodes[i]
		depth := node.Depth
		if depth < 1 {
			if node.Path == "." || node.Path == "" {
				continue
			}
			depth = 1
		}
		last := true
		for j := i + 1; j < len(nodes); j++ {
			nextDepth := nodes[j].Depth
			if nextDepth < 1 {
				if nodes[j].Path == "." || nodes[j].Path == "" {
					continue
				}
				nextDepth = 1
			}
			if nextDepth < depth {
				last = true
				break
			}
			if nextDepth == depth {
				last = false
				break
			}
		}
		if len(stack) > depth-1 {
			stack = stack[:depth-1]
		}
		for len(stack) < depth-1 {
			stack = append(stack, false)
		}
		var prefix strings.Builder
		for _, ancestorLast := range stack {
			if ancestorLast {
				prefix.WriteString("    ")
			} else {
				prefix.WriteString("│   ")
			}
		}
		if last {
			prefix.WriteString("└── ")
		} else {
			prefix.WriteString("├── ")
		}
		stack = append(stack, last)
		name := node.Name
		if name == "" {
			name = path.Base(filepath.ToSlash(node.Path))
		}
		b.WriteString("\n")
		b.WriteString(prefix.String())
		b.WriteString(name)
		if node.IsDir {
			b.WriteString("/")
			fmt.Fprintf(&b, " (%d files, %d bytes)", node.Files, node.Bytes)
		} else {
			fmt.Fprintf(&b, " (%d bytes)", node.Bytes)
		}
	}
	if truncated {
		b.WriteString("\n[truncated]")
	}
	minDepth := 0
	for _, node := range nodes {
		if node.Depth < 1 {
			continue
		}
		if minDepth == 0 || node.Depth < minDepth {
			minDepth = node.Depth
		}
	}
	files := 0
	var bytesTotal int64
	for _, node := range nodes {
		if node.Depth != minDepth {
			continue
		}
		if node.IsDir {
			files += node.Files
			bytesTotal += node.Bytes
		} else {
			files++
			bytesTotal += node.Bytes
		}
	}
	return b.String(), files, bytesTotal
}
