package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var validToolName = regexp.MustCompile(`^[a-z0-9_]+$`)

type registryEntry struct {
	tool Tool
	def  Definition
}

type Registry struct {
	mu      sync.RWMutex
	entries map[string]registryEntry
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]registryEntry)}
}

func (r *Registry) Register(t Tool) error {
	if isNilTool(t) {
		return fmt.Errorf("tools: register: %w: nil tool", ErrInvalidArguments)
	}
	def := t.Definition()
	name := strings.TrimSpace(def.Name)
	if name == "" {
		return fmt.Errorf("tools: register: %w: name is required", ErrInvalidArguments)
	}
	if !validToolName.MatchString(name) {
		return fmt.Errorf("tools: register %q: %w: name must match [a-z0-9_]+", name, ErrInvalidArguments)
	}
	if strings.TrimSpace(def.Description) == "" {
		return fmt.Errorf("tools: register %q: %w: description is required", name, ErrInvalidArguments)
	}
	if !json.Valid(def.InputSchema) {
		return fmt.Errorf("tools: register %q: %w: input schema is not valid json", name, ErrInvalidArguments)
	}
	schema := strings.TrimSpace(string(def.InputSchema))
	if !strings.HasPrefix(schema, "{") {
		return fmt.Errorf("tools: register %q: %w: input schema must be a json object", name, ErrInvalidArguments)
	}
	if !def.Category.Valid() {
		return fmt.Errorf("tools: register %q: %w: invalid category %q", name, ErrInvalidArguments, def.Category)
	}
	if def.Category.IsReadOnly() && !def.ReadOnly {
		return fmt.Errorf("tools: register %q: %w: read category requires read_only", name, ErrInvalidArguments)
	}
	if !def.Category.IsReadOnly() && def.ReadOnly {
		return fmt.Errorf("tools: register %q: %w: non-read category cannot be read_only", name, ErrInvalidArguments)
	}
	if def.Timeout < 0 {
		return fmt.Errorf("tools: register %q: %w: timeout must not be negative", name, ErrInvalidArguments)
	}

	def.Name = name
	def.InputSchema = cloneRaw(def.InputSchema)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[name]; ok {
		return fmt.Errorf("tools: register %q: %w", name, ErrDuplicate)
	}
	r.entries[name] = registryEntry{tool: t, def: def}
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		return nil, false
	}
	return e.tool, true
}

func (r *Registry) List() []Definition {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.entries))
	for _, e := range r.entries {
		def := e.def
		def.InputSchema = cloneRaw(def.InputSchema)
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.entries))
	for name := range r.entries {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	out := make(json.RawMessage, len(raw))
	copy(out, raw)
	return out
}

func isNilTool(t Tool) bool {
	if t == nil {
		return true
	}
	v := reflect.ValueOf(t)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
