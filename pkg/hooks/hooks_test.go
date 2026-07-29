package hooks

import (
	"context"
	"testing"
)

func TestHooksManager_Emit(t *testing.T) {
	m := New()
	called := 0
	m.On(BeforeAgentStart, func(ctx context.Context, ev HookEvent) error {
		called++
		return nil
	})
	m.On(BeforeToolExec, func(ctx context.Context, ev HookEvent) error {
		called += 10
		return nil
	})
	if err := m.Emit(context.Background(), BeforeAgentStart, nil); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("called=%d", called)
	}
	if err := m.Emit(context.Background(), BeforeToolExec, "shell"); err != nil {
		t.Fatal(err)
	}
	if called != 11 {
		t.Fatalf("called=%d", called)
	}
}
