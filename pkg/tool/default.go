package tool

// DefaultRegistry builds a registry with the Phase 1 tool set.
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
