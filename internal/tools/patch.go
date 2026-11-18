package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"velo/internal/repository"
	"velo/internal/security"
)

var schemaApplyPatch = json.RawMessage(`{
	"type": "object",
	"properties": {
		"patch": {"type": "string", "description": "Unified diff with ---/+++ file headers and @@ hunks. Creating and modifying text files is supported; deletion and binary patches are not."},
		"create_dirs": {"type": "boolean", "description": "Create missing parent directories for new files (default true)."}
	},
	"required": ["patch"],
	"additionalProperties": false
}`)

var hunkHeaderPattern = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

type applyPatchArgs struct {
	Patch      *string `json:"patch"`
	CreateDirs *bool   `json:"create_dirs"`
}

type patchLine struct {
	op   byte
	text string
}

type patchHunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []patchLine
}

type patchFile struct {
	oldPath string
	newPath string
	hunks   []patchHunk
}

type applyPatchTool struct {
	repo     *repository.Repository
	policy   *security.Policy
	maxBytes int64
}

func (t *applyPatchTool) Definition() Definition {
	return Definition{
		Name:        "apply_patch",
		Description: "Apply a unified diff to workspace files. Context lines must match current content or the patch is rejected as a conflict. Deletion and binary patches are not supported.",
		InputSchema: schemaApplyPatch,
		Category:    CategoryWrite,
	}
}

func (t *applyPatchTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args applyPatchArgs
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	if args.Patch == nil || strings.TrimSpace(*args.Patch) == "" {
		return Result{}, fmt.Errorf("%w: patch is required", ErrInvalidArguments)
	}
	if int64(len(*args.Patch)) > t.maxBytes {
		return Result{}, fmt.Errorf("%w: patch exceeds %d bytes", ErrInvalidArguments, t.maxBytes)
	}
	files, err := parseUnifiedPatch(*args.Patch)
	if err != nil {
		return Result{}, err
	}
	createDirs := true
	if args.CreateDirs != nil {
		createDirs = *args.CreateDirs
	}

	type prepared struct {
		rel       string
		abs       string
		content   string
		created   bool
		additions int
		deletions int
	}
	results := make([]prepared, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file.newPath == "" {
			return Result{}, fmt.Errorf("%w: file deletion is not supported (%s)", ErrInvalidArguments, file.oldPath)
		}
		abs, rel, err := resolveWritable(t.policy, file.newPath)
		if err != nil {
			return Result{}, err
		}
		if deniedWritePath(rel) {
			return Result{}, fmt.Errorf("%w: writing %q is not allowed", ErrDenied, rel)
		}
		if _, dup := seen[rel]; dup {
			return Result{}, fmt.Errorf("%w: patch touches %q more than once", ErrInvalidArguments, rel)
		}
		seen[rel] = struct{}{}

		var original string
		created := false
		info, statErr := os.Lstat(abs)
		switch {
		case statErr == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return Result{}, fmt.Errorf("%w: %q is not a regular file", ErrDenied, rel)
			}
			if info.Size() > t.maxBytes {
				return Result{}, fmt.Errorf("%w: %q is too large to patch", ErrInvalidArguments, rel)
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return Result{}, fmt.Errorf("apply_patch: read %q: %w", rel, err)
			}
			if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
				return Result{}, fmt.Errorf("%w: %q is not a text file", ErrInvalidArguments, rel)
			}
			original = string(data)
			if file.oldPath == "" {
				return Result{}, fmt.Errorf("%w: %q already exists", ErrConflict, rel)
			}
		case os.IsNotExist(statErr):
			if file.oldPath != "" {
				return Result{}, fmt.Errorf("%w: %q does not exist", ErrConflict, rel)
			}
			created = true
			parent := filepath.Dir(abs)
			if createDirs {
				if err := os.MkdirAll(parent, 0o755); err != nil {
					return Result{}, fmt.Errorf("apply_patch: create parent directories for %q: %w", rel, err)
				}
			} else if pinfo, err := os.Stat(parent); err != nil || !pinfo.IsDir() {
				return Result{}, fmt.Errorf("%w: parent directory for %q does not exist", ErrNotFound, rel)
			}
		default:
			return Result{}, fmt.Errorf("apply_patch: stat %q: %w", rel, statErr)
		}

		updated, additions, deletions, err := applyHunksToContent(original, file.hunks)
		if err != nil {
			return Result{}, fmt.Errorf("%w in %s: %v", ErrConflict, rel, err)
		}
		results = append(results, prepared{
			rel:       rel,
			abs:       abs,
			content:   updated,
			created:   created,
			additions: additions,
			deletions: deletions,
		})
	}

	// Validate all paths once more immediately before writing.
	for i := range results {
		abs, rel, err := resolveWritable(t.policy, results[i].rel)
		if err != nil {
			return Result{}, err
		}
		if rel != results[i].rel {
			return Result{}, fmt.Errorf("%w: %q resolved to %q", ErrConflict, results[i].rel, rel)
		}
		results[i].abs = abs
	}

	var (
		summary   strings.Builder
		additions int
		deletions int
		written   []string
	)
	fmt.Fprintf(&summary, "applied patch to %d file(s):", len(results))
	for _, res := range results {
		perm := os.FileMode(0o644)
		if !res.created {
			if info, err := os.Stat(res.abs); err == nil {
				perm = info.Mode().Perm()
			}
		}
		if err := writeAtomic(res.abs, []byte(res.content), perm); err != nil {
			if len(written) > 0 {
				return Result{}, fmt.Errorf("apply_patch: %q failed after writing %s: %w", res.rel, strings.Join(written, ", "), err)
			}
			return Result{}, fmt.Errorf("apply_patch: %w", err)
		}
		marker := "M"
		if res.created {
			marker = "A"
		}
		written = append(written, res.rel)
		additions += res.additions
		deletions += res.deletions
		fmt.Fprintf(&summary, "\n  %s %s (+%d -%d)", marker, res.rel, res.additions, res.deletions)
	}
	return Result{
		CallID:  call.ID,
		Content: summary.String(),
		Metadata: map[string]any{
			"files":     len(results),
			"additions": additions,
			"deletions": deletions,
			"paths":     written,
		},
	}, nil
}

func parseUnifiedPatch(patch string) ([]patchFile, error) {
	patch = strings.ReplaceAll(patch, "\r\n", "\n")
	lines := strings.Split(patch, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var files []patchFile
	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.TrimSpace(line) == "":
			i++
		case strings.HasPrefix(line, "diff --git "):
			i++
		case strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode "),
			strings.HasPrefix(line, "deleted file mode "),
			strings.HasPrefix(line, "old mode "),
			strings.HasPrefix(line, "new mode "),
			strings.HasPrefix(line, "similarity index "),
			strings.HasPrefix(line, "dissimilarity index "):
			i++
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "):
			return nil, fmt.Errorf("%w: rename patches are not supported", ErrInvalidArguments)
		case strings.HasPrefix(line, "--- "):
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
				return nil, fmt.Errorf("%w: malformed file header near %q", ErrInvalidArguments, line)
			}
			file := patchFile{
				oldPath: parseDiffPath(lines[i][4:]),
				newPath: parseDiffPath(lines[i+1][4:]),
			}
			i += 2
			for i < len(lines) && strings.HasPrefix(lines[i], "@@") {
				hunk, next, err := parseHunk(lines, i)
				if err != nil {
					return nil, err
				}
				file.hunks = append(file.hunks, hunk)
				i = next
			}
			if len(file.hunks) == 0 {
				return nil, fmt.Errorf("%w: file %q has no hunks", ErrInvalidArguments, file.newPath)
			}
			files = append(files, file)
		default:
			return nil, fmt.Errorf("%w: unexpected patch line %q", ErrInvalidArguments, line)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: patch contains no file headers", ErrInvalidArguments)
	}
	return files, nil
}

func parseDiffPath(raw string) string {
	value := raw
	if idx := strings.IndexByte(value, '\t'); idx >= 0 {
		value = value[:idx]
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	value = strings.ReplaceAll(value, `\`, "/")
	if value == "/dev/null" || value == "" {
		return ""
	}
	if strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/") {
		value = value[2:]
	}
	return value
}

func parseHunk(lines []string, start int) (patchHunk, int, error) {
	header := lines[start]
	m := hunkHeaderPattern.FindStringSubmatch(header)
	if m == nil {
		return patchHunk{}, 0, fmt.Errorf("%w: malformed hunk header %q", ErrInvalidArguments, header)
	}
	oldStart, _ := strconv.Atoi(m[1])
	oldCount := 1
	if m[2] != "" {
		oldCount, _ = strconv.Atoi(m[2])
	}
	newStart, _ := strconv.Atoi(m[3])
	newCount := 1
	if m[4] != "" {
		newCount, _ = strconv.Atoi(m[4])
	}
	h := patchHunk{oldStart: oldStart, oldCount: oldCount, newStart: newStart, newCount: newCount}
	i := start + 1
	oldSeen, newSeen := 0, 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "diff --git ") {
			break
		}
		if line == "" {
			if oldSeen == oldCount && newSeen == newCount {
				break
			}
			h.lines = append(h.lines, patchLine{op: ' ', text: ""})
			oldSeen++
			newSeen++
			i++
			continue
		}
		if strings.HasPrefix(line, `\`) {
			// "\ No newline at end of file" is accepted but not tracked; the
			// trailing-newline state of the existing file is preserved.
			i++
			continue
		}
		op := line[0]
		switch op {
		case ' ':
			oldSeen++
			newSeen++
		case '-':
			oldSeen++
		case '+':
			newSeen++
		default:
			return patchHunk{}, 0, fmt.Errorf("%w: unexpected hunk line %q", ErrInvalidArguments, line)
		}
		if oldSeen > oldCount || newSeen > newCount {
			return patchHunk{}, 0, fmt.Errorf("%w: hunk line counts exceed header %q", ErrInvalidArguments, header)
		}
		h.lines = append(h.lines, patchLine{op: op, text: line[1:]})
		i++
	}
	if oldSeen != oldCount || newSeen != newCount {
		return patchHunk{}, 0, fmt.Errorf("%w: hunk %q expected -%d +%d, found -%d +%d", ErrInvalidArguments, header, oldCount, newCount, oldSeen, newSeen)
	}
	return h, i, nil
}

func applyHunksToContent(content string, hunks []patchHunk) (string, int, int, error) {
	src, trailing := splitContent(content)
	out := make([]string, 0, len(src)+8)
	cursor := 0
	additions, deletions := 0, 0
	for _, hunk := range hunks {
		oldLines := make([]string, 0, hunk.oldCount)
		for _, line := range hunk.lines {
			if line.op == ' ' || line.op == '-' {
				oldLines = append(oldLines, line.text)
			}
		}
		target := hunk.oldStart - 1
		if hunk.oldStart == 0 {
			target = cursor
		}
		if target < cursor || target > len(src) {
			return "", 0, 0, fmt.Errorf("hunk target %d is out of range (cursor %d)", target, cursor)
		}
		if !matchesAt(src, target, oldLines) {
			unique := -1
			matches := 0
			for i := cursor; i+len(oldLines) <= len(src); i++ {
				if matchesAt(src, i, oldLines) {
					matches++
					unique = i
					if matches > 1 {
						break
					}
				}
			}
			if matches != 1 {
				return "", 0, 0, fmt.Errorf("context does not match at line %d", target+1)
			}
			target = unique
		}
		out = append(out, src[cursor:target]...)
		pos := target
		for _, line := range hunk.lines {
			switch line.op {
			case ' ':
				out = append(out, src[pos])
				pos++
			case '-':
				pos++
				deletions++
			case '+':
				out = append(out, line.text)
				additions++
			}
		}
		cursor = pos
	}
	out = append(out, src[cursor:]...)
	if len(src) == 0 && len(out) > 0 {
		trailing = true
	}
	result := strings.Join(out, "\n")
	if trailing && len(out) > 0 {
		result += "\n"
	}
	return result, additions, deletions, nil
}

func splitContent(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	trailing := strings.HasSuffix(content, "\n")
	if trailing {
		content = content[:len(content)-1]
	}
	return strings.Split(content, "\n"), trailing
}

func matchesAt(src []string, at int, want []string) bool {
	if at < 0 || at+len(want) > len(src) {
		return false
	}
	for i, line := range want {
		if src[at+i] != line {
			return false
		}
	}
	return true
}
