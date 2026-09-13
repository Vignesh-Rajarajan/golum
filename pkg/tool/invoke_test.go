package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/mcp"
)

func TestInvokeListsAndCallsFakeBackend(t *testing.T) {
	cat := mcp.NewCatalog()
	cat.Add(mcp.Echo())
	reg := NewRegistry()
	RegisterInvoke(reg, cat)
	names := reg.AsLLMTools()
	if len(names) != 1 || names[0].Function.Name != InvokeName {
		t.Fatalf("native tools = %#v, mcp ops must not be registered", names)
	}

	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inv, _ := reg.Get(InvokeName)
	listed, err := inv.Execute(context.Background(), map[string]any{"action": "list"}, env)
	if err != nil || listed.IsError || !strings.Contains(listed.Content, "echo") {
		t.Fatalf("list: %+v err=%v", listed, err)
	}
	called, err := inv.Execute(context.Background(), map[string]any{
		"action": "call", "server": "echo", "name": "echo",
		"arguments": map[string]any{"text": "ECHO_OK"},
	}, env)
	if err != nil || called.IsError || called.Content != "ECHO_OK" {
		t.Fatalf("call: %+v err=%v", called, err)
	}
	if !strings.Contains(called.Display, "echo/echo") {
		t.Fatalf("display=%q", called.Display)
	}
}

func TestDefaultRegistryDoesNotAdvertiseMCPOps(t *testing.T) {
	reg, _ := DefaultRegistry(nil)
	CatalogOf(reg).Add(mcp.Echo())
	for _, tdef := range reg.AsLLMTools() {
		if tdef.Function.Name == "echo" {
			t.Fatal("discovered MCP op leaked into the native roster")
		}
	}
	if _, ok := reg.Get(InvokeName); !ok {
		t.Fatal("invoke gateway missing")
	}
}
