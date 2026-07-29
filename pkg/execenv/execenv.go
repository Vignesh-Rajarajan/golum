package execenv

import (
	"context"
	"io/fs"
	"time"
)

// DirEntry is a directory listing entry.
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// FileInfo describes a file or directory.
type FileInfo struct {
	Name    string
	Size    int64
	IsDir   bool
	ModTime time.Time
}

// ExecOptions configures a shell invocation.
type ExecOptions struct {
	Timeout time.Duration // 0 = default (120s)
}

// ShellResult is the outcome of a shell command.
type ShellResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// FileSystem is the workspace-scoped filesystem API for tools.
type FileSystem interface {
	ReadTextFile(ctx context.Context, path string) (string, error)
	WriteFile(ctx context.Context, path, content string) error
	ListDir(ctx context.Context, path string) ([]DirEntry, error)
	Stat(ctx context.Context, path string) (FileInfo, error)
	Walk(ctx context.Context, root string, fn func(rel string, d fs.DirEntry) error) error
}

// Shell runs commands with CWD fixed to the workspace root.
type Shell interface {
	Exec(ctx context.Context, command string, opts ExecOptions) (ShellResult, error)
}

// ExecutionEnv is the single security choke point for tool I/O.
type ExecutionEnv interface {
	FileSystem
	Shell
	CWD() string
}
