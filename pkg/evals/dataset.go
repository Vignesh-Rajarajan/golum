package evals

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
)

// SplitEnv names the environment variable selecting which split to run.
const SplitEnv = "GOLUM_EVAL_SPLIT"

// Dataset is a versioned collection of tasks. The version travels into every
// report so a change in pass rate can be read against the dataset that
// produced it: a task edited between two runs is a different measurement, not
// a regression.
type Dataset struct {
	Version string
	Tasks   []Task
}

// Validate checks every task and rejects duplicate IDs, which would silently
// make one task shadow another in a report keyed by ID.
func (d Dataset) Validate() error {
	if d.Version == "" {
		return fmt.Errorf("evals: dataset has no version")
	}
	seen := map[string]bool{}
	for _, task := range d.Tasks {
		if err := task.Validate(); err != nil {
			return err
		}
		if seen[task.ID] {
			return fmt.Errorf("evals: duplicate task ID %q in dataset %s", task.ID, d.Version)
		}
		seen[task.ID] = true
	}
	return nil
}

// Filter selects tasks in a split, optionally narrowing to some difficulties.
func (d Dataset) Filter(split Split, difficulties ...Difficulty) []Task {
	if split == "" {
		split = SplitDev
	}
	want := map[Difficulty]bool{}
	for _, diff := range difficulties {
		want[diff] = true
	}
	var out []Task
	for _, task := range d.Tasks {
		if task.SplitOrDefault() != split {
			continue
		}
		if len(want) > 0 && !want[task.Difficulty] {
			continue
		}
		out = append(out, task)
	}
	return out
}

// Get returns a task by ID.
func (d Dataset) Get(id string) (Task, bool) {
	for _, task := range d.Tasks {
		if task.ID == id {
			return task, true
		}
	}
	return Task{}, false
}

// TaskHash fingerprints a task's inputs and criteria. Reports carry it so an
// edited task is visible as a dataset change instead of masquerading as a
// behavioral regression.
func (d Dataset) TaskHash(id string) string {
	task, ok := d.Get(id)
	if !ok {
		return ""
	}
	return TaskHash(task)
}

// TaskHash fingerprints a single task. Verifiers contribute their names rather
// than their behaviour, since a Go closure cannot be hashed: renaming a
// verifier is treated as a change, rewriting its body silently is not.
func TaskHash(task Task) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			_, _ = h.Write([]byte(p))
			_, _ = h.Write([]byte{0})
		}
	}
	write("id", task.ID, "version", task.Version, "objective", task.Objective)
	write("split", string(task.SplitOrDefault()), "difficulty", string(task.Difficulty))
	for _, s := range task.Steps {
		write("step", s.Kind, s.Content)
	}
	write("seed", task.InitialState.Seed)
	for _, path := range sortedKeys(task.InitialState.Files) {
		write("file", path, task.InitialState.Files[path])
	}
	for _, cmd := range task.InitialState.Commands {
		write("cmd", cmd)
	}
	skills := make([]string, 0, len(task.InitialState.Skills))
	for _, sk := range task.InitialState.Skills {
		skills = append(skills, sk.Name+"\x00"+sk.Body)
	}
	sort.Strings(skills)
	for _, sk := range skills {
		write("skill", sk)
	}
	for _, v := range task.Acceptance.Outcome {
		write("outcome", v.Name())
	}
	for _, v := range task.Acceptance.Process {
		write("process", v.Name())
	}
	for _, v := range task.Acceptance.Safety {
		write("safety", v.Name())
	}
	for _, v := range task.Acceptance.Reliability {
		write("reliability", v.Name())
	}
	for _, v := range task.Acceptance.Performance {
		write("performance", v.Name())
	}
	write("subjective", fmt.Sprint(len(task.Acceptance.Subjective)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// SelectedSplit reads the split to run from the environment, defaulting to
// dev.
//
// Holdout tasks are opt-in for a reason: iterating against them contaminates
// them. Once a task has driven a fix, its pass rate stops measuring
// generalization and starts measuring how hard it was fit, and there is no way
// to un-see it. Defaulting to dev makes that a deliberate act.
func SelectedSplit() (Split, error) {
	switch raw := strings.ToLower(strings.TrimSpace(os.Getenv(SplitEnv))); raw {
	case "":
		return SplitDev, nil
	case string(SplitDev):
		return SplitDev, nil
	case string(SplitHoldout):
		return SplitHoldout, nil
	default:
		return "", fmt.Errorf("evals: %s=%q is not a known split (want %q or %q)",
			SplitEnv, raw, SplitDev, SplitHoldout)
	}
}
