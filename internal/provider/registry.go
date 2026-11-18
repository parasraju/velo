package provider

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"velo/internal/protocol"
)

type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

func (r *Registry) Register(name string, factory Factory) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return protocol.NewProviderError(protocol.ErrProtocol, "", "provider registry: name is required")
	}
	if factory == nil {
		return protocol.NewProviderError(protocol.ErrProtocol, name, "provider registry: factory is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories == nil {
		r.factories = make(map[string]Factory)
	}
	if _, ok := r.factories[name]; ok {
		return protocol.NewProviderError(protocol.ErrProtocol, name, fmt.Sprintf("provider registry: %q is already registered", name))
	}
	r.factories[name] = factory
	return nil
}

func (r *Registry) New(name string, opts Options) (Provider, error) {
	r.mu.RLock()
	factory, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, name, fmt.Sprintf("provider registry: unknown provider %q", name))
	}

	p, err := factory(opts)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, name, fmt.Sprintf("provider registry: factory for %q returned nil provider", name))
	}
	return p, nil
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}

func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	_, ok := r.factories[name]
	r.mu.RUnlock()
	return ok
}
