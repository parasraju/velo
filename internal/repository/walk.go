package repository

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func (r *Repository) walkTree(absRoot, relRoot string, fn func(rel, abs string, info os.FileInfo) error) error {
	var walk func(abs, rel string) error
	walk = func(abs, rel string) error {
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
			info, err := os.Lstat(childAbs)
			if err != nil {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if info.IsDir() {
				if r.ignored(childRel, true) {
					continue
				}
				if err := walk(childAbs, childRel); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if r.ignored(childRel, false) {
				continue
			}
			if err := fn(childRel, childAbs, info); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(absRoot, relRoot)
}

func (r *Repository) entryFor(rel, abs string, info os.FileInfo) Entry {
	secret := isSecretPath(rel)
	binary := false
	if !secret {
		binary = fileHasNUL(abs)
	}
	return Entry{Path: rel, Size: info.Size(), Binary: binary, Secret: secret}
}

func fileHasNUL(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false
	}
	return bytes.IndexByte(buf[:n], 0) >= 0
}

func contentHasNUL(content []byte) bool {
	if len(content) > 8192 {
		content = content[:8192]
	}
	return bytes.IndexByte(content, 0) >= 0
}
