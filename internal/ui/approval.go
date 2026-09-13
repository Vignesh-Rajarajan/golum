package ui

import (
	"context"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

// approvalBroker implements harness.ApprovalBroker for the TUI.
// Request emits nothing itself — the loop already emits EventToolCallAwaitingApproval;
// Request blocks on a fresh channel until Decide is called from Update().
type approvalBroker struct {
	mu      sync.Mutex
	pending chan bool
}

func newApprovalBroker() *approvalBroker {
	return &approvalBroker{}
}

func (b *approvalBroker) Request(ctx context.Context, _ llm.ToolCall) (bool, error) {
	ch := make(chan bool, 1)
	b.mu.Lock()
	b.pending = ch
	b.mu.Unlock()

	select {
	case <-ctx.Done():
		b.mu.Lock()
		if b.pending == ch {
			b.pending = nil
		}
		b.mu.Unlock()
		return false, ctx.Err()
	case approved := <-ch:
		b.mu.Lock()
		if b.pending == ch {
			b.pending = nil
		}
		b.mu.Unlock()
		return approved, nil
	}
}

// Decide sends the user's y/n decision. Safe to call when no request is pending.
func (b *approvalBroker) Decide(approved bool) {
	b.mu.Lock()
	ch := b.pending
	b.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- approved:
	default:
	}
}

func (b *approvalBroker) Waiting() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending != nil
}
