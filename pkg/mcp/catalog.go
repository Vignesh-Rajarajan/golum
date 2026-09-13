package mcp

import (
	"context"
	"sync"
)

// Operation is one discovered remote tool. It is never registered as a native
// model tool; the invoke gateway is the only way to call it.
type Operation struct {
	Name        string
	Description string
	Version     string
	Schema      map[string]any
}

// Backend is an in-process or remote source of extra operations.
type Backend interface {
	Name() string
	List(ctx context.Context) ([]Operation, error)
	Call(ctx context.Context, name string, args map[string]any) (string, error)
}

// Catalog holds backends the invoke gateway can reach.
type Catalog struct {
	mu       sync.RWMutex
	backends []Backend
}

func NewCatalog() *Catalog { return &Catalog{} }

func (c *Catalog) Add(b Backend) {
	if c == nil || b == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backends = append(c.backends, b)
}

func (c *Catalog) Backends() []Backend {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Backend, len(c.backends))
	copy(out, c.backends)
	return out
}

func (c *Catalog) Get(name string) (Backend, bool) {
	for _, b := range c.Backends() {
		if b.Name() == name {
			return b, true
		}
	}
	return nil, false
}
