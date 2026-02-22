package tools

import (
	"context"
)

// Tool defines the interface for all agent capabilities.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any // JSON Schema for the tool's inputs
	Execute(ctx context.Context, input string) (string, error)
}

// Registry manages the set of available tools.
type Registry struct {
	Tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		Tools: make(map[string]Tool),
	}
}

func (r *Registry) Register(t Tool) {
	r.Tools[t.Name()] = t
}

func (r *Registry) Get(name string) Tool {
	return r.Tools[name]
}
