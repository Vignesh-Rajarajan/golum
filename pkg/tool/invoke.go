package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/mcp"
)

const InvokeName = "invoke"

type invokeTool struct{ cat *mcp.Catalog }

func NewInvokeTool(cat *mcp.Catalog) AgentTool {
	if cat == nil {
		cat = mcp.NewCatalog()
	}
	return invokeTool{cat: cat}
}

func (invokeTool) Name() string { return InvokeName }

func (invokeTool) Description() string {
	return "Call an extra operation discovered from MCP (or other backends). " +
		"Use action=list to see servers and operations, action=help for a schema, action=call to run one. " +
		"These operations are not registered as native tools."
}

func (invokeTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"action":         map[string]any{"type": "string", "description": "list, help, or call"},
		"server":         map[string]any{"type": "string", "description": "Backend/server name"},
		"name":           map[string]any{"type": "string", "description": "Operation name"},
		"arguments":      map[string]any{"type": "object", "description": "Arguments for action=call"},
		"arguments_path": map[string]any{"type": "string", "description": "Workspace file whose JSON contents are the call arguments"},
	}, []string{"action"})
}

func (t invokeTool) Catalog() *mcp.Catalog { return t.cat }

func (t invokeTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	action, _ := stringArg(args, "action")
	action = strings.ToLower(strings.TrimSpace(action))
	server, _ := stringArg(args, "server")
	name, _ := stringArg(args, "name")
	switch action {
	case "list":
		return t.list(ctx)
	case "help":
		return t.help(ctx, server, name)
	case "call":
		callArgs, err := t.callArgs(ctx, args, env)
		if err != nil {
			return errResult(err.Error()), nil
		}
		return t.call(ctx, server, name, callArgs)
	default:
		return errResult("action must be list, help, or call"), nil
	}
}

func (t invokeTool) list(ctx context.Context) (Result, error) {
	var b strings.Builder
	backends := t.cat.Backends()
	if len(backends) == 0 {
		return Result{Content: "No extra operations configured."}, nil
	}
	for _, be := range backends {
		ops, err := be.List(ctx)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", be.Name(), err)
			continue
		}
		fmt.Fprintf(&b, "%s:\n", be.Name())
		for _, op := range ops {
			fmt.Fprintf(&b, "  %s", op.Name)
			if op.Version != "" {
				fmt.Fprintf(&b, " v%s", op.Version)
			}
			if op.Description != "" {
				fmt.Fprintf(&b, " — %s", op.Description)
			}
			b.WriteByte('\n')
		}
	}
	return Result{Content: strings.TrimSpace(b.String()), Display: fmt.Sprintf("listed %d servers", len(backends))}, nil
}

func (t invokeTool) help(ctx context.Context, server, name string) (Result, error) {
	be, op, err := t.lookup(ctx, server, name)
	if err != nil {
		return errResult(err.Error()), nil
	}
	schema, _ := json.MarshalIndent(op.Schema, "", "  ")
	body := fmt.Sprintf("server: %s\nname: %s\nversion: %s\n%s\n\nschema:\n%s",
		be.Name(), op.Name, op.Version, op.Description, schema)
	return Result{Content: body, Display: fmt.Sprintf("%s/%s", be.Name(), op.Name)}, nil
}

func (t invokeTool) call(ctx context.Context, server, name string, args map[string]any) (Result, error) {
	be, op, err := t.lookup(ctx, server, name)
	if err != nil {
		return errResult(err.Error()), nil
	}
	out, err := be.Call(ctx, op.Name, args)
	display := fmt.Sprintf("%s/%s", be.Name(), op.Name)
	if op.Version != "" {
		display += "@" + op.Version
	}
	if err != nil {
		return Result{Content: err.Error(), IsError: true, Display: display}, nil
	}
	return Result{Content: out, Display: display}, nil
}

func (t invokeTool) lookup(ctx context.Context, server, name string) (mcp.Backend, mcp.Operation, error) {
	if name == "" {
		return nil, mcp.Operation{}, fmt.Errorf("missing operation name")
	}
	backends := t.cat.Backends()
	if server != "" {
		be, ok := t.cat.Get(server)
		if !ok {
			return nil, mcp.Operation{}, fmt.Errorf("unknown server %q", server)
		}
		backends = []mcp.Backend{be}
	}
	for _, be := range backends {
		ops, err := be.List(ctx)
		if err != nil {
			continue
		}
		for _, op := range ops {
			if op.Name == name {
				return be, op, nil
			}
		}
	}
	return nil, mcp.Operation{}, fmt.Errorf("unknown operation %q", name)
}

func (t invokeTool) callArgs(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (map[string]any, error) {
	if path, ok := stringArg(args, "arguments_path"); ok && path != "" {
		raw, err := env.ReadTextFile(ctx, path)
		if err != nil {
			return nil, err
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return nil, fmt.Errorf("arguments_path: %w", err)
		}
		return parsed, nil
	}
	if raw, ok := args["arguments"]; ok {
		switch v := raw.(type) {
		case map[string]any:
			return v, nil
		case string:
			if strings.TrimSpace(v) == "" {
				return map[string]any{}, nil
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(v), &parsed); err != nil {
				return nil, fmt.Errorf("arguments: %w", err)
			}
			return parsed, nil
		}
	}
	return map[string]any{}, nil
}

func CatalogOf(r *Registry) *mcp.Catalog {
	if r == nil {
		return nil
	}
	t, ok := r.Get(InvokeName)
	if !ok {
		return nil
	}
	if h, ok := t.(interface{ Catalog() *mcp.Catalog }); ok {
		return h.Catalog()
	}
	return nil
}

func RegisterInvoke(r *Registry, cat *mcp.Catalog) {
	if r == nil {
		return
	}
	r.Register(NewInvokeTool(cat))
}

// LoadMCPInto fills the invoke catalog from a config file if the registry has invoke.
func LoadMCPInto(ctx context.Context, r *Registry, configPath string) {
	cat := CatalogOf(r)
	if cat == nil {
		return
	}
	loaded, err := mcp.OpenCatalog(ctx, configPath)
	if err != nil || loaded == nil {
		return
	}
	for _, b := range loaded.Backends() {
		cat.Add(b)
	}
}
