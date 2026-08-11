package evals

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

var artifactOnce sync.Once
var artifactDir string
var artifactDirErr error

// ArtifactDir returns GOLUM_EVAL_ARTIFACT_DIR when set, otherwise creates
// ./.eval/<RFC3339>_<uuid> with mode 0700 relative to the current working
// directory and caches the path for the process lifetime (mirrors pi's ignored
// .eval/ artifact directory). `go test` always runs with its working directory
// set to the package's source directory, so this resolves to pkg/evals/.eval
// without needing debug info (which -trimpath strips) to find it.
//
// Unlike an earlier version, failures are not swallowed: callers must check the
// returned error, since a broken artifact dir means every eval run silently
// loses its audit trail otherwise.
func ArtifactDir() (string, error) {
	artifactOnce.Do(func() {
		if dir := os.Getenv("GOLUM_EVAL_ARTIFACT_DIR"); dir != "" {
			artifactDir = dir
			artifactDirErr = os.MkdirAll(dir, 0o700)
			return
		}
		cwd, err := os.Getwd()
		if err != nil {
			artifactDirErr = fmt.Errorf("evals: resolve working directory: %w", err)
			return
		}
		ts := time.Now().UTC().Format("20060102T150405Z")
		dir := filepath.Join(cwd, ".eval", fmt.Sprintf("%s_%s", ts, uuid.NewString()))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			artifactDirErr = fmt.Errorf("evals: create artifact dir %s: %w", dir, err)
			return
		}
		artifactDir = dir
	})
	return artifactDir, artifactDirErr
}

type runIndexLine struct {
	RunID     string  `json:"run_id"`
	Harness   string  `json:"harness"`
	Input     string  `json:"input"`
	Output    string  `json:"output"`
	Usage     Usage   `json:"usage"`
	Scores    []Score `json:"scores"`
	ElapsedMs int64   `json:"elapsed_ms"`
	Timestamp string  `json:"timestamp"`
	Session   string  `json:"session"`
}

// WriteRunArtifact writes the session transcript and appends one line to runs.jsonl.
func WriteRunArtifact(dir, runID string, result *Result, scores []Score) error {
	if dir == "" {
		return fmt.Errorf("artifact dir empty")
	}
	if result == nil {
		return fmt.Errorf("result is nil")
	}
	if runID == "" {
		runID = result.RunID
	}
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return err
	}
	sessionPath := filepath.Join(sessionsDir, runID+".json")
	raw, err := json.MarshalIndent(result.Entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(sessionPath, raw, 0o600); err != nil {
		return err
	}

	line := runIndexLine{
		RunID:     runID,
		Harness:   result.Harness,
		Input:     result.Input,
		Output:    result.Output,
		Usage:     result.Usage,
		Scores:    scores,
		ElapsedMs: result.Elapsed.Milliseconds(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Session:   filepath.Join("sessions", runID+".json"),
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "runs.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}
