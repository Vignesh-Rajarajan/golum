package tool

import (
	"context"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

// AgentTool is a tool the model can invoke.
type AgentTool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error)
}

// Result is what the model sees (and what the UI renders for that tool turn).
type Result struct {
	Content string
	IsError bool
	Display string // optional one-line UI summary
}

// Registry holds registered tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]AgentTool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]AgentTool)}
}

// Register adds a tool. Later registrations with the same name replace earlier ones.
func (r *Registry) Register(t AgentTool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (AgentTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns registered tool names in stable sorted order? Insertion order via map is unstable;
// return unsorted list — callers that need order should sort.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	return out
}

// AsLLMTools converts the registry to llm.Tool definitions for the API request.
func (r *Registry) AsLLMTools() []llm.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return out
}

// RequiresApproval reports whether the named tool needs a y/n gate.
func RequiresApproval(name string) bool {
	switch name {
	case "write_file", "edit", "shell":
		return true
	default:
		return false
	}
}
