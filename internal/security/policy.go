package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrOutsideWorkspace = errors.New("security: path is outside workspace")
	ErrDenied           = errors.New("security: path denied by policy")
	ErrWriteDenied      = errors.New("security: write access denied")
	ErrSymlink          = errors.New("security: symlink escapes workspace")
	ErrInvalidPath      = errors.New("security: invalid path")
)

type Policy struct {
	workspace    string
	AllowedRoots []string
	AllowWrite   bool
	AllowExecute bool
	AllowNetwork bool
	AllowShell   bool
}

type Option func(*Policy)

func WithAllowedRoots(roots ...string) Option {
	copied := append([]string(nil), roots...)
	return func(p *Policy) {
		p.AllowedRoots = append(p.AllowedRoots, copied...)
	}
}

func WithWrite(enabled bool) Option {
	return func(p *Policy) {
		p.AllowWrite = enabled
	}
}

func WithExecute(enabled bool) Option {
	return func(p *Policy) {
		p.AllowExecute = enabled
	}
}

func WithNetwork(enabled bool) Option {
	return func(p *Policy) {
		p.AllowNetwork = enabled
	}
}

func WithShell(enabled bool) Option {
	return func(p *Policy) {
		p.AllowShell = enabled
	}
}

func New(workspace string, opts ...Option) (*Policy, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("security: workspace is required: %w", ErrInvalidPath)
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("security: resolve workspace %q: %w", workspace, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("security: stat workspace %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("security: workspace %q is not a directory: %w", abs, ErrInvalidPath)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("security: evaluate workspace %q: %w", abs, err)
	}
	p := &Policy{workspace: filepath.Clean(real)}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	roots := make([]string, 0, len(p.AllowedRoots))
	for _, root := range p.AllowedRoots {
		if strings.TrimSpace(root) == "" || strings.IndexByte(root, 0) >= 0 {
			return nil, fmt.Errorf("security: invalid allowed root %q: %w", root, ErrInvalidPath)
		}
		rootAbs := root
		if !filepath.IsAbs(rootAbs) {
			rootAbs = filepath.Join(p.workspace, rootAbs)
		}
		rootAbs = filepath.Clean(rootAbs)
		if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
			rootAbs = resolved
		}
		ok, err := pathContains(p.workspace, rootAbs)
		if err != nil {
			return nil, fmt.Errorf("security: check allowed root %q: %w", root, err)
		}
		if !ok {
			return nil, fmt.Errorf("security: allowed root %q is outside workspace: %w", root, ErrOutsideWorkspace)
		}
		roots = append(roots, rootAbs)
	}
	p.AllowedRoots = roots
	return p, nil
}

func (p *Policy) Workspace() string {
	if p == nil {
		return ""
	}
	return p.workspace
}

// AllowsWrite reports whether the policy permits file writes.
func (p *Policy) AllowsWrite() bool { return p != nil && p.AllowWrite }

// AllowsExecute reports whether the policy permits process execution.
func (p *Policy) AllowsExecute() bool { return p != nil && p.AllowExecute }

// AllowsNetwork reports whether the policy permits network-capable actions.
func (p *Policy) AllowsNetwork() bool { return p != nil && p.AllowNetwork }

// AllowsShell reports whether the policy permits invoking a shell.
func (p *Policy) AllowsShell() bool { return p != nil && p.AllowShell }

func (p *Policy) Resolve(path string) (string, error) {
	if p == nil || p.workspace == "" {
		return "", fmt.Errorf("security: policy is not initialized: %w", ErrDenied)
	}
	if path == "" {
		return "", fmt.Errorf("security: empty path: %w", ErrInvalidPath)
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("security: path contains NUL byte: %w", ErrInvalidPath)
	}
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(filepath.Join(p.workspace, path))
	}
	ok, err := pathContains(p.workspace, abs)
	if err != nil {
		return "", fmt.Errorf("security: check workspace containment for %q: %w", path, err)
	}
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrOutsideWorkspace, path)
	}
	if len(p.AllowedRoots) > 0 {
		allowed := false
		for _, root := range p.AllowedRoots {
			inside, err := pathContains(root, abs)
			if err != nil {
				return "", fmt.Errorf("security: check allowed root containment for %q: %w", path, err)
			}
			if inside {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("%w: %q is outside allowed roots", ErrOutsideWorkspace, path)
		}
	}
	return abs, nil
}

func (p *Policy) EnsureReadable(path string) (string, error) {
	abs, err := p.Resolve(path)
	if err != nil {
		return "", err
	}
	physical, err := p.ensurePhysical(abs, false)
	if err != nil {
		return "", err
	}
	return physical, nil
}

func (p *Policy) EnsureWritable(path string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("security: policy is not initialized: %w", ErrWriteDenied)
	}
	if !p.AllowWrite {
		return "", fmt.Errorf("security: write access is disabled: %w", ErrWriteDenied)
	}
	abs, err := p.Resolve(path)
	if err != nil {
		return "", err
	}
	if _, err := p.ensurePhysical(abs, true); err != nil {
		return "", err
	}
	return abs, nil
}

func (p *Policy) CheckSymlink(path string) error {
	abs, err := p.Resolve(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("security: lstat %q: %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("%w: %q could not be resolved: %w", ErrSymlink, path, err)
	}
	ok, err := p.containedTarget(target)
	if err != nil {
		return fmt.Errorf("security: check symlink target %q: %w", target, err)
	}
	if !ok {
		return fmt.Errorf("%w: %q resolves to %q", ErrSymlink, path, target)
	}
	return nil
}

func (p *Policy) Relative(path string) (string, error) {
	if p == nil || p.workspace == "" {
		return "", fmt.Errorf("security: policy is not initialized: %w", ErrDenied)
	}
	if path == "" {
		return "", fmt.Errorf("security: empty path: %w", ErrInvalidPath)
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("security: path contains NUL byte: %w", ErrInvalidPath)
	}
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
		ok, err := pathContains(p.workspace, abs)
		if err != nil {
			return "", fmt.Errorf("security: check containment for %q: %w", path, err)
		}
		if !ok {
			return "", fmt.Errorf("%w: %q", ErrOutsideWorkspace, path)
		}
	} else {
		resolved, err := p.Resolve(path)
		if err != nil {
			return "", err
		}
		abs = resolved
	}
	rel, err := filepath.Rel(p.workspace, abs)
	if err != nil {
		return "", fmt.Errorf("security: relative %q: %w", path, err)
	}
	if rel == "." {
		return ".", nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q", ErrOutsideWorkspace, path)
	}
	return filepath.ToSlash(rel), nil
}

func IsDenied(err error) bool {
	return errors.Is(err, ErrDenied) ||
		errors.Is(err, ErrWriteDenied) ||
		errors.Is(err, ErrOutsideWorkspace) ||
		errors.Is(err, ErrSymlink) ||
		errors.Is(err, ErrInvalidPath)
}

func (p *Policy) ensurePhysical(abs string, allowMissing bool) (string, error) {
	rel, err := filepath.Rel(p.workspace, abs)
	if err != nil {
		return "", fmt.Errorf("security: relative to workspace %q: %w", abs, err)
	}
	if rel != "." {
		parts := strings.Split(rel, string(filepath.Separator))
		current := p.workspace
		for _, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				if allowMissing && os.IsNotExist(err) {
					return abs, nil
				}
				return "", fmt.Errorf("security: lstat %q: %w", current, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(current)
				if err != nil {
					return "", fmt.Errorf("%w: %q could not be resolved: %w", ErrSymlink, current, err)
				}
				ok, err := p.containedTarget(target)
				if err != nil {
					return "", fmt.Errorf("security: check symlink target %q: %w", target, err)
				}
				if !ok {
					return "", fmt.Errorf("%w: %q resolves to %q", ErrSymlink, current, target)
				}
			}
		}
	}
	physical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if allowMissing && os.IsNotExist(err) {
			return abs, nil
		}
		return "", fmt.Errorf("security: evaluate %q: %w", abs, err)
	}
	ok, err := p.containedTarget(physical)
	if err != nil {
		return "", fmt.Errorf("security: check target %q: %w", physical, err)
	}
	if !ok {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrSymlink, abs, physical)
	}
	return physical, nil
}

func (p *Policy) containedTarget(target string) (bool, error) {
	ok, err := pathContains(p.workspace, target)
	if err != nil || !ok {
		return ok, err
	}
	if len(p.AllowedRoots) == 0 {
		return true, nil
	}
	for _, root := range p.AllowedRoots {
		inside, err := pathContains(root, target)
		if err != nil {
			return false, err
		}
		if inside {
			return true, nil
		}
	}
	return false, nil
}

func pathContains(root, target string) (bool, error) {
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
