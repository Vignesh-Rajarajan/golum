package evals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

// initialSnapshot is the snapshot key for the workspace as it looked before
// the first tool call, i.e. the seeded initial state.
const initialSnapshot = "initial"

// prepareSnapshotDir creates the per-run snapshot root under the artifact dir.
func prepareSnapshotDir(runID string) (string, error) {
	base, err := ArtifactDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "snapshots", snapshotKey(runID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("evals: create snapshot dir %s: %w", dir, err)
	}
	return dir, nil
}

// snapshotKey makes an arbitrary identifier safe as a single path element
// while keeping it collision-free and recognizable.
func snapshotKey(id string) string {
	safe := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			safe = append(safe, r)
		default:
			safe = append(safe, '_')
		}
	}
	sum := sha256.Sum256([]byte(id))
	return string(safe) + "-" + hex.EncodeToString(sum[:4])
}

// SnapshotWorkspaceAt copies the workspace tree into snapshotDir under key.
// Re-snapshotting the same key is a no-op, which keeps a retried tool call
// from overwriting the state its first attempt produced.
func SnapshotWorkspaceAt(workspace, snapshotDir, key string) error {
	if snapshotDir == "" {
		return nil
	}
	dst := filepath.Join(snapshotDir, snapshotKey(key))
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	return copyTree(workspace, dst)
}

// RestoreWorkspace copies the snapshot stored under key into dst, which must
// already exist and should be empty.
func RestoreWorkspace(snapshotDir, key, dst string) error {
	src := filepath.Join(snapshotDir, snapshotKey(key))
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("evals: no workspace snapshot for %q: %w", key, err)
	}
	return copyTree(src, dst)
}

// copyTree copies regular files and directories from src to dst. Symlinks are
// skipped rather than followed: an eval workspace is a sandbox boundary, and
// resolving a link during a snapshot would copy content from outside it.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type()&fs.ModeSymlink != 0:
			return nil
		case !d.Type().IsRegular():
			return nil
		default:
			return copyFile(path, target)
		}
	})
}

// workspaceManifest lists regular files under the confined workspace, sorted.
func workspaceManifest(ctx context.Context, env execenv.ExecutionEnv) ([]string, error) {
	if env == nil {
		return nil, nil
	}
	var files []string
	err := env.Walk(ctx, ".", func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(files)
	return files, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
