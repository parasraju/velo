package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"velo/internal/repository"
	"velo/internal/security"
)

var (
	schemaGitStatus = json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Optional workspace-relative path filter."}
		},
		"additionalProperties": false
	}`)
	schemaGitDiff = json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Optional workspace-relative path filter."},
			"staged": {"type": "boolean", "description": "Show staged changes instead of the working tree."},
			"context_lines": {"type": "integer", "description": "Unified diff context lines, 0 to 20."}
		},
		"additionalProperties": false
	}`)
	schemaGitLog = json.RawMessage(`{
		"type": "object",
		"properties": {
			"limit": {"type": "integer", "description": "Maximum commits to return, 1 to 100."},
			"path": {"type": "string", "description": "Optional workspace-relative path filter."},
			"format": {"type": "string", "description": "log, oneline, or short."}
		},
		"additionalProperties": false
	}`)
	schemaGitBranch = json.RawMessage(`{
		"type": "object",
		"properties": {
			"all": {"type": "boolean", "description": "Include remote-tracking branches."}
		},
		"additionalProperties": false
	}`)
)

type gitOptions struct {
	Repo   *repository.Repository
	Policy *security.Policy
	// Workspace is the repository root used as the git working directory.
	Workspace string
}

type gitStatusTool struct{ opts gitOptions }

func (t *gitStatusTool) Definition() Definition {
	return Definition{
		Name:        "git_status",
		Description: "Show the Git working tree status, including the current branch.",
		InputSchema: schemaGitStatus,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *gitStatusTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	argv := []string{"status", "--short", "--branch"}
	pathArg, err := gitPath(t.opts, args.Path)
	if err != nil {
		return Result{}, err
	}
	if pathArg != "" {
		argv = append(argv, "--", pathArg)
	}
	out, stderr, code, err := runGit(ctx, t.opts.Workspace, argv...)
	if err != nil {
		return Result{}, err
	}
	out = redactSecretStatus(out)
	return gitResult(call, "git_status", argv, out, stderr, code), nil
}

type gitDiffTool struct{ opts gitOptions }

func (t *gitDiffTool) Definition() Definition {
	return Definition{
		Name:        "git_diff",
		Description: "Show the Git diff for the working tree or the index.",
		InputSchema: schemaGitDiff,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *gitDiffTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Path         string `json:"path"`
		Staged       bool   `json:"staged"`
		ContextLines *int   `json:"context_lines"`
	}
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	argv := []string{"diff", "--no-ext-diff", "--no-textconv"}
	if args.Staged {
		argv = append(argv, "--cached")
	}
	if args.ContextLines != nil {
		if *args.ContextLines < 0 || *args.ContextLines > 20 {
			return Result{}, fmt.Errorf("%w: context_lines must be between 0 and 20", ErrInvalidArguments)
		}
		argv = append(argv, "-U"+strconv.Itoa(*args.ContextLines))
	}
	pathArg, err := gitPath(t.opts, args.Path)
	if err != nil {
		return Result{}, err
	}
	if pathArg != "" {
		argv = append(argv, "--", pathArg)
	} else {
		excludes, err := gitSecretDiffPaths(ctx, t.opts.Workspace, args.Staged)
		if err != nil {
			return Result{}, err
		}
		if len(excludes) > 0 {
			argv = append(argv, "--")
			for _, name := range excludes {
				argv = append(argv, ":(exclude,literal)"+name)
			}
		}
	}
	out, stderr, code, err := runGit(ctx, t.opts.Workspace, argv...)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(out) == "" {
		out = "(no changes)"
	}
	return gitResult(call, "git_diff", argv, out, stderr, code), nil
}

type gitLogTool struct{ opts gitOptions }

func (t *gitLogTool) Definition() Definition {
	return Definition{
		Name:        "git_log",
		Description: "Show recent commits in the repository.",
		InputSchema: schemaGitLog,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *gitLogTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Limit  *int   `json:"limit"`
		Path   string `json:"path"`
		Format string `json:"format"`
	}
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	limit := 20
	if args.Limit != nil {
		if *args.Limit < 1 || *args.Limit > 100 {
			return Result{}, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidArguments)
		}
		limit = *args.Limit
	}
	format := strings.ToLower(strings.TrimSpace(args.Format))
	if format == "" {
		format = "log"
	}
	var pretty string
	switch format {
	case "log":
		pretty = "medium"
	case "oneline":
		pretty = "oneline"
	case "short":
		pretty = "short"
	default:
		return Result{}, fmt.Errorf("%w: format must be log, oneline, or short", ErrInvalidArguments)
	}
	argv := []string{"log", "-n", strconv.Itoa(limit), "--pretty=" + pretty, "--no-color"}
	pathArg, err := gitPath(t.opts, args.Path)
	if err != nil {
		return Result{}, err
	}
	if pathArg != "" {
		argv = append(argv, "--", pathArg)
	}
	out, stderr, code, err := runGit(ctx, t.opts.Workspace, argv...)
	if err != nil {
		return Result{}, err
	}
	return gitResult(call, "git_log", argv, out, stderr, code), nil
}

type gitBranchTool struct{ opts gitOptions }

func (t *gitBranchTool) Definition() Definition {
	return Definition{
		Name:        "git_branch",
		Description: "List local Git branches, optionally including remote-tracking branches.",
		InputSchema: schemaGitBranch,
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func (t *gitBranchTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args struct {
		All bool `json:"all"`
	}
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	argv := []string{"branch", "--no-color"}
	if args.All {
		argv = append(argv, "--all")
	}
	out, stderr, code, err := runGit(ctx, t.opts.Workspace, argv...)
	if err != nil {
		return Result{}, err
	}
	return gitResult(call, "git_branch", argv, out, stderr, code), nil
}

func gitPath(opts gitOptions, provided string) (string, error) {
	provided = strings.TrimSpace(provided)
	if provided == "" {
		return "", nil
	}
	abs, err := opts.Policy.EnsureReadable(provided)
	if err != nil {
		return "", denyError(err)
	}
	rel, err := opts.Policy.Relative(abs)
	if err != nil {
		return "", denyError(err)
	}
	rel = normalizeRelative(rel)
	if repository.IsSecretPath(rel) {
		return "", fmt.Errorf("%w: %q is a secret path", ErrDenied, rel)
	}
	return rel, nil
}

// statusPath extracts the path from a `git status --short` line. It returns an
// empty string for branch headers and malformed lines.
func statusPath(line string) string {
	if len(line) < 4 || strings.HasPrefix(line, "## ") {
		return ""
	}
	value := line[3:]
	if idx := strings.Index(value, " -> "); idx >= 0 {
		value = value[idx+4:]
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	return strings.ReplaceAll(value, `\`, "/")
}

// redactSecretStatus removes secret file names from status output while still
// reporting that a change exists.
func redactSecretStatus(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		path := statusPath(line)
		if path == "" || !repository.IsSecretPath(path) {
			continue
		}
		lines[i] = line[:2] + " [secret path redacted]"
	}
	return strings.Join(lines, "\n")
}

// gitSecretDiffPaths lists changed files that must be excluded from a
// repository-wide diff so secret contents are never emitted.
func gitSecretDiffPaths(ctx context.Context, workspace string, staged bool) ([]string, error) {
	argv := []string{"diff", "--name-only", "-z"}
	if staged {
		argv = append(argv, "--cached")
	}
	out, _, _, err := runGit(ctx, workspace, argv...)
	if err != nil {
		return nil, err
	}
	var excludes []string
	for _, name := range strings.Split(out, "\x00") {
		name = strings.TrimSpace(name)
		if name == "" || !repository.IsSecretPath(name) {
			continue
		}
		excludes = append(excludes, name)
	}
	return excludes, nil
}

func runGit(ctx context.Context, workspace string, argv ...string) (string, string, int, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", 0, fmt.Errorf("%w: git executable not found: %v", ErrNotFound, err)
	}
	all := append([]string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "core.quotepath=false"}, argv...)
	cmd := exec.CommandContext(ctx, "git", all...)
	cmd.Dir = workspace
	cmd.Env = commandEnv(map[string]string{
		"GIT_PAGER":           "cat",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_OPTIONAL_LOCKS":  "0",
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return "", "", 0, fmt.Errorf("git: %w", err)
		}
	}
	out := stdout.String()
	errOut := stderr.String()
	if code != 0 {
		return "", "", code, fmt.Errorf("%w: git exited with code %d: %s", ErrInvalidArguments, code, strings.TrimSpace(errOut))
	}
	if strings.TrimSpace(out) == "" && strings.TrimSpace(errOut) != "" {
		out = errOut
	}
	return out, errOut, code, nil
}

func gitResult(call Call, name string, argv []string, stdout, stderr string, code int) Result {
	content := stdout
	if strings.TrimSpace(stderr) != "" {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "stderr:\n" + stderr
	}
	if strings.TrimSpace(content) == "" {
		content = "(no output)"
	}
	return Result{
		CallID:  call.ID,
		Content: content,
		Metadata: map[string]any{
			"tool":      name,
			"cmd":       strings.Join(argv, " "),
			"exit_code": code,
		},
	}
}

// commandEnv builds a conservative environment for child processes. It is an
// allowlist: API keys, tokens, and other secret-bearing variables are never
// inherited. The OS and Go toolchain entries listed here are required for
// ordinary builds and test runs.
func commandEnv(overrides map[string]string) []string {
	keys := []string{
		// Unix basics.
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TERM",
		"TMPDIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		// Windows basics.
		"PATHEXT", "SystemRoot", "SYSTEMROOT", "SystemDrive", "windir",
		"COMSPEC", "TEMP", "TMP", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
		"APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES",
		"PROGRAMFILES(X86)", "COMMONPROGRAMFILES", "NUMBER_OF_PROCESSORS",
		"PROCESSOR_ARCHITECTURE", "OS",
		// Go toolchain cache and module locations.
		"GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT", "GOTOOLCHAIN", "GOFLAGS",
	}
	env := make([]string, 0, len(keys)+len(overrides))
	seen := make(map[string]struct{}, len(keys)+len(overrides))
	add := func(key, value string) {
		upper := strings.ToUpper(key)
		if _, ok := seen[upper]; ok {
			return
		}
		seen[upper] = struct{}{}
		env = append(env, key+"="+value)
	}
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			add(key, value)
		}
	}
	// Overrides are applied last and always win, including case-insensitive
	// duplicates on Windows.
	for key, value := range overrides {
		upper := strings.ToUpper(key)
		for i, entry := range env {
			if eq := strings.IndexByte(entry, '='); eq > 0 && strings.ToUpper(entry[:eq]) == upper {
				env = append(env[:i], env[i+1:]...)
				break
			}
		}
		env = append(env, key+"="+value)
	}
	return env
}
