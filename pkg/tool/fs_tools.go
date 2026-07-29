package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

type readFileTool struct{}

func (readFileTool) Name() string { return "read_file" }
func (readFileTool) Description() string {
	return "Read a text file from the workspace. Returns content with line numbers. Optional offset (1-based) and limit."
}
func (readFileTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"path":   map[string]any{"type": "string", "description": "Path relative to workspace root"},
		"offset": map[string]any{"type": "integer", "description": "1-based start line (optional)"},
		"limit":  map[string]any{"type": "integer", "description": "Max lines to return (optional)"},
	}, []string{"path"})
}

func (readFileTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	path, ok := stringArg(args, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return errResult("missing required argument: path"), nil
	}
	content, err := env.ReadTextFile(ctx, path)
	if err != nil {
		return errResult(err.Error()), nil
	}
	offset, _ := intArg(args, "offset")
	limit, _ := intArg(args, "limit")
	numbered := formatLineNumbered(content, offset, limit)
	lines := strings.Count(numbered, "\n")
	return Result{
		Content: numbered,
		Display: fmt.Sprintf("read %d lines from %s", lines, path),
	}, nil
}

type writeFileTool struct{}

func (writeFileTool) Name() string { return "write_file" }
func (writeFileTool) Description() string {
	return "Create or overwrite a file in the workspace with the given content. Requires approval."
}
func (writeFileTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"path":    map[string]any{"type": "string", "description": "Path relative to workspace root"},
		"content": map[string]any{"type": "string", "description": "Full file content to write"},
	}, []string{"path", "content"})
}

func (writeFileTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	path, ok := stringArg(args, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return errResult("missing required argument: path"), nil
	}
	content, ok := stringArg(args, "content")
	if !ok {
		return errResult("missing required argument: content"), nil
	}
	if err := env.WriteFile(ctx, path, content); err != nil {
		return errResult(err.Error()), nil
	}
	return Result{
		Content: fmt.Sprintf("Wrote %d bytes to %s", len(content), path),
		Display: fmt.Sprintf("wrote %s", path),
	}, nil
}

type editTool struct{}

func (editTool) Name() string { return "edit" }
func (editTool) Description() string {
	return "Replace old_string with new_string in a file. Requires a unique match unless replace_all is true. Requires approval."
}
func (editTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"path":        map[string]any{"type": "string", "description": "Path relative to workspace root"},
		"old_string":  map[string]any{"type": "string", "description": "Exact text to find"},
		"new_string":  map[string]any{"type": "string", "description": "Replacement text"},
		"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences (default false)"},
	}, []string{"path", "old_string", "new_string"})
}

func (editTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	path, ok := stringArg(args, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return errResult("missing required argument: path"), nil
	}
	oldStr, ok := stringArg(args, "old_string")
	if !ok {
		return errResult("missing required argument: old_string"), nil
	}
	newStr, ok := stringArg(args, "new_string")
	if !ok {
		return errResult("missing required argument: new_string"), nil
	}
	if oldStr == "" {
		return errResult("old_string must not be empty"), nil
	}
	replaceAll := boolArg(args, "replace_all")

	content, err := env.ReadTextFile(ctx, path)
	if err != nil {
		return errResult(err.Error()), nil
	}
	// Strip truncation marker if present from oversized files — edit should fail clearly
	if strings.Contains(content, "[truncated: file exceeds") {
		return errResult("file is too large to edit safely; use a smaller file or shell"), nil
	}

	count := strings.Count(content, oldStr)
	if count == 0 {
		return errResult(fmt.Sprintf("old_string not found in %s", path)), nil
	}
	if count > 1 && !replaceAll {
		return errResult(fmt.Sprintf("old_string is ambiguous: found %d matches in %s (set replace_all=true to replace all)", count, path)), nil
	}

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		updated = strings.Replace(content, oldStr, newStr, 1)
		count = 1
	}
	if err := env.WriteFile(ctx, path, updated); err != nil {
		return errResult(err.Error()), nil
	}
	return Result{
		Content: fmt.Sprintf("Replaced %d occurrence(s) in %s", count, path),
		Display: fmt.Sprintf("edited %s (%d)", path, count),
	}, nil
}

type listDirTool struct{}

func (listDirTool) Name() string { return "list_dir" }
func (listDirTool) Description() string {
	return "List files and directories at path. Directories are marked with a trailing slash. Skips .git."
}
func (listDirTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"path": map[string]any{"type": "string", "description": "Directory path relative to workspace root (default \".\")"},
	}, nil)
}

func (listDirTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	path, _ := stringArg(args, "path")
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	entries, err := env.ListDir(ctx, path)
	if err != nil {
		return errResult(err.Error()), nil
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(&b, "%s/\n", e.Name)
		} else {
			fmt.Fprintf(&b, "%s\n", e.Name)
		}
	}
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("listed %s (%d entries)", path, len(entries)),
	}, nil
}
