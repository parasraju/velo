package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"velo/internal/repository"
	"velo/internal/security"
)

var schemaWriteFile = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Workspace-relative file path."},
		"content": {"type": "string", "description": "Complete new file content."},
		"expected_sha256": {"type": "string", "description": "SHA-256 returned by read_file for the current content. Required when the file already exists; omit for a new file."},
		"create_dirs": {"type": "boolean", "description": "Create missing parent directories (default true)."}
	},
	"required": ["path", "content"],
	"additionalProperties": false
}`)

type writeFileArgs struct {
	Path           string  `json:"path"`
	Content        *string `json:"content"`
	ExpectedSHA256 string  `json:"expected_sha256"`
	CreateDirs     *bool   `json:"create_dirs"`
}

type writeFileTool struct {
	repo     *repository.Repository
	policy   *security.Policy
	maxBytes int64
}

func (t *writeFileTool) Definition() Definition {
	return Definition{
		Name:        "write_file",
		Description: "Create a file or replace its complete content. Replacing an existing file requires the SHA-256 reported by read_file; stale content is rejected.",
		InputSchema: schemaWriteFile,
		Category:    CategoryWrite,
	}
}

func (t *writeFileTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args writeFileArgs
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return Result{}, fmt.Errorf("%w: path is required", ErrInvalidArguments)
	}
	if args.Content == nil {
		return Result{}, fmt.Errorf("%w: content is required", ErrInvalidArguments)
	}
	if int64(len(*args.Content)) > t.maxBytes {
		return Result{}, fmt.Errorf("%w: content exceeds %d bytes", ErrInvalidArguments, t.maxBytes)
	}
	expected := strings.ToLower(strings.TrimSpace(args.ExpectedSHA256))
	if expected != "" && !isHexSHA256(expected) {
		return Result{}, fmt.Errorf("%w: expected_sha256 must be 64 hex characters", ErrInvalidArguments)
	}

	abs, rel, err := resolveWritable(t.policy, args.Path)
	if err != nil {
		return Result{}, err
	}
	if deniedWritePath(rel) {
		return Result{}, fmt.Errorf("%w: writing %q is not allowed", ErrDenied, rel)
	}

	createDirs := true
	if args.CreateDirs != nil {
		createDirs = *args.CreateDirs
	}

	info, statErr := os.Lstat(abs)
	created := false
	perm := fs.FileMode(0o644)
	switch {
	case statErr == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Result{}, fmt.Errorf("%w: %q is not a regular file", ErrDenied, rel)
		}
		if expected == "" {
			return Result{}, fmt.Errorf("%w: %q exists; read it first and pass expected_sha256", ErrConflict, rel)
		}
		current, err := t.repo.HashFile(rel)
		if err != nil {
			return Result{}, fmt.Errorf("write_file: %w", err)
		}
		if !strings.EqualFold(current, expected) {
			return Result{}, fmt.Errorf("%w: %q changed since it was read (current sha256 %s)", ErrConflict, rel, current)
		}
		perm = info.Mode().Perm()
	case os.IsNotExist(statErr):
		if expected != "" {
			return Result{}, fmt.Errorf("%w: %q does not exist, but expected_sha256 was provided", ErrConflict, rel)
		}
		created = true
		parent := filepath.Dir(abs)
		if createDirs {
			if err := os.MkdirAll(parent, 0o755); err != nil {
				return Result{}, fmt.Errorf("write_file: create parent directories for %q: %w", rel, err)
			}
		} else if info, err := os.Stat(parent); err != nil || !info.IsDir() {
			return Result{}, fmt.Errorf("%w: parent directory for %q does not exist", ErrNotFound, rel)
		}
	default:
		return Result{}, fmt.Errorf("write_file: stat %q: %w", rel, statErr)
	}

	// Revalidate immediately before the write to reduce TOCTOU exposure.
	abs, rel, err = resolveWritable(t.policy, args.Path)
	if err != nil {
		return Result{}, err
	}
	if err := writeAtomic(abs, []byte(*args.Content), perm); err != nil {
		return Result{}, fmt.Errorf("write_file: %w", err)
	}

	sum := sha256.Sum256([]byte(*args.Content))
	digest := hex.EncodeToString(sum[:])
	action := "updated"
	if created {
		action = "created"
	}
	return Result{
		CallID:  call.ID,
		Content: fmt.Sprintf("%s %s (%d bytes)\nsha256: %s", action, rel, len(*args.Content), digest),
		Metadata: map[string]any{
			"path":    rel,
			"bytes":   len(*args.Content),
			"sha256":  digest,
			"created": created,
		},
	}, nil
}

func resolveWritable(p *security.Policy, provided string) (abs, rel string, err error) {
	abs, err = p.EnsureWritable(provided)
	if err != nil {
		return "", "", denyError(err)
	}
	rel, err = p.Relative(abs)
	if err != nil {
		return "", "", denyError(err)
	}
	return abs, normalizeRelative(rel), nil
}

func deniedWritePath(rel string) bool {
	clean := strings.ToLower(strings.TrimPrefix(filepath.ToSlash(rel), "./"))
	for _, prefix := range []string{".git", ".velo"} {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

func writeAtomic(path string, data []byte, perm fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".velo-write-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func isHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
