package repository

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

var defaultIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".velo":        true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
}

func readGitignore(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	patterns := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

func (r *Repository) ignored(rel string, isDir bool) bool {
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "./")
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")
	if isDir && defaultIgnoredDirs[strings.ToLower(parts[len(parts)-1])] {
		return true
	}
	ignored := false
	for _, pattern := range r.ignores {
		matched, applies := matchIgnore(pattern, rel, isDir)
		if applies {
			ignored = matched
		}
	}
	return ignored
}

func matchIgnore(pattern, rel string, isDir bool) (bool, bool) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "#") {
		return false, false
	}
	negated := false
	if strings.HasPrefix(pattern, "!") {
		negated = true
		pattern = strings.TrimSpace(pattern[1:])
	}
	if pattern == "" {
		return false, false
	}
	dirOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if dirOnly && !isDir {
		return false, false
	}
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return false, false
	}
	base := path.Base(rel)
	matched := false
	if strings.Contains(pattern, "/") {
		matched = globMatch(pattern, rel)
		if !matched && !anchored {
			parts := strings.Split(rel, "/")
			for i := 1; i < len(parts); i++ {
				if globMatch(pattern, strings.Join(parts[i:], "/")) {
					matched = true
					break
				}
			}
		}
	} else {
		matched = globMatch(pattern, base)
		if !matched {
			for _, part := range strings.Split(rel, "/") {
				if globMatch(pattern, part) {
					matched = true
					break
				}
			}
		}
	}
	if !matched {
		return false, false
	}
	return !negated, true
}

func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	if err != nil {
		return pattern == name
	}
	return ok
}

func includeMatches(include, rel, startRel, base string) bool {
	include = strings.TrimSpace(strings.ReplaceAll(include, "\\", "/"))
	if include == "" {
		return true
	}
	include = strings.TrimPrefix(include, "./")
	if strings.Contains(include, "/") {
		if globMatch(include, rel) {
			return true
		}
		if startRel != "." && startRel != "" {
			if strings.HasPrefix(rel, startRel+"/") {
				if globMatch(include, strings.TrimPrefix(rel, startRel+"/")) {
					return true
				}
			}
		}
	}
	return globMatch(include, base)
}

func isSecretPath(rel string) bool {
	clean := strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "./")
	// .velo holds session transcripts and local config. Direct reads are
	// denied even though directory walks already skip the folder: a model
	// must not be able to open .velo/sessions/*.jsonl by path.
	for _, part := range strings.Split(clean, "/") {
		if strings.EqualFold(part, ".velo") {
			return true
		}
	}
	base := strings.ToLower(path.Base(clean))
	if base == "" || base == "." || base == "/" {
		return false
	}
	if strings.HasPrefix(base, ".env") {
		return true
	}
	switch base {
	case ".npmrc", ".netrc", "id_rsa", "id_ed25519", "credentials", "secrets":
		return true
	}
	if strings.HasPrefix(base, "credentials.") || strings.HasPrefix(base, "secrets.") {
		return true
	}
	switch path.Ext(base) {
	case ".pem", ".key", ".p12", ".pfx":
		return true
	}
	return false
}

// IsSecretPath reports whether rel names a file whose contents must never be
// exposed by tools. That includes secret file names and anything under .velo,
// including session transcripts. Tools that bypass Repository.ReadFile (for
// example Git diff) must apply the same predicate to their output.
func IsSecretPath(rel string) bool { return isSecretPath(rel) }
