package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

// TodoItem is one tracked task.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed | cancelled
}

// TodoStore holds session-scoped todos behind a mutex.
type TodoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

// NewTodoStore creates an empty todo store.
func NewTodoStore() *TodoStore {
	return &TodoStore{items: nil}
}

// List returns a copy of current todos.
func (s *TodoStore) List() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TodoItem, len(s.items))
	copy(out, s.items)
	return out
}

// Replace replaces the entire todo list.
func (s *TodoStore) Replace(items []TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make([]TodoItem, len(items))
	copy(s.items, items)
}

// todosTool mutates in-memory session state; env is unused.
type todosTool struct {
	store *TodoStore
}

// NewTodosTool creates a todos tool bound to store.
func NewTodosTool(store *TodoStore) AgentTool {
	return &todosTool{store: store}
}

func (t *todosTool) Name() string { return "todos" }
func (t *todosTool) Description() string {
	return "Create or update the session todo list. Pass items as an array of {id, content, status}. Status: pending|in_progress|completed|cancelled."
}
func (t *todosTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"items": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
					"status":  map[string]any{"type": "string", "enum": []any{"pending", "in_progress", "completed", "cancelled"}},
				},
				"required": []string{"id", "content", "status"},
			},
			"description": "Full replacement list of todo items",
		},
	}, []string{"items"})
}

func (t *todosTool) Execute(ctx context.Context, args map[string]any, _ execenv.ExecutionEnv) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	raw, ok := args["items"]
	if !ok {
		return errResult("missing required argument: items"), nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return errResult("items must be an array"), nil
	}
	items := make([]TodoItem, 0, len(arr))
	validStatus := map[string]struct{}{
		"pending": {}, "in_progress": {}, "completed": {}, "cancelled": {},
	}
	for i, el := range arr {
		m, ok := el.(map[string]any)
		if !ok {
			return errResult(fmt.Sprintf("items[%d] must be an object", i)), nil
		}
		id, _ := stringArg(m, "id")
		content, _ := stringArg(m, "content")
		status, _ := stringArg(m, "status")
		if id == "" || content == "" || status == "" {
			return errResult(fmt.Sprintf("items[%d] requires id, content, and status", i)), nil
		}
		if _, ok := validStatus[status]; !ok {
			return errResult(fmt.Sprintf("items[%d] has invalid status %q", i, status)), nil
		}
		items = append(items, TodoItem{ID: id, Content: content, Status: status})
	}
	t.store.Replace(items)

	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "[%s] %s (%s)\n", it.Status, it.Content, it.ID)
	}
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("todos updated (%d)", len(items)),
	}, nil
}

// FormatTodosForDisplay returns a short UI summary of todos.
func FormatTodosForDisplay(items []TodoItem) string {
	if len(items) == 0 {
		return ""
	}
	sorted := make([]TodoItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		return statusRank(sorted[i].Status) < statusRank(sorted[j].Status)
	})
	var b strings.Builder
	b.WriteString("Todos:\n")
	for _, it := range sorted {
		mark := " "
		switch it.Status {
		case "completed":
			mark = "x"
		case "in_progress":
			mark = ">"
		case "cancelled":
			mark = "-"
		}
		fmt.Fprintf(&b, "  [%s] %s\n", mark, it.Content)
	}
	return b.String()
}

func statusRank(s string) int {
	switch s {
	case "in_progress":
		return 0
	case "pending":
		return 1
	case "completed":
		return 2
	default:
		return 3
	}
}
