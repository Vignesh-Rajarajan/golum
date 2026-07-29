package execenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultExecTimeout = 120 * time.Second
	MaxReadBytes       = 256 * 1024
	MaxShellOutput     = 30 * 1024
	binaryProbeBytes   = 8 * 1024
)

// ErrOutsideWorkspace is returned when a path resolves outside the workspace root.
var ErrOutsideWorkspace = errors.New("path is outside the workspace root")

// ErrSensitivePath is returned when a path matches a sensitive pattern.
var ErrSensitivePath = errors.New("access to sensitive path denied")

// ErrBinaryFile is returned when a read target looks like a binary file.
var ErrBinaryFile = errors.New("refusing to read binary file")

// OsExecutionEnv is a concrete ExecutionEnv rooted at a workspace directory.
type OsExecutionEnv struct {
	root string
}

// NewOsExecutionEnv creates an execution environment rooted at root.
// root is cleaned and made absolute.
func NewOsExecutionEnv(root string) (*OsExecutionEnv, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		// Root must exist; if EvalSymlinks fails, keep Abs path.
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("eval root symlinks: %w", err)
		}
	}
	return &OsExecutionEnv{root: abs}, nil
}

// CWD returns the workspace root.
func (e *OsExecutionEnv) CWD() string { return e.root }

// resolve turns a user-supplied path into an absolute path confined to root.
// For non-existent paths (needed by write_file), walks up to the deepest existing
// ancestor, EvalSymlinks that, then re-joins the remainder.
func (e *OsExecutionEnv) resolve(path string) (string, error) {
	if path == "" {
		path = "."
	}
	var candidate string
	if filepath.IsAbs(path) {
		candidate = filepath.Clean(path)
	} else {
		candidate = filepath.Clean(filepath.Join(e.root, path))
	}

	resolved, err := resolveWithSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !confined(e.root, resolved) {
		return "", fmt.Errorf("%w %q (resolved %q, root %q)", ErrOutsideWorkspace, path, resolved, e.root)
	}
	if isSensitive(resolved, e.root) {
		return "", fmt.Errorf("%w: %s", ErrSensitivePath, path)
	}
	return resolved, nil
}

func resolveWithSymlinks(candidate string) (string, error) {
	// Walk up to deepest existing ancestor for EvalSymlinks.
	cur := candidate
	var missing []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
	}
	evaluated, err := filepath.EvalSymlinks(cur)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			evaluated = cur
		} else {
			return "", err
		}
	}
	return filepath.Clean(filepath.Join(append([]string{evaluated}, missing...)...)), nil
}

func confined(root, resolved string) bool {
	if resolved == root {
		return true
	}
	prefix := root + string(os.PathSeparator)
	return strings.HasPrefix(resolved, prefix)
}

func isSensitive(resolved, root string) bool {
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		rel = resolved
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(resolved)
	lowerBase := strings.ToLower(base)
	lowerRel := strings.ToLower(rel)

	// Exact / glob-ish sensitive patterns
	if strings.HasPrefix(lowerBase, ".env") {
		return true
	}
	switch {
	case strings.HasSuffix(lowerBase, ".pem"),
		strings.HasSuffix(lowerBase, ".key"),
		strings.HasSuffix(lowerBase, ".p12"),
		strings.HasPrefix(lowerBase, "id_rsa"),
		strings.Contains(lowerBase, "credentials"):
		return true
	}
	if strings.Contains(lowerRel, "/.aws/") || strings.HasPrefix(lowerRel, ".aws/") {
		return true
	}
	if strings.Contains(lowerRel, "/.ssh/") || strings.HasPrefix(lowerRel, ".ssh/") {
		return true
	}
	if lowerRel == ".git/config" || strings.HasSuffix(lowerRel, "/.git/config") {
		return true
	}
	return false
}

// ReadTextFile reads a text file capped at MaxReadBytes.
func (e *OsExecutionEnv) ReadTextFile(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	abs, err := e.resolve(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()

	probe := make([]byte, binaryProbeBytes)
	n, err := io.ReadFull(f, probe)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	probe = probe[:n]
	if containsNUL(probe) {
		return "", fmt.Errorf("%w: %s", ErrBinaryFile, path)
	}

	restBudget := MaxReadBytes - n
	if restBudget < 0 {
		restBudget = 0
	}
	rest, err := io.ReadAll(io.LimitReader(f, int64(restBudget+1)))
	if err != nil {
		return "", err
	}
	data := append(probe, rest...)
	truncated := false
	if len(data) > MaxReadBytes {
		data = data[:MaxReadBytes]
		truncated = true
	}
	out := string(data)
	if truncated {
		out += "\n\n[truncated: file exceeds 256 KB read limit]"
	}
	return out, nil
}

func containsNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// WriteFile atomically writes content to path (temp + rename), creating parents inside root.
func (e *OsExecutionEnv) WriteFile(ctx context.Context, path, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, err := e.resolve(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	if !confined(e.root, dir) && dir != e.root {
		return fmt.Errorf("%w %q", ErrOutsideWorkspace, path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".golum-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, abs)
}

// ListDir lists directory entries, skipping .git.
func (e *OsExecutionEnv) ListDir(ctx context.Context, path string) ([]DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := e.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, ent := range entries {
		name := ent.Name()
		if name == ".git" {
			continue
		}
		info, err := ent.Info()
		size := int64(0)
		if err == nil {
			size = info.Size()
		}
		out = append(out, DirEntry{
			Name:  name,
			IsDir: ent.IsDir(),
			Size:  size,
		})
	}
	return out, nil
}

// Stat returns file info for path.
func (e *OsExecutionEnv) Stat(ctx context.Context, path string) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	abs, err := e.resolve(path)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		IsDir:   info.IsDir(),
		ModTime: info.ModTime(),
	}, nil
}

// Walk walks the tree under root (relative to workspace), skipping .git.
func (e *OsExecutionEnv) Walk(ctx context.Context, root string, fn func(rel string, d fs.DirEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, err := e.resolve(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(e.root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		return fn(rel, d)
	})
}

// Exec runs command via sh -c with CWD = root, scrubbed env, and process-group kill on cancel.
func (e *OsExecutionEnv) Exec(ctx context.Context, command string, opts ExecOptions) (ShellResult, error) {
	if err := ctx.Err(); err != nil {
		return ShellResult{}, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultExecTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", command)
	cmd.Dir = e.root
	cmd.Env = scrubEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second

	var stdout, stderr strings.Builder
	cmd.Stdout = &limitedWriter{w: &stdout, limit: MaxShellOutput}
	cmd.Stderr = &limitedWriter{w: &stderr, limit: MaxShellOutput}

	err := cmd.Start()
	if err != nil {
		return ShellResult{}, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-execCtx.Done():
		killProcessGroup(cmd)
		<-done
		result := ShellResult{
			Stdout:   truncateHeadTail(stdout.String(), MaxShellOutput),
			Stderr:   truncateHeadTail(stderr.String(), MaxShellOutput),
			ExitCode: -1,
			TimedOut: errors.Is(execCtx.Err(), context.DeadlineExceeded),
		}
		if !result.TimedOut {
			return result, execCtx.Err()
		}
		return result, nil
	case err := <-done:
		result := ShellResult{
			Stdout: truncateHeadTail(stdout.String(), MaxShellOutput),
			Stderr: truncateHeadTail(stderr.String(), MaxShellOutput),
		}
		if err == nil {
			result.ExitCode = 0
			return result, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			result.ExitCode = ee.ExitCode()
			return result, nil
		}
		return result, err
	}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if upper == "OPENAI_API_KEY" || upper == "OPENROUTER_API_KEY" {
			continue
		}
		if strings.HasSuffix(upper, "_API_KEY") ||
			strings.HasSuffix(upper, "_TOKEN") ||
			strings.HasSuffix(upper, "_SECRET") {
			continue
		}
		out = append(out, e)
	}
	return out
}

type limitedWriter struct {
	w     *strings.Builder
	limit int
	n     int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	remain := lw.limit - lw.n
	if remain <= 0 {
		return len(p), nil
	}
	if len(p) > remain {
		_, _ = lw.w.Write(p[:remain])
		lw.n += remain
		return len(p), nil
	}
	_, _ = lw.w.Write(p)
	lw.n += len(p)
	return len(p), nil
}

func truncateHeadTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := (max - 40) / 2
	if half < 1 {
		return s[:max]
	}
	return s[:half] + "\n\n...[output truncated]...\n\n" + s[len(s)-half:]
}
