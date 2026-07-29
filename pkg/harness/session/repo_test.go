package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
)

func TestFileSessionRepo_CreateOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Model: "gpt-4o"}
	tools := []llm.Tool{{Type: "function", Function: llm.ToolFunction{Name: "read_file", Description: "r"}}}
	repo, err := NewFileSessionRepo(dir, cfg, prompt.PromptConfig{CWD: dir}, tools)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := repo.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendUserMessage("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendAssistantMessage("hi", nil); err != nil {
		t.Fatal(err)
	}
	id := sess.ID()

	opened, err := repo.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := opened.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	// system + user + assistant
	if len(msgs) < 3 {
		t.Fatalf("expected rehydrated messages, got %d", len(msgs))
	}
	list, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("list=%+v", list)
	}
	if err := repo.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected deleted, err=%v", err)
	}
}
