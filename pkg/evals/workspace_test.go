package evals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func TestSnapshotAndRestoreRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	snapshots := t.TempDir()
	files := map[string]string{
		"note.txt":            "one",
		"config/app.json":     `{"a":1}`,
		"deep/nested/file.md": "# hi",
	}
	writeTree(t, workspace, files)

	if err := SnapshotWorkspaceAt(workspace, snapshots, "call_1"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Mutate afterwards; the snapshot must not follow.
	writeTree(t, workspace, map[string]string{"note.txt": "two", "extra.txt": "new"})

	restored := t.TempDir()
	if err := RestoreWorkspace(snapshots, "call_1", restored); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := readTree(t, restored)
	if len(got) != len(files) {
		t.Fatalf("restored %d files, want %d: %v", len(got), len(files), got)
	}
	for rel, want := range files {
		if got[rel] != want {
			t.Fatalf("%s = %q want %q", rel, got[rel], want)
		}
	}
}

// A retried tool call must not overwrite the state its first attempt captured,
// or replaying to that point would restore the wrong workspace.
func TestSnapshotIsWriteOnce(t *testing.T) {
	workspace := t.TempDir()
	snapshots := t.TempDir()
	writeTree(t, workspace, map[string]string{"a.txt": "first"})
	if err := SnapshotWorkspaceAt(workspace, snapshots, "call_1"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	writeTree(t, workspace, map[string]string{"a.txt": "second"})
	if err := SnapshotWorkspaceAt(workspace, snapshots, "call_1"); err != nil {
		t.Fatalf("re-snapshot: %v", err)
	}

	restored := t.TempDir()
	if err := RestoreWorkspace(snapshots, "call_1", restored); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readTree(t, restored)["a.txt"]; got != "first" {
		t.Fatalf("a.txt = %q, want the originally captured %q", got, "first")
	}
}

func TestSnapshotWithoutDirIsANoOp(t *testing.T) {
	if err := SnapshotWorkspaceAt(t.TempDir(), "", "call_1"); err != nil {
		t.Fatalf("snapshotting with no directory configured should do nothing: %v", err)
	}
}

func TestRestoreUnknownKey(t *testing.T) {
	if err := RestoreWorkspace(t.TempDir(), "missing", t.TempDir()); err == nil {
		t.Fatal("expected an error for a snapshot that was never taken")
	}
}

// Tool call ids are opaque and may contain characters that are not safe as a
// path element; distinct ids must still map to distinct directories.
func TestSnapshotKeyIsSafeAndDistinct(t *testing.T) {
	weird := snapshotKey("call/../../etc/passwd")
	if strings.ContainsAny(weird, "/\\") {
		t.Fatalf("snapshot key %q must be a single path element", weird)
	}
	if snapshotKey("a/b") == snapshotKey("a_b") {
		t.Fatal("sanitizing must not collapse distinct ids onto one key")
	}
	if snapshotKey("call_1") != snapshotKey("call_1") {
		t.Fatal("the same id must always produce the same key")
	}
}

// Replaying without snapshots would silently reason about a workspace that
// does not exist, so it has to fail loudly and say what to turn on.
func TestRestorePrefixWorkspaceRequiresSnapshots(t *testing.T) {
	r := synthetic()
	steps := r.Trajectory()

	err := restorePrefixWorkspace(r, steps, len(steps), t.TempDir())
	if err == nil {
		t.Fatal("expected an error when the prior run took no snapshots")
	}
	if !strings.Contains(err.Error(), "SnapshotWorkspace") {
		t.Fatalf("error should name the option to enable, got %q", err)
	}
}

func TestRestorePrefixWorkspaceUsesLastToolCall(t *testing.T) {
	r := synthetic()
	steps := r.Trajectory()
	snapshots := t.TempDir()
	r.SnapshotDir = snapshots

	// Snapshot a distinct workspace state per tool call.
	for _, key := range []string{initialSnapshot, "call_a", "call_b"} {
		staging := t.TempDir()
		writeTree(t, staging, map[string]string{"marker.txt": key})
		if err := SnapshotWorkspaceAt(staging, snapshots, key); err != nil {
			t.Fatalf("snapshot %s: %v", key, err)
		}
	}

	cases := map[int]string{
		1:          initialSnapshot, // before any tool ran
		5:          "call_a",        // after the first result
		len(steps): "call_b",        // after the last result
	}
	for keep, want := range cases {
		dst := t.TempDir()
		if err := restorePrefixWorkspace(r, steps, keep, dst); err != nil {
			t.Fatalf("keep=%d: %v", keep, err)
		}
		if got := readTree(t, dst)["marker.txt"]; got != want {
			t.Fatalf("keep=%d restored %q want %q", keep, got, want)
		}
	}
}
