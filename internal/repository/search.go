package repository

import (
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

var errStopSearch = errors.New("repository: stop search")

func (r *Repository) Search(opts SearchOptions) ([]Match, bool, error) {
	pattern := opts.Pattern
	if strings.TrimSpace(pattern) == "" {
		return nil, false, fmt.Errorf("repository: empty search pattern: %w", ErrInvalidPath)
	}
	expr := pattern
	if !opts.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if !opts.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, false, fmt.Errorf("repository: invalid pattern: %w: %v", ErrInvalidPath, err)
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = defaultSearchMaxResults
	}
	if maxResults > maxSearchResults {
		maxResults = maxSearchResults
	}
	contextLines := opts.ContextLines
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > maxContextLines {
		contextLines = maxContextLines
	}
	maxFileBytes := opts.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = r.maxFileBytes
	}
	if maxFileBytes <= 0 {
		maxFileBytes = defaultMaxFileBytes
	}
	if maxFileBytes > maxMaxFileBytes {
		maxFileBytes = maxMaxFileBytes
	}
	matches := make([]Match, 0)
	truncated := false
	walkErr := r.walkTree(r.Root, ".", func(rel, abs string, info os.FileInfo) error {
		if isSecretPath(rel) {
			return nil
		}
		if !includeMatches(opts.Include, rel, ".", path.Base(rel)) {
			return nil
		}
		if info.Size() > maxFileBytes {
			return nil
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			return nil
		}
		if contentHasNUL(content) {
			return nil
		}
		lines := splitLines(content)
		for i, line := range lines {
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			if len(matches) >= maxResults {
				truncated = true
				return errStopSearch
			}
			match := Match{Path: rel, Line: i + 1, Column: loc[0] + 1, Text: line}
			if contextLines > 0 {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				end := i + contextLines
				if end >= len(lines) {
					end = len(lines) - 1
				}
				context := make([]string, 0, end-start)
				for j := start; j <= end; j++ {
					if j == i {
						continue
					}
					context = append(context, lines[j])
				}
				match.Context = context
			}
			matches = append(matches, match)
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStopSearch) {
		return nil, false, walkErr
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	return matches, truncated, nil
}

func splitLines(content []byte) []string {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}
