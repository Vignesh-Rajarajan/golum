package harness

import (
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

func call(name string, args map[string]any) *llm.ToolCall {
	return &llm.ToolCall{Name: name, Arguments: args}
}

func TestEpisodicTracker_RecordsFileChanges(t *testing.T) {
	e := NewEpisodicTracker()
	e.Observe(call("write_file", map[string]any{"path": "a.go"}), false, "wrote")
	e.Observe(call("edit", map[string]any{"path": "b.go"}), false, "edited")
	e.Observe(call("read_file", map[string]any{"path": "c.go"}), false, "read")

	files := e.ChangedFiles()
	if len(files) != 2 || files[0] != "a.go" || files[1] != "b.go" {
		t.Fatalf("expected only mutating tools tracked, got %v", files)
	}
}

func TestEpisodicTracker_DeduplicatesPaths(t *testing.T) {
	e := NewEpisodicTracker()
	e.Observe(call("edit", map[string]any{"path": "a.go"}), false, "")
	e.Observe(call("edit", map[string]any{"path": "a.go"}), false, "")
	if files := e.ChangedFiles(); len(files) != 1 {
		t.Fatalf("expected 1 distinct file, got %v", files)
	}
}

func TestEpisodicTracker_FailedWriteNotCountedAsChange(t *testing.T) {
	e := NewEpisodicTracker()
	e.Observe(call("write_file", map[string]any{"path": "denied.go"}), true, "permission denied")
	if files := e.ChangedFiles(); len(files) != 0 {
		t.Fatalf("a failed write did not change anything, got %v", files)
	}
	if !strings.Contains(e.Digest(), "Failures: 1") {
		t.Fatalf("failure should still appear in the digest:\n%s", e.Digest())
	}
}

func TestEpisodicTracker_RetainsLastFailureOutput(t *testing.T) {
	e := NewEpisodicTracker()
	e.Observe(call("shell", map[string]any{"command": "go test ./..."}), true,
		"--- FAIL: TestThing\n    thing_test.go:12: boom")

	last := e.LastFailure()
	if !strings.Contains(last, "TestThing") {
		t.Fatalf("traceback not retained: %q", last)
	}
	digest := e.Digest()
	if !strings.Contains(digest, "go test") || !strings.Contains(digest, "FAILED") {
		t.Fatalf("digest missing command status:\n%s", digest)
	}
}

func TestEpisodicTracker_EmptyDigestWhenNothingHappened(t *testing.T) {
	e := NewEpisodicTracker()
	e.Observe(call("read_file", map[string]any{"path": "a.go"}), false, "contents")
	if d := e.Digest(); d != "" {
		t.Fatalf("read-only turns should not produce a digest, got %q", d)
	}
}

func TestEpisodicTracker_Reset(t *testing.T) {
	e := NewEpisodicTracker()
	e.Observe(call("write_file", map[string]any{"path": "a.go"}), false, "")
	e.Reset()
	if len(e.Ops()) != 0 || e.Digest() != "" || e.LastFailure() != "" {
		t.Fatal("Reset should clear the tracker")
	}
}

func TestEpisodicTracker_NilSafe(t *testing.T) {
	var e *EpisodicTracker
	e.Observe(call("write_file", map[string]any{"path": "a"}), false, "")
	e.Reset()
	if e.Digest() != "" || e.LastFailure() != "" || len(e.Ops()) != 0 {
		t.Fatal("nil tracker must be inert, not panic")
	}
}
