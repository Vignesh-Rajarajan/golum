package session

import (
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
)

func BenchmarkJournalAppend(b *testing.B) {
	cm := contextmgr.NewContextManager(&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{}, nil, nil)
	sess := NewInMemorySession("bench", cm)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = sess.AppendUserMessage("hello")
	}
}

func BenchmarkContextRebuild(b *testing.B) {
	cm := contextmgr.NewContextManager(&config.Config{Model: "gpt-4o"}, prompt.PromptConfig{}, nil, nil)
	sess := NewInMemorySession("bench", cm)
	for i := 0; i < 50; i++ {
		_, _ = sess.AppendUserMessage("hello")
		_, _ = sess.AppendAssistantMessage("ok", nil)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sess.RebuildContext()
	}
}
