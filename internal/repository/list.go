package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func (r *Repository) List(opts ListOptions) ([]Entry, int, error) {
	clean, err := r.normalizeDir(opts.Path)
	if err != nil {
		return nil, 0, err
	}
	abs, err := r.safeAbs(clean)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, fmt.Errorf("%w: %q", ErrNotFound, clean)
		}
		return nil, 0, fmt.Errorf("repository: stat %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, 0, fmt.Errorf("%w: %q", ErrNotFile, clean)
	}
	entries := make([]Entry, 0)
	total := 0
	maxEntries := opts.MaxEntries
	var walk func(abs, rel string, depth int) error
	walk = func(abs, rel string, depth int) error {
		items, err := os.ReadDir(abs)
		if err != nil {
			return fmt.Errorf("repository: read directory %q: %w", rel, err)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
		for _, item := range items {
			name := item.Name()
			childAbs := filepath.Join(abs, name)
			childRel := name
			if rel != "." && rel != "" {
				childRel = rel + "/" + name
			}
			childDepth := depth + 1
			childInfo, err := os.Lstat(childAbs)
			if err != nil {
				continue
			}
			if childInfo.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if childInfo.IsDir() {
				if r.ignored(childRel, true) {
					continue
				}
				if !opts.Recursive {
					continue
				}
				if opts.MaxDepth > 0 && childDepth > opts.MaxDepth {
					continue
				}
				if err := walk(childAbs, childRel, childDepth); err != nil {
					return err
				}
				continue
			}
			if !childInfo.Mode().IsRegular() {
				continue
			}
			if opts.MaxDepth > 0 && childDepth > opts.MaxDepth {
				continue
			}
			if r.ignored(childRel, false) {
				continue
			}
			if !includeMatches(opts.Include, childRel, clean, name) {
				continue
			}
			total++
			if maxEntries > 0 && len(entries) >= maxEntries {
				continue
			}
			entries = append(entries, r.entryFor(childRel, childAbs, childInfo))
		}
		return nil
	}
	if err := walk(abs, clean, 0); err != nil {
		return nil, 0, err
	}
	sortEntries(entries)
	return entries, total, nil
}
