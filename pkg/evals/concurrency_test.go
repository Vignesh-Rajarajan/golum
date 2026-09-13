package evals

import (
	"sync"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

func TestConcurrentSessionsAreIsolated(t *testing.T) {
	const n = 100
	ids := make([]string, n)
	var wg sync.WaitGroup
	errc := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			cm := contextmgr.NewContextManager(&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{}, nil, nil)
			sess := session.NewInMemorySession("", cm)
			ids[i] = sess.ID()
			todos := tool.NewTodoStore()
			if _, err := sess.AppendUserMessage("hello"); err != nil {
				errc <- err
				return
			}
			if _, err := sess.AppendTodos(todos.List()); err != nil {
				errc <- err
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("bad session id %q", id)
		}
		seen[id] = true
	}
}
