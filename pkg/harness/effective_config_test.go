package harness

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
)

func TestEffectiveConfigReplaysAndForkInherits(t *testing.T) {
	store, err := session.OpenSQLiteStore(filepath.Join(t.TempDir(), "golum.db"),
		&config.Config{Model: "default"}, prompt.PromptConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw, err := store.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.AppendModelChange("branch-model"); err != nil {
		t.Fatal(err)
	}
	point, err := raw.AppendUserMessage("point")
	if err != nil {
		t.Fatal(err)
	}
	got := DeriveEffectiveConfig(raw, EffectiveConfig{Model: "default"})
	if got.Model != "branch-model" {
		t.Fatalf("model=%q", got.Model)
	}
	fork, err := store.Fork(context.Background(), raw.ID(), point.ID)
	if err != nil {
		t.Fatal(err)
	}
	got = DeriveEffectiveConfig(fork, EffectiveConfig{Model: "default"})
	if got.Model != "branch-model" {
		t.Fatalf("fork model=%q", got.Model)
	}
}
