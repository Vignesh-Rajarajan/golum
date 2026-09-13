package evals

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/mcp"
	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
)

// Split separates tasks used while developing the agent from tasks held back
// to measure it. Iterating against holdout tasks contaminates them: they stop
// reporting generalization and start reporting how hard they were fit.
type Split string

const (
	SplitDev     Split = "dev"
	SplitHoldout Split = "holdout"
)

// Difficulty labels a task so a pass rate can be read against the difficulty
// mix that produced it.
type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

// InitialState is the workspace the agent wakes up in.
type InitialState struct {
	// Files maps workspace-relative paths to contents. Applied after Seed, so
	// a task can seed a directory and then override one file in it.
	Files map[string]string
	// Seed names a directory inside SeedFS whose tree is copied into the
	// workspace root.
	Seed   string
	SeedFS fs.FS
	// Skills are written under .golum/skills/ before the harness is built.
	Skills []skill.Skill
	// Commands run in the workspace after files are in place, e.g. "git init".
	Commands []string
}

// Apply materializes the initial state into env's workspace.
func (s InitialState) Apply(ctx context.Context, env execenv.ExecutionEnv) error {
	if s.Seed != "" {
		if s.SeedFS == nil {
			return fmt.Errorf("evals: initial state names seed %q but has no SeedFS", s.Seed)
		}
		if err := copySeed(ctx, env, s.SeedFS, s.Seed); err != nil {
			return err
		}
	}
	for _, rel := range sortedKeys(s.Files) {
		if err := env.WriteFile(ctx, rel, s.Files[rel]); err != nil {
			return fmt.Errorf("evals: seed file %s: %w", rel, err)
		}
	}
	if err := writeSkills(ctx, env, s.Skills); err != nil {
		return err
	}
	for _, cmd := range s.Commands {
		res, err := env.Exec(ctx, cmd, execenv.ExecOptions{})
		if err != nil {
			return fmt.Errorf("evals: setup command %q: %w", cmd, err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("evals: setup command %q exited %d: %s",
				cmd, res.ExitCode, strings.TrimSpace(res.Stderr))
		}
	}
	return nil
}

func copySeed(ctx context.Context, env execenv.ExecutionEnv, seedFS fs.FS, root string) error {
	return fs.WalkDir(seedFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("evals: walk seed %s: %w", root, err)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepathRel(root, p)
		if err != nil {
			return err
		}
		raw, err := fs.ReadFile(seedFS, p)
		if err != nil {
			return fmt.Errorf("evals: read seed %s: %w", p, err)
		}
		if err := env.WriteFile(ctx, rel, string(raw)); err != nil {
			return fmt.Errorf("evals: write seed %s: %w", rel, err)
		}
		return nil
	})
}

// filepathRel works on slash-separated embed.FS paths, which never use the
// host separator even on Windows.
func filepathRel(root, p string) (string, error) {
	root = path.Clean(root)
	p = path.Clean(p)
	if root == "." {
		return p, nil
	}
	if !strings.HasPrefix(p, root+"/") {
		return "", fmt.Errorf("evals: seed path %q escapes root %q", p, root)
	}
	return strings.TrimPrefix(p, root+"/"), nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Environment is the harness configuration a task runs under. It is the axis a
// comparative eval varies while holding the task fixed.
type Environment struct {
	Name                  string
	ActiveTools           []string // nil = all defaults; empty non-nil = none
	Loop                  harness.LoopConfig
	Model                 string
	TransformSystemPrompt func(defaultPrompt string) string
	Approvals             ApprovalPolicy
	SnapshotWorkspace     bool
	ForceTool             string
	MCPBackends           []mcp.Backend
	// Script, when set, drives the run against a local OpenAI-compatible
	// server so the task can run on every PR without a real model.
	Script []harnesstest.Turn
}

// AcceptanceCriteria states what "done" means along five independent
// deterministic axes plus an informational quality axis.
//
// A fluent answer must never compensate for a policy violation. Subjective
// judges never override any of the five gates.
type AcceptanceCriteria struct {
	// Outcome checks the world the run left behind. Authoritative.
	Outcome []OutcomeVerifier
	// Process checks how the run got there. Authoritative.
	Process []ProcessVerifier
	// Safety is a hard gate for approvals, sandbox boundaries, and secrets.
	Safety []SafetyVerifier
	// Reliability checks restart, replay, cancellation, and concurrency.
	Reliability []ReliabilityVerifier
	// Performance checks latency, token, cost, output, and call budgets.
	Performance []PerformanceVerifier
	// Subjective grades qualities no deterministic check can express. Never
	// allowed to override the five gates.
	Subjective []Judge
}

// Task is a fully specified evaluation: where the agent starts, what it is
// asked to do, what it may use, and what counts as success.
type Task struct {
	ID          string
	Version     string
	Split       Split
	Difficulty  Difficulty
	Description string

	// Objective is the user's request. Used as the single prompt step unless
	// Steps is set.
	Objective string
	Steps     []Step

	Environment  Environment
	InitialState InitialState
	Acceptance   AcceptanceCriteria
	Tags         []string
}

// Validate reports structural problems that would otherwise surface as a
// confusing runtime failure or, worse, a task that always passes.
func (t Task) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("evals: task has no ID")
	}
	if t.Objective == "" && len(t.Steps) == 0 {
		return fmt.Errorf("evals: task %q has neither Objective nor Steps", t.ID)
	}
	switch t.Split {
	case "", SplitDev, SplitHoldout:
	default:
		return fmt.Errorf("evals: task %q has unknown split %q", t.ID, t.Split)
	}
	switch t.Difficulty {
	case "", DifficultyEasy, DifficultyMedium, DifficultyHard:
	default:
		return fmt.Errorf("evals: task %q has unknown difficulty %q", t.ID, t.Difficulty)
	}
	if len(t.Acceptance.Outcome) == 0 &&
		len(t.Acceptance.Process) == 0 &&
		len(t.Acceptance.Safety) == 0 &&
		len(t.Acceptance.Reliability) == 0 &&
		len(t.Acceptance.Performance) == 0 &&
		len(t.Acceptance.Subjective) == 0 {
		return fmt.Errorf("evals: task %q has no acceptance criteria", t.ID)
	}
	return nil
}

// SplitOrDefault reports the task's split, treating unset as dev.
func (t Task) SplitOrDefault() Split {
	if t.Split == "" {
		return SplitDev
	}
	return t.Split
}

// PromptSteps returns the steps to run: explicit Steps when set, otherwise a
// single prompt carrying the objective.
func (t Task) PromptSteps() []Step {
	if len(t.Steps) > 0 {
		return t.Steps
	}
	return []Step{Prompt(t.Objective)}
}

// Options converts the task's environment and initial state into harness
// options. Skills live in InitialState because they are part of the workspace
// the agent wakes up in, not a knob the comparison varies.
func (t Task) Options() Options {
	name := t.Environment.Name
	if name == "" {
		name = t.ID
	}
	opts := Options{
		Name:                  name,
		Model:                 t.Environment.Model,
		ActiveTools:           t.Environment.ActiveTools,
		Skills:                t.InitialState.Skills,
		TransformSystemPrompt: t.Environment.TransformSystemPrompt,
		Loop:                  t.Environment.Loop,
		Approvals:             t.Environment.Approvals,
		SnapshotWorkspace:     t.Environment.SnapshotWorkspace,
		MCPBackends:           t.Environment.MCPBackends,
		Script:                t.Environment.Script,
	}
	if t.Environment.ForceTool != "" {
		opts.Loop.ForceTool = t.Environment.ForceTool
	}
	return opts
}

// With returns a copy of the task running under env, for baseline-versus-
// candidate comparisons over the same task.
func (t Task) With(env Environment) Task {
	t.Environment = env
	return t
}
