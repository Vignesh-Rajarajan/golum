package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

type shellTool struct{}

func (shellTool) Name() string { return "shell" }
func (shellTool) Description() string {
	return "Run a shell command (sh -c) with CWD fixed to the workspace root. Requires approval. Optional timeout in seconds."
}
func (shellTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"command": map[string]any{"type": "string", "description": "Shell command to run"},
		"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (optional, default 120)"},
	}, []string{"command"})
}

func (shellTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	command, ok := stringArg(args, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return errResult("missing required argument: command"), nil
	}
	opts := execenv.ExecOptions{}
	if secs, ok := intArg(args, "timeout"); ok && secs > 0 {
		opts.Timeout = time.Duration(secs) * time.Second
	}
	res, err := env.Exec(ctx, command, opts)
	if err != nil {
		return errResult(err.Error()), nil
	}
	var b strings.Builder
	if res.Stdout != "" {
		b.WriteString(res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if res.Stderr != "" {
		b.WriteString(res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	if res.TimedOut {
		fmt.Fprintf(&b, "\n[command timed out]\n")
	}
	fmt.Fprintf(&b, "\n[exit code %d]\n", res.ExitCode)
	isErr := res.ExitCode != 0 || res.TimedOut
	return Result{
		Content: b.String(),
		IsError: isErr,
		Display: fmt.Sprintf("shell exit %d", res.ExitCode),
	}, nil
}
