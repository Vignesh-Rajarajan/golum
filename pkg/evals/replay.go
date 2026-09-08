package evals

import (
	"context"
	"fmt"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/google/uuid"
)

// ReplayFrom re-runs a task from a point partway through a previous
// trajectory. Given the step where a run first went wrong, it rebuilds the
// state as of just before that step and lets the model try again from there.
//
// This is what makes a failure testable in isolation. Re-running a whole task
// to reach the interesting step wastes the earlier calls and, worse, may not
// reach the same state twice; replaying the recorded prefix puts the agent
// back at the exact fork every time.
//
// Two things have to be restored together, or the replay is incoherent:
//
//   - The conversation, rebuilt from the recorded entries. The cut is
//     normalized by SafeCutIndex so a tool call never loses its result.
//   - The workspace, restored from the snapshot taken after the last tool call
//     in the kept prefix. This requires the prior run to have used
//     Options.SnapshotWorkspace; without it the files the prefix created would
//     be missing and the agent would be reasoning about a workspace that does
//     not exist.
//
// steps are the prompts to continue with, and may be empty to simply let the
// agent proceed from where it left off.
func (h *Harness) ReplayFrom(
	ctx context.Context,
	t *testing.T,
	prior *Result,
	cut int,
	steps ...Step,
) (*Result, error) {
	t.Helper()
	if prior == nil {
		return nil, fmt.Errorf("evals: replay needs a prior result")
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("evals: replay needs at least one step to continue with")
	}

	trajectory := prior.Trajectory()
	keep := SafeCutIndex(trajectory, cut)
	seed := EntriesForPrefix(prior, keep)
	if len(seed) == 0 {
		return nil, fmt.Errorf("evals: cut %d leaves no prefix to replay", cut)
	}

	workspace := t.TempDir()
	if err := restorePrefixWorkspace(prior, trajectory, keep, workspace); err != nil {
		return nil, err
	}
	env, err := execenv.NewOsExecutionEnv(workspace)
	if err != nil {
		return nil, err
	}
	return h.run(ctx, t, uuid.NewString(), workspace, env, seed, steps)
}

// ReplayTaskFrom replays a task's prior run from a cut and scores the
// continuation against the same acceptance criteria, so "does it recover from
// here" is measured the same way as "does it succeed from scratch".
//
// With no steps given, the continuation restates the task's own objective on
// top of the replayed history, which is the closest thing to "carry on" that
// a chat-shaped loop accepts.
func ReplayTaskFrom(
	ctx context.Context,
	t *testing.T,
	task Task,
	prior *Result,
	cut int,
	steps ...Step,
) (*TaskRun, error) {
	t.Helper()
	if len(steps) == 0 {
		steps = task.PromptSteps()
	}
	result, runErr := HarnessFor(task).ReplayFrom(ctx, t, prior, cut, steps...)
	if result == nil {
		return nil, fmt.Errorf("evals: replay of task %q did not start: %w", task.ID, runErr)
	}
	return Evaluate(ctx, task, result, runErr), nil
}

// restorePrefixWorkspace puts the workspace back to the state it had at the
// cut, using the snapshot keyed by the last tool call in the kept prefix.
func restorePrefixWorkspace(prior *Result, trajectory []TrajectoryStep, keep int, dst string) error {
	key := LastToolCallID(trajectory, keep)
	if key == "" {
		key = initialSnapshot
	}
	if prior.SnapshotDir == "" {
		// Even at the initial snapshot this is an error rather than an empty
		// workspace: the task may have seeded files, and replaying into a
		// workspace that quietly lost them is worse than refusing.
		return fmt.Errorf("evals: prior run has no workspace snapshots, so state at %s "+
			"cannot be restored; set Options.SnapshotWorkspace "+
			"(or Environment.SnapshotWorkspace) on the run being replayed", key)
	}
	return RestoreWorkspace(prior.SnapshotDir, key, dst)
}
