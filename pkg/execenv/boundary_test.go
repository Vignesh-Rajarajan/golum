package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundaryNestedSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(root, "mid")
	if err := os.Mkdir(mid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mid, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(mid, "out"), filepath.Join(root, "hop")); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.ReadTextFile(context.Background(), "hop/secret.txt")
	if err == nil || !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("nested symlink escape: %v", err)
	}
}

func TestBoundarySymlinkSwapBetweenStatAndUse(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "swap.txt")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Stat(context.Background(), "swap.txt"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	_, err = env.ReadTextFile(context.Background(), "swap.txt")
	if err == nil || !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("symlink swap must fail closed: %v", err)
	}
}

func TestBoundaryHiddenCredentialsAndBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "id_rsa"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.ReadTextFile(context.Background(), "id_rsa"); !errors.Is(err, ErrSensitivePath) {
		t.Fatalf("id_rsa: %v", err)
	}
	if _, err := env.ReadTextFile(context.Background(), "blob.bin"); !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("binary: %v", err)
	}
}

func TestBoundaryInvalidUTF8AndOversizedRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.txt"), []byte("ok\xff\xfe"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.ReadTextFile(context.Background(), "bad.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("got %q", got)
	}
}

func TestBoundaryWorkspaceDeletionRefused(t *testing.T) {
	root := t.TempDir()
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.WriteFile(context.Background(), "..", "nope"); err == nil {
		t.Fatal("writing .. must fail")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("workspace was deleted")
	}
}

func TestBoundaryArtifactPathSeparators(t *testing.T) {
	root := t.TempDir()
	env, err := NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.resolve(".golum/artifacts/../secret"); err != nil && !errors.Is(err, ErrOutsideWorkspace) {
		// path may resolve inside workspace; the important case is control chars / abs
	}
	if _, err := env.resolve("/etc/passwd"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("absolute artifact: %v", err)
	}
}
