package hooks

import (
	"context"
	"sync"
	"time"
)

// HookKind identifies a lifecycle point.
type HookKind string

const (
	BeforeAgentStart HookKind = "before_agent_start"
	BeforeToolExec   HookKind = "before_tool_exec"
	AfterToolExec    HookKind = "after_tool_exec"
	AfterAgentTurn   HookKind = "after_agent_turn"
)

// HookEvent is delivered to subscribers.
type HookEvent struct {
	Kind    HookKind
	Payload any
	Time    time.Time
}

// HookHandler handles a hook event.
type HookHandler func(ctx context.Context, ev HookEvent) error

// HooksManager fans out lifecycle events to independent subscribers.
type HooksManager struct {
	mu       sync.RWMutex
	handlers map[HookKind][]HookHandler
}

// New creates an empty HooksManager.
func New() *HooksManager {
	return &HooksManager{handlers: make(map[HookKind][]HookHandler)}
}

// On registers a handler for a kind.
func (m *HooksManager) On(kind HookKind, h HookHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[kind] = append(m.handlers[kind], h)
}

// Emit delivers an event to all handlers for its kind. First error is returned.
func (m *HooksManager) Emit(ctx context.Context, kind HookKind, payload any) error {
	m.mu.RLock()
	hs := append([]HookHandler(nil), m.handlers[kind]...)
	m.mu.RUnlock()
	ev := HookEvent{Kind: kind, Payload: payload, Time: time.Now().UTC()}
	for _, h := range hs {
		if err := h(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}
