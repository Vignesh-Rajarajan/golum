package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ConfigFile is the Claude-style MCP server map.
type ConfigFile struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// ServerSpec launches a stdio MCP server.
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// LoadConfig reads GOLUM_MCP_CONFIG if set, else path (typically .golum/mcp.json).
func LoadConfig(path string) (ConfigFile, error) {
	if env := os.Getenv("GOLUM_MCP_CONFIG"); env != "" {
		path = env
	}
	if strings.TrimSpace(path) == "" {
		return ConfigFile{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ConfigFile{}, nil
		}
		return ConfigFile{}, err
	}
	var cfg ConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ConfigFile{}, fmt.Errorf("mcp config: %w", err)
	}
	return cfg, nil
}

// OpenCatalog loads configured stdio servers into a catalog. Failed servers
// are skipped so a bad entry cannot block the harness.
func OpenCatalog(ctx context.Context, path string) (*Catalog, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	cat := NewCatalog()
	for name, spec := range cfg.MCPServers {
		if strings.TrimSpace(spec.Command) == "" {
			continue
		}
		b, err := StartStdio(ctx, name, spec)
		if err != nil {
			continue
		}
		cat.Add(b)
	}
	return cat, nil
}

// Fake is an in-process backend for tests and evals.
type Fake struct {
	Server  string
	Ops     []Operation
	Handler func(name string, args map[string]any) (string, error)
}

func (f Fake) Name() string { return f.Server }

func (f Fake) List(context.Context) ([]Operation, error) { return f.Ops, nil }

func (f Fake) Call(_ context.Context, name string, args map[string]any) (string, error) {
	if f.Handler != nil {
		return f.Handler(name, args)
	}
	return "", fmt.Errorf("no handler for %s", name)
}

// Echo is a one-op fake used by evals.
func Echo() Fake {
	return Fake{
		Server: "echo",
		Ops: []Operation{{
			Name:        "echo",
			Description: "Return a sentinel",
			Version:     "1",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
				},
			},
		}},
		Handler: func(_ string, args map[string]any) (string, error) {
			text, _ := args["text"].(string)
			if text == "" {
				text = "ECHO_OK"
			}
			return text, nil
		},
	}
}
