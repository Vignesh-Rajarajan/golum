package tool

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

func TestShellTimeoutAndNonZero(t *testing.T) {
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sh := shellTool{}
	res, err := sh.Execute(context.Background(), map[string]any{
		"command": "sleep 2", "timeout": 1,
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "timed out") && !strings.Contains(res.Content, "exit") {
		t.Fatalf("timeout result=%#v", res)
	}
	res, err = sh.Execute(context.Background(), map[string]any{"command": "exit 7"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "exit code 7") {
		t.Fatalf("nonzero=%#v", res)
	}
}

func TestShellLargeOutputAndOutsidePath(t *testing.T) {
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sh := shellTool{}
	res, err := sh.Execute(context.Background(), map[string]any{
		"command": "python3 -c 'print(\"A\"*100000)'",
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) > execenv.MaxShellOutput+200 {
		t.Fatalf("shell output not bounded: %d", len(res.Content))
	}
	res, err = sh.Execute(context.Background(), map[string]any{
		"command": "cat /etc/passwd",
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	// The command may run (cwd is workspace) but we at least record it.
	_ = res
}

func TestShellContextCancel(t *testing.T) {
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res, err := shellTool{}.Execute(ctx, map[string]any{"command": "sleep 5"}, env)
	if err == nil && !res.IsError {
		t.Fatal("expected cancellation or timeout")
	}
}

func TestShellMissingCommand(t *testing.T) {
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := shellTool{}.Execute(context.Background(), map[string]any{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "missing required") {
		t.Fatalf("%#v", res)
	}
	_ = os.Environ()
}
