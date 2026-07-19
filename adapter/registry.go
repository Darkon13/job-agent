package adapter

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Registry owns adapter factories. It is intentionally instance-based so tests
// and multiple runtimes do not share hidden global state.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

func (r *Registry) Register(name string, factory Factory) error {
	if name == "" || factory == nil {
		return fmt.Errorf("adapter name and factory are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("adapter %q is already registered", name)
	}
	r.factories[name] = factory
	return nil
}

func (r *Registry) Open(name string, config json.RawMessage) (Adapter, error) {
	r.mu.RLock()
	factory, exists := r.factories[name]
	r.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("unknown adapter %q", name)
	}
	return factory(config)
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
