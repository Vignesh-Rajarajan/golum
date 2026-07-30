package harness

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
)

// FileOp records one workspace mutation made during a turn.
type FileOp struct {
	Tool    string // write_file | edit | shell
	Path    string // "" for shell
	Command string // shell only
	Failed  bool
}

// EpisodicTracker accumulates what actually happened during a turn: which files
// changed and which commands failed.
//
// This is the "change tracking" and "traceback audit" half of episodic memory —
// the loop already sees every tool call, so the record costs nothing extra and
// survives compaction, which otherwise summarizes those details away.
type EpisodicTracker struct {
	mu      sync.Mutex
	ops     []FileOp
	lastErr string
}

// NewEpisodicTracker returns an empty tracker.
func NewEpisodicTracker() *EpisodicTracker { return &EpisodicTracker{} }

// Observe records a completed tool call.
func (e *EpisodicTracker) Observe(tc *llm.ToolCall, failed bool, output string) {
	if e == nil || tc == nil {
		return
	}
	op, ok := fileOpFor(tc)
	if !ok {
		return
	}
	op.Failed = failed

	e.mu.Lock()
	defer e.mu.Unlock()
	e.ops = append(e.ops, op)
	if failed {
		// Keep the most recent failure verbatim so the next turn can tell
		// whether a change fixed or worsened the error.
		e.lastErr = strings.TrimSpace(output)
		if len(e.lastErr) > 2000 {
			e.lastErr = e.lastErr[:2000] + "\n[truncated]"
		}
	}
}

// Ops returns a copy of the recorded operations.
func (e *EpisodicTracker) Ops() []FileOp {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]FileOp, len(e.ops))
	copy(out, e.ops)
	return out
}

// LastFailure returns the most recent failing tool output.
func (e *EpisodicTracker) LastFailure() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

// Reset clears the tracker for a new turn.
func (e *EpisodicTracker) Reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ops = nil
	e.lastErr = ""
}

// ChangedFiles returns the distinct paths written during the turn.
func (e *EpisodicTracker) ChangedFiles() []string {
	seen := map[string]bool{}
	for _, op := range e.Ops() {
		if op.Failed || op.Path == "" {
			continue
		}
		seen[op.Path] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Digest renders a short human- and model-readable summary of the turn.
// Returns "" when nothing worth recording happened.
func (e *EpisodicTracker) Digest() string {
	ops := e.Ops()
	if len(ops) == 0 {
		return ""
	}
	var b strings.Builder
	if files := e.ChangedFiles(); len(files) > 0 {
		fmt.Fprintf(&b, "Files changed: %s\n", strings.Join(files, ", "))
	}
	var commands []string
	failures := 0
	for _, op := range ops {
		if op.Tool == "shell" && op.Command != "" {
			status := "ok"
			if op.Failed {
				status = "FAILED"
			}
			commands = append(commands, fmt.Sprintf("%s (%s)", truncate(op.Command, 80), status))
		}
		if op.Failed {
			failures++
		}
	}
	if len(commands) > 0 {
		fmt.Fprintf(&b, "Commands: %s\n", strings.Join(commands, "; "))
	}
	if failures > 0 {
		fmt.Fprintf(&b, "Failures: %d\n", failures)
	}
	if last := e.LastFailure(); last != "" {
		fmt.Fprintf(&b, "Last failure output:\n%s\n", truncate(last, 600))
	}
	return strings.TrimSpace(b.String())
}

// recordEpisode writes the turn's episodic digest to durable memory and clears
// the tracker. Failures are non-fatal — losing a digest must never fail a turn.
func recordEpisode(ctx context.Context, deps LoopDeps) {
	if deps.Episodic == nil {
		return
	}
	defer deps.Episodic.Reset()

	digest := deps.Episodic.Digest()
	if digest == "" || deps.Memory == nil {
		return
	}
	sessionID := ""
	if deps.Session != nil {
		sessionID = deps.Session.ID()
	}
	if _, err := deps.Memory.Put(ctx, memory.Record{
		Tier:      memory.TierEpisodic,
		Scope:     memory.ScopeProject,
		Content:   digest,
		SessionID: sessionID,
	}); err != nil {
		applog.Printf("episodic: record digest: %v", err)
	}
}

// fileOpFor maps a tool call to a workspace mutation, if it is one.
func fileOpFor(tc *llm.ToolCall) (FileOp, bool) {
	switch tc.Name {
	case "write_file", "edit":
		path := ""
		if tc.Arguments != nil {
			path, _ = tc.Arguments["path"].(string)
		}
		return FileOp{Tool: tc.Name, Path: path}, true
	case "shell":
		cmd := ""
		if tc.Arguments != nil {
			cmd, _ = tc.Arguments["command"].(string)
		}
		return FileOp{Tool: "shell", Command: cmd}, true
	default:
		return FileOp{}, false
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
