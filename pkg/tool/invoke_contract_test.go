package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/mcp"
)

func TestInvokeUnknownsAndFailures(t *testing.T) {
	cat := mcp.NewCatalog()
	cat.Add(mcp.Echo())
	cat.Add(mcp.Fake{Server: "down", Ops: []mcp.Operation{{Name: "echo"}}, Handler: func(string, map[string]any) (string, error) {
		return "", errors.New("backend down")
	}})
	reg := NewRegistry()
	RegisterInvoke(reg, cat)
	inv, _ := reg.Get(InvokeName)
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	help, err := inv.Execute(ctx, map[string]any{"action": "help", "server": "echo", "name": "echo"}, env)
	if err != nil || help.IsError || !strings.Contains(help.Content, "schema") {
		t.Fatalf("help: %+v err=%v", help, err)
	}
	unk, _ := inv.Execute(ctx, map[string]any{"action": "call", "name": "nope"}, env)
	if !unk.IsError {
		t.Fatal("unknown operation must fail")
	}
	unkSrv, _ := inv.Execute(ctx, map[string]any{"action": "call", "server": "ghost", "name": "echo"}, env)
	if !unkSrv.IsError {
		t.Fatal("unknown server must fail")
	}
	fail, _ := inv.Execute(ctx, map[string]any{"action": "call", "server": "down", "name": "echo"}, env)
	if !fail.IsError {
		t.Fatal("backend failure must surface")
	}
	badJSON, _ := inv.Execute(ctx, map[string]any{"action": "call", "server": "echo", "name": "echo", "arguments": "{"}, env)
	if !badJSON.IsError {
		t.Fatal("invalid JSON arguments must fail")
	}
}

func TestInvokeArgumentsPathAndStableSchema(t *testing.T) {
	cat := mcp.NewCatalog()
	cat.Add(mcp.Echo())
	reg := NewRegistry()
	RegisterInvoke(reg, cat)
	inv, _ := reg.Get(InvokeName)
	root := t.TempDir()
	env, err := execenv.NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "args.json"), []byte(`{"text":"FROM_FILE"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := inv.Execute(context.Background(), map[string]any{
		"action": "call", "server": "echo", "name": "echo", "arguments_path": "args.json",
	}, env)
	if err != nil || res.IsError || res.Content != "FROM_FILE" {
		t.Fatalf("arguments_path: %+v err=%v", res, err)
	}
	first, _ := json.Marshal(inv.Parameters())
	cat.Add(mcp.Fake{Server: "other", Ops: []mcp.Operation{{Name: "echo", Description: "dup"}}})
	second, _ := json.Marshal(inv.Parameters())
	if string(first) != string(second) {
		t.Fatal("invoke schema must stay stable when backends change")
	}
	for _, def := range reg.AsLLMTools() {
		if def.Function.Name == "echo" {
			t.Fatal("dynamic op leaked into the native roster")
		}
	}
}

func TestInvokeTimeout(t *testing.T) {
	cat := mcp.NewCatalog()
	cat.Add(mcp.Fake{Server: "slow", Ops: []mcp.Operation{{Name: "sleep"}}, Handler: func(string, map[string]any) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "late", nil
	}})
	reg := NewRegistry()
	RegisterInvoke(reg, cat)
	inv, _ := reg.Get(InvokeName)
	env, _ := execenv.NewOsExecutionEnv(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _ = inv.Execute(ctx, map[string]any{"action": "call", "server": "slow", "name": "sleep"}, env)
}

func TestInvokeDuplicateNamesStayServerScoped(t *testing.T) {
	cat := mcp.NewCatalog()
	cat.Add(mcp.Fake{Server: "a", Ops: []mcp.Operation{{Name: "echo"}}, Handler: func(string, map[string]any) (string, error) {
		return "from-a", nil
	}})
	cat.Add(mcp.Fake{Server: "b", Ops: []mcp.Operation{{Name: "echo"}}, Handler: func(string, map[string]any) (string, error) {
		return "from-b", nil
	}})
	reg := NewRegistry()
	RegisterInvoke(reg, cat)
	inv, _ := reg.Get(InvokeName)
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := inv.Execute(context.Background(), map[string]any{
		"action": "call", "server": "b", "name": "echo",
	}, env)
	if err != nil || res.IsError || res.Content != "from-b" {
		t.Fatalf("duplicate names must stay server-scoped: %+v err=%v", res, err)
	}
}
