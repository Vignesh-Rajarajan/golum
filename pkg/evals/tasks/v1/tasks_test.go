package v1

import (
	"context"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

// The dataset is data, so its invariants are checkable without a model. This
// runs on every `go test ./...` and catches a malformed task long before an
// eval run burns API calls discovering it.
func TestDatasetIsValid(t *testing.T) {
	d := Dataset()
	if err := d.Validate(); err != nil {
		t.Fatalf("dataset invalid: %v", err)
	}
	if len(d.Tasks) == 0 {
		t.Fatal("dataset is empty")
	}
	for _, task := range d.Tasks {
		if task.Version != Version {
			t.Fatalf("task %q has version %q, want %q", task.ID, task.Version, Version)
		}
	}
}

// Every seed a task names must actually exist in the embedded tree; a typo
// would otherwise surface as an empty workspace and a mystifying failure.
func TestSeedsApplyCleanly(t *testing.T) {
	for _, task := range Dataset().Tasks {
		if task.InitialState.Seed == "" && len(task.InitialState.Files) == 0 {
			continue
		}
		t.Run(task.ID, func(t *testing.T) {
			env, err := execenv.NewOsExecutionEnv(t.TempDir())
			if err != nil {
				t.Fatalf("execenv: %v", err)
			}
			if err := task.InitialState.Apply(context.Background(), env); err != nil {
				t.Fatalf("apply initial state: %v", err)
			}
		})
	}
}

// The seeded config task checks that specific values survive an edit, so the
// seed has to contain them to begin with. Without this, a mismatch between
// fixture and verifier would look like an agent failure.
func TestSeededConfigMatchesItsVerifiers(t *testing.T) {
	task, ok := Dataset().Get("fs/edit-seeded-config")
	if !ok {
		t.Fatal("fs/edit-seeded-config missing from the dataset")
	}
	env, err := execenv.NewOsExecutionEnv(t.TempDir())
	if err != nil {
		t.Fatalf("execenv: %v", err)
	}
	ctx := context.Background()
	if err := task.InitialState.Apply(ctx, env); err != nil {
		t.Fatalf("apply initial state: %v", err)
	}

	// The value the task asks the agent to change must start out unchanged...
	before := evals.FileContains("config/app.json", `"timeout_seconds": 30`)
	if got := before.Verify(ctx, env, &evals.Result{}); !got.Passed {
		t.Fatalf("seed does not contain the value to edit: %s", got.Detail)
	}
	// ...and the target value must not already be present, or the task would
	// pass without the agent doing anything.
	after := evals.FileContains("config/app.json", `"timeout_seconds": 60`)
	if got := after.Verify(ctx, env, &evals.Result{}); got.Passed {
		t.Fatal("the seed already satisfies the task")
	}
	// The untouched-file checks need their file present in the seed.
	untouched := evals.FileContains("README.md", "untouched")
	if got := untouched.Verify(ctx, env, &evals.Result{}); !got.Passed {
		t.Fatalf("seed is missing the untouched-file fixture: %s", got.Detail)
	}
}

// Swapping the ritual environment must not change the task fingerprint, or
// every comparative run would look like it ran against a different dataset.
func TestRitualEnvironmentsShareATaskHash(t *testing.T) {
	task, ok := Dataset().Get(RitualTaskID)
	if !ok {
		t.Fatalf("%s missing from the dataset", RitualTaskID)
	}
	base := evals.TaskHash(task.With(RitualBaseline()))
	cand := evals.TaskHash(task.With(RitualCandidate()))
	if base != cand {
		t.Fatalf("environment change altered the task hash: %q vs %q", base, cand)
	}
	// Seeding the skill does change the initial state, and should be visible.
	if withSkill := evals.TaskHash(WithRitualSkill(task)); withSkill == base {
		t.Fatal("seeding the skill should change the task hash")
	}
}

// The bounded-read task only measures the harness limit if the seed is larger
// than that limit. A too-small blob would pass without ever truncating.
func TestBoundedReadTaskExceedsItsLimit(t *testing.T) {
	task, ok := Dataset().Get("fs/read-bounded-output")
	if !ok {
		t.Fatal("fs/read-bounded-output missing from the dataset")
	}
	limit := task.Environment.Loop.MaxToolResultBytes
	if limit != boundedReadLimit {
		t.Fatalf("task limit %d, want %d", limit, boundedReadLimit)
	}
	blob := task.InitialState.Files["blob.txt"]
	if len(blob) <= limit {
		t.Fatalf("seed is %d bytes, not larger than the %d-byte bound", len(blob), limit)
	}
	if !strings.HasPrefix(blob, "HEAD_MARK\n") || !strings.HasSuffix(blob, "TAIL_MARK\n") {
		t.Fatal("seed must put the markers at the head and tail the truncated preview keeps")
	}
}
