package evals

import (
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
)

func sampleTask(id string, split Split, diff Difficulty) Task {
	return Task{
		ID: id, Version: "v1", Split: split, Difficulty: diff,
		Objective:  "do the thing",
		Acceptance: AcceptanceCriteria{Outcome: []OutcomeVerifier{FileEquals("a.txt", "x")}},
	}
}

func sampleDataset() Dataset {
	return Dataset{Version: "v1", Tasks: []Task{
		sampleTask("dev-easy", SplitDev, DifficultyEasy),
		sampleTask("dev-hard", SplitDev, DifficultyHard),
		sampleTask("held-easy", SplitHoldout, DifficultyEasy),
	}}
}

func TestDatasetFilterDefaultsToDev(t *testing.T) {
	d := sampleDataset()
	got := d.Filter("")
	if len(got) != 2 {
		t.Fatalf("got %d tasks, want the 2 dev tasks", len(got))
	}
	for _, task := range got {
		if task.SplitOrDefault() != SplitDev {
			t.Fatalf("task %q leaked into the dev split", task.ID)
		}
	}
}

func TestDatasetFilterByDifficulty(t *testing.T) {
	got := sampleDataset().Filter(SplitDev, DifficultyHard)
	if len(got) != 1 || got[0].ID != "dev-hard" {
		t.Fatalf("got %+v, want just dev-hard", got)
	}
}

// A task with no explicit split counts as dev, so forgetting the field cannot
// accidentally hide a task from every run.
func TestUnsetSplitIsDev(t *testing.T) {
	d := Dataset{Version: "v1", Tasks: []Task{sampleTask("unset", "", DifficultyEasy)}}
	if got := d.Filter(SplitDev); len(got) != 1 {
		t.Fatalf("got %d tasks, want the unset-split task to count as dev", len(got))
	}
}

func TestSelectedSplit(t *testing.T) {
	t.Run("defaults_to_dev", func(t *testing.T) {
		t.Setenv(SplitEnv, "")
		got, err := SelectedSplit()
		if err != nil {
			t.Fatal(err)
		}
		if got != SplitDev {
			t.Fatalf("got %q want %q", got, SplitDev)
		}
	})
	t.Run("holdout_is_opt_in", func(t *testing.T) {
		t.Setenv(SplitEnv, "holdout")
		got, err := SelectedSplit()
		if err != nil {
			t.Fatal(err)
		}
		if got != SplitHoldout {
			t.Fatalf("got %q want %q", got, SplitHoldout)
		}
	})
	t.Run("typo_is_an_error_not_a_silent_dev_run", func(t *testing.T) {
		t.Setenv(SplitEnv, "hold-out")
		if _, err := SelectedSplit(); err == nil {
			t.Fatal("expected an error for an unknown split")
		}
	})
}

func TestDatasetValidate(t *testing.T) {
	if err := sampleDataset().Validate(); err != nil {
		t.Fatalf("valid dataset rejected: %v", err)
	}
	t.Run("rejects_duplicate_ids", func(t *testing.T) {
		d := Dataset{Version: "v1", Tasks: []Task{
			sampleTask("same", SplitDev, DifficultyEasy),
			sampleTask("same", SplitHoldout, DifficultyEasy),
		}}
		if err := d.Validate(); err == nil {
			t.Fatal("expected duplicate IDs to be rejected")
		}
	})
	t.Run("rejects_missing_version", func(t *testing.T) {
		if err := (Dataset{Tasks: nil}).Validate(); err == nil {
			t.Fatal("expected a dataset without a version to be rejected")
		}
	})
	t.Run("rejects_task_without_criteria", func(t *testing.T) {
		d := Dataset{Version: "v1", Tasks: []Task{{ID: "x", Objective: "go"}}}
		if err := d.Validate(); err == nil {
			t.Fatal("a task that can never fail should be rejected")
		}
	})
}

func TestTaskValidate(t *testing.T) {
	base := sampleTask("t", SplitDev, DifficultyEasy)
	t.Run("needs_an_id", func(t *testing.T) {
		task := base
		task.ID = "  "
		if err := task.Validate(); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("needs_something_to_do", func(t *testing.T) {
		task := base
		task.Objective = ""
		if err := task.Validate(); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("rejects_unknown_difficulty", func(t *testing.T) {
		task := base
		task.Difficulty = "trivial"
		if err := task.Validate(); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("steps_substitute_for_objective", func(t *testing.T) {
		task := base
		task.Objective = ""
		task.Steps = []Step{Prompt("go")}
		if err := task.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestTaskHashIsStableAndSensitive(t *testing.T) {
	task := sampleTask("t", SplitDev, DifficultyEasy)
	task.InitialState.Files = map[string]string{"a.txt": "seed", "b.txt": "other"}
	task.InitialState.Skills = []skill.Skill{{Name: "s", Body: "body"}}

	first := TaskHash(task)
	if first == "" {
		t.Fatal("expected a hash")
	}
	for i := 0; i < 10; i++ {
		if got := TaskHash(task); got != first {
			t.Fatalf("hash drifted on iteration %d: %q vs %q", i, got, first)
		}
	}

	// The environment is the axis a comparison varies; changing it must not
	// look like a dataset edit.
	sameTask := task.With(Environment{Name: "candidate", ActiveTools: []string{"glob"}})
	if got := TaskHash(sameTask); got != first {
		t.Fatalf("changing the environment changed the task hash: %q vs %q", got, first)
	}

	changes := map[string]func(Task) Task{
		"objective": func(x Task) Task { x.Objective = "do something else"; return x },
		"seed_file": func(x Task) Task {
			x.InitialState.Files = map[string]string{"a.txt": "different", "b.txt": "other"}
			return x
		},
		"verifier": func(x Task) Task {
			x.Acceptance.Outcome = []OutcomeVerifier{FileEquals("a.txt", "y")}
			return x
		},
		"difficulty": func(x Task) Task { x.Difficulty = DifficultyHard; return x },
		"skill":      func(x Task) Task { x.InitialState.Skills = []skill.Skill{{Name: "s", Body: "new"}}; return x },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			if got := TaskHash(mutate(task)); got == first {
				t.Fatalf("changing the %s did not change the hash", name)
			}
		})
	}
}

func TestDatasetTaskHashUnknownID(t *testing.T) {
	if got := sampleDataset().TaskHash("nope"); got != "" {
		t.Fatalf("got %q want empty for an unknown task", got)
	}
}
