package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMissingIsEmpty(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadConfigReadsServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"echo":{"command":"true"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["echo"].Command != "true" {
		t.Fatalf("got %+v", cfg)
	}
}
