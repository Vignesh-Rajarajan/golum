package tool

import "github.com/Vignesh-Rajarajan/golum/pkg/memory"

// DefaultRegistry builds a registry with the standard tool set.
// todosStore may be nil; a new store is created when nil.
func DefaultRegistry(todosStore *TodoStore) (*Registry, *TodoStore) {
	if todosStore == nil {
		todosStore = NewTodoStore()
	}
	r := NewRegistry()
	r.Register(readFileTool{})
	r.Register(writeFileTool{})
	r.Register(editTool{})
	r.Register(listDirTool{})
	r.Register(globTool{})
	r.Register(grepTool{})
	r.Register(shellTool{})
	r.Register(NewTodosTool(todosStore))
	return r, todosStore
}

// RegisterMemory adds the memory tool. It is separate from DefaultRegistry
// because it needs a store, which only exists when session persistence is
// available — the prompt's tool list is derived from the registry, so an
// unavailable memory tool is simply never advertised to the model.
func RegisterMemory(r *Registry, store *memory.Store, sessionID func() string) {
	if r == nil || store == nil {
		return
	}
	r.Register(NewMemoryTool(store, sessionID))
}
