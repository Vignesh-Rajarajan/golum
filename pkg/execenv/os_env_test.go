package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolve_rejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.resolve("../../../etc/passwd")
	if !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("expected ErrOutsideWorkspace, got %v", err)
	}
	_, err = env.resolve("/etc/passwd")
	if !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("expected ErrOutsideWorkspace for absolute path, got %v", err)
	}
}

func TestResolve_rejectsPrefixEscape(t *testing.T) {
	// /work must not allow /work-evil
	parent := t.TempDir()
	root := filepath.Join(parent, "work")
	evil := filepath.Join(parent, "work-evil")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.resolve(filepath.Join(evil, "secret.txt"))
	if !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("expected ErrOutsideWorkspace for prefix escape, got %v", err)
	}
}

func TestResolve_rejectsSymlinkOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.ReadTextFile(context.Background(), "link.txt")
	if !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("expected ErrOutsideWorkspace for symlink escape, got %v", err)
	}
}

func TestResolve_deniesEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("KEY=secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.ReadTextFile(context.Background(), ".env")
	if !errors.Is(err, ErrSensitivePath) {
		t.Fatalf("expected ErrSensitivePath, got %v", err)
	}
}

func TestReadTextFile_truncatesLarge(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("a", MaxReadBytes+10_000)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := env.ReadTextFile(context.Background(), "big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[truncated:") {
		t.Fatal("expected truncation marker")
	}
	if len(out) > MaxReadBytes+100 {
		t.Fatalf("output too large: %d", len(out))
	}
}

func TestReadTextFile_refusesBinary(t *testing.T) {
	root := t.TempDir()
	data := []byte{0x00, 0x01, 0x02, 'a', 'b'}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.ReadTextFile(context.Background(), "bin.dat")
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("expected ErrBinaryFile, got %v", err)
	}
}

func TestWriteFile_atomicAndConfined(t *testing.T) {
	root := t.TempDir()
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.WriteFile(context.Background(), "subdir/hello.txt", "hi"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "subdir/hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hi" {
		t.Fatalf("got %q", data)
	}
}

func TestShell_scrubsAPIKeys(t *testing.T) {
	root := t.TempDir()
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("MY_API_KEY", "secret")
	t.Setenv("MY_TOKEN", "tok")
	t.Setenv("SAFE_VAR", "ok")

	res, err := env.Exec(context.Background(), "env", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "sk-test") || strings.Contains(res.Stdout, "OPENAI_API_KEY") {
		t.Fatal("OPENAI_API_KEY leaked into child env")
	}
	if strings.Contains(res.Stdout, "MY_API_KEY") || strings.Contains(res.Stdout, "MY_TOKEN=") {
		t.Fatal("secret env vars leaked")
	}
	if !strings.Contains(res.Stdout, "SAFE_VAR=ok") {
		t.Fatalf("SAFE_VAR missing from env output:\n%s", res.Stdout)
	}
}

func TestConfined(t *testing.T) {
	if !confined("/work", "/work") {
		t.Fatal("root itself should be confined")
	}
	if !confined("/work", "/work/a") {
		t.Fatal("/work/a should be confined")
	}
	if confined("/work", "/work-evil") {
		t.Fatal("/work-evil must not be confined under /work")
	}
}

func TestIsSensitive(t *testing.T) {
	root := "/proj"
	cases := []struct {
		path string
		want bool
	}{
		{"/proj/.env", true},
		{"/proj/.env.local", true},
		{"/proj/key.pem", true},
		{"/proj/id_rsa", true},
		{"/proj/.aws/credentials", true},
		{"/proj/.ssh/id_ed25519", true},
		{"/proj/.git/config", true},
		{"/proj/main.go", false},
		{"/proj/README.md", false},
	}
	for _, tc := range cases {
		if got := isSensitive(tc.path, root); got != tc.want {
			t.Errorf("isSensitive(%q) = %v want %v", tc.path, got, tc.want)
		}
	}
}
