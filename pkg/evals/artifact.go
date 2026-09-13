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

	// Task-run fields, empty for a bare Result artifact.
	TaskID     string       `json:"task_id,omitempty"`
	TaskHash   string       `json:"task_hash,omitempty"`
	Version    string       `json:"dataset_version,omitempty"`
	Split      Split        `json:"split,omitempty"`
	Difficulty Difficulty   `json:"difficulty,omitempty"`
	Outcome    []Check      `json:"outcome,omitempty"`
	Process    []Check      `json:"process,omitempty"`
	Subjective []Check      `json:"subjective,omitempty"`
	Passed     *bool        `json:"passed,omitempty"`
	Metrics    *Metrics     `json:"metrics,omitempty"`
	Attributed *Attribution `json:"attribution,omitempty"`
	RunError   string       `json:"run_error,omitempty"`
}

// WriteRunArtifact writes the session transcript and appends one line to runs.jsonl.
func WriteRunArtifact(dir, runID string, result *Result, scores []Score) error {
	line, err := newRunIndexLine(dir, runID, result, scores)
	if err != nil {
		return err
	}
	return appendRunIndex(dir, line)
}

// WriteTaskRunArtifact records a scored task run: the transcript plus the
// three verifier axes, the metrics, and the failure attribution. It is the
// same index file as WriteRunArtifact so a mixed suite still produces one
// readable audit trail.
func WriteTaskRunArtifact(dir string, run *TaskRun) error {
	if run == nil {
		return fmt.Errorf("task run is nil")
	}
	line, err := newRunIndexLine(dir, "", run.Result, nil)
	if err != nil {
		return err
	}
	passed := run.Passed()
	metrics := run.Metrics
	line.TaskID = run.Task.ID
	line.TaskHash = TaskHash(run.Task)
	line.Version = run.Task.Version
	line.Split = run.Task.SplitOrDefault()
	line.Difficulty = run.Task.Difficulty
	line.Outcome = run.Outcome
	line.Process = run.Process
	line.Subjective = run.Subjective
	line.Passed = &passed
	line.Metrics = &metrics
	line.Attributed = run.Attribution
	if run.Err != nil {
		line.RunError = run.Err.Error()
	}
	return appendRunIndex(dir, line)
}

func newRunIndexLine(dir, runID string, result *Result, scores []Score) (runIndexLine, error) {
	if dir == "" {
		return runIndexLine{}, fmt.Errorf("artifact dir empty")
	}
	if result == nil {
		return runIndexLine{}, fmt.Errorf("result is nil")
	}
	if runID == "" {
		runID = result.RunID
	}
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return runIndexLine{}, err
	}
	sessionPath := filepath.Join(sessionsDir, runID+".json")
	raw, err := json.MarshalIndent(result.Entries, "", "  ")
	if err != nil {
		return runIndexLine{}, err
	}
	if err := os.WriteFile(sessionPath, raw, 0o600); err != nil {
		return runIndexLine{}, err
	}
	return runIndexLine{
		RunID:     runID,
		Harness:   result.Harness,
		Input:     result.Input,
		Output:    result.Output,
		Usage:     result.Usage,
		Scores:    scores,
		ElapsedMs: result.Elapsed.Milliseconds(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Session:   filepath.Join("sessions", runID+".json"),
	}, nil
}

func appendRunIndex(dir string, line runIndexLine) error {
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
