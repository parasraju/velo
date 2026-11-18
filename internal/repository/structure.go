package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type treeNode struct {
	name     string
	rel      string
	isDir    bool
	size     int64
	files    int
	bytes    int64
	children []*treeNode
}

func (r *Repository) Structure(opts StructureOptions) ([]Node, bool, error) {
	clean, err := r.normalizeDir(opts.Path)
	if err != nil {
		return nil, false, err
	}
	abs, err := r.safeAbs(clean)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, fmt.Errorf("%w: %q", ErrNotFound, clean)
		}
		return nil, false, fmt.Errorf("repository: stat %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, fmt.Errorf("%w: %q", ErrNotFile, clean)
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultStructureDepth
	}
	if maxDepth > maxStructureDepth {
		maxDepth = maxStructureDepth
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultStructureEntries
	}
	if maxEntries > maxStructureEntries {
		maxEntries = maxStructureEntries
	}
	children, err := r.buildTree(abs, clean)
	if err != nil {
		return nil, false, err
	}
	nodes := make([]Node, 0)
	truncated := false
	var emit func(items []*treeNode, depth int) bool
	emit = func(items []*treeNode, depth int) bool {
		for _, item := range items {
			if len(nodes) >= maxEntries {
				truncated = true
				return false
			}
			nodes = append(nodes, Node{
				Path:  item.rel,
				Name:  item.name,
				Depth: depth,
				IsDir: item.isDir,
				Files: item.files,
				Bytes: item.bytes,
			})
			if !item.isDir {
				continue
			}
			if depth >= maxDepth {
				if len(item.children) > 0 {
					truncated = true
				}
				continue
			}
			if !emit(item.children, depth+1) {
				return false
			}
		}
		return true
	}
	emit(children, 1)
	return nodes, truncated, nil
}

func (r *Repository) buildTree(abs, rel string) ([]*treeNode, error) {
	items, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("repository: read directory %q: %w", rel, err)
	}
	nodes := make([]*treeNode, 0, len(items))
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
			node := &treeNode{name: name, rel: childRel, isDir: true}
			children, err := r.buildTree(childAbs, childRel)
			if err != nil {
				continue
			}
			node.children = children
			for _, child := range children {
				if child.isDir {
					node.files += child.files
					node.bytes += child.bytes
					continue
				}
				node.files++
				node.bytes += child.size
			}
			nodes = append(nodes, node)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if r.ignored(childRel, false) {
			continue
		}
		nodes = append(nodes, &treeNode{name: name, rel: childRel, size: info.Size(), bytes: info.Size()})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].name < nodes[j].name })
	return nodes, nil
}
